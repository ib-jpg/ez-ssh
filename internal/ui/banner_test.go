package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderBannerFitsWidthAndShowsVersion(t *testing.T) {
	for _, width := range []int{24, 40, 60, 80, 120} {
		frame := RenderBanner("1.0", width)
		for i, line := range strings.Split(frame, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: line %d is %d cells wide", width, i, w)
			}
		}
		if !strings.Contains(ansi.Strip(frame), "v1.0") {
			t.Fatalf("width %d: version missing from banner", width)
		}
	}
	prefixed := ansi.Strip(RenderBanner("v1.0.2", 80))
	if !strings.Contains(prefixed, "v1.0.2") || strings.Contains(prefixed, "vv1.0.2") {
		t.Fatalf("expected normalized v1.0.2 without duplicate v prefix:\n%s", prefixed)
	}
}

func TestRenderBannerWordmark(t *testing.T) {
	for ch, rows := range bannerGlyphs {
		if len(rows) != 6 {
			t.Fatalf("glyph %q has %d rows, want 6", ch, len(rows))
		}
		wantW := utf8.RuneCountInString(rows[0])
		for r, row := range rows {
			if gotW := utf8.RuneCountInString(row); gotW != wantW {
				t.Fatalf("glyph %q row %d has width %d, want %d", ch, r, gotW, wantW)
			}
		}
	}

	eGlyph := strings.Join(bannerGlyphs['e'], "\n")
	if strings.Contains(eGlyph, "██╔══██╗") || strings.Contains(eGlyph, "██║  ██║") {
		t.Fatalf("'e' glyph should not resemble 'R' or 'A':\n%s", eGlyph)
	}

	plain := ansi.Strip(RenderBanner("1.0", 80))
	for _, want := range []string{"███████╗", "██╔════╝", "█████╗  ", "╚══███╔╝", "████╗ ", "╚█████╗ ", "╚═╝  ╚═╝"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("banner missing wordmark fragment %q:\n%s", want, plain)
		}
	}
	narrow := ansi.Strip(RenderBanner("1.0", 20))
	if strings.Contains(narrow, "██") {
		t.Fatal("narrow terminals should use the compact banner")
	}
	if !strings.Contains(narrow, "EZ-SSH") {
		t.Fatalf("narrow banner missing EZ-SSH: %q", narrow)
	}
}

func TestIntroSplashSkipsOnKeyAndTimeout(t *testing.T) {
	m := &AppModel{width: 80, height: 24}
	m.EnableIntro("1.0")
	if !m.intro {
		t.Fatal("EnableIntro did not arm the splash")
	}
	frame := ansi.Strip(m.View())
	if !strings.Contains(frame, "v1.0") || !strings.Contains(frame, "██") {
		t.Fatalf("splash missing banner:\n%s", frame)
	}

	m.Update(introDoneMsg{})
	if m.intro {
		t.Fatal("introDoneMsg did not dismiss the splash")
	}

	m.EnableIntro("1.0")
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.intro {
		t.Fatal("a key press should skip the splash")
	}
}
