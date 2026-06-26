package internal

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB creates an in-memory SQLite database with the users/traffic schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE users (
			name       TEXT PRIMARY KEY,
			uuid       TEXT NOT NULL,
			password   TEXT NOT NULL,
			quota_gb   REAL DEFAULT 0,
			enabled    INTEGER DEFAULT 1,
			quota_disabled INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE traffic (
			user_name  TEXT NOT NULL,
			bytes_up   INTEGER DEFAULT 0,
			bytes_down INTEGER DEFAULT 0,
			month      TEXT NOT NULL,
			PRIMARY KEY (user_name, month)
		);
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// addTestUser inserts a test user into the DB.
func addTestUser(t *testing.T, db *sql.DB, name string, quotaGB float64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO users (name, uuid, password, quota_gb, enabled) VALUES (?, 'uuid', 'pass', ?, 1)`, name, quotaGB)
	if err != nil {
		t.Fatal(err)
	}
}

// ── Delta computation ───────────────────────────────────────────────────────

func TestDeltaNormalAccumulation(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0)

	// Reset global tracker
	tracker.mu.Lock()
	tracker.prev = make(map[string]*V2RayUserTraffic)
	tracker.mu.Unlock()

	// Simulate first poll: baseline, no delta recorded
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 1000, Downlink: 2000},
	})
	assertTraffic(t, db, "alice", 0, 0) // first poll = baseline only

	// Second poll: deltas should be recorded
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 1500, Downlink: 3000},
	})
	assertTraffic(t, db, "alice", 500, 1000) // 1500-1000=500, 3000-2000=1000

	// Third poll: more accumulation
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 2000, Downlink: 4000},
	})
	assertTraffic(t, db, "alice", 1000, 2000) // cumulative: 500+500, 1000+1000
}

func TestDeltaCounterReset(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0)

	tracker.mu.Lock()
	tracker.prev = make(map[string]*V2RayUserTraffic)
	tracker.mu.Unlock()

	// First poll: baseline
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 5000, Downlink: 10000},
	})

	// Second poll: normal delta
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 6000, Downlink: 12000},
	})
	assertTraffic(t, db, "alice", 1000, 2000)

	// Third poll: sing-box restarted → counter reset to small values
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 200, Downlink: 300},
	})
	// deltaUp = 200 - 6000 = negative → use cur.Uplink = 200
	// deltaDown = 300 - 12000 = negative → use cur.Downlink = 300
	assertTraffic(t, db, "alice", 1200, 2300) // 1000+200, 2000+300
}

func TestDeltaMultipleUsers(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0)
	addTestUser(t, db, "bob", 0)

	tracker.mu.Lock()
	tracker.prev = make(map[string]*V2RayUserTraffic)
	tracker.mu.Unlock()

	// First poll: baseline for both
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 100, Downlink: 200},
		"bob":   {Uplink: 300, Downlink: 400},
	})
	assertTraffic(t, db, "alice", 0, 0)
	assertTraffic(t, db, "bob", 0, 0)

	// Second poll
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 150, Downlink: 250},
		"bob":   {Uplink: 500, Downlink: 900},
	})
	assertTraffic(t, db, "alice", 50, 50)
	assertTraffic(t, db, "bob", 200, 500)
}

func TestDeltaNewUserAppears(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0)
	addTestUser(t, db, "bob", 0)

	tracker.mu.Lock()
	tracker.prev = make(map[string]*V2RayUserTraffic)
	tracker.mu.Unlock()

	// First poll: only alice
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 100, Downlink: 200},
	})

	// Second poll: bob appears (new user connected)
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 200, Downlink: 400},
		"bob":   {Uplink: 50, Downlink: 60},
	})
	assertTraffic(t, db, "alice", 100, 200)
	assertTraffic(t, db, "bob", 0, 0) // bob's first poll = baseline

	// Third poll: bob gets delta
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 300, Downlink: 500},
		"bob":   {Uplink: 150, Downlink: 200},
	})
	assertTraffic(t, db, "alice", 200, 300)
	assertTraffic(t, db, "bob", 100, 140)
}

func TestDeltaZeroTrafficNotRecorded(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0)

	tracker.mu.Lock()
	tracker.prev = make(map[string]*V2RayUserTraffic)
	tracker.mu.Unlock()

	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 100, Downlink: 200},
	})

	// Same values → delta = 0, should not write to DB
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 100, Downlink: 200},
	})
	assertTraffic(t, db, "alice", 0, 0) // no traffic record created
}

// ── Quota enforcement ───────────────────────────────────────────────────────

func TestQuotaExceeded(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0.001) // ~1 MB quota

	tracker.mu.Lock()
	tracker.prev = make(map[string]*V2RayUserTraffic)
	tracker.mu.Unlock()

	// Baseline
	simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 0, Downlink: 0},
	})

	// Exceed quota: 2 MB
	disabled := simulatePoll(t, db, map[string]*V2RayUserTraffic{
		"alice": {Uplink: 1_000_000, Downlink: 1_000_000},
	})

	found := false
	for _, d := range disabled {
		if d == "alice" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected alice in disabled list, got %v", disabled)
	}
}

// quotaDisabledOf returns the quota_disabled flag for a user.
func quotaDisabledOf(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var qd int
	if err := db.QueryRow("SELECT quota_disabled FROM users WHERE name = ?", name).Scan(&qd); err != nil {
		t.Fatalf("read quota_disabled %s: %v", name, err)
	}
	return qd
}

// TestGlobalQuotaAggregation: local traffic alone is under quota, but local +
// other-node (globalExtra) traffic exceeds it → user is quota-disabled.
func TestGlobalQuotaAggregation(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 0.002) // 2 MB quota

	// Local: 1 MB (under quota on its own)
	if err := RecordTraffic(db, "alice", 600_000, 400_000); err != nil {
		t.Fatal(err)
	}

	// Without global extra → not disabled
	disabled, err := CheckQuotas(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 0 {
		t.Fatalf("local-only should be under quota, got disabled=%v", disabled)
	}
	if quotaDisabledOf(t, db, "alice") != 0 {
		t.Fatalf("alice should not be quota-disabled by local traffic alone")
	}

	// With another node contributing 1.5 MB → global 2.5 MB ≥ 2 MB → disabled
	disabled, err = CheckQuotas(db, map[string]int64{"alice": 1_500_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0] != "alice" {
		t.Fatalf("global total should exceed quota, got disabled=%v", disabled)
	}
	if quotaDisabledOf(t, db, "alice") != 1 {
		t.Fatalf("alice should be quota-disabled after global aggregation")
	}
	// enabled flag must stay untouched (orthogonal to quota_disabled)
	var enabled int
	db.QueryRow("SELECT enabled FROM users WHERE name = 'alice'").Scan(&enabled)
	if enabled != 1 {
		t.Fatalf("quota disable must not touch enabled flag, got enabled=%d", enabled)
	}
}

// TestMonthlyRollover: rollover clears quota_disabled but leaves manual disables.
func TestMonthlyRollover(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 1)
	addTestUser(t, db, "bob", 1)

	// alice quota-disabled, bob manually disabled
	SetQuotaDisabled(db, "alice", true)
	db.Exec("UPDATE users SET enabled = 0 WHERE name = 'bob'")

	// Seed meta with a stale month so rollover fires.
	db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`)
	db.Exec(`INSERT INTO meta (key, value) VALUES ('quota_month', '2000-01')`)

	rolled, err := MonthlyRollover(db)
	if err != nil {
		t.Fatal(err)
	}
	if !rolled {
		t.Fatal("expected rollover to fire on stale month")
	}
	if quotaDisabledOf(t, db, "alice") != 0 {
		t.Fatal("rollover should clear alice quota_disabled")
	}
	var bobEnabled int
	db.QueryRow("SELECT enabled FROM users WHERE name = 'bob'").Scan(&bobEnabled)
	if bobEnabled != 0 {
		t.Fatal("rollover must NOT re-enable a manually-disabled user")
	}

	// Second call in the same month is a no-op.
	rolled, _ = MonthlyRollover(db)
	if rolled {
		t.Fatal("rollover must not fire twice in the same month")
	}
}

// TestGetEnabledUsersExcludesQuotaDisabled: served set excludes both manual and
// quota disables.
func TestGetEnabledUsersExcludesQuotaDisabled(t *testing.T) {
	db := openTestDB(t)
	addTestUser(t, db, "alice", 1)
	addTestUser(t, db, "bob", 1)
	addTestUser(t, db, "carol", 1)
	SetQuotaDisabled(db, "bob", true)
	db.Exec("UPDATE users SET enabled = 0 WHERE name = 'carol'")

	users, err := GetEnabledUsers(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "alice" {
		names := []string{}
		for _, u := range users {
			names = append(names, u.Name)
		}
		t.Fatalf("expected only alice served, got %v", names)
	}
}

// ── Test helpers ────────────────────────────────────────────────────────────

// simulatePoll calls the delta logic directly (without gRPC) by injecting fake stats.
func simulatePoll(t *testing.T, db *sql.DB, current map[string]*V2RayUserTraffic) []string {
	t.Helper()

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

	for _, d := range deltas {
		if err := RecordTraffic(db, d.name, d.up, d.down); err != nil {
			t.Fatalf("RecordTraffic %s: %v", d.name, err)
		}
	}

	disabled, err := CheckQuotas(db, nil)
	if err != nil {
		t.Fatalf("CheckQuotas: %v", err)
	}
	return disabled
}

// assertTraffic checks the cumulative traffic in the DB for this month.
func assertTraffic(t *testing.T, db *sql.DB, user string, wantUp, wantDown int64) {
	t.Helper()
	var up, down int64
	err := db.QueryRow(`SELECT COALESCE(SUM(bytes_up),0), COALESCE(SUM(bytes_down),0) FROM traffic WHERE user_name = ?`, user).Scan(&up, &down)
	if err != nil {
		t.Fatal(err)
	}
	if up != wantUp || down != wantDown {
		t.Errorf("%s traffic: got up=%d down=%d, want up=%d down=%d", user, up, down, wantUp, wantDown)
	}
}
