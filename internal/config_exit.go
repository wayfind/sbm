package internal

// GenerateExitConfig generates a sing-box config for an exit node (家宽).
// Currently identical to transit (VLESS Reality + Hy2 inbound, direct outbound).
// Future: may use lighter-weight inbound protocols for transit→exit links.
var GenerateExitConfig = GenerateTransitConfig
