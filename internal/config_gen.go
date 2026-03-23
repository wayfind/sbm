package internal

// Config generation is role-dispatched:
// - Transit/Exit: GenerateTransitConfig / GenerateExitConfig → *SBConfig → SaveConfig
// - Entry: GenerateEntryConfig → map[string]interface{} → SaveRawConfig
// - Client: GenerateClientConfig → map[string]interface{} (output to stdout/file)
//
// There is no unified dispatcher because transit/exit use typed structs
// while entry/client use map[string]interface{} for richer JSON output.
// Callers (cmd/install.go) dispatch by role directly.
