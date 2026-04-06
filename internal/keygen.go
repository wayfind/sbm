package internal

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

func GenerateRealityKeypair() (privateKey, publicKey string, err error) {
	out, err := exec.Command("sing-box", "generate", "reality-keypair").Output()
	if err != nil {
		return "", "", fmt.Errorf("sing-box generate reality-keypair: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PrivateKey:") {
			privateKey = strings.TrimSpace(strings.TrimPrefix(line, "PrivateKey:"))
		} else if strings.HasPrefix(line, "PublicKey:") {
			publicKey = strings.TrimSpace(strings.TrimPrefix(line, "PublicKey:"))
		}
	}
	if privateKey == "" || publicKey == "" {
		return "", "", fmt.Errorf("failed to parse reality keypair from: %s", string(out))
	}
	return
}

func GenerateUUID() (string, error) {
	out, err := exec.Command("sing-box", "generate", "uuid").Output()
	if err != nil {
		return "", fmt.Errorf("sing-box generate uuid: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func GenerateShortID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func GeneratePassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// GenerateHexString returns a random hex string of n bytes (2n hex chars).
func GenerateHexString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func GenerateRandomPort(min, max int, exclude ...int) int {
	excl := make(map[int]bool)
	for _, p := range exclude {
		excl[p] = true
	}
	b := make([]byte, 2)
	for i := 0; i < 10000; i++ {
		rand.Read(b)
		port := min + int(uint16(b[0])<<8|uint16(b[1]))%(max-min)
		if !excl[port] {
			return port
		}
	}
	// fallback: should never reach here in practice
	return min
}

func CheckPortAvailable(port int) bool {
	out, _ := exec.Command("ss", "-tlnH", "sport", "=", fmt.Sprintf(":%d", port)).Output()
	if len(strings.TrimSpace(string(out))) > 0 {
		return false
	}
	out, _ = exec.Command("ss", "-ulnH", "sport", "=", fmt.Sprintf(":%d", port)).Output()
	return len(strings.TrimSpace(string(out))) == 0
}
