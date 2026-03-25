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

// FetchAndRecordTraffic queries V2Ray Stats API for per-user cumulative traffic,
// computes deltas since last poll, records to SQLite, and checks quotas.
func FetchAndRecordTraffic(client *StatsClient, db *sql.DB) (disabled []string, err error) {
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

	return CheckQuotas(db)
}

// StartTrafficCollector runs periodic traffic collection from V2Ray Stats API.
func StartTrafficCollector(db *sql.DB, interval time.Duration, onDisable func([]string)) {
	go func() {
		time.Sleep(10 * time.Second)

		client, err := NewStatsClient("")
		if err != nil {
			log.Printf("[traffic] failed to create stats client: %v", err)
			return
		}
		defer client.Close()

		for {
			disabled, err := FetchAndRecordTraffic(client, db)
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
