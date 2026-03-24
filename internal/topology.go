package internal

import (
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// --- Data types ---

type BWGStatus struct {
	MonthlyBW    int64   `json:"monthly_bw_bytes"`
	UsedBW       int64   `json:"used_bw_bytes"`
	RemainingPct float64 `json:"remaining_pct"`
}

type NodeInfo struct {
	NodeID     string        `json:"node_id"`
	Name       string        `json:"name,omitempty"`
	Role       NodeRole      `json:"role"`
	Domain     string        `json:"domain,omitempty"`
	Address    string        `json:"address,omitempty"`
	Inbounds   []InboundMeta `json:"inbounds,omitempty"`
	BWGStatus  *BWGStatus    `json:"bwg_status,omitempty"`
	Alive      bool          `json:"alive"`
	ReportedAt time.Time     `json:"reported_at"`
}

type TopologyTree struct {
	Self        NodeInfo       `json:"self"`
	Downstreams []TopologyTree `json:"downstreams,omitempty"`
}

// --- Topology store ---

// TopoStore holds the aggregated topology from downstream reports.
type TopoStore struct {
	mu          sync.RWMutex
	downstreams map[string]TopologyTree // keyed by node_id
}

func NewTopoStore() *TopoStore {
	return &TopoStore{
		downstreams: make(map[string]TopologyTree),
	}
}

func (s *TopoStore) Update(tree TopologyTree) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downstreams[tree.Self.NodeID] = tree
}

// StaleThreshold is how long since last report before a node is considered dead.
const StaleThreshold = 90 * time.Second // debug: ~3 report intervals at 30s

func (s *TopoStore) GetDownstreams() []TopologyTree {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	result := make([]TopologyTree, 0, len(s.downstreams))
	for _, t := range s.downstreams {
		if now.Sub(t.Self.ReportedAt) > StaleThreshold {
			t.Self.Alive = false
		}
		result = append(result, t)
	}
	return result
}

// Prune removes entries that haven't reported in over 1 hour.
func (s *TopoStore) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, t := range s.downstreams {
		if now.Sub(t.Self.ReportedAt) > 3*time.Minute { // debug: prune after 3min
			delete(s.downstreams, id)
		}
	}
}

// --- Build self NodeInfo from metadata ---

func BuildNodeInfo(meta *Metadata) NodeInfo {
	info := NodeInfo{
		NodeID:     meta.NodeID,
		Name:       meta.Name,
		Role:       meta.Role,
		Domain:     meta.Domain,
		Inbounds:   meta.Inbounds,
		Alive:      true,
		ReportedAt: time.Now(),
	}
	// Use domain as address, or detect public IP if no domain
	if meta.Domain != "" {
		info.Address = meta.Domain
	}
	return info
}

func BuildTopologyTree(meta *Metadata, store *TopoStore) TopologyTree {
	tree := TopologyTree{
		Self: BuildNodeInfo(meta),
	}
	if store != nil {
		tree.Downstreams = store.GetDownstreams()
	}
	return tree
}

// --- HTTP handlers ---

// BearerAuth is middleware that checks Authorization: Bearer <token>.
func BearerAuth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			// No auth configured, allow all
			next(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		given := strings.TrimPrefix(auth, "Bearer ")
		if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// HandleNodeInfo returns GET /node-info — this node's NodeInfo.
func HandleNodeInfo(meta *Metadata) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		info := BuildNodeInfo(meta)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}
}

// HandleTopology returns GET /topology — full topology tree from this node's perspective.
func HandleTopology(meta *Metadata, store *TopoStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		tree := BuildTopologyTree(meta, store)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(tree)
	}
}

// HandleReport accepts POST /report — downstream node submits its TopologyTree.
func HandleReport(store *TopoStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var tree TopologyTree
		if err := json.Unmarshal(body, &tree); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if tree.Self.NodeID == "" {
			http.Error(w, "missing node_id", http.StatusBadRequest)
			return
		}
		// Auto-fill address from RemoteAddr if node didn't provide one
		if tree.Self.Address == "" && tree.Self.Domain == "" {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			if host != "" {
				tree.Self.Address = host
			}
		}
		store.Update(tree)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	}
}

// --- Topology reporter (runs as goroutine) ---

// StartTopologyReporter starts a goroutine that periodically reports this node's
// topology tree to all upstream nodes.
func StartTopologyReporter(meta *Metadata, store *TopoStore, interval time.Duration) {
	if len(meta.Upstreams) == 0 {
		log.Println("[topology] no upstreams configured, reporter not started")
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	go func() {
		// Initial report after short delay
		time.Sleep(5 * time.Second)
		reportToUpstreams(meta, store, client)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			reportToUpstreams(meta, store, client)
			store.Prune()
		}
	}()

	log.Printf("[topology] reporter started, interval=%s, upstreams=%d", interval, len(meta.Upstreams))
}

func reportToUpstreams(meta *Metadata, store *TopoStore, client *http.Client) {
	tree := BuildTopologyTree(meta, store)
	data, err := json.Marshal(tree)
	if err != nil {
		log.Printf("[topology] marshal error: %v", err)
		return
	}

	for _, upstream := range meta.Upstreams {
		reportURL := buildReportURL(upstream)
		if reportURL == "" {
			continue
		}

		req, err := http.NewRequest(http.MethodPost, reportURL, bytes.NewReader(data))
		if err != nil {
			log.Printf("[topology] build request to %s error: %v", upstream.Name, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if meta.APIToken != "" {
			req.Header.Set("Authorization", "Bearer "+meta.APIToken)
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[topology] report to %s error: %v", upstream.Name, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("[topology] report to %s returned %d", upstream.Name, resp.StatusCode)
		}
	}
}

// buildReportURL derives the /report URL from an upstream's sub_url or address.
func buildReportURL(upstream UpstreamMeta) string {
	if upstream.SubURL != "" {
		u, err := url.Parse(upstream.SubURL)
		if err != nil {
			return ""
		}
		u.Path = "/report"
		u.RawQuery = ""
		return u.String()
	}
	if upstream.Address == "" {
		return ""
	}
	host := upstream.Address
	if !strings.Contains(host, ":") {
		host = host + ":20033"
	}
	return "https://" + host + "/report"
}

// --- Print topology tree (for CLI) ---

func PrintTopologyTree(tree *TopologyTree, indent string) {
	role := string(tree.Self.Role)
	name := tree.Self.Name
	if name == "" {
		name = tree.Self.NodeID
	}
	addr := tree.Self.Domain
	if addr == "" {
		addr = tree.Self.Address
	}

	status := "alive"
	if !tree.Self.Alive {
		status = "dead"
	}

	fmt.Printf("%s[%s] %s (%s) %s", indent, role, name, addr, status)
	if tree.Self.BWGStatus != nil {
		fmt.Printf(" bw=%.1f%%", tree.Self.BWGStatus.RemainingPct)
	}
	fmt.Println()

	// Print inbounds
	for _, inb := range tree.Self.Inbounds {
		fmt.Printf("%s  %s %s/%d\n", indent, inb.Protocol, inb.Transport, inb.Port)
	}

	// Print downstreams
	for i := range tree.Downstreams {
		connector := "├── "
		childIndent := indent + "│   "
		if i == len(tree.Downstreams)-1 {
			connector = "└── "
			childIndent = indent + "    "
		}
		// First line uses connector prefix, recursive lines use childIndent
		printTopologyNode(&tree.Downstreams[i], indent+connector, childIndent)
	}
}

func printTopologyNode(tree *TopologyTree, firstPrefix, restPrefix string) {
	role := string(tree.Self.Role)
	name := tree.Self.Name
	if name == "" {
		name = tree.Self.NodeID
	}
	addr := tree.Self.Domain
	if addr == "" {
		addr = tree.Self.Address
	}
	status := "alive"
	if !tree.Self.Alive {
		status = "dead"
	}

	fmt.Printf("%s[%s] %s (%s) %s", firstPrefix, role, name, addr, status)
	if tree.Self.BWGStatus != nil {
		fmt.Printf(" bw=%.1f%%", tree.Self.BWGStatus.RemainingPct)
	}
	fmt.Println()

	for _, inb := range tree.Self.Inbounds {
		fmt.Printf("%s  %s %s/%d\n", restPrefix, inb.Protocol, inb.Transport, inb.Port)
	}

	for i := range tree.Downstreams {
		connector := "├── "
		childIndent := restPrefix + "│   "
		if i == len(tree.Downstreams)-1 {
			connector = "└── "
			childIndent = restPrefix + "    "
		}
		printTopologyNode(&tree.Downstreams[i], restPrefix+connector, childIndent)
	}
}
