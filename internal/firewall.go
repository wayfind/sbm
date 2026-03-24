package internal

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

type firewallBackend int

const (
	fwNone firewallBackend = iota
	fwFirewalld
	fwUFW
)

var (
	detectedFW     firewallBackend
	detectedFWOnce sync.Once
)

func detectFirewall() firewallBackend {
	detectedFWOnce.Do(func() {
		// Check firewalld first (preferred on RHEL/AlmaLinux)
		if _, err := exec.LookPath("firewall-cmd"); err == nil {
			if exec.Command("systemctl", "is-active", "--quiet", "firewalld").Run() == nil {
				detectedFW = fwFirewalld
				return
			}
		}
		// Check ufw (Ubuntu/Debian)
		if _, err := exec.LookPath("ufw"); err == nil {
			out, _ := exec.Command("ufw", "status").Output()
			if strings.Contains(string(out), "Status: active") {
				detectedFW = fwUFW
				return
			}
		}
		detectedFW = fwNone
	})
	return detectedFW
}

func FirewallAddPort(port int, proto string) error {
	switch detectFirewall() {
	case fwFirewalld:
		return RunCmdSilent("firewall-cmd", "--permanent", fmt.Sprintf("--add-port=%d/%s", port, proto))
	case fwUFW:
		return RunCmdSilent("ufw", "allow", fmt.Sprintf("%d/%s", port, proto))
	default:
		log.Printf("[firewall] no firewall detected, skipping add port %d/%s", port, proto)
		return nil
	}
}

func FirewallRemovePort(port int, proto string) error {
	switch detectFirewall() {
	case fwFirewalld:
		return RunCmdSilent("firewall-cmd", "--permanent", fmt.Sprintf("--remove-port=%d/%s", port, proto))
	case fwUFW:
		return RunCmdSilent("ufw", "delete", "allow", fmt.Sprintf("%d/%s", port, proto))
	default:
		return nil
	}
}

func FirewallTempAddPort(port int, proto string) error {
	switch detectFirewall() {
	case fwFirewalld:
		return RunCmdSilent("firewall-cmd", fmt.Sprintf("--add-port=%d/%s", port, proto))
	case fwUFW:
		// ufw has no temp rules; add permanent (will be removed by TempRemovePort)
		return RunCmdSilent("ufw", "allow", fmt.Sprintf("%d/%s", port, proto))
	default:
		return nil
	}
}

func FirewallTempRemovePort(port int, proto string) error {
	switch detectFirewall() {
	case fwFirewalld:
		return RunCmdSilent("firewall-cmd", fmt.Sprintf("--remove-port=%d/%s", port, proto))
	case fwUFW:
		return RunCmdSilent("ufw", "delete", "allow", fmt.Sprintf("%d/%s", port, proto))
	default:
		return nil
	}
}

func FirewallReload() error {
	switch detectFirewall() {
	case fwFirewalld:
		return RunCmdSilent("firewall-cmd", "--reload")
	case fwUFW:
		return nil // ufw rules take effect immediately
	default:
		return nil
	}
}

func FirewallListPorts() []string {
	switch detectFirewall() {
	case fwFirewalld:
		out, err := exec.Command("firewall-cmd", "--list-ports").Output()
		if err != nil {
			return nil
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			return nil
		}
		return strings.Fields(s)
	case fwUFW:
		out, err := exec.Command("ufw", "status").Output()
		if err != nil {
			return nil
		}
		var ports []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			// Parse lines like "22/tcp  ALLOW  Anywhere"
			if strings.Contains(line, "ALLOW") {
				fields := strings.Fields(line)
				if len(fields) > 0 && strings.Contains(fields[0], "/") {
					ports = append(ports, fields[0])
				}
			}
		}
		return ports
	default:
		return nil
	}
}
