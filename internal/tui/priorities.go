package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

type prioritySection int

const (
	okrsSection prioritySection = iota
	focusAreasSection
	projectsSection
	stakeholdersSection
)

type inputMode int

const (
	normalMode inputMode = iota
	addingMode
	editingMode
	reorderingMode
)

type prioritiesLoadedMsg struct {
	priorities *config.Priorities
	err        error
}

type PrioritiesModel struct {
	config             *config.Config
	configPath         string
	apiClient          *APIClient
	planner            *planner.Planner
	currentSection     prioritySection
	cursor             int
	mode               inputMode
	textInput          textinput.Model
	editingIndex       int
	previousPriorities *config.Priorities
	message            string
	viewport viewport.Model
	ready bool
}

func NewPrioritiesModel(cfg *config.Config, plannerService *planner.Planner, apiClient *APIClient) PrioritiesModel {
	ti := textinput.New()
	ti.Placeholder = "Enter new priority..."
	ti.CharLimit = 200

	m := PrioritiesModel{
		config:             cfg,
		configPath:         os.ExpandEnv("$HOME/.focus-agent/config.yaml"),
		apiClient:          apiClient,
		planner:            plannerService,
		currentSection:     okrsSection,
		cursor:             0,
		mode:               normalMode,
		textInput:          ti,
		previousPriorities: nil,
		viewport:           viewport.New(80, 20),
	}

	// Load priorities from database (if available)
	m.loadPriorities()

	return m
}

// loadPriorities loads priorities from the database via planner or API
func (m *PrioritiesModel) loadPriorities() {
	if m.apiClient != nil {
		// Remote mode: fetch from API
		priorities, err := m.apiClient.GetPriorities()
		if err == nil {
			m.config.Priorities = *priorities
		}
	} else if m.planner != nil {
		// Local mode: load from database via planner (with config fallback)
		dbPriorities := m.planner.GetPriorities()
		m.config.Priorities = *dbPriorities
	}
}

// fetchPriorities returns a command to reload priorities from the database/API
func (m PrioritiesModel) fetchPriorities() tea.Cmd {
	return func() tea.Msg {
		var priorities *config.Priorities
		var err error

		if m.apiClient != nil {
			// Remote mode: fetch from API
			priorities, err = m.apiClient.GetPriorities()
		} else if m.planner != nil {
			// Local mode: load from database via planner (with config fallback)
			priorities = m.planner.GetPriorities()
		}

		return prioritiesLoadedMsg{priorities: priorities, err: err}
	}
}

// SetSize updates the viewport dimensions
func (m *PrioritiesModel) SetSize(width, height int) {
	m.viewport.Width = width
	m.viewport.Height = height
	m.ready = true
}

func (m PrioritiesModel) IsInInputMode() bool {
	return m.mode == addingMode || m.mode == editingMode || m.mode == reorderingMode
}

func (m PrioritiesModel) Update(msg tea.Msg) (PrioritiesModel, tea.Cmd) {
	var cmd tea.Cmd
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)

	// Handle priorities loaded message
	switch msg := msg.(type) {
	case prioritiesLoadedMsg:
		if msg.err == nil && msg.priorities != nil {
			m.config.Priorities = *msg.priorities
		}
		return m, nil
	}

	// Handle adding mode
	if m.mode == addingMode {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				// Add the new item
				value := strings.TrimSpace(m.textInput.Value())
				if value != "" {
					m.saveStateForUndo()
					m.addPriority(value)
					if err := m.saveConfig(); err != nil {
						m.message = fmt.Sprintf("Error saving: %v", err)
					} else {
						m.message = "Priority added (reprioritising in background...)"
					}
				}
				m.mode = normalMode
				m.textInput.SetValue("")
				m.textInput.Blur()
				return m, nil

			case "esc":
				// Cancel adding
				m.mode = normalMode
				m.textInput.SetValue("")
				m.textInput.Blur()
				return m, nil
			}
		}

		// Update text input
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}

	// Handle editing mode
	if m.mode == editingMode {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				// Update the item
				value := strings.TrimSpace(m.textInput.Value())
				if value != "" {
					m.saveStateForUndo()
					m.updatePriority(m.editingIndex, value)
					if err := m.saveConfig(); err != nil {
						m.message = fmt.Sprintf("Error saving: %v", err)
					} else {
						m.message = "Priority updated (reprioritising in background...)"
					}
				}
				m.mode = normalMode
				m.textInput.SetValue("")
				m.textInput.Blur()
				return m, nil

			case "esc":
				// Cancel editing
				m.mode = normalMode
				m.textInput.SetValue("")
				m.textInput.Blur()
				return m, nil
			}
		}

		// Update text input
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}

	// Handle reordering mode
	if m.mode == reorderingMode {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				// Parse the position
				value := strings.TrimSpace(m.textInput.Value())
				if value != "" {
					newPos, err := fmt.Sscanf(value, "%d", new(int))
					var position int
					if err == nil && newPos == 1 {
						fmt.Sscanf(value, "%d", &position)
						// Convert to 0-based index
						position--
						sectionLen := m.getSectionLength(m.currentSection)
						if position >= 0 && position < sectionLen {
							m.saveStateForUndo()
							m.reorderPriority(m.cursor, position)
							if err := m.saveConfig(); err != nil {
								m.message = fmt.Sprintf("Error saving: %v", err)
							} else {
								m.message = fmt.Sprintf("Moved to position %d", position+1)
								m.cursor = position
							}
						} else {
							m.message = fmt.Sprintf("Invalid position (must be 1-%d)", sectionLen)
						}
					} else {
						m.message = "Invalid number"
					}
				}
				m.mode = normalMode
				m.textInput.SetValue("")
				m.textInput.Blur()
				return m, nil

			case "esc":
				// Cancel reordering
				m.mode = normalMode
				m.textInput.SetValue("")
				m.textInput.Blur()
				return m, nil
			}
		}

		// Update text input
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}

	// Normal mode
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			} else {
				// At beginning of section, move to previous section
				if m.currentSection == 0 {
					m.currentSection = 3
				} else {
					m.currentSection--
				}
				// Move cursor to last item of new section
				m.cursor = m.getSectionLength(m.currentSection) - 1
				if m.cursor < 0 {
					m.cursor = 0
				}
			}

		case "down", "j":
			maxCursor := m.getSectionLength(m.currentSection) - 1
			if m.cursor < maxCursor {
				m.cursor++
			} else {
				// At end of section, move to next section
				m.currentSection = (m.currentSection + 1) % 4
				m.cursor = 0
			}

		case "tab":
			// Switch to next section
			m.currentSection = (m.currentSection + 1) % 4
			m.cursor = 0

		case "shift+tab":
			// Switch to previous section
			if m.currentSection == 0 {
				m.currentSection = 3
			} else {
				m.currentSection--
			}
			m.cursor = 0

		case "enter":
			// Start editing mode
			if currentValue := m.getCurrentPriorityValue(); currentValue != "" {
				m.mode = editingMode
				m.editingIndex = m.cursor
				m.textInput.SetValue(currentValue)
				m.textInput.Focus()
				// Position cursor at end
				m.textInput.CursorEnd()
				return m, textinput.Blink
			}

		case "a":
			// Start adding mode
			m.mode = addingMode
			m.textInput.SetValue("")
			m.textInput.Focus()
			return m, textinput.Blink

		case "o":
			// Start reordering mode
			if m.getSectionLength(m.currentSection) > 0 {
				m.mode = reorderingMode
				m.textInput.SetValue("")
				m.textInput.Placeholder = fmt.Sprintf("Enter position (1-%d)...", m.getSectionLength(m.currentSection))
				m.textInput.Focus()
				return m, textinput.Blink
			}

		case "d", "delete", "backspace":
			// Delete current item
			m.saveStateForUndo()
			if m.deletePriority() {
				if err := m.saveConfig(); err != nil {
					m.message = fmt.Sprintf("Error saving: %v", err)
				} else {
					m.message = "Priority deleted (reprioritising in background...)"
				}
				// Adjust cursor if needed
				maxCursor := m.getSectionLength(m.currentSection) - 1
				if m.cursor > maxCursor && maxCursor >= 0 {
					m.cursor = maxCursor
				}
			}

		case "u":
			// Undo last change
			if m.previousPriorities != nil {
				m.config.Priorities = *m.previousPriorities
				if err := m.saveConfig(); err != nil {
					m.message = fmt.Sprintf("Error undoing: %v", err)
				} else {
					m.message = "Undone (reprioritising in background...)"
					m.previousPriorities = nil
				}
			}
		}
	}

	return m, vpCmd
}

func (m *PrioritiesModel) saveStateForUndo() {
	// Create a deep copy of current priorities
	m.previousPriorities = &config.Priorities{
		OKRs:            append([]string{}, m.config.Priorities.OKRs...),
		FocusAreas:      append([]string{}, m.config.Priorities.FocusAreas...),
		KeyProjects:     append([]string{}, m.config.Priorities.KeyProjects...),
		KeyStakeholders: append([]string{}, m.config.Priorities.KeyStakeholders...),
	}
}

func (m *PrioritiesModel) addPriority(value string) {
	switch m.currentSection {
	case okrsSection:
		m.config.Priorities.OKRs = append(m.config.Priorities.OKRs, value)
	case focusAreasSection:
		m.config.Priorities.FocusAreas = append(m.config.Priorities.FocusAreas, value)
	case projectsSection:
		m.config.Priorities.KeyProjects = append(m.config.Priorities.KeyProjects, value)
	case stakeholdersSection:
		m.config.Priorities.KeyStakeholders = append(m.config.Priorities.KeyStakeholders, value)
	}
}

func (m *PrioritiesModel) updatePriority(index int, value string) {
	switch m.currentSection {
	case okrsSection:
		if index < len(m.config.Priorities.OKRs) {
			m.config.Priorities.OKRs[index] = value
		}
	case focusAreasSection:
		if index < len(m.config.Priorities.FocusAreas) {
			m.config.Priorities.FocusAreas[index] = value
		}
	case projectsSection:
		if index < len(m.config.Priorities.KeyProjects) {
			m.config.Priorities.KeyProjects[index] = value
		}
	case stakeholdersSection:
		if index < len(m.config.Priorities.KeyStakeholders) {
			m.config.Priorities.KeyStakeholders[index] = value
		}
	}
}

func (m PrioritiesModel) getCurrentPriorityValue() string {
	switch m.currentSection {
	case okrsSection:
		if m.cursor < len(m.config.Priorities.OKRs) {
			return m.config.Priorities.OKRs[m.cursor]
		}
	case focusAreasSection:
		if m.cursor < len(m.config.Priorities.FocusAreas) {
			return m.config.Priorities.FocusAreas[m.cursor]
		}
	case projectsSection:
		if m.cursor < len(m.config.Priorities.KeyProjects) {
			return m.config.Priorities.KeyProjects[m.cursor]
		}
	case stakeholdersSection:
		if m.cursor < len(m.config.Priorities.KeyStakeholders) {
			return m.config.Priorities.KeyStakeholders[m.cursor]
		}
	}
	return ""
}

func (m *PrioritiesModel) deletePriority() bool {
	switch m.currentSection {
	case okrsSection:
		if m.cursor < len(m.config.Priorities.OKRs) {
			m.config.Priorities.OKRs = append(
				m.config.Priorities.OKRs[:m.cursor],
				m.config.Priorities.OKRs[m.cursor+1:]...,
			)
			return true
		}
	case focusAreasSection:
		if m.cursor < len(m.config.Priorities.FocusAreas) {
			m.config.Priorities.FocusAreas = append(
				m.config.Priorities.FocusAreas[:m.cursor],
				m.config.Priorities.FocusAreas[m.cursor+1:]...,
			)
			return true
		}
	case projectsSection:
		if m.cursor < len(m.config.Priorities.KeyProjects) {
			m.config.Priorities.KeyProjects = append(
				m.config.Priorities.KeyProjects[:m.cursor],
				m.config.Priorities.KeyProjects[m.cursor+1:]...,
			)
			return true
		}
	case stakeholdersSection:
		if m.cursor < len(m.config.Priorities.KeyStakeholders) {
			m.config.Priorities.KeyStakeholders = append(
				m.config.Priorities.KeyStakeholders[:m.cursor],
				m.config.Priorities.KeyStakeholders[m.cursor+1:]...,
			)
			return true
		}
	}
	return false
}

func (m *PrioritiesModel) reorderPriority(oldPos, newPos int) {
	if oldPos == newPos {
		return
	}

	switch m.currentSection {
	case okrsSection:
		if oldPos < len(m.config.Priorities.OKRs) && newPos < len(m.config.Priorities.OKRs) {
			item := m.config.Priorities.OKRs[oldPos]
			// Remove from old position
			m.config.Priorities.OKRs = append(
				m.config.Priorities.OKRs[:oldPos],
				m.config.Priorities.OKRs[oldPos+1:]...,
			)
			// Insert at new position
			m.config.Priorities.OKRs = append(
				m.config.Priorities.OKRs[:newPos],
				append([]string{item}, m.config.Priorities.OKRs[newPos:]...)...,
			)
		}
	case focusAreasSection:
		if oldPos < len(m.config.Priorities.FocusAreas) && newPos < len(m.config.Priorities.FocusAreas) {
			item := m.config.Priorities.FocusAreas[oldPos]
			m.config.Priorities.FocusAreas = append(
				m.config.Priorities.FocusAreas[:oldPos],
				m.config.Priorities.FocusAreas[oldPos+1:]...,
			)
			m.config.Priorities.FocusAreas = append(
				m.config.Priorities.FocusAreas[:newPos],
				append([]string{item}, m.config.Priorities.FocusAreas[newPos:]...)...,
			)
		}
	case projectsSection:
		if oldPos < len(m.config.Priorities.KeyProjects) && newPos < len(m.config.Priorities.KeyProjects) {
			item := m.config.Priorities.KeyProjects[oldPos]
			m.config.Priorities.KeyProjects = append(
				m.config.Priorities.KeyProjects[:oldPos],
				m.config.Priorities.KeyProjects[oldPos+1:]...,
			)
			m.config.Priorities.KeyProjects = append(
				m.config.Priorities.KeyProjects[:newPos],
				append([]string{item}, m.config.Priorities.KeyProjects[newPos:]...)...,
			)
		}
	case stakeholdersSection:
		if oldPos < len(m.config.Priorities.KeyStakeholders) && newPos < len(m.config.Priorities.KeyStakeholders) {
			item := m.config.Priorities.KeyStakeholders[oldPos]
			m.config.Priorities.KeyStakeholders = append(
				m.config.Priorities.KeyStakeholders[:oldPos],
				m.config.Priorities.KeyStakeholders[oldPos+1:]...,
			)
			m.config.Priorities.KeyStakeholders = append(
				m.config.Priorities.KeyStakeholders[:newPos],
				append([]string{item}, m.config.Priorities.KeyStakeholders[newPos:]...)...,
			)
		}
	}
}

func (m PrioritiesModel) getSectionLength(section prioritySection) int {
	switch section {
	case okrsSection:
		return len(m.config.Priorities.OKRs)
	case focusAreasSection:
		return len(m.config.Priorities.FocusAreas)
	case projectsSection:
		return len(m.config.Priorities.KeyProjects)
	case stakeholdersSection:
		return len(m.config.Priorities.KeyStakeholders)
	}
	return 0
}

func (m PrioritiesModel) saveConfig() error {
	if m.apiClient != nil {
		// Use remote API
		return m.apiClient.UpdatePriorities(&m.config.Priorities)
	}

	// Local mode: save to database via planner
	if m.planner != nil {
		if err := m.planner.SavePriorities(&m.config.Priorities); err != nil {
			return fmt.Errorf("failed to save priorities to database: %w", err)
		}
	}

	// Trigger rescore of tasks with new priorities asynchronously
	// This can take several minutes due to LLM API calls, so don't block the UI
	if m.planner != nil {
		go func() {
			ctx := context.Background()
			// Recalculate task priorities with updated strategic priorities
			if err := m.planner.PrioritizeTasks(ctx); err != nil {
				// Silently fail - will retry on next scheduled run
				return
			}

			// Recalculate thread priorities based on updated task scores
			_ = m.planner.RecalculateThreadPriorities(ctx)
		}()
	}

	return nil
}

func (m PrioritiesModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	var b strings.Builder

	contentStyle := lipgloss.NewStyle().
		Padding(0, 2)

	// Render each section
	m.renderSection(&b, "📊 Objectives & Key Results (OKRs)", okrsSection, m.config.Priorities.OKRs)
	m.renderSection(&b, "🎯 Focus Areas", focusAreasSection, m.config.Priorities.FocusAreas)
	m.renderSection(&b, "🚀 Key Projects", projectsSection, m.config.Priorities.KeyProjects)
	m.renderSection(&b, "👥 Key Stakeholders", stakeholdersSection, m.config.Priorities.KeyStakeholders)

	// Input field when adding, editing, or reordering
	if m.mode == addingMode {
		inputStyle := lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(lipgloss.Color("86"))

		b.WriteString("\n")
		b.WriteString(inputStyle.Render("➕ Add new: ") + m.textInput.View() + "\n")
		b.WriteString(contentStyle.Render("Press Enter to add, Esc to cancel\n"))
	} else if m.mode == editingMode {
		inputStyle := lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(lipgloss.Color("226"))

		b.WriteString("\n")
		b.WriteString(inputStyle.Render("✏️  Edit: ") + m.textInput.View() + "\n")
		b.WriteString(contentStyle.Render("Press Enter to save, Esc to cancel\n"))
	} else if m.mode == reorderingMode {
		inputStyle := lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(lipgloss.Color("63"))

		b.WriteString("\n")
		b.WriteString(inputStyle.Render("🔢 Move to position: ") + m.textInput.View() + "\n")
		b.WriteString(contentStyle.Render("Press Enter to move, Esc to cancel\n"))
	}

	// Status message
	if m.message != "" {
		messageStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("86")).
			Padding(1, 2, 0, 2)
		b.WriteString("\n")
		b.WriteString(messageStyle.Render(m.message))
	}

	// Help text
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 2)

	helpText := "tab: switch sections | enter: edit | a: add | o: reorder | d: delete | u: undo"
	if m.previousPriorities != nil {
		helpText += " | ↶ Undo available"
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render(helpText))

	content := contentStyle.Render(b.String())
	m.viewport.SetContent(content)
	return m.viewport.View()
}

func (m PrioritiesModel) renderSection(b *strings.Builder, title string, section prioritySection, items []string) {
	isActive := section == m.currentSection && m.mode == normalMode

	// Header style
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("86")).
		Padding(0, 1)

	if isActive {
		headerStyle = headerStyle.
			Background(lipgloss.Color("236"))
	}

	b.WriteString(headerStyle.Render(title) + "\n\n")

	// Items
	if len(items) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 2)
		b.WriteString(emptyStyle.Render("  (empty)") + "\n\n")
		return
	}

	for i, item := range items {
		isSelected := isActive && i == m.cursor

		cursor := "  "
		if isSelected {
			cursor = "→ "
		}

		itemStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Padding(0, 2)

		if isSelected {
			itemStyle = itemStyle.
				Background(lipgloss.Color("236")).
				Bold(true)
		}

		text := fmt.Sprintf("%s%d. %s", cursor, i+1, item)
		b.WriteString(itemStyle.Render(text) + "\n")
	}

	b.WriteString("\n")
}
