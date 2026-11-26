package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Catppuccin Mocha 3-layer dark surface palette.
const (
	Crust    = lipgloss.Color("#11111b") // Layer 0: App canvas / main workspace background
	Mantle   = lipgloss.Color("#181825") // Layer 1: Left sidebar & right detail panel
	Base     = lipgloss.Color("#1e1e2e") // Layer 2: Elevated cards, search bar, modals, dropdowns
	Surface0 = lipgloss.Color("#313244") // Active/hover highlight & subtle borders
	Surface1 = lipgloss.Color("#45475a") // Stronger borders
	Mauve    = lipgloss.Color("#cba6f7") // Secondary accent
	Blue     = lipgloss.Color("#89b4fa") // Primary focus & CTA accent
	Sky      = lipgloss.Color("#89dceb") // Winter / frost accent
	Text     = lipgloss.Color("#cdd6f4") // Primary text
	Subtext0 = lipgloss.Color("#a6adc8") // Secondary labels
	Overlay1 = lipgloss.Color("#7f849c") // Muted metadata & keyhints
	Green    = lipgloss.Color("#a6e3a1") // Active / success
	Yellow   = lipgloss.Color("#f9e2af") // Connecting / warning
	Peach    = lipgloss.Color("#fab387") // Carrot / warm accent
	Red      = lipgloss.Color("#f38ba8") // Error / danger
)

var (
	CanvasBg = lipgloss.NewStyle().
			Background(Crust).
			Foreground(Text)

	PanelBg = lipgloss.NewStyle().
		Background(Mantle).
		Foreground(Text)

	DividerStyle = lipgloss.NewStyle().
			Foreground(Surface0).
			Background(Mantle)

	CanvasDividerStyle = lipgloss.NewStyle().
				Foreground(Surface0).
				Background(Crust)

	BrandStyle = lipgloss.NewStyle().
			Foreground(Mauve).
			Background(Base).
			Bold(true)

	VersionStyle = lipgloss.NewStyle().
			Foreground(Overlay1).
			Background(Base)

	BorderActive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Blue).
			BorderBackground(Crust).
			Background(Base)

	BorderInactive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Surface0).
			BorderBackground(Crust).
			Background(Mantle)

	NavBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Surface0).
			BorderBackground(Mantle).
			Background(Base)

	WinterBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Surface0).
			BorderBackground(Mantle).
			Background(Base)

	SidebarItem = lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Base)

	SidebarItemSelected = lipgloss.NewStyle().
				Foreground(Text).
				Background(Surface0).
				Bold(true)

	SidebarBadge = lipgloss.NewStyle().
			Foreground(Overlay1).
			Background(Base)

	SidebarBadgeSelected = lipgloss.NewStyle().
				Foreground(Blue).
				Background(Surface0).
				Bold(true)

	SectionTitle = lipgloss.NewStyle().
			Foreground(Blue).
			Background(Base).
			Bold(true)

	TableHeader = lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Base).
			Bold(true)

	TableSeparator = lipgloss.NewStyle().
			Foreground(Surface0).
			Background(Base)

	RowNormal = lipgloss.NewStyle().
			Foreground(Text).
			Background(Base)

	RowSelected = lipgloss.NewStyle().
			Foreground(Text).
			Background(Surface0).
			Bold(true)

	RowMuted = lipgloss.NewStyle().
			Foreground(Overlay1).
			Background(Base)

	TagPill = lipgloss.NewStyle().
		Foreground(Blue).
		Background(Surface0).
		Padding(0, 1)

	ActionPill = lipgloss.NewStyle().
			Foreground(Mauve).
			Background(Surface0).
			Bold(true).
			Padding(0, 1)

	ActionBarPill = lipgloss.NewStyle().
			Foreground(Text).
			Background(Base).
			Padding(0, 1)

	ActionBarPrimaryPill = lipgloss.NewStyle().
				Foreground(Crust).
				Background(Blue).
				Bold(true).
				Padding(0, 1)

	SearchPill = lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Base).
			Padding(0, 1)

	SearchPillActive = lipgloss.NewStyle().
				Foreground(Text).
				Background(Surface0).
				Bold(true).
				Padding(0, 1)

	SearchBarBlurred = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(Surface0).
				BorderBackground(Crust).
				Background(Base).
				Foreground(Text).
				Padding(0, 1)

	SearchBarFocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(Blue).
				BorderBackground(Crust).
				Background(Base).
				Foreground(Text).
				Padding(0, 1)

	StatusActive = lipgloss.NewStyle().
			Foreground(Green).
			Background(Base).
			Bold(true)

	StatusStarting = lipgloss.NewStyle().
			Foreground(Yellow).
			Background(Base).
			Bold(true)

	StatusStopped = lipgloss.NewStyle().
			Foreground(Overlay1).
			Background(Base)

	StatusError = lipgloss.NewStyle().
			Foreground(Red).
			Background(Base).
			Bold(true)

	InspectorTitle = lipgloss.NewStyle().
			Foreground(Mauve).
			Background(Mantle).
			Bold(true)

	InspectorLabel = lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Mantle)

	InspectorValue = lipgloss.NewStyle().
			Foreground(Text).
			Background(Mantle)

	InspectorMuted = lipgloss.NewStyle().
			Foreground(Overlay1).
			Background(Mantle)

	MiniCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Surface0).
			BorderBackground(Mantle).
			Background(Base).
			Foreground(Text).
			Padding(0, 1)

	DropdownBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Surface1).
			BorderBackground(Crust).
			Background(Base).
			Foreground(Text)

	PrimaryConnectBtn = lipgloss.NewStyle().
				Foreground(Crust).
				Background(Blue).
				Bold(true).
				Align(lipgloss.Center)

	ModalBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Blue).
			BorderBackground(Crust).
			Background(Base).
			Foreground(Text).
			Padding(1, 2)

	ModalTitle = lipgloss.NewStyle().
			Foreground(Blue).
			Background(Base).
			Bold(true)

	InputLabel = lipgloss.NewStyle().
			Foreground(Blue).
			Background(Base).
			Bold(true)

	InputHint = lipgloss.NewStyle().
			Foreground(Overlay1).
			Background(Base)

	ButtonActive = lipgloss.NewStyle().
			Foreground(Crust).
			Background(Blue).
			Bold(true).
			Padding(0, 2)

	ButtonDanger = lipgloss.NewStyle().
			Foreground(Crust).
			Background(Red).
			Bold(true).
			Padding(0, 2)

	ButtonInactive = lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Mantle).
			Padding(0, 2)

	ToastSuccess = lipgloss.NewStyle().
			Foreground(Crust).
			Background(Green).
			Bold(true).
			Padding(0, 1)

	ToastError = lipgloss.NewStyle().
			Foreground(Crust).
			Background(Red).
			Bold(true).
			Padding(0, 1)

	ToastInfo = lipgloss.NewStyle().
			Foreground(Crust).
			Background(Blue).
			Bold(true).
			Padding(0, 1)

	KeyHintKey = lipgloss.NewStyle().
			Foreground(Blue).
			Background(Mantle).
			Bold(true)

	KeyHintDesc = lipgloss.NewStyle().
			Foreground(Subtext0).
			Background(Mantle)

	FooterBar = lipgloss.NewStyle().
			Background(Mantle).
			Foreground(Subtext0)

	TopBarBg = lipgloss.NewStyle().
			Background(Mantle).
			Foreground(Subtext0)

	TabActive = lipgloss.NewStyle().
			Background(Surface0).
			Foreground(Text).
			Bold(true).
			Padding(0, 1)

	TabInactive = lipgloss.NewStyle().
			Background(Mantle).
			Foreground(Subtext0).
			Padding(0, 1)

	TabPlus = lipgloss.NewStyle().
		Background(Mantle).
		Foreground(Blue).
		Bold(true).
		Padding(0, 1)

	CardNormal = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Surface0).
			BorderBackground(Crust).
			Background(Base).
			Foreground(Text).
			Padding(1, 2)

	CardSelected = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Blue).
			BorderBackground(Crust).
			Background(Surface0).
			Foreground(Text).
			Padding(1, 2)

	FormSectionHeader = lipgloss.NewStyle().
				Foreground(Blue).
				Background(Base).
				Bold(true)

	InputBoxBlurred = lipgloss.NewStyle().
			Foreground(Text).
			Background(Mantle).
			Padding(0, 1)

	InputBoxFocused = lipgloss.NewStyle().
			Foreground(Text).
			Background(Surface0).
			Padding(0, 1)
)

// SpaceBg renders n spaces carrying an explicit background color.
func SpaceBg(bg lipgloss.Color, n int) string {
	if n <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", n))
}
