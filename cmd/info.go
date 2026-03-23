package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sing-box-manager/internal"
)

func RunConfig() error {
	data, err := os.ReadFile(internal.ConfigPath())
	if err != nil {
		return err
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
		subPort = 20032
	}
	fmt.Printf("订阅地址: https://%s:%d/sub/hz-office\n\n", meta.Domain, subPort)

	sub, err := internal.GenerateSubscription()
	if err != nil {
		return fmt.Errorf("generate subscription: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(sub)
	if err != nil {
		return err
	}
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
		fmt.Println("证书未找到")
		return nil
	}
	return internal.RunCmd("openssl", "x509", "-in", certFile, "-noout", "-subject", "-dates")
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
	fmt.Println("sing-box-manager v1.0.0")
	out, err := exec.Command("sing-box", "version").Output()
	if err == nil {
		fmt.Printf("sing-box: %s", out)
	}
	return nil
}
