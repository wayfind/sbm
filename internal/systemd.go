package internal

import (
	"fmt"
	"os"
	"os/exec"
	"context"
	"time"
)

const singboxUnit = `[Unit]
Description=sing-box service
After=network.target
StartLimitBurst=3
StartLimitIntervalSec=60

[Service]
ExecStart=/usr/local/bin/sing-box run -c /etc/sing-box/config.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
`

func sbmServerUnit(sbmPath string) string {
	return fmt.Sprintf(`[Unit]
Description=sbm server
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
	// Migrate: remove legacy sub-server.service if present
	const legacyUnit = "/etc/systemd/system/sub-server.service"
	if _, err := os.Stat(legacyUnit); err == nil {
		RunCmdSilent("systemctl", "stop", "sub-server")
		RunCmdSilent("systemctl", "disable", "sub-server")
		os.Remove(legacyUnit)
	}

	if err := os.WriteFile("/etc/systemd/system/sing-box.service", []byte(singboxUnit), 0644); err != nil {
		return fmt.Errorf("write sing-box.service: %w", err)
	}
	if err := os.WriteFile("/etc/systemd/system/sbm-server.service", []byte(sbmServerUnit(sbmPath)), 0644); err != nil {
		return fmt.Errorf("write sbm-server.service: %w", err)
	}
	return RunCmd("systemctl", "daemon-reload")
}

func SystemctlOp(op string, units ...string) error {
	args := append([]string{op}, units...)
	return RunCmdTimeout(30*time.Second, "systemctl", args...)
}

func IsActive(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run()
	return err == nil
}

func RunCmd(name string, args ...string) error {
	return RunCmdTimeout(60*time.Second, name, args...)
}

func RunCmdTimeout(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RunCmdSilent(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}
