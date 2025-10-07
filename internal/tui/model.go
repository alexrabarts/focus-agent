package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

type view int

const (
	tasksView view = iota
	prioritiesView
	statsView
	threadsView
)

type Model struct {
	currentView view
	width       int
	height      int

	// Dependencies
	database  *db.DB
	clients   *google.Clients
	llm       *llm.GeminiClient
	planner   *planner.Planner
	config    *config.Config
	apiClient *APIClient

	// Sub-models
	tasksModel      TasksModel
	prioritiesModel PrioritiesModel
	statsModel      StatsModel
	threadsModel    ThreadsModel
}

func NewModel(database *db.DB, clients *google.Clients, llmClient *llm.GeminiClient, plannerService *planner.Planner, cfg *config.Config) Model {
	// Initialize API client if remote mode is configured
	var apiClient *APIClient
	if cfg.Remote.URL != "" {
		apiClient = NewAPIClient(cfg)
	}

	return Model{
		currentView:     tasksView,
		database:        database,
		clients:         clients,
		llm:             llmClient,
		planner:         plannerService,
		config:          cfg,
		apiClient:       apiClient,
		tasksModel:      NewTasksModel(database, plannerService, apiClient),
		prioritiesModel: NewPrioritiesModel(cfg, apiClient),
		statsModel:      NewStatsModel(database, apiClient),
		threadsModel:    NewThreadsModel(database, apiClient),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tasksModel.fetchTasks(),
		m.statsModel.fetchStats(),
		m.threadsModel.fetchThreads(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Check if priorities view is in input mode
		inInputMode := m.currentView == prioritiesView && m.prioritiesModel.IsInInputMode()

		// Only handle navigation keys when not in input mode
		if !inInputMode {
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit

			case "left", "h":
				// Move to previous tab
				if m.currentView > 0 {
					m.currentView--
				} else {
					m.currentView = threadsView
				}
				return m, m.refreshCurrentView()

			case "right", "l":
				// Move to next tab
				if m.currentView < threadsView {
					m.currentView++
				} else {
					m.currentView = tasksView
				}
				return m, m.refreshCurrentView()
			}
		} else {
			// In input mode, only allow quit
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
		}
	}

	// Update current view
	var cmd tea.Cmd
	switch m.currentView {
	case tasksView:
		m.tasksModel, cmd = m.tasksModel.Update(msg)
	case prioritiesView:
		m.prioritiesModel, cmd = m.prioritiesModel.Update(msg)
	case statsView:
		m.statsModel, cmd = m.statsModel.Update(msg)
	case threadsView:
		m.threadsModel, cmd = m.threadsModel.Update(msg)
	}

	return m, cmd
}

func (m Model) refreshCurrentView() tea.Cmd {
	switch m.currentView {
	case tasksView:
		return m.tasksModel.fetchTasks()
	case statsView:
		return m.statsModel.fetchStats()
	case threadsView:
		return m.threadsModel.fetchThreads()
	default:
		return nil
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Header
	header := m.renderHeader()

	// Content
	var content string
	switch m.currentView {
	case tasksView:
		content = m.tasksModel.View()
	case prioritiesView:
		content = m.prioritiesModel.View()
	case statsView:
		content = m.statsModel.View()
	case threadsView:
		content = m.threadsModel.View()
	}

	// Footer
	footer := m.renderFooter()

	return fmt.Sprintf("%s\n\n%s\n\n%s", header, content, footer)
}

func (m Model) renderHeader() string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("86")).
		Padding(0, 1)

	tabStyle := lipgloss.NewStyle().
		Padding(0, 2)

	activeTabStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("86")).
		Background(lipgloss.Color("236")).
		Padding(0, 2)

	title := titleStyle.Render("Focus Agent")

	tabs := ""
	for i, label := range []string{"Tasks", "Priorities", "About", "Threads"} {
		if view(i) == m.currentView {
			tabs += activeTabStyle.Render(label)
		} else {
			tabs += tabStyle.Render(label)
		}
	}

	headerStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1)

	return headerStyle.Render(title + "  " + tabs)
}

func (m Model) renderFooter() string {
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(0, 1)

	return helpStyle.Render("q: quit | ←/→: switch tabs | ↑/↓: navigate | enter: select")
}

func Start(database *db.DB, clients *google.Clients, llmClient *llm.GeminiClient, plannerService *planner.Planner, cfg *config.Config) error {
	m := NewModel(database, clients, llmClient, plannerService, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
