package cmd

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sing-box-manager/internal"
	"strings"
	"time"
)

func RunClient(args []string) error {
	if len(args) == 0 {
		return clientUsage()
	}

	switch args[0] {
	case "gen":
		return clientGen(args[1:])
	default:
		return clientUsage()
	}
}

func clientUsage() error {
	fmt.Println(`用法:
  sbm client gen --upstream <订阅URL> --platform <平台> [--output <文件>]

平台: linux, macos, windows, openwrt
输出: 默认 stdout，指定 --output 写入文件`)
	return nil
}

func clientGen(args []string) error {
	fs := flag.NewFlagSet("client gen", flag.ExitOnError)
	upstream := fs.String("upstream", "", "上游订阅 URL")
	platform := fs.String("platform", "linux", "平台: linux, macos, windows, openwrt")
	output := fs.String("output", "", "输出文件 (默认 stdout)")
	proxiesFile := fs.String("proxies", "", "手动指定代理节点 JSON 文件 (替代 --upstream)")
	fs.Parse(args)

	// Validate platform
	switch *platform {
	case "linux", "macos", "windows", "openwrt":
	default:
		return fmt.Errorf("不支持的平台: %s", *platform)
	}

	var proxies []internal.ClientProxy

	if *proxiesFile != "" {
		// Load from file
		data, err := os.ReadFile(*proxiesFile)
		if err != nil {
			return fmt.Errorf("read proxies file: %w", err)
		}
		if err := json.Unmarshal(data, &proxies); err != nil {
			return fmt.Errorf("parse proxies file: %w", err)
		}
	} else if *upstream != "" {
		// Fetch from subscription URL
		var err error
		proxies, err = fetchProxiesFromSub(*upstream)
		if err != nil {
			return fmt.Errorf("fetch subscription: %w", err)
		}
	} else {
		return fmt.Errorf("需要 --upstream 或 --proxies 参数")
	}

	if len(proxies) == 0 {
		return fmt.Errorf("未找到代理节点")
	}
	fmt.Fprintf(os.Stderr, "发现 %d 个代理节点\n", len(proxies))

	config := internal.GenerateClientConfig(&internal.ClientParams{
		Platform: *platform,
		Proxies:  proxies,
	})

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	if *output != "" {
		if err := os.WriteFile(*output, data, 0644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "配置已写入: %s\n", *output)
	} else {
		fmt.Println(string(data))
	}
	return nil
}

// fetchProxiesFromSub fetches a base64-encoded subscription and parses proxy URIs.
func fetchProxiesFromSub(subURL string) ([]internal.ClientProxy, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Get(subURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		// Try RawStdEncoding
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(body)))
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
	}

	lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	var proxies []internal.ClientProxy
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		proxy, err := parseProxyURI(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "跳过无法解析的 URI: %s (%v)\n", line, err)
			continue
		}
		proxies = append(proxies, *proxy)
	}
	return proxies, nil
}

// parseProxyURI parses a vless:// or hysteria2:// URI into ClientProxy.
func parseProxyURI(uri string) (*internal.ClientProxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	proxy := &internal.ClientProxy{
		Address: u.Hostname(),
	}

	port := u.Port()
	if port != "" {
		fmt.Sscanf(port, "%d", &proxy.Port)
	}

	// Fragment as name
	proxy.Name = u.Fragment
	if proxy.Name == "" {
		proxy.Name = u.Hostname()
	}

	params := u.Query()

	switch u.Scheme {
	case "vless":
		proxy.Protocol = "vless"
		proxy.UUID = u.User.Username()
		proxy.Flow = params.Get("flow")
		proxy.SNI = params.Get("sni")
		proxy.PublicKey = params.Get("pbk")
		proxy.ShortID = params.Get("sid")

	case "hysteria2", "hy2":
		proxy.Protocol = "hysteria2"
		if u.User != nil {
			proxy.Password, _ = url.QueryUnescape(u.User.Username())
		}
		proxy.SNI = params.Get("sni")
		if proxy.SNI == "" {
			proxy.SNI = u.Hostname()
		}
		proxy.ObfsPass = params.Get("obfs-password")

	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	return proxy, nil
}
