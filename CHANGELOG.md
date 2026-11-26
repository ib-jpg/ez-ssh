# Changelog

All notable changes to `ez-ssh` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-11-26

### Added
- Local, zero-cloud SSH & SFTP Manager TUI built in Go with Bubble Tea, Lip Gloss, `charmbracelet/x/vt`, `pkg/sftp`, and pure-Go SQLite.
- Global top workspace tab bar with Vaults (`Alt+1`), Dual-Pane SFTP (`Alt+2`), and embedded SSH session tabs (`Alt+3`–`Alt+9`).
- Hosts grid with OS Nerd Font icons, `~/.ssh/config` auto-import (`I`), quick terminal (`t`), and structured host detail inspector (`i`).
- AES-256-GCM encrypted Keychain (`2`) with Ed25519 / RSA-4096 keypair generation (`g`), clipboard public-key copy (`c`), and `ssh-copy-id` (`s`).
- Background Local (`-L`), Remote (`-R`), and Dynamic SOCKS5 (`-D`) Port Forwarding (`3`).
- Multi-line Snippets CRUD and remote SSH execution (`4`), `~/.ssh/known_hosts` viewer/cleaner (`5`), and connection audit Logs with sanitized transcript viewer (`6`).
- Cross-distro packaging (`deb`, `rpm`, `apk`, Arch Linux, `.tar.gz`), GitHub Releases pipeline, and signed APT repository automation.
