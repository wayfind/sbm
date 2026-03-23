package internal

import (
	"fmt"
	"os/exec"
	"strings"
)

func FirewallAddPort(port int, proto string) error {
	return RunCmdSilent("firewall-cmd", "--permanent", fmt.Sprintf("--add-port=%d/%s", port, proto))
}

func FirewallRemovePort(port int, proto string) error {
	return RunCmdSilent("firewall-cmd", "--permanent", fmt.Sprintf("--remove-port=%d/%s", port, proto))
}

func FirewallTempAddPort(port int, proto string) error {
	return RunCmdSilent("firewall-cmd", fmt.Sprintf("--add-port=%d/%s", port, proto))
}

func FirewallTempRemovePort(port int, proto string) error {
	return RunCmdSilent("firewall-cmd", fmt.Sprintf("--remove-port=%d/%s", port, proto))
}

func FirewallReload() error {
	return RunCmdSilent("firewall-cmd", "--reload")
}

func FirewallListPorts() []string {
	out, err := exec.Command("firewall-cmd", "--list-ports").Output()
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}
