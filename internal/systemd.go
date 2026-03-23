package internal

import (
	"fmt"
	"os"
	"os/exec"
)

const singboxUnit = `[Unit]
Description=sing-box service
After=network.target

[Service]
ExecStart=/usr/local/bin/sing-box run -c /etc/sing-box/config.json
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
`

func subServerUnit(sbmPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Subscription Server
After=network.target sing-box.service

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, sbmPath)
}

func InstallSystemdUnits(sbmPath string) error {
	if err := os.WriteFile("/etc/systemd/system/sing-box.service", []byte(singboxUnit), 0644); err != nil {
		return fmt.Errorf("write sing-box.service: %w", err)
	}
	if err := os.WriteFile("/etc/systemd/system/sub-server.service", []byte(subServerUnit(sbmPath)), 0644); err != nil {
		return fmt.Errorf("write sub-server.service: %w", err)
	}
	return RunCmd("systemctl", "daemon-reload")
}

func SystemctlOp(op string, units ...string) error {
	args := append([]string{op}, units...)
	return RunCmd("systemctl", args...)
}

func IsActive(unit string) bool {
	err := exec.Command("systemctl", "is-active", "--quiet", unit).Run()
	return err == nil
}

func RunCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RunCmdSilent(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}
