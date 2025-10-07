package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	"github.com/alexrabarts/focus-agent/internal/config"
	"gopkg.in/yaml.v3"
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
)

type PrioritiesModel struct {
	config             *config.Config
	configPath         string
	apiClient          *APIClient
	currentSection     prioritySection
	cursor             int
	mode               inputMode
	textInput          textinput.Model
	editingIndex       int
	previousPriorities *config.Priorities
	message            string
}

func NewPrioritiesModel(cfg *config.Config, apiClient *APIClient) PrioritiesModel {
	ti := textinput.New()
	ti.Placeholder = "Enter new priority..."
	ti.CharLimit = 200

	return PrioritiesModel{
		config:             cfg,
		configPath:         os.ExpandEnv("$HOME/.focus-agent/config.yaml"),
		apiClient:          apiClient,
		currentSection:     okrsSection,
		cursor:             0,
		mode:               normalMode,
		textInput:          ti,
		previousPriorities: nil,
	}
}

func (m PrioritiesModel) IsInInputMode() bool {
	return m.mode == addingMode || m.mode == editingMode
}

func (m PrioritiesModel) Update(msg tea.Msg) (PrioritiesModel, tea.Cmd) {
	var cmd tea.Cmd

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
						m.message = "Priority added"
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
						m.message = "Priority updated"
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

		case "d", "delete", "backspace":
			// Delete current item
			m.saveStateForUndo()
			if m.deletePriority() {
				if err := m.saveConfig(); err != nil {
					m.message = fmt.Sprintf("Error saving: %v", err)
				} else {
					m.message = "Priority deleted"
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
					m.message = "Undone"
					m.previousPriorities = nil
				}
			}
		}
	}

	return m, nil
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

	// Local mode: write to config file
	// Read the current config file
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Parse into generic map to preserve structure
	var configMap map[string]interface{}
	if err := yaml.Unmarshal(data, &configMap); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Update priorities section
	priorities := make(map[string]interface{})
	priorities["okrs"] = m.config.Priorities.OKRs
	priorities["focus_areas"] = m.config.Priorities.FocusAreas
	priorities["key_projects"] = m.config.Priorities.KeyProjects
	priorities["key_stakeholders"] = m.config.Priorities.KeyStakeholders
	configMap["priorities"] = priorities

	// Marshal back to YAML
	newData, err := yaml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write back to file
	if err := os.WriteFile(m.configPath, newData, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

func (m PrioritiesModel) View() string {
	var b strings.Builder

	contentStyle := lipgloss.NewStyle().
		Padding(0, 2)

	// Render each section
	m.renderSection(&b, "📊 Objectives & Key Results (OKRs)", okrsSection, m.config.Priorities.OKRs)
	m.renderSection(&b, "🎯 Focus Areas", focusAreasSection, m.config.Priorities.FocusAreas)
	m.renderSection(&b, "🚀 Key Projects", projectsSection, m.config.Priorities.KeyProjects)
	m.renderSection(&b, "👥 Key Stakeholders", stakeholdersSection, m.config.Priorities.KeyStakeholders)

	// Input field when adding or editing
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

	helpText := "tab: switch sections | enter: edit | a: add | d: delete | u: undo"
	if m.previousPriorities != nil {
		helpText += " | ↶ Undo available"
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render(helpText))

	return contentStyle.Render(b.String())
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
