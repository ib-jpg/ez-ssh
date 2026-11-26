package components

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// ConfirmModel implements a reusable confirmation modal dialog.
type ConfirmModel struct {
	Active   bool
	Tag      string
	Title    string
	Message  string
	Danger   bool
	FocusYes bool
}

// Open opens the confirmation dialog. Dangerous actions default focus to Cancel/No.
func (c *ConfirmModel) Open(tag, title, message string, danger bool) {
	c.Active = true
	c.Tag = tag
	c.Title = title
	c.Message = message
	c.Danger = danger
	c.FocusYes = !danger
}

// Close resets the dialog state.
func (c *ConfirmModel) Close() {
	c.Active = false
	c.Tag = ""
	c.Title = ""
	c.Message = ""
}

// Update processes keyboard input and returns (confirmed, closed).
func (c *ConfirmModel) Update(msg tea.KeyMsg) (confirmed bool, closed bool) {
	if !c.Active {
		return false, false
	}
	switch msg.String() {
	case "y", "Y":
		c.Active = false
		return true, true
	case "n", "N", "esc", "ctrl+c":
		c.Active = false
		return false, true
	case "tab", "shift+tab", "left", "right", "h", "l":
		c.FocusYes = !c.FocusYes
		return false, false
	case "enter", " ":
		res := c.FocusYes
		c.Active = false
		return res, true
	}
	return false, false
}

// View renders the confirmation modal centered in w x h.
func (c ConfirmModel) View(w, h int) string {
	if !c.Active {
		return ""
	}

	yesLabel := "Confirm"
	if c.Danger {
		yesLabel = "Delete"
	}
	noLabel := "Cancel"

	var yesBtn, noBtn string
	if c.FocusYes {
		if c.Danger {
			yesBtn = theme.ButtonDanger.Render(yesLabel)
		} else {
			yesBtn = theme.ButtonActive.Render(yesLabel)
		}
		noBtn = theme.ButtonInactive.Render(noLabel)
	} else {
		yesBtn = theme.ButtonInactive.Render(yesLabel)
		noBtn = theme.ButtonActive.Render(noLabel)
	}

	buttons := lipgloss.JoinHorizontal(lipgloss.Center, yesBtn, theme.SpaceBg(theme.Base, 2), noBtn)
	footer := buttons + "\n\n" + theme.InputHint.Render("[y] Confirm  [n/Esc] Cancel  [Tab] Switch")
	body := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render(c.Message)
	return RenderModal(w, h, c.Title, body, footer, 56)
}
