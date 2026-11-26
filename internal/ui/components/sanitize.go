package components

import (
	"bytes"
	"strings"
)

// SanitizeTranscript strips ANSI escape sequences and unsafe terminal control
// bytes so recorded session logs render cleanly inside a Bubble Tea viewport.
func SanitizeTranscript(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}

	// Normalize CRLF to LF before stripping stray CR.
	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))

	var out strings.Builder
	out.Grow(len(normalized))

	n := len(normalized)
	for i := 0; i < n; i++ {
		b := normalized[i]
		switch {
		case b == 0x1b: // ESC
			if i+1 >= n {
				continue
			}
			next := normalized[i+1]
			switch next {
			case '[': // CSI: ESC [ ... (0x40..0x7E)
				i += 2
				for i < n && (normalized[i] < 0x40 || normalized[i] > 0x7e) {
					i++
				}
			case ']': // OSC: ESC ] ... (BEL or ST)
				i += 2
				for i < n {
					if normalized[i] == 0x07 {
						break
					}
					if normalized[i] == 0x1b && i+1 < n && normalized[i+1] == '\\' {
						i++
						break
					}
					i++
				}
			case 'P', 'X', '^', '_': // DCS / SOS / PM / APC: until ST (ESC \)
				i += 2
				for i < n {
					if normalized[i] == 0x1b && i+1 < n && normalized[i+1] == '\\' {
						i++
						break
					}
					i++
				}
			case '(', ')', '*', '+': // Character set designation: ESC ( X
				i += 2
			default: // 2-byte ESC sequence
				i++
			}
		case b == 0x9b: // C1 CSI
			i++
			for i < n && (normalized[i] < 0x40 || normalized[i] > 0x7e) {
				i++
			}
		case b == 0x9d: // C1 OSC
			i++
			for i < n && normalized[i] != 0x07 && normalized[i] != 0x9c {
				i++
			}
		case b == '\r':
			// Discard standalone CR (e.g. progress spinners)
			continue
		case b == '\b':
			// Discard raw backspace control character
			continue
		case b == '\n' || b == '\t':
			out.WriteByte(b)
		case b < 0x20 || b == 0x7f:
			// Strip other C0/DEL control bytes
			continue
		default:
			out.WriteByte(b)
		}
	}

	return out.String()
}
