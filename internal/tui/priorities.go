package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/alexrabarts/focus-agent/internal/config"
)

type PrioritiesModel struct {
	config *config.Config
}

func NewPrioritiesModel(cfg *config.Config) PrioritiesModel {
	return PrioritiesModel{
		config: cfg,
	}
}

func (m PrioritiesModel) Update(msg tea.Msg) (PrioritiesModel, tea.Cmd) {
	return m, nil
}

func (m PrioritiesModel) View() string {
	var b strings.Builder

	contentStyle := lipgloss.NewStyle().
		Padding(0, 2)

	// OKRs
	if len(m.config.Priorities.OKRs) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Padding(0, 1)

		b.WriteString(headerStyle.Render("📊 Objectives & Key Results (OKRs)") + "\n\n")

		for i, okr := range m.config.Priorities.OKRs {
			itemStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 2)
			b.WriteString(itemStyle.Render(fmt.Sprintf("%d. %s", i+1, okr)) + "\n")
		}
		b.WriteString("\n")
	}

	// Focus Areas
	if len(m.config.Priorities.FocusAreas) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Padding(0, 1)

		b.WriteString(headerStyle.Render("🎯 Focus Areas") + "\n\n")

		for i, area := range m.config.Priorities.FocusAreas {
			itemStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 2)
			b.WriteString(itemStyle.Render(fmt.Sprintf("%d. %s", i+1, area)) + "\n")
		}
		b.WriteString("\n")
	}

	// Key Projects
	if len(m.config.Priorities.KeyProjects) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Padding(0, 1)

		b.WriteString(headerStyle.Render("🚀 Key Projects") + "\n\n")

		for i, project := range m.config.Priorities.KeyProjects {
			itemStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 2)
			b.WriteString(itemStyle.Render(fmt.Sprintf("%d. %s", i+1, project)) + "\n")
		}
		b.WriteString("\n")
	}

	// Key Stakeholders
	if len(m.config.Priorities.KeyStakeholders) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Padding(0, 1)

		b.WriteString(headerStyle.Render("👥 Key Stakeholders") + "\n\n")

		for i, stakeholder := range m.config.Priorities.KeyStakeholders {
			itemStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 2)
			b.WriteString(itemStyle.Render(fmt.Sprintf("%d. %s", i+1, stakeholder)) + "\n")
		}
		b.WriteString("\n")
	}

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString(helpStyle.Render("To edit priorities, modify ~/.focus-agent/config.yaml"))

	return contentStyle.Render(b.String())
}
