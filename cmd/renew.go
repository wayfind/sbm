package cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sing-box-manager/internal"
	"strings"
	"time"
)

func RunRenew() error {
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}

	certFile, _ := findCertFiles(meta.Domain)
	certInfo, err := os.Stat(certFile)
	if os.IsNotExist(err) {
		logMsg("证书不存在，尝试签发")
	} else if err != nil {
		return fmt.Errorf("stat cert: %w", err)
	} else {
		daysLeft, err := certDaysLeft(certFile)
		if err != nil {
			return err
		}
		logMsg("证书剩余 %d 天", daysLeft)
		if daysLeft > 30 {
			if internal.JSONMode {
				internal.PrintJSON(map[string]interface{}{
					"action": "skipped", "days_remaining": daysLeft,
				})
			} else {
				logMsg("无需续期")
			}
			return nil
		}
	}

	var mtimeBefore time.Time
	if certInfo != nil {
		mtimeBefore = certInfo.ModTime()
	}

	logMsg("开始续期")
	internal.FirewallTempAddPort(80, "tcp")
	defer internal.FirewallTempRemovePort(80, "tcp")

	// Renewal routing by who MANAGES the domain, not by the served cert path.
	// findCertFiles now returns the unified /etc/sing-box/cert path first, so the
	// old HasPrefix(certFile,"/etc/letsencrypt/") check would wrongly skip certbot.
	// certbot's deploy-hook keeps /etc/sing-box/cert in sync after renewal.
	_, certbotErr := os.Stat("/etc/letsencrypt/live/" + meta.Domain)
	isCertbot := certbotErr == nil
	if isCertbot {
		out, err := exec.Command("certbot", "renew", "--force-renewal").CombinedOutput()
		if err != nil {
			if internal.JSONMode {
				internal.PrintJSON(map[string]interface{}{
					"action": "failed", "error": string(out),
				})
			} else {
				logMsg("ERROR: certbot renew failed: %s", string(out))
			}
			return nil
		}
		days, _ := certDaysLeft(certFile)
		if internal.JSONMode {
			internal.PrintJSON(map[string]interface{}{
				"action": "renewed", "days_remaining": days,
			})
		} else {
			logMsg("续期成功，证书剩余 %d 天", days)
		}
		return nil
	}

	internal.SystemctlOp("restart", "sing-box")

	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)
		newInfo, err := os.Stat(certFile)
		if err != nil {
			continue
		}
		if newInfo.ModTime().After(mtimeBefore) {
			days, _ := certDaysLeft(certFile)
			if internal.JSONMode {
				internal.PrintJSON(map[string]interface{}{
					"action": "renewed", "days_remaining": days,
				})
			} else {
				logMsg("续期成功，证书剩余 %d 天", days)
			}
			return nil
		}
	}

	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{
			"action":          "failed",
			"singbox_running": internal.IsActive("sing-box"),
		})
	} else if internal.IsActive("sing-box") {
		logMsg("sing-box 运行中，但证书可能未更新")
	} else {
		logMsg("ERROR: sing-box 未运行，尝试启动")
		internal.SystemctlOp("start", "sing-box")
	}
	return nil
}

// logMsg logs only in human mode.
func logMsg(format string, args ...interface{}) {
	if !internal.JSONMode {
		log.Printf(format, args...)
	}
}

func certDaysLeft(certFile string) (int, error) {
	out, err := exec.Command("openssl", "x509", "-in", certFile, "-noout", "-enddate").Output()
	if err != nil {
		return 0, fmt.Errorf("read cert: %w", err)
	}
	line := strings.TrimSpace(string(out))
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("unexpected openssl output: %s", line)
	}
	expiry, err := time.Parse("Jan  2 15:04:05 2006 MST", parts[1])
	if err != nil {
		expiry, err = time.Parse("Jan 2 15:04:05 2006 MST", parts[1])
		if err != nil {
			return 0, fmt.Errorf("parse date %q: %w", parts[1], err)
		}
	}
	return int(time.Until(expiry).Hours() / 24), nil
}
