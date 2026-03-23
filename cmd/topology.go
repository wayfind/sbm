package cmd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sing-box-manager/internal"
	"time"
)

func RunTopology(args []string) error {
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}

	tree, err := fetchLocalTopology(meta)
	if err != nil {
		if internal.JSONMode {
			// Fallback: output static local-only tree as JSON
			localTree := internal.BuildTopologyTree(meta, nil)
			internal.PrintJSON(localTree)
			return nil
		}
		fmt.Println("(无法连接本地服务，显示静态视图)")
		fmt.Println()
		localTree := internal.BuildTopologyTree(meta, nil)
		internal.PrintTopologyTree(&localTree, "")
		return nil
	}

	if internal.JSONMode {
		internal.PrintJSON(tree)
		return nil
	}
	internal.PrintTopologyTree(tree, "")
	return nil
}

func fetchLocalTopology(meta *internal.Metadata) (*internal.TopologyTree, error) {
	port := meta.SubPort
	if port == 0 {
		port = 20033
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/topology", port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if meta.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+meta.APIToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var tree internal.TopologyTree
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, err
	}
	return &tree, nil
}
