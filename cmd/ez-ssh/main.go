package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui"
)

var version = "1.0.1"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "--internal-askpass" {
		os.Exit(ssh.RunInternalAskpass(os.Args[2:]))
	}
	if len(os.Args) >= 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		printHelp()
		return
	}
	if len(os.Args) >= 2 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(ui.RenderBanner(version, 80))
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ez-ssh config error: %v\n", err)
		os.Exit(1)
	}

	vault, err := crypto.NewVault(cfg.VaultKeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ez-ssh vault error: %v\n", err)
		os.Exit(1)
	}

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ez-ssh database error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	tunnelMgr := ssh.NewTunnelManager(vault, cfg.KnownHostsPath)
	defer tunnelMgr.ShutdownAll()

	app := ui.NewAppModel(cfg, store, vault, tunnelMgr)
	app.SetVersion(version)
	app.EnableIntro(version)
	defer app.Shutdown()

	prog := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "ez-ssh runtime error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Printf(`ez-ssh v%s - Local, zero-cloud SSH & SFTP Manager TUI

Usage:
  ez-ssh [flags]

Flags:
  -h, --help       Show this help message and exit
  -v, --version    Print the version banner and exit

Storage & Environment:
  Data and encrypted vault live under ~/.config/ez-ssh/
  Override config directory with EZ_SSH_CONFIG_DIR.

Homepage:
  https://github.com/ib-jpg/ez-ssh
`, version)
}
