package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sing-box-manager/internal"
	"time"
)

const defaultUpdateURL = "https://tun.vspeak.top"

type latestInfo struct {
	SBM     string `json:"sbm"`
	SingBox string `json:"sing_box"`
}

func RunUpdate(args []string) error {
	baseURL := defaultUpdateURL

	// Allow override via metadata cf_worker_url
	if meta, err := internal.LoadMetadata(); err == nil && meta.CFWorkerURL != "" {
		baseURL = meta.CFWorkerURL
	}

	// 1. Fetch latest version
	humanPrint("检查更新... (%s)\n", baseURL)
	latest, err := fetchLatest(baseURL)
	if err != nil {
		return fmt.Errorf("获取版本信息失败: %w", err)
	}

	humanPrint("远程版本: sbm v%s, sing-box %s\n", latest.SBM, latest.SingBox)
	humanPrint("本地版本: sbm v%s\n", sbmVersion)

	if latest.SBM == sbmVersion {
		if internal.JSONMode {
			internal.PrintJSON(map[string]interface{}{
				"status":          "up_to_date",
				"current_version": sbmVersion,
			})
		} else {
			humanPrint("已是最新版本\n")
		}
		return nil
	}

	// 2. Download new binary
	arch := runtime.GOARCH
	downloadURL := fmt.Sprintf("%s/releases/sbm/v%s/sbm-linux-%s", baseURL, latest.SBM, arch)
	humanPrint("下载 sbm v%s...\n", latest.SBM)

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	tmpFile := self + ".new"
	if err := downloadFile(downloadURL, tmpFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("下载失败: %w", err)
	}

	// 3. Atomic replace
	if err := os.Chmod(tmpFile, 0755); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpFile, self); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("替换失败: %w", err)
	}

	humanPrint("sbm 已更新: v%s → v%s\n", sbmVersion, latest.SBM)

	// 4. Restart sbm-server if running
	if internal.IsActive("sbm-server") {
		humanPrint("重启 sbm-server...\n")
		if err := internal.SystemctlOp("restart", "sbm-server"); err != nil {
			return fmt.Errorf("重启 sbm-server 失败: %w", err)
		}
		humanPrint("sbm-server 已重启\n")
	}

	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{
			"status":       "updated",
			"old_version":  sbmVersion,
			"new_version":  latest.SBM,
		})
	}
	return nil
}

func fetchLatest(baseURL string) (*latestInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + "/releases/latest.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var info latestInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
