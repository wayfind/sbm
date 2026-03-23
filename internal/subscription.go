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

	// Build lookup from metadata inbounds for protocol-specific fields
	inboundMeta := make(map[string]*InboundMeta)
	for i := range meta.Inbounds {
		inboundMeta[meta.Inbounds[i].Tag] = &meta.Inbounds[i]
	}

	var uris []string
	for _, inb := range cfg.Inbounds {
		im := inboundMeta[inb.Tag]
		switch inb.Type {
		case "vless":
			if len(inb.Users) == 0 {
				continue
			}
			user := inb.Users[0]
			// Get reality public key from metadata or fallback
			pbk := ""
			sid := ""
			if im != nil {
				pbk = im.PublicKey
				sid = im.ShortID
			}
			params := url.Values{
				"flow": {user.Flow}, "type": {"tcp"}, "security": {"reality"},
				"pbk": {pbk}, "sid": {sid},
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
