package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// OSInfo maps a host os_type identifier to its display label, Nerd Font glyph, and muted accent color.
type OSInfo struct {
	Key   string
	Label string
	Icon  string
	Color lipgloss.Color
}

// OSOptions lists all selectable OS profiles in display order.
var OSOptions = []OSInfo{
	{Key: "linux", Label: "Linux", Icon: "\uf17c", Color: Subtext0},
	{Key: "ubuntu", Label: "Ubuntu", Icon: "\uf31b", Color: Peach},
	{Key: "debian", Label: "Debian", Icon: "\uf306", Color: Red},
	{Key: "arch", Label: "Arch", Icon: "\uf303", Color: Blue},
	{Key: "fedora", Label: "Fedora", Icon: "\uf30a", Color: Blue},
	{Key: "centos", Label: "CentOS/RHEL", Icon: "\uf304", Color: Mauve},
	{Key: "alpine", Label: "Alpine", Icon: "\uf300", Color: Blue},
	{Key: "kali", Label: "Kali", Icon: "\uf327", Color: Subtext0},
	{Key: "mikrotik", Label: "MikroTik/Router", Icon: "\U000f0469", Color: Yellow},
	{Key: "windows", Label: "Windows", Icon: "\ue70f", Color: Blue},
}

// LookupOS returns the OSInfo matching key (defaulting to Linux).
func LookupOS(key string) OSInfo {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "centos/rhel", "rhel":
		k = "centos"
	case "mikrotik/router", "router", "routeros":
		k = "mikrotik"
	}
	for _, opt := range OSOptions {
		if opt.Key == k {
			return opt
		}
	}
	return OSOptions[0]
}

// OSIndex returns the index of key inside OSOptions (0 for Linux).
func OSIndex(key string) int {
	info := LookupOS(key)
	for i, opt := range OSOptions {
		if opt.Key == info.Key {
			return i
		}
	}
	return 0
}
