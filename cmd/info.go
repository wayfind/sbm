package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sing-box-manager/internal"
	"strings"
	"time"
)

const sbmVersion = "2.0.0"

func RunConfig() error {
	data, err := os.ReadFile(internal.ConfigPath())
	if err != nil {
		return err
	}
	if internal.JSONMode {
		// Output raw config JSON (already JSON)
		fmt.Println(string(data))
		return nil
	}
	re := regexp.MustCompile(`"private_key"\s*:\s*"[^"]*"`)
	masked := re.ReplaceAllString(string(data), `"private_key": "***"`)
	fmt.Println(masked)
	return nil
}

func RunSub() error {
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}
	subPort := meta.SubPort
	if subPort == 0 {
		subPort = 20033
	}
	subURL := fmt.Sprintf("https://%s:%d/sub/hz-office", meta.Domain, subPort)

	sub, err := internal.GenerateSubscription()
	if err != nil {
		return fmt.Errorf("generate subscription: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(sub)
	if err != nil {
		return err
	}
	uris := strings.Split(strings.TrimSpace(string(decoded)), "\n")

	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{
			"url":            subURL,
			"content_base64": sub,
			"uris":           uris,
		})
		return nil
	}

	fmt.Printf("订阅地址: %s\n\n", subURL)
	fmt.Println("=== 解码内容 ===")
	fmt.Println(string(decoded))
	return nil
}

func RunCert() error {
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}
	certFile := internal.CertPath(meta.Domain)
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		if internal.JSONMode {
			internal.PrintJSON(map[string]interface{}{"error": "not_found"})
			return nil
		}
		fmt.Println("证书未找到")
		return nil
	}

	if internal.JSONMode {
		return printCertJSON(certFile)
	}
	return internal.RunCmd("openssl", "x509", "-in", certFile, "-noout", "-subject", "-dates")
}

func printCertJSON(certFile string) error {
	result := map[string]interface{}{}

	// Subject
	out, err := exec.Command("openssl", "x509", "-in", certFile, "-noout", "-subject").Output()
	if err == nil {
		result["subject"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "subject="))
	}

	// Dates
	out, err = exec.Command("openssl", "x509", "-in", certFile, "-noout", "-startdate").Output()
	if err == nil {
		dateStr := strings.TrimPrefix(strings.TrimSpace(string(out)), "notBefore=")
		result["not_before"] = strings.TrimSpace(dateStr)
	}

	out, err = exec.Command("openssl", "x509", "-in", certFile, "-noout", "-enddate").Output()
	if err == nil {
		dateStr := strings.TrimPrefix(strings.TrimSpace(string(out)), "notAfter=")
		result["not_after"] = strings.TrimSpace(dateStr)
		// Parse for days_remaining
		expiry, parseErr := time.Parse("Jan  2 15:04:05 2006 MST", strings.TrimSpace(dateStr))
		if parseErr != nil {
			expiry, parseErr = time.Parse("Jan 2 15:04:05 2006 MST", strings.TrimSpace(dateStr))
		}
		if parseErr == nil {
			result["days_remaining"] = int(time.Until(expiry).Hours() / 24)
		}
	}

	internal.PrintJSON(result)
	return nil
}

func RunMetadata() error {
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	fmt.Println(string(data))
	return nil
}

func RunVersion() error {
	sbVersion := ""
	out, err := exec.Command("sing-box", "version").Output()
	if err == nil {
		// First line of output typically: "sing-box version 1.x.x"
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			sbVersion = strings.TrimSpace(lines[0])
		}
	}

	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{
			"sbm_version":      sbmVersion,
			"singbox_version":  sbVersion,
		})
		return nil
	}

	fmt.Printf("sing-box-manager v%s\n", sbmVersion)
	if sbVersion != "" {
		fmt.Println(sbVersion)
	}
	return nil
}
