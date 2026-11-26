# ez-ssh

Local, fast, zero-cloud SSH & SFTP Manager TUI built in Go with Bubble Tea, Lip Gloss, `charmbracelet/x/vt`, `pkg/sftp`, and pure-Go SQLite (`modernc.org/sqlite`).

Launching `ez-ssh` shows an embossed 3D ASCII wordmark with the version; `ez-ssh --version` prints the same banner, and `ez-ssh --help` shows CLI options. Press any key or click to skip the intro. The layout reflows cleanly when the terminal is resized or restored.

## Installation & Upgrade

### Ubuntu / Debian (via signed APT repository)

Once the repository's GitHub Pages APT repo is active (see [docs/RELEASING.md](docs/RELEASING.md)):

```bash
curl -fsSL https://ib-jpg.github.io/ez-ssh/apt/gpg.key | sudo gpg --dearmor -o /usr/share/keyrings/ez-ssh-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/ez-ssh-archive-keyring.gpg] https://ib-jpg.github.io/ez-ssh/apt stable main" | sudo tee /etc/apt/sources.list.d/ez-ssh.list
sudo apt update && sudo apt install ez-ssh
```

Upgrading when a new version (e.g. `1.0.2`) is released:

```bash
sudo apt update && sudo apt upgrade
```

### Universal Installer (Any Linux Distro / macOS)

Downloads the release tarball from [GitHub Releases](https://github.com/ib-jpg/ez-ssh/releases), verifies `checksums.txt`, and installs `ez-ssh`:

```bash
curl -fsSL https://raw.githubusercontent.com/ib-jpg/ez-ssh/main/scripts/install-release.sh | sh
```

### Native Distro Packages (GitHub Releases)

Pre-built packages are attached to every tag on [GitHub Releases](https://github.com/ib-jpg/ez-ssh/releases):
- **Debian / Ubuntu** (`.deb`): `sudo apt install ./ez-ssh_<version>_amd64.deb`
- **Fedora / RHEL / openSUSE** (`.rpm`): `sudo dnf install ./ez-ssh_<version>_x86_64.rpm`
- **Alpine Linux** (`.apk`): `sudo apk add --allow-untrusted ./ez-ssh_<version>_x86_64.apk`
- **Arch Linux** (`.pkg.tar.zst`): `sudo pacman -U ./ez-ssh_<version>_x86_64.pkg.tar.zst`

### Using Go

```bash
go install github.com/ib-jpg/ez-ssh/cmd/ez-ssh@latest
```

### Build from Source

```bash
git clone https://github.com/ib-jpg/ez-ssh.git
cd ez-ssh
./scripts/install.sh
```

Installs the static binary to `~/.local/bin/ez-ssh`. To build a local `.deb` package directly, run `./scripts/build-deb.sh`.

## What's New

- **Global Top Workspace Tab Bar**: Persistent workspace tabs (`[ Vaults ]  [ SFTP ]  [ ● session × ]  [ + ]`) with mouse clicks and `Alt+1..9` / `Alt+Left/Right` / `Ctrl+W` switching while keeping background SSH sessions alive.
- **True Embedded Terminal Engine**: Pure-Go `xterm-256color` VT emulator (`2000`-line capped scrollback, mouse wheel / `Shift+PgUp/PgDn` history, automatic `WindowChange` PTY resize, `Ctrl+C` pass-through) with:
  - **Connecting Card**: Live stage progress (`TCP Handshake` → `SSH Auth` → `Allocating PTY`) with spinner.
  - **Auth Failed Recovery**: Inline masked password retry with optional `[✓] Update saved password in vault`.
  - **Connection Error Card**: Concise failure reason with `[ ↻ Retry ]`, `[ ✎ Edit Host ]`, and `[ × Close ]`.
- **Dual-Pane SFTP File Manager (`Alt+2`)**: Local (`~`) vs Remote panes with upload/download via the clickable `↑ Upload` / `↓ Download` buttons, keyboard (`u` / `d` / `F5`), or **drag a file from one pane and drop it in the other** (green border marks the drop target), all with a live progress bar. Also: directory browse (double-click / `Enter`), `mkdir` (`m`), `rename` (`r`), `delete` (`x`), and hidden dotfiles toggle (`.`).

  > Note: dragging a file from the desktop file manager into the terminal is not possible — terminal emulators do not forward OS file drops to TUI apps. Drag & drop here works between the two SFTP panes; use the buttons or `u` / `d` for keyboard transfers.
- **Redesigned Hosts Grid & Pro Host Form**: Rounded host cards with OS Nerd Font icons (`Ubuntu`, `Debian`, `Arch`, `Fedora`, `CentOS`, `Alpine`, `Kali`, `MikroTik`, `Windows`, `Linux`), single-click Right Detail Sidebar with `[ Connect ]` / `[ SFTP ]` / `[ Edit ]` / `[ Delete ]`, double-click quick connect, and 3-section Add/Edit Host modal.

## Vault Modules

1. **Hosts (`1`)**: Card grid, fuzzy search (`/`), `~/.ssh/config` auto-import (`I`), optional session transcript recording.
2. **Keychain (`2`)**: AES-256-GCM encrypted passwords & passphrases (`.vault.key` `0600`), Ed25519 & RSA-4096 SSH keypair generator (`g`), public key clipboard copy (`c`), and `ssh-copy-id` (`s`).
3. **Port Forwarding (`3`)**: Background Local (`-L`), Remote (`-R`), and Dynamic SOCKS5 (`-D`) tunnels (`Space`/`Enter`).
4. **Snippets (`4`)**: Multi-line snippets CRUD, copy (`c`), and remote SSH execution (`r`).
5. **Known Hosts (`5`)**: Native `~/.ssh/known_hosts` parser with atomic removal & `.bak` backup (`d`).
6. **Logs (`6`)**: Connection audit logs, host filter (`f`), old log cleanup (`C`), and lazy-loaded sanitized transcript viewer (`Enter`).

## Data Storage

Clipboard copy/paste uses, in order: `xclip`/`xsel`/`wl-clipboard`/`pbcopy`, then KDE Klipper over D-Bus (`qdbus6`), then OSC 52 on `/dev/tty`. On KDE/Wayland desktops Klipper works without installing extra utilities.

All configuration, encrypted vault database, and session logs live locally under `~/.config/ez-ssh/` (override with `EZ_SSH_CONFIG_DIR`; the former `LAZY_SSH_CONFIG_DIR` still works, and an existing `~/.config/lazy-ssh` folder is moved over on first launch):

- `~/.config/ez-ssh/.vault.key` (`0600`)
- `~/.config/ez-ssh/vault.db` (`0600`)
- `~/.config/ez-ssh/logs/` (`0700`)

## Global Keybindings

| Key | Action |
| :--- | :--- |
| `Alt+1` / `Alt+2` | Switch to `Vaults` / `SFTP` |
| `Alt+3`–`Alt+9` | Switch to embedded SSH session tab |
| `Alt+Left` / `Alt+Right` | Cycle top workspace tabs |
| `Ctrl+W` | Close active SSH session tab |
| `1`–`6` / `Tab` (in Vaults) | Switch Vault module |
| `i` (in Vaults) | Toggle Detail Sidebar / Inspector |
| `/` | Fuzzy search |
| `Esc` | Close modal / clear filter |
| `q` / `Ctrl+C` (outside terminal) | Graceful shutdown & quit |
| Drag mouse in SSH terminal | Select text with a visible highlight; releasing the mouse copies it to the system clipboard |
| Drag a file in SFTP (`Alt+2`) | Drop it in the opposite pane to upload (local → remote) or download (remote → local) |
| `↑ Upload` / `↓ Download` buttons | Clickable transfer actions in the SFTP transfer row (`u` / `d` / `F5` do the same from the keyboard) |
| `Alt+C` (in SSH terminal) | Copy the whole visible terminal screen |
| `Ctrl+V` / `Alt+V` (in SSH terminal) | Paste from the system clipboard (terminal's native `Ctrl+Shift+V` bracketed paste also works) |
| `Shift+PgUp` / `Shift+PgDn` (in SSH terminal) | Scroll scrollback |

## Releasing & Maintainer Docs

See [docs/RELEASING.md](docs/RELEASING.md) for instructions on pushing updates, setting up GPG signing for the APT repo, and publishing new versions (such as `v1.0.2`).

## License

Licensed under the [MIT License](LICENSE) — Copyright (c) 2026 ib-jpg.
