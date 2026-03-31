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
	DataCounter     int64 `json:"data_counter"`
	PlanMonthlyData int64 `json:"plan_monthly_data"`
}

// HasData returns true if BWG tracking is active with valid monthly quota.
func (b *BWGStatus) HasData() bool {
	return b != nil && b.PlanMonthlyData > 0
}

// RemainingPct returns remaining bandwidth as percentage [0, 100].
// Returns -1 if BWG tracking is not active.
func (b *BWGStatus) RemainingPct() float64 {
	if !b.HasData() {
		return -1
	}
	pct := float64(b.PlanMonthlyData-b.DataCounter) / float64(b.PlanMonthlyData) * 100
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// IsExhausted returns true if remaining bandwidth is below 5%.
func (b *BWGStatus) IsExhausted() bool {
	return b.HasData() && b.RemainingPct() < 5
}

// UsedLessThan compares bandwidth usage ratio. Returns true if b has used
// proportionally less bandwidth than other. Nil or zero-quota BWGStatus is
// treated as "unlimited" (always less used than any tracked status).
func (b *BWGStatus) UsedLessThan(other *BWGStatus) bool {
	bHas := b.HasData()
	oHas := other.HasData()
	if bHas != oHas {
		return !bHas // unlimited (no tracking) sorts first
	}
	if !bHas {
		return false // both unlimited, equal
	}
	// Compare usage ratios as float64. Values are bandwidth counters (max ~2TB)
	// and quotas (~2TB), well within float64's 52-bit mantissa precision.
	return float64(b.DataCounter)/float64(b.PlanMonthlyData) < float64(other.DataCounter)/float64(other.PlanMonthlyData)
}

type NodeInfo struct {
	NodeID     string        `json:"node_id"`
	Name       string        `json:"name,omitempty"`
	Role       NodeRole      `json:"role"`
	Domain     string        `json:"domain,omitempty"`
	Address    string        `json:"address,omitempty"`
	SubPort    int           `json:"sub_port,omitempty"`
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
const StaleThreshold = 15 * time.Minute // ~3 report intervals at 5m

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
		if now.Sub(t.Self.ReportedAt) > time.Hour {
			delete(s.downstreams, id)
		}
	}
}

// --- BWG status cache ---

var (
	bwgCacheMu sync.RWMutex
	bwgCached  *BWGStatus // nil until first successful fetch
)

// refreshBWGCache fetches BWG data and updates the cache. Non-blocking for callers.
func refreshBWGCache(cfg *BWGConfig) {
	svc, err := GetBWGServiceInfo(cfg.VEID, cfg.APIKey)
	if err != nil {
		log.Printf("[topology] BWG fetch error: %v", err)
		return // keep stale cache
	}
	monthlyBW := int64(float64(svc.PlanMonthlyData) * svc.MonthlyDataMultiplier)
	if monthlyBW == 0 {
		monthlyBW = svc.PlanMonthlyData
	}
	bwgCacheMu.Lock()
	bwgCached = &BWGStatus{
		DataCounter:     svc.DataCounter,
		PlanMonthlyData: monthlyBW,
	}
	bwgCacheMu.Unlock()
}

func getCachedBWG() *BWGStatus {
	bwgCacheMu.RLock()
	defer bwgCacheMu.RUnlock()
	if bwgCached == nil {
		return nil
	}
	cp := *bwgCached
	return &cp
}

// --- Build self NodeInfo from metadata ---

func BuildNodeInfo(meta *Metadata) NodeInfo {
	info := NodeInfo{
		NodeID:     meta.NodeID,
		Name:       meta.Name,
		Role:       meta.Role,
		Domain:     meta.Domain,
		SubPort:    meta.SubPort,
		Inbounds:   meta.Inbounds,
		BWGStatus:  getCachedBWG(),
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
// Also accepts ?token= query parameter (for browser access to dashboard).
func BearerAuth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next(w, r)
			return
		}
		// Check header first
		given := ""
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			given = strings.TrimPrefix(auth, "Bearer ")
		}
		// Fallback to query param
		if given == "" {
			given = r.URL.Query().Get("token")
		}
		if given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
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
// info via two channels: Cloudflare KV (if configured) and chain /report (if upstreams configured).
func StartTopologyReporter(meta *Metadata, store *TopoStore, cfClient *CFTopoClient, interval time.Duration) {
	hasChain := len(meta.Upstreams) > 0
	hasCF := cfClient != nil

	if !hasChain && !hasCF {
		log.Println("[topology] no upstreams and no CF configured, reporter not started")
		return
	}

	var chainClient *http.Client
	if hasChain {
		chainClient = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	go func() {
		time.Sleep(5 * time.Second)
		doReport(meta, store, cfClient, chainClient)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			doReport(meta, store, cfClient, chainClient)
			store.Prune()
		}
	}()

	channels := []string{}
	if hasCF {
		channels = append(channels, "CF")
	}
	if hasChain {
		channels = append(channels, "chain")
	}
	log.Printf("[topology] reporter started, interval=%s, channels=%v", interval, channels)
}

func doReport(meta *Metadata, store *TopoStore, cfClient *CFTopoClient, chainClient *http.Client) {
	// Refresh BWG cache first (non-blocking: updates cache, tolerates failure)
	if meta.BWG != nil {
		refreshBWGCache(meta.BWG)
	}

	// Channel A: Cloudflare KV (flat NodeInfo)
	if cfClient != nil {
		info := BuildNodeInfo(meta)
		if err := cfClient.ReportNode(info); err != nil {
			log.Printf("[topology] CF report error: %v", err)
		}
	}

	// Channel B: Chain /report to upstreams (full tree)
	if chainClient != nil && len(meta.Upstreams) > 0 {
		reportToUpstreams(meta, store, chainClient)
	}
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
	if pct := tree.Self.BWGStatus.RemainingPct(); pct >= 0 {
		fmt.Printf(" bw=%.1f%%", pct)
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
	if pct := tree.Self.BWGStatus.RemainingPct(); pct >= 0 {
		fmt.Printf(" bw=%.1f%%", pct)
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
