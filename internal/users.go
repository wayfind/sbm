package internal

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const UsersDBFile = "users.db"

func UsersDBPath() string { return filepath.Join(DeployDir, UsersDBFile) }

type User struct {
	Name      string  `json:"name"`
	UUID      string  `json:"uuid"`
	Password  string  `json:"password"`
	QuotaGB   float64 `json:"quota_gb"`   // 0 = unlimited
	Enabled   bool    `json:"enabled"`
	CreatedAt string  `json:"created_at"`
}

type UserTraffic struct {
	UserName  string `json:"user_name"`
	BytesUp   int64  `json:"bytes_up"`
	BytesDown int64  `json:"bytes_down"`
	Month     string `json:"month"`
}

type UserWithTraffic struct {
	User
	BytesUp   int64  `json:"bytes_up"`
	BytesDown int64  `json:"bytes_down"`
	BytesTotal int64 `json:"bytes_total"`
	Month     string `json:"month"`
}

// OpenUsersDB opens (or creates) the users database.
func OpenUsersDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", UsersDBPath())
	if err != nil {
		return nil, fmt.Errorf("open users db: %w", err)
	}

	// Create tables if not exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			name       TEXT PRIMARY KEY,
			uuid       TEXT NOT NULL,
			password   TEXT NOT NULL,
			quota_gb   REAL DEFAULT 0,
			enabled    INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS traffic (
			user_name  TEXT NOT NULL,
			bytes_up   INTEGER DEFAULT 0,
			bytes_down INTEGER DEFAULT 0,
			month      TEXT NOT NULL,
			PRIMARY KEY (user_name, month)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT
		);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	// Migration: quota_disabled separates auto quota-disable from manual enabled.
	// A user is served only when enabled=1 AND quota_disabled=0. Idempotent.
	if !columnExists(db, "users", "quota_disabled") {
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN quota_disabled INTEGER DEFAULT 0`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate quota_disabled: %w", err)
		}
	}

	return db, nil
}

// columnExists reports whether a column is present on a table (for idempotent migrations).
func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}

// CurrentMonth returns the current month as "YYYY-MM" in UTC. MUST be UTC so it
// agrees with the CF Worker's aggregation (which uses getUTCMonth). A local-time
// definition here would diverge from the Worker by the node's UTC offset for the
// last hours of each month, making the Worker filter out freshly-reported usage
// as a "future month" and under-count the global quota total.
func CurrentMonth() string {
	return time.Now().UTC().Format("2006-01")
}

// AddUser creates a new user with auto-generated credentials.
func AddUser(db *sql.DB, name string, quotaGB float64) (*User, error) {
	uuid, err := GenerateUUID()
	if err != nil {
		return nil, err
	}
	password, err := GeneratePassword(12)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(
		"INSERT INTO users (name, uuid, password, quota_gb) VALUES (?, ?, ?, ?)",
		name, uuid, password, quotaGB,
	)
	if err != nil {
		return nil, fmt.Errorf("add user: %w", err)
	}

	return &User{Name: name, UUID: uuid, Password: password, QuotaGB: quotaGB, Enabled: true}, nil
}

// DelUser removes a user and their traffic records.
func DelUser(db *sql.DB, name string) error {
	res, err := db.Exec("DELETE FROM users WHERE name = ?", name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", name)
	}
	db.Exec("DELETE FROM traffic WHERE user_name = ?", name)
	return nil
}

// ListUsers returns all users with current month traffic.
func ListUsers(db *sql.DB) ([]UserWithTraffic, error) {
	month := CurrentMonth()
	rows, err := db.Query(`
		SELECT u.name, u.uuid, u.password, u.quota_gb, u.enabled, u.created_at,
		       COALESCE(t.bytes_up, 0), COALESCE(t.bytes_down, 0), COALESCE(t.month, ?)
		FROM users u
		LEFT JOIN traffic t ON u.name = t.user_name AND t.month = ?
		ORDER BY u.name
	`, month, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []UserWithTraffic
	for rows.Next() {
		var u UserWithTraffic
		var enabled int
		err := rows.Scan(&u.Name, &u.UUID, &u.Password, &u.QuotaGB, &enabled,
			&u.CreatedAt, &u.BytesUp, &u.BytesDown, &u.Month)
		if err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		u.BytesTotal = u.BytesUp + u.BytesDown
		result = append(result, u)
	}
	return result, nil
}

// GetUser returns a single user with traffic.
func GetUser(db *sql.DB, name string) (*UserWithTraffic, error) {
	month := CurrentMonth()
	var u UserWithTraffic
	var enabled int
	err := db.QueryRow(`
		SELECT u.name, u.uuid, u.password, u.quota_gb, u.enabled, u.created_at,
		       COALESCE(t.bytes_up, 0), COALESCE(t.bytes_down, 0), COALESCE(t.month, ?)
		FROM users u
		LEFT JOIN traffic t ON u.name = t.user_name AND t.month = ?
		WHERE u.name = ?
	`, month, month, name).Scan(&u.Name, &u.UUID, &u.Password, &u.QuotaGB, &enabled,
		&u.CreatedAt, &u.BytesUp, &u.BytesDown, &u.Month)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	u.BytesTotal = u.BytesUp + u.BytesDown
	return &u, nil
}

// GetEnabledUsers returns only enabled users (for config generation).
func GetEnabledUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query("SELECT name, uuid, password, quota_gb, enabled, created_at FROM users WHERE enabled = 1 AND quota_disabled = 0 ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []User
	for rows.Next() {
		var u User
		var enabled int
		err := rows.Scan(&u.Name, &u.UUID, &u.Password, &u.QuotaGB, &enabled, &u.CreatedAt)
		if err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		result = append(result, u)
	}
	return result, nil
}

func SetUserQuota(db *sql.DB, name string, quotaGB float64) error {
	res, err := db.Exec("UPDATE users SET quota_gb = ? WHERE name = ?", quotaGB, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", name)
	}
	return nil
}

func SetUserEnabled(db *sql.DB, name string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	res, err := db.Exec("UPDATE users SET enabled = ? WHERE name = ?", val, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", name)
	}
	return nil
}

// RecordTraffic updates or inserts traffic for the current month.
func RecordTraffic(db *sql.DB, name string, bytesUp, bytesDown int64) error {
	month := CurrentMonth()
	_, err := db.Exec(`
		INSERT INTO traffic (user_name, bytes_up, bytes_down, month)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_name, month) DO UPDATE SET
			bytes_up = bytes_up + excluded.bytes_up,
			bytes_down = bytes_down + excluded.bytes_down
	`, name, bytesUp, bytesDown, month)
	return err
}

// GetMonthUsage returns every user's traffic (up/down) for the given month,
// keyed by user name. Used to build this node's CF usage report.
func GetMonthUsage(db *sql.DB, month string) (map[string]UsageBytes, error) {
	rows, err := db.Query(
		"SELECT user_name, bytes_up, bytes_down FROM traffic WHERE month = ?", month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]UsageBytes)
	for rows.Next() {
		var name string
		var up, down int64
		if err := rows.Scan(&name, &up, &down); err != nil {
			return nil, err
		}
		out[name] = UsageBytes{Up: up, Down: down}
	}
	return out, rows.Err()
}

// ResetUserTraffic resets the current month's traffic for a user.
func ResetUserTraffic(db *sql.DB, name string) error {
	month := CurrentMonth()
	_, err := db.Exec("DELETE FROM traffic WHERE user_name = ? AND month = ?", name, month)
	return err
}

// EnsureDefaultUser migrates the legacy node-level credentials into a named user.
// Reads UUID/Password from metadata inbounds and inserts as "hz-office" if not exists.
// Idempotent — safe to call on every startup.
func EnsureDefaultUser(db *sql.DB, meta *Metadata) error {
	const defaultName = "hz-office"

	// Already exists?
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE name = ?", defaultName).Scan(&count); err != nil {
		return fmt.Errorf("check default user: %w", err)
	}
	if count > 0 {
		return nil
	}

	// Extract credentials from metadata inbounds
	var uuid, password string
	meta.InboundsMu.RLock()
	for _, inb := range meta.Inbounds {
		if inb.Protocol == "vless" && inb.ForExit == "" && inb.UUID != "" {
			uuid = inb.UUID
		}
		if inb.Protocol == "hysteria2" && inb.ForExit == "" && inb.Password != "" {
			password = inb.Password
		}
	}
	meta.InboundsMu.RUnlock()
	if uuid == "" || password == "" {
		return fmt.Errorf("cannot migrate: no vless UUID or hy2 password in metadata inbounds")
	}

	_, err := db.Exec(
		"INSERT OR IGNORE INTO users (name, uuid, password, quota_gb) VALUES (?, ?, ?, 0)",
		defaultName, uuid, password,
	)
	if err != nil {
		return fmt.Errorf("insert default user: %w", err)
	}

	log.Printf("[users] migrated node-level credentials to user %q", defaultName)
	return nil
}

// CheckQuotas disables users whose GLOBAL traffic (this node + all other entry
// nodes, passed in via globalExtra keyed by user name) meets or exceeds their
// quota. globalExtra holds OTHER nodes' bytes for the current month; this node's
// own bytes are read from the local traffic table. A quota hit sets
// quota_disabled=1 (NOT enabled — manual enable/disable stays orthogonal).
// Returns names of users newly quota-disabled by this call.
func CheckQuotas(db *sql.DB, globalExtra map[string]int64) ([]string, error) {
	month := CurrentMonth()
	rows, err := db.Query(`
		SELECT u.name, u.quota_gb, COALESCE(t.bytes_up, 0) + COALESCE(t.bytes_down, 0) as total
		FROM users u
		LEFT JOIN traffic t ON u.name = t.user_name AND t.month = ?
		WHERE u.enabled = 1 AND u.quota_disabled = 0 AND u.quota_gb > 0
	`, month)
	if err != nil {
		return nil, err
	}

	// Collect over-quota users first, then close rows before writing
	var overQuota []string
	for rows.Next() {
		var name string
		var quotaGB float64
		var localBytes int64
		if err := rows.Scan(&name, &quotaGB, &localBytes); err != nil {
			continue
		}
		totalBytes := localBytes + globalExtra[name]
		quotaBytes := int64(quotaGB * 1e9)
		if totalBytes >= quotaBytes {
			overQuota = append(overQuota, name)
		}
	}
	rows.Close()

	// Now disable (rows closed, no read lock held)
	var disabled []string
	for _, name := range overQuota {
		if err := SetQuotaDisabled(db, name, true); err != nil {
			log.Printf("[quota] failed to quota-disable %s: %v", name, err)
			continue
		}
		disabled = append(disabled, name)
	}
	return disabled, nil
}

// SetQuotaDisabled sets the quota_disabled flag (auto quota enforcement),
// independent of the manual enabled flag.
func SetQuotaDisabled(db *sql.DB, name string, disabled bool) error {
	val := 0
	if disabled {
		val = 1
	}
	_, err := db.Exec("UPDATE users SET quota_disabled = ? WHERE name = ?", val, name)
	return err
}

// MonthlyRollover clears quota_disabled for all users when the processed month
// changes, so quota-disabled users are automatically restored at the start of a
// new month. Manual disables (enabled=0) are untouched. Idempotent: the meta row
// records the last processed month so this runs at most once per month.
// Returns true if a rollover happened.
func MonthlyRollover(db *sql.DB) (bool, error) {
	month := CurrentMonth()
	var last string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'quota_month'`).Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		// A transient DB error must NOT be treated as "no record" — that would
		// clear quota_disabled mid-month and release every over-quota user.
		// Bail out; the next poll retries.
		return false, fmt.Errorf("rollover read last month: %w", err)
	}
	if last == month {
		return false, nil
	}
	if _, err := db.Exec(`UPDATE users SET quota_disabled = 0`); err != nil {
		return false, err
	}
	if _, err := db.Exec(
		`INSERT INTO meta (key, value) VALUES ('quota_month', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, month); err != nil {
		return false, err
	}
	if last != "" {
		log.Printf("[quota] monthly rollover %s→%s: cleared quota_disabled", last, month)
	}
	return true, nil
}
