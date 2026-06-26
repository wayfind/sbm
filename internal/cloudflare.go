package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CFTopoClient is a client for the Cloudflare Workers KV topology registry.
type CFTopoClient struct {
	WorkerURL string // e.g. "https://sbm-topo.whyer1.workers.dev"
	APIKey    string
	client    *http.Client
}

func NewCFTopoClient(workerURL, apiKey string) *CFTopoClient {
	return &CFTopoClient{
		WorkerURL: workerURL,
		APIKey:    apiKey,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ReportNode PUTs this node's info to CF KV.
func (c *CFTopoClient) ReportNode(info NodeInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/nodes/%s", c.WorkerURL, info.NodeID)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cf report: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("cf report status %d", resp.StatusCode)
	}
	return nil
}

// --- CF User registry ---

// CFUser represents a user in the CF KV registry (source of truth).
type CFUser struct {
	Name      string  `json:"name"`
	UUID      string  `json:"uuid"`
	Password  string  `json:"password"`
	QuotaGB   float64 `json:"quota_gb"`
	CreatedAt string  `json:"created_at,omitempty"`
}

// GetAllCFUsers fetches all users from CF KV registry.
func (c *CFTopoClient) GetAllCFUsers() ([]CFUser, error) {
	url := fmt.Sprintf("%s/api/cf-users", c.WorkerURL)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cf get users: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cf get users status %d", resp.StatusCode)
	}

	var users []CFUser
	if err := json.Unmarshal(body, &users); err != nil {
		return nil, fmt.Errorf("cf parse users: %w", err)
	}
	return users, nil
}

// PutCFUser creates or updates a user in CF KV registry.
func (c *CFTopoClient) PutCFUser(user CFUser) error {
	data, err := json.Marshal(user)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/cf-users/%s", c.WorkerURL, user.Name)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cf put user: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("cf put user status %d", resp.StatusCode)
	}
	return nil
}

// DeleteCFUser deletes a user from CF KV registry.
func (c *CFTopoClient) DeleteCFUser(name string) error {
	url := fmt.Sprintf("%s/api/cf-users/%s", c.WorkerURL, name)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cf delete user: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("cf delete user status %d", resp.StatusCode)
	}
	return nil
}

// --- Per-user traffic usage (for global quota aggregation) ---

// UsageBytes is one user's up/down byte counters within a node's monthly usage.
type UsageBytes struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// NodeUsage is one entry node's per-user traffic for a given month, as stored
// under the CF KV key usage/{node_id}. Each entry node writes its own key;
// the Worker sums same-name users across all nodes (current month only) to
// produce a global total for quota enforcement and the dashboard.
type NodeUsage struct {
	NodeID     string                `json:"node_id"`
	Month      string                `json:"month"`
	ReportedAt string                `json:"reported_at"`
	Users      map[string]UsageBytes `json:"users"`
}

// ReportUsage PUTs this node's per-user traffic for the current month to CF KV.
func (c *CFTopoClient) ReportUsage(nodeID, month string, users map[string]UsageBytes) error {
	u := NodeUsage{
		NodeID:     nodeID,
		Month:      month,
		ReportedAt: time.Now().UTC().Format(time.RFC3339),
		Users:      users,
	}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/usage/%s", c.WorkerURL, nodeID)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cf report usage: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("cf report usage status %d", resp.StatusCode)
	}
	return nil
}

// GetAllUsage fetches per-node usage from all nodes for the current month.
// Returns each node's NodeUsage; the caller sums same-name users to get the
// global per-user total. Other-month entries are filtered out by the Worker.
func (c *CFTopoClient) GetAllUsage() ([]NodeUsage, error) {
	url := fmt.Sprintf("%s/api/usage", c.WorkerURL)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cf get usage: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cf get usage status %d", resp.StatusCode)
	}

	var usage []NodeUsage
	if err := json.Unmarshal(body, &usage); err != nil {
		return nil, fmt.Errorf("cf parse usage: %w", err)
	}
	return usage, nil
}

func (c *CFTopoClient) GetAllNodes() ([]NodeInfo, error) {
	url := fmt.Sprintf("%s/nodes", c.WorkerURL)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cf get nodes: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cf get nodes status %d", resp.StatusCode)
	}

	var nodes []NodeInfo
	if err := json.Unmarshal(body, &nodes); err != nil {
		return nil, fmt.Errorf("cf parse nodes: %w", err)
	}
	return nodes, nil
}
