package network

import (
	"os"
	"strings"
)

type Interface struct {
	Name string
	Up   bool
}

// List returns all network interfaces found in /sys/class/net, excluding loopback.
func List() ([]Interface, error) {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil, err
	}

	var ifaces []Interface
	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		operstate, _ := os.ReadFile("/sys/class/net/" + name + "/operstate")
		up := strings.TrimSpace(string(operstate)) == "up"
		ifaces = append(ifaces, Interface{Name: name, Up: up})
	}
	return ifaces, nil
}
