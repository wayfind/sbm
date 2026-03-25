package internal

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// GenerateUserSubscription generates a subscription for a specific user.
// Includes main inbounds (auto-routing) + per-exit inbounds (manual exit selection).
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

	// --- Main inbounds (auto-routing) ---
	var mainVlessPbk, mainVlessSid, mainVlessSNI string
	var mainHy2ObfsPass string
	for _, inb := range meta.Inbounds {
		if inb.Protocol == "vless" && inb.ForExit == "" {
			mainVlessPbk = inb.PublicKey
			mainVlessSid = inb.ShortID
			mainVlessSNI = inb.SNI
			if mainVlessSNI == "" {
				mainVlessSNI = "www.walmart.com"
			}
			uris = append(uris, buildVlessURI(user.UUID, addr, inb.Port,
				mainVlessPbk, mainVlessSid, mainVlessSNI, user.Name+"-vless"))
		}

		if inb.Protocol == "hysteria2" && inb.ForExit == "" {
			mainHy2ObfsPass = inb.ObfsPass
			uris = append(uris, buildHy2URI(user.Password, addr, inb.Port,
				addr, mainHy2ObfsPass, user.Name+"-hy2"))
		}
	}

	// --- Per-exit inbounds (manual exit selection) ---
	// Sort exit names for stable subscription output
	exitNames := make([]string, 0, len(meta.ExitVlessPorts))
	for name := range meta.ExitVlessPorts {
		exitNames = append(exitNames, name)
	}
	sort.Strings(exitNames)

	for _, exitName := range exitNames {
		// VLESS per-exit (reuse main vless Reality material)
		if port, ok := meta.ExitVlessPorts[exitName]; ok && mainVlessPbk != "" {
			uris = append(uris, buildVlessURI(user.UUID, addr, port,
				mainVlessPbk, mainVlessSid, mainVlessSNI, user.Name+"-"+exitName+"-vless"))
		}
		// Hy2 per-exit (reuse main hy2 obfs/TLS)
		if port, ok := meta.ExitHy2Ports[exitName]; ok {
			uris = append(uris, buildHy2URI(user.Password, addr, port,
				addr, mainHy2ObfsPass, user.Name+"-"+exitName+"-hy2"))
		}
	}

	content := strings.Join(uris, "\n")
	return base64.StdEncoding.EncodeToString([]byte(content)), nil
}

func buildVlessURI(uuid, addr string, port int, pbk, sid, sni, name string) string {
	params := url.Values{
		"flow": {"xtls-rprx-vision"}, "type": {"tcp"}, "security": {"reality"},
		"pbk": {pbk}, "sid": {sid}, "fp": {"chrome"}, "sni": {sni},
	}
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", uuid, addr, port, params.Encode(), name)
}

func buildHy2URI(password, addr string, port int, sni, obfsPass, name string) string {
	params := url.Values{
		"sni": {sni}, "obfs": {"salamander"},
		"obfs-password": {obfsPass}, "alpn": {"h3"},
	}
	return fmt.Sprintf("hysteria2://%s@%s:%d?%s#%s",
		url.QueryEscape(password), addr, port, params.Encode(), name)
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
