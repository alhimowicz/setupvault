package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) contentWidth() int { return max(1, m.width-4) }

func wrapText(text string, width int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\t", "    ")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(ansi.Strip(line), "> ") {
			// Keep the selection marker attached to long paths and package IDs.
			lines[i] = ansi.Hardwrap(line, width, true)
		} else {
			lines[i] = ansi.Wrap(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) layout() ([]string, string, string, int) {
	width := m.contentWidth()
	raw := strings.Split(m.content(), "\n")
	count := min(3, len(raw))
	header := wrapText(strings.Join(raw[:count], "\n"), width)
	body := strings.Join(raw[count:], "\n")
	if width < 64 || m.height < 12 || lipgloss.Height(header) > 3 {
		header = m.styles.heading.Render("SetupVault") + "\n" + m.styles.selected.Render(fmt.Sprintf("%d/%d %s", m.page+1, len(pages), pages[m.page]))
		header = wrapText(header, width)
		body = "Profile: " + m.app.Profile.Name + "\n\n" + body
	}
	help := "PgUp/PgDn scroll · Tab/Shift+Tab pages · ↑↓ select · q quit"
	if width < 60 {
		help = "PgUp/PgDn scroll · Tab pages · q quit"
	}
	if width < 40 {
		help = "PgUp/Dn · Tab · q quit"
	}
	if width < 22 {
		help = "PgUp/Dn q quit"
	}
	help = m.styles.muted.Render(wrapText(help, width))
	lines := strings.Split(wrapText(body, width), "\n")
	room := max(1, m.height-2-lipgloss.Height(header)-lipgloss.Height(help))
	return lines, header, help, room
}

func (m Model) maxScroll() int {
	lines, _, _, room := m.layout()
	return max(0, len(lines)-room)
}

func (m *Model) keepSelectionVisible() {
	lines, _, _, room := m.layout()
	marker := "> "
	if m.form != nil {
		marker = "▸ "
	}
	for start, line := range lines {
		if !strings.HasPrefix(ansi.Strip(line), marker) {
			continue
		}
		end := start + 1
		if m.form != nil {
			for end < len(lines) && strings.TrimSpace(ansi.Strip(lines[end])) != "" {
				end++
			}
		}
		if start < m.scroll {
			m.scroll = start
		}
		if end > m.scroll+room {
			m.scroll = end - room
		}
		m.scroll = max(0, min(m.scroll, len(lines)-room))
		return
	}
}

func (m Model) View() tea.View {
	if m.width < 20 || m.height < 6 {
		v := tea.NewView(lipgloss.NewStyle().MaxHeight(max(1, m.height)).Render(wrapText("Resize terminal to at least 20 columns × 6 rows. q quits.", max(1, m.width))))
		v.AltScreen = true
		return v
	}
	lines, header, help, room := m.layout()
	start := max(0, min(m.scroll, len(lines)-room))
	end := min(len(lines), start+room)
	content := header + "\n" + strings.Join(lines[start:end], "\n")
	content += strings.Repeat("\n", max(1, room-(end-start)+1)) + help
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(content))
	v.AltScreen = true
	return v
}
