package internal

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

func GenerateSubscription() (string, error) {
	cfg, err := LoadSingBoxConfig()
	if err != nil {
		return "", err
	}
	meta, err := LoadMetadata()
	if err != nil {
		return "", err
	}

	var uris []string
	for _, inb := range cfg.Inbounds {
		switch inb.Type {
		case "vless":
			if len(inb.Users) == 0 {
				continue
			}
			user := inb.Users[0]
			params := url.Values{
				"flow": {user.Flow}, "type": {"tcp"}, "security": {"reality"},
				"pbk": {meta.PublicKey}, "sid": {meta.ShortID},
				"fp": {"chrome"}, "sni": {"www.walmart.com"},
			}
			uris = append(uris, fmt.Sprintf("vless://%s@%s:%d?%s#vless-reality",
				user.UUID, meta.Domain, inb.ListenPort, params.Encode()))

		case "hysteria2":
			if len(inb.Users) == 0 {
				continue
			}
			user := inb.Users[0]
			obfsPw := ""
			if inb.Obfs != nil {
				obfsPw = inb.Obfs.Password
			}
			params := url.Values{
				"sni": {meta.Domain}, "obfs": {"salamander"},
				"obfs-password": {obfsPw}, "alpn": {"h3"},
			}
			uris = append(uris, fmt.Sprintf("hysteria2://%s@%s:%d?%s#hysteria2",
				url.QueryEscape(user.Password), meta.Domain, inb.ListenPort, params.Encode()))
		}
	}

	content := strings.Join(uris, "\n")
	return base64.StdEncoding.EncodeToString([]byte(content)), nil
}
