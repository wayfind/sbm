package internal

import (
	"database/sql"
	"fmt"
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
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	return db, nil
}

func CurrentMonth() string {
	return time.Now().Format("2006-01")
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
	rows, err := db.Query("SELECT name, uuid, password, quota_gb, enabled, created_at FROM users WHERE enabled = 1 ORDER BY name")
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

// ResetUserTraffic resets the current month's traffic for a user.
func ResetUserTraffic(db *sql.DB, name string) error {
	month := CurrentMonth()
	_, err := db.Exec("DELETE FROM traffic WHERE user_name = ? AND month = ?", name, month)
	return err
}

// CheckQuotas checks all users and disables those over quota. Returns names of newly disabled users.
func CheckQuotas(db *sql.DB) ([]string, error) {
	month := CurrentMonth()
	rows, err := db.Query(`
		SELECT u.name, u.quota_gb, COALESCE(t.bytes_up, 0) + COALESCE(t.bytes_down, 0) as total
		FROM users u
		LEFT JOIN traffic t ON u.name = t.user_name AND t.month = ?
		WHERE u.enabled = 1 AND u.quota_gb > 0
	`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var disabled []string
	for rows.Next() {
		var name string
		var quotaGB float64
		var totalBytes int64
		if err := rows.Scan(&name, &quotaGB, &totalBytes); err != nil {
			continue
		}
		quotaBytes := int64(quotaGB * 1e9)
		if totalBytes >= quotaBytes {
			SetUserEnabled(db, name, false)
			disabled = append(disabled, name)
		}
	}
	return disabled, nil
}
