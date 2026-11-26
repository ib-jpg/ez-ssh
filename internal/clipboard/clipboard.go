package clipboard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	atotto "github.com/atotto/clipboard"
)

const (
	klipperService = "org.kde.klipper"
	klipperPath    = "/klipper"
	klipperIface   = "org.kde.klipper.klipper"
)

// Read returns the current system clipboard contents. It tries the standard
// clipboard utilities first (xclip/xsel/wl-paste/pbcopy) and then KDE Klipper
// over D-Bus, which is common on Wayland/KDE desktops without those utilities.
func Read() (string, error) {
	if text, err := atotto.ReadAll(); err == nil && text != "" {
		return text, nil
	}
	if text, err := klipperRead(); err == nil && text != "" {
		return text, nil
	}
	return "", errors.New("clipboard unavailable (no utility and no KDE Klipper)")
}

// Write copies text to the system clipboard. On desktops without xclip /
// wl-copy it falls back to KDE Klipper via D-Bus, and finally to an OSC 52
// escape sequence written to /dev/tty for terminals that support it.
func Write(text string) (string, error) {
	if text == "" {
		return "", errors.New("nothing to copy")
	}

	var failures []string

	if err := atotto.WriteAll(text); err == nil {
		return "system", nil
	} else {
		failures = append(failures, err.Error())
	}

	if err := klipperWrite(text); err == nil {
		return "Klipper (D-Bus)", nil
	} else {
		failures = append(failures, "klipper: "+err.Error())
	}

	if oscErr := writeOSC52ToTTY(text); oscErr == nil {
		return "OSC 52", nil
	} else {
		failures = append(failures, "osc52: "+oscErr.Error())
	}

	return "", fmt.Errorf("clipboard unavailable (%s)", strings.Join(failures, "; "))
}

func qdbusBin() string {
	for _, name := range []string{"qdbus6", "qdbus", "qdbus-qt5"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func klipperWrite(text string) error {
	bin := qdbusBin()
	if bin == "" {
		return errors.New("qdbus not found")
	}
	return exec.Command(bin, klipperService, klipperPath,
		klipperIface+".setClipboardContents", text).Run()
}

func klipperRead() (string, error) {
	bin := qdbusBin()
	if bin == "" {
		return "", errors.New("qdbus not found")
	}
	out, err := exec.Command(bin, klipperService, klipperPath,
		klipperIface+".getClipboardContents").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func writeOSC52ToTTY(text string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	return writeOSC52(tty, text, os.Getenv("TMUX") != "")
}

func writeOSC52(w io.Writer, text string, inTMUX bool) error {
	seq := buildOSC52(text, inTMUX)
	_, err := io.WriteString(w, seq)
	return err
}

func buildOSC52(text string, inTMUX bool) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if inTMUX {
		return "\x1bPtmux;\x1b\x1b]52;c;" + encoded + "\a\x1b\\"
	}
	return "\x1b]52;c;" + encoded + "\a"
}
