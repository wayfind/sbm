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

	certFile := internal.CertPath(meta.Domain)
	certInfo, err := os.Stat(certFile)
	if os.IsNotExist(err) {
		log.Println("证书不存在，尝试签发")
	} else if err != nil {
		return fmt.Errorf("stat cert: %w", err)
	} else {
		daysLeft, err := certDaysLeft(certFile)
		if err != nil {
			return err
		}
		log.Printf("证书剩余 %d 天", daysLeft)
		if daysLeft > 30 {
			log.Println("无需续期")
			return nil
		}
	}

	// 记录续期前的 mtime（证书不存在时为 zero time）
	var mtimeBefore time.Time
	if certInfo != nil {
		mtimeBefore = certInfo.ModTime()
	}

	log.Println("开始续期")
	internal.FirewallTempAddPort(80, "tcp")
	defer internal.FirewallTempRemovePort(80, "tcp")

	internal.SystemctlOp("restart", "sing-box")

	// 等待续期完成，最多 60 秒，通过 mtime 变化判断
	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)
		newInfo, err := os.Stat(certFile)
		if err != nil {
			continue
		}
		if newInfo.ModTime().After(mtimeBefore) {
			days, _ := certDaysLeft(certFile)
			log.Printf("续期成功，证书剩余 %d 天", days)
			return nil
		}
	}

	if internal.IsActive("sing-box") {
		log.Println("sing-box 运行中，但证书可能未更新")
	} else {
		log.Println("ERROR: sing-box 未运行，尝试启动")
		internal.SystemctlOp("start", "sing-box")
	}
	return nil
}

func certDaysLeft(certFile string) (int, error) {
	out, err := exec.Command("openssl", "x509", "-in", certFile, "-noout", "-enddate").Output()
	if err != nil {
		return 0, fmt.Errorf("read cert: %w", err)
	}
	// notAfter=Jun 21 02:26:14 2026 GMT
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
