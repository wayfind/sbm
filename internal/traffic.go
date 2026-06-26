package internal

import (
	"database/sql"
	"log"
	"sync"
	"time"
)

// deltaTracker tracks previous cumulative values to compute deltas.
type deltaTracker struct {
	mu   sync.Mutex
	prev map[string]*V2RayUserTraffic // user → last seen cumulative values
}

var tracker = deltaTracker{prev: make(map[string]*V2RayUserTraffic)}

// ResetTrackerUser drops a user's in-memory cumulative baseline so the next poll
// re-establishes it from scratch. Call after ResetUserTraffic to keep the delta
// logic consistent with the cleared SQLite counters.
func ResetTrackerUser(name string) {
	tracker.mu.Lock()
	delete(tracker.prev, name)
	tracker.mu.Unlock()
}

// FetchAndRecordTraffic queries V2Ray Stats API for per-user cumulative traffic,
// computes deltas since last poll, records to SQLite, reports this node's usage
// to CF, then enforces quotas using the GLOBAL total (this node + all other
// entry nodes via CF). cfClient/nodeID may be nil/"" to fall back to local-only
// behavior (no global aggregation, no usage report).
func FetchAndRecordTraffic(client *StatsClient, db *sql.DB, cfClient *CFTopoClient, nodeID string) (disabled []string, err error) {
	current, err := client.QueryUserTraffic()
	if err != nil {
		return nil, err
	}

	// Compute deltas under lock, then release before SQLite I/O
	type userDelta struct {
		name string
		up   int64
		down int64
	}
	var deltas []userDelta

	tracker.mu.Lock()
	for user, cur := range current {
		prev := tracker.prev[user]
		if prev == nil {
			tracker.prev[user] = &V2RayUserTraffic{Uplink: cur.Uplink, Downlink: cur.Downlink}
			continue
		}

		deltaUp := cur.Uplink - prev.Uplink
		deltaDown := cur.Downlink - prev.Downlink

		// Counter reset (sing-box restart) → treat current values as delta
		if deltaUp < 0 {
			deltaUp = cur.Uplink
		}
		if deltaDown < 0 {
			deltaDown = cur.Downlink
		}

		tracker.prev[user] = &V2RayUserTraffic{Uplink: cur.Uplink, Downlink: cur.Downlink}

		if deltaUp+deltaDown > 0 {
			deltas = append(deltas, userDelta{name: user, up: deltaUp, down: deltaDown})
		}
	}
	tracker.mu.Unlock()

	// Record to SQLite (no lock held)
	for _, d := range deltas {
		if err := RecordTraffic(db, d.name, d.up, d.down); err != nil {
			log.Printf("[traffic] record %s error: %v", d.name, err)
		}
	}

	month := CurrentMonth()

	// Restore quota-disabled users at the start of a new month (before checking).
	if _, err := MonthlyRollover(db); err != nil {
		log.Printf("[traffic] monthly rollover error: %v", err)
	}

	// Report this node's usage to CF and aggregate other nodes' usage for the
	// global quota total. CF failures degrade to local-only enforcement.
	var globalExtra map[string]int64
	if cfClient != nil && nodeID != "" {
		if usage, uErr := GetMonthUsage(db, month); uErr == nil {
			if rErr := cfClient.ReportUsage(nodeID, month, usage); rErr != nil {
				log.Printf("[traffic] report usage error: %v", rErr)
			}
		} else {
			log.Printf("[traffic] read month usage error: %v", uErr)
		}

		if all, gErr := cfClient.GetAllUsage(); gErr == nil {
			globalExtra = make(map[string]int64)
			for _, nu := range all {
				if nu.NodeID == nodeID || nu.Month != month {
					continue // skip self (counted locally) and stale months
				}
				for name, b := range nu.Users {
					globalExtra[name] += b.Up + b.Down
				}
			}
		} else {
			log.Printf("[traffic] get global usage error (local-only enforcement): %v", gErr)
		}
	}

	return CheckQuotas(db, globalExtra)
}

// StartTrafficCollector runs periodic traffic collection from V2Ray Stats API.
func StartTrafficCollector(db *sql.DB, cfClient *CFTopoClient, nodeID string, interval time.Duration, onDisable func([]string)) {
	go func() {
		time.Sleep(10 * time.Second)

		client, err := NewStatsClient("")
		if err != nil {
			log.Printf("[traffic] failed to create stats client: %v", err)
			return
		}
		defer client.Close()

		for {
			disabled, err := FetchAndRecordTraffic(client, db, cfClient, nodeID)
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
	log.Printf("[traffic] collector started (v2ray stats), interval=%s", interval)
}
