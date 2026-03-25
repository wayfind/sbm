package internal

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// GenerateUserSubscription generates a subscription for a specific user.
func GenerateUserSubscription(user *UserWithTraffic) (string, error) {
	meta, err := LoadMetadata()
	if err != nil {
		return "", err
	}

	addr := meta.Domain
	if addr == "" {
		return "", fmt.Errorf("no domain configured")
	}

	var uris []string

	// Find VLESS inbound port from metadata
	for _, inb := range meta.Inbounds {
		if inb.Protocol == "vless" && inb.ForExit == "" {
			pbk := inb.PublicKey
			sid := inb.ShortID
			params := url.Values{
				"flow": {"xtls-rprx-vision"}, "type": {"tcp"}, "security": {"reality"},
				"pbk": {pbk}, "sid": {sid},
				"fp": {"chrome"}, "sni": {"www.walmart.com"},
			}
			uris = append(uris, fmt.Sprintf("vless://%s@%s:%d?%s#%s",
				user.UUID, addr, inb.Port, params.Encode(), user.Name+"-vless"))
		}

		if inb.Protocol == "hysteria2" && inb.ForExit == "" {
			obfsPw := inb.ObfsPass
			params := url.Values{
				"sni": {addr}, "obfs": {"salamander"},
				"obfs-password": {obfsPw}, "alpn": {"h3"},
			}
			uris = append(uris, fmt.Sprintf("hysteria2://%s@%s:%d?%s#%s",
				url.QueryEscape(user.Password), addr, inb.Port, params.Encode(), user.Name+"-hy2"))
		}
	}

	content := strings.Join(uris, "\n")
	return base64.StdEncoding.EncodeToString([]byte(content)), nil
}

// GenerateSubscriptionForUser generates subscription for a named user from SQLite.
func GenerateSubscriptionForUser(db *sql.DB, username string) (string, error) {
	user, err := GetUser(db, username)
	if err != nil {
		return "", err
	}
	if !user.Enabled {
		return "", fmt.Errorf("user %s is disabled (quota exceeded?)", username)
	}
	return GenerateUserSubscription(user)
}
