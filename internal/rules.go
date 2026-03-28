package internal

import "os"

// Rule set URLs used by client config generation.
const (
	RuleSetGeositeCN  = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/geolocation-cn.srs"
	RuleSetGeoipCN    = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geoip/cn.srs"
	RuleSetLocalDir   = "/etc/sing-box/rules"
)

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
