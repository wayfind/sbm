package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ClashAPIAddr is the Clash API listen address for traffic monitoring.
const ClashAPIAddr = "127.0.0.1:9090"

// ClashConnections is the response from GET /connections.
type ClashConnections struct {
	DownloadTotal int64            `json:"downloadTotal"`
	UploadTotal   int64            `json:"uploadTotal"`
	Connections   []ClashConnection `json:"connections"`
}

type ClashConnection struct {
	ID       string         `json:"id"`
	Upload   int64          `json:"upload"`
	Download int64          `json:"download"`
	Metadata ClashMetadata  `json:"metadata"`
	Chains   []string       `json:"chains"`
}

type ClashMetadata struct {
	Network     string `json:"network"`
	Type        string `json:"type"`
	SrcIP       string `json:"sourceIP"`
	DstIP       string `json:"destinationIP"`
	SrcPort     string `json:"sourcePort"`
	DstPort     string `json:"destinationPort"`
	Host        string `json:"host"`
	InboundName string `json:"inboundName"`
	InboundUser string `json:"inboundUser"`
}

// lastSeen tracks cumulative bytes per connection ID to compute deltas.
var lastSeen = map[string]int64{}

// FetchAndRecordTraffic queries Clash API /connections, aggregates per-user traffic,
// records deltas to SQLite, and checks quotas.
func FetchAndRecordTraffic(db *sql.DB, secret string) (disabled []string, err error) {
	conns, err := fetchConnections(secret)
	if err != nil {
		return nil, err
	}

	// Aggregate per user
	userUp := map[string]int64{}
	userDown := map[string]int64{}
	currentIDs := map[string]bool{}

	for _, c := range conns.Connections {
		user := c.Metadata.InboundUser
		if user == "" {
			continue
		}
		currentIDs[c.ID] = true

		total := c.Upload + c.Download
		prev, exists := lastSeen[c.ID]
		if !exists {
			prev = 0
		}
		delta := total - prev
		if delta <= 0 {
			continue
		}
		lastSeen[c.ID] = total

		// Split delta proportionally between up/down
		if c.Upload+c.Download > 0 {
			upRatio := float64(c.Upload) / float64(c.Upload+c.Download)
			userUp[user] += int64(float64(delta) * upRatio)
			userDown[user] += delta - int64(float64(delta)*upRatio)
		}
	}

	// Clean up closed connections from lastSeen
	for id := range lastSeen {
		if !currentIDs[id] {
			delete(lastSeen, id)
		}
	}

	// Record to SQLite
	for user, up := range userUp {
		down := userDown[user]
		if up+down > 0 {
			if err := RecordTraffic(db, user, up, down); err != nil {
				log.Printf("[traffic] record %s error: %v", user, err)
			}
		}
	}

	// Check quotas
	return CheckQuotas(db)
}

func fetchConnections(secret string) (*ClashConnections, error) {
	url := fmt.Sprintf("http://%s/connections", ClashAPIAddr)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clash api: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("clash api status %d: %s", resp.StatusCode, string(body[:100]))
	}

	var conns ClashConnections
	if err := json.Unmarshal(body, &conns); err != nil {
		return nil, fmt.Errorf("clash api parse: %w", err)
	}
	return &conns, nil
}

// StartTrafficCollector runs periodic traffic collection from Clash API.
func StartTrafficCollector(db *sql.DB, secret string, interval time.Duration, onDisable func([]string)) {
	go func() {
		time.Sleep(10 * time.Second) // wait for sing-box to start

		for {
			disabled, err := FetchAndRecordTraffic(db, secret)
			if err != nil {
				log.Printf("[traffic] collect error: %v", err)
			}
			if len(disabled) > 0 {
				log.Printf("[traffic] quota exceeded, disabled users: %v", disabled)
				if onDisable != nil {
					onDisable(disabled)
				}
			}
			time.Sleep(interval)
		}
	}()
	log.Printf("[traffic] collector started, interval=%s", interval)
}
