package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sing-box-manager/internal"
	"strings"
	"time"
)

const sbmVersion = "2.5.0"

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
	subBase := fmt.Sprintf("https://%s:%d/sub", meta.Domain, subPort)

	db, err := internal.OpenUsersDB()
	if err != nil {
		return fmt.Errorf("open users DB: %w", err)
	}
	defer db.Close()

	users, err := internal.ListUsers(db)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	if internal.JSONMode {
		type subEntry struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Enabled bool   `json:"enabled"`
		}
		entries := make([]subEntry, len(users))
		for i, u := range users {
			entries[i] = subEntry{Name: u.Name, URL: subBase + "/" + u.Name, Enabled: u.Enabled}
		}
		internal.PrintJSON(entries)
		return nil
	}

	if len(users) == 0 {
		fmt.Println("暂无用户，使用 sbm user add <name> 添加")
		return nil
	}
	fmt.Println("订阅地址:")
	for _, u := range users {
		status := "✓"
		if !u.Enabled {
			status = "✗"
		}
		fmt.Printf("  %s %s/%s\n", status, subBase, u.Name)
	}
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
