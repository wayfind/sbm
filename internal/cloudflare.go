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

// GetAllNodes GETs all nodes from CF KV.
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
