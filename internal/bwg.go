package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const bwgAPIBase = "https://api.64clouds.com/v1"

// BWGServiceInfo is the raw response from getServiceInfo API.
type BWGServiceInfo struct {
	// Bandwidth
	DataCounter            int64   `json:"data_counter"`             // bytes used this period
	DataNextReset          int64   `json:"data_next_reset"`          // unix timestamp
	MonthlyDataMultiplier  float64 `json:"monthly_data_multiplier"`  // multiplier (e.g. 1)
	PlanMonthlyData        int64   `json:"plan_monthly_data"`        // bytes per period

	// Instance info
	Hostname string          `json:"hostname"`
	NodeIP   json.RawMessage `json:"ip_addresses"` // can be string or array
	VEID     int64           `json:"veid"`

	// Status
	VeStatus string `json:"ve_status"` // "running", "stopped", etc.

	Error int    `json:"error,omitempty"`
	Msg   string `json:"message,omitempty"`
}

// GetBWGServiceInfo calls the BWG getServiceInfo API.
func GetBWGServiceInfo(veid, apiKey string) (*BWGServiceInfo, error) {
	url := fmt.Sprintf("%s/getServiceInfo?veid=%s&api_key=%s", bwgAPIBase, veid, apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("bwg api request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("bwg api read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bwg api status %d: %s", resp.StatusCode, string(body))
	}

	var info BWGServiceInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("bwg api parse: %w", err)
	}
	if info.Error != 0 {
		return nil, fmt.Errorf("bwg api error: %s", info.Msg)
	}

	return &info, nil
}

// FormatBytes formats bytes to human-readable string.
func FormatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	default:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	}
}
