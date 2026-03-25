package internal

import (
	"database/sql"
	"log"
	"time"
)

// SyncUsersFromCF pulls the authoritative user list from CF KV and merges into local SQLite.
// CF is source of truth: CF has → local must have; CF doesn't have → local must delete.
// enabled and traffic are local-only state and are never overwritten.
func SyncUsersFromCF(db *sql.DB, cfClient *CFTopoClient) error {
	if cfClient == nil {
		return nil
	}

	cfUsers, err := cfClient.GetAllCFUsers()
	if err != nil {
		return err
	}

	// Build CF user map
	cfMap := make(map[string]CFUser, len(cfUsers))
	for _, u := range cfUsers {
		cfMap[u.Name] = u
	}

	// Get local users
	localUsers, err := listAllUsers(db)
	if err != nil {
		return err
	}
	localMap := make(map[string]bool, len(localUsers))
	for _, u := range localUsers {
		localMap[u.Name] = true
	}

	// CF has, local doesn't → add
	for _, cfu := range cfUsers {
		if !localMap[cfu.Name] {
			_, err := db.Exec(
				`INSERT INTO users (name, uuid, password, quota_gb, enabled) VALUES (?, ?, ?, ?, 1)`,
				cfu.Name, cfu.UUID, cfu.Password, cfu.QuotaGB,
			)
			if err != nil {
				log.Printf("[user-sync] add %s error: %v", cfu.Name, err)
			} else {
				log.Printf("[user-sync] added user %s from CF", cfu.Name)
			}
		}
	}

	// Local has, CF doesn't → delete
	for _, lu := range localUsers {
		if _, inCF := cfMap[lu.Name]; !inCF {
			db.Exec(`DELETE FROM traffic WHERE user_name = ?`, lu.Name)
			db.Exec(`DELETE FROM users WHERE name = ?`, lu.Name)
			log.Printf("[user-sync] deleted user %s (not in CF)", lu.Name)
		}
	}

	// Both have → sync quota/password from CF
	for _, lu := range localUsers {
		cfu, inCF := cfMap[lu.Name]
		if !inCF {
			continue
		}
		if lu.QuotaGB != cfu.QuotaGB || lu.Password != cfu.Password {
			db.Exec(`UPDATE users SET quota_gb = ?, password = ? WHERE name = ?`,
				cfu.QuotaGB, cfu.Password, lu.Name)
			log.Printf("[user-sync] updated %s: quota=%.1f→%.1f", lu.Name, lu.QuotaGB, cfu.QuotaGB)
		}
	}

	return nil
}

// PushUserToCF pushes a local user to CF KV registry.
func PushUserToCF(db *sql.DB, cfClient *CFTopoClient, name string) error {
	if cfClient == nil {
		return nil
	}
	user, err := GetUser(db, name)
	if err != nil {
		return err
	}
	return cfClient.PutCFUser(CFUser{
		Name:     user.Name,
		UUID:     user.UUID,
		Password: user.Password,
		QuotaGB:  user.QuotaGB,
	})
}

// StartUserSyncLoop runs periodic user sync from CF KV.
func StartUserSyncLoop(db *sql.DB, cfClient *CFTopoClient, interval time.Duration) {
	if cfClient == nil {
		log.Println("[user-sync] no CF client, sync disabled")
		return
	}

	go func() {
		for {
			time.Sleep(interval)
			if err := SyncUsersFromCF(db, cfClient); err != nil {
				log.Printf("[user-sync] error: %v", err)
			}
		}
	}()
	log.Printf("[user-sync] started, interval=%s", interval)
}

// listAllUsers returns all users (including disabled) for sync comparison.
func listAllUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query(`SELECT name, uuid, password, quota_gb, enabled, created_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []User
	for rows.Next() {
		var u User
		var enabled int
		if err := rows.Scan(&u.Name, &u.UUID, &u.Password, &u.QuotaGB, &enabled, &u.CreatedAt); err != nil {
			continue
		}
		u.Enabled = enabled == 1
		result = append(result, u)
	}
	return result, nil
}
