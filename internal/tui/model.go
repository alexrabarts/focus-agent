package tui

import (
	"fmt"
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
	"github.com/alexrabarts/focus-agent/internal/scheduler"
)

type view int

const (
	tasksView view = iota
	prioritiesView
	queueView
	threadsView
	statsView
)

// tickMsg is sent when it's time to refresh
type tickMsg struct{}

// renderTickMsg is sent every second to update the timestamp display
type renderTickMsg struct{}

// tick returns a command that sends a tickMsg after the configured interval
func tick(cfg *config.Config) tea.Cmd {
	if cfg.TUI.AutoRefreshSeconds <= 0 {
		return nil
	}
	return tea.Tick(time.Duration(cfg.TUI.AutoRefreshSeconds)*time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// renderTick returns a command that sends a renderTickMsg every second
func renderTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return renderTickMsg{}
	})
}

type Model struct {
	currentView view
	width       int
	height      int

	// Dependencies
	database  *db.DB
	clients   *google.Clients
	llm       llm.Client
	planner   *planner.Planner
	config    *config.Config
	apiClient *APIClient

	// Sub-models
	tasksModel      TasksModel
	prioritiesModel PrioritiesModel
	queueModel      QueueModel
	statsModel      StatsModel
	threadsModel    ThreadsModel

	// State
	lastRefreshTime time.Time
	logBuffer       *LogBuffer
}

func NewModel(database *db.DB, clients *google.Clients, llmClient llm.Client, plannerService *planner.Planner, cfg *config.Config, logBuffer *LogBuffer) Model {
	// Initialize API client if remote mode is configured
	var apiClient *APIClient
	var sched *scheduler.Scheduler

	if cfg.Remote.URL != "" {
		apiClient = NewAPIClient(cfg)
	} else {
		// For local mode, create a scheduler for processing
		sched = scheduler.New(database, clients, llmClient, plannerService, cfg)
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
		prioritiesModel: NewPrioritiesModel(cfg, plannerService, apiClient),
		queueModel:      NewQueueModel(database, apiClient, sched),
		statsModel:      NewStatsModel(database, apiClient),
		threadsModel:    NewThreadsModel(database, apiClient),
		lastRefreshTime: time.Now(),
		logBuffer:       logBuffer,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tasksModel.fetchTasks(),
		m.statsModel.fetchStats(),
		m.queueModel.fetchQueue(),
		m.threadsModel.fetchThreads(),
		tick(m.config),  // Start auto-refresh ticker
		renderTick(),    // Start render ticker for timestamp updates
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Calculate content area height (total - header - footer - margins)
		headerHeight := 3
		footerHeight := 2
		contentHeight := m.height - headerHeight - footerHeight - 4
		if contentHeight < 10 {
			contentHeight = 10 // Minimum height
		}

		// Update all sub-model viewports
		m.tasksModel.SetSize(m.width-4, contentHeight)
		m.threadsModel.SetSize(m.width-4, contentHeight)
		m.queueModel.SetSize(m.width-4, contentHeight)
		m.prioritiesModel.SetSize(m.width-4, contentHeight)
		m.statsModel.SetSize(m.width-4, contentHeight)

		return m, nil

	case tickMsg:
		// Update last refresh time and auto-refresh current view and stats (for footer), then schedule next tick
		m.lastRefreshTime = time.Now()
		return m, tea.Batch(
			m.refreshCurrentView(),
			m.statsModel.fetchStats(), // Always refresh stats for footer queue count
			tick(m.config),
		)

	case renderTickMsg:
		// Just schedule the next render tick - this triggers a re-render to update the timestamp
		return m, renderTick()

	case tea.KeyMsg:
		// Check if priorities view is in input mode
		inInputMode := m.currentView == prioritiesView && m.prioritiesModel.IsInInputMode()

		// Check if threads view is in detail mode
		inThreadDetail := m.currentView == threadsView && m.threadsModel.selectedThread != nil

		// Check if queue view is in detail mode
		inQueueDetail := m.currentView == queueView && m.queueModel.selectedItem != nil

		// Only handle navigation keys when not in input mode or detail view
		if !inInputMode && !inThreadDetail && !inQueueDetail {
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit

			case "left", "h":
				// Move to previous tab
				if m.currentView > 0 {
					m.currentView--
				} else {
					m.currentView = statsView
				}
				return m, m.refreshCurrentView()

			case "right", "l":
				// Move to next tab
				if m.currentView < statsView {
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

	// Always update stats model (for footer), then update current view
	m.statsModel, _ = m.statsModel.Update(msg)

	var cmd tea.Cmd
	switch m.currentView {
	case tasksView:
		tasksModelPtr, tasksCmd := m.tasksModel.Update(msg)
		m.tasksModel = *tasksModelPtr
		cmd = tasksCmd
	case prioritiesView:
		m.prioritiesModel, cmd = m.prioritiesModel.Update(msg)
	case queueView:
		m.queueModel, cmd = m.queueModel.Update(msg)
	case statsView:
		// Already updated above
	case threadsView:
		m.threadsModel, cmd = m.threadsModel.Update(msg)
	}

	return m, cmd
}

func (m Model) refreshCurrentView() tea.Cmd {
	switch m.currentView {
	case tasksView:
		return m.tasksModel.fetchTasks()
	case queueView:
		return m.queueModel.fetchQueue()
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
	case queueView:
		content = m.queueModel.View()
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
	for i, label := range []string{"Tasks", "Priorities", "Queue", "Threads", "About"} {
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

	logStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Padding(0, 1)

	footer := "q: quit | ←/→: switch tabs | ↑/↓: navigate | enter: select | c: complete task"

	// Add status information
	stats := m.statsModel.stats
	if stats.ThreadsNeedingAI > 0 {
		footer += fmt.Sprintf(" | 📋 Queue: %d", stats.ThreadsNeedingAI)
	}

	// Add last refresh time if auto-refresh is enabled
	if m.config.TUI.AutoRefreshSeconds > 0 {
		footer += fmt.Sprintf(" | %s", m.formatLastRefresh())
	}

	footerText := helpStyle.Render(footer)

	// Add recent log message if available
	if m.logBuffer != nil {
		if latest := m.logBuffer.GetLatest(); latest != nil {
			// Show log message if it's recent (within last 10 seconds)
			if time.Since(latest.Timestamp) < 10*time.Second {
				// Truncate long messages
				msg := latest.Message
				if len(msg) > 100 {
					msg = msg[:97] + "..."
				}
				logLine := logStyle.Render(fmt.Sprintf("⚙️  %s", msg))
				footerText = footerText + "\n" + logLine
			}
		}
	}

	return footerText
}

// formatLastRefresh returns a human-readable string for when data was last refreshed
func (m Model) formatLastRefresh() string {
	if m.lastRefreshTime.IsZero() {
		return "Updated: never"
	}

	elapsed := time.Since(m.lastRefreshTime)

	if elapsed < 30*time.Second {
		// Show "just now" for the first 30 seconds (normal refresh interval)
		return "Updated: just now"
	} else if elapsed < time.Minute {
		// Show seconds from 30-59 seconds (indicates a delay)
		return fmt.Sprintf("Updated: %ds ago", int(elapsed.Seconds()))
	} else if elapsed < time.Hour {
		// After 1 minute, round to minutes
		mins := int(elapsed.Minutes())
		return fmt.Sprintf("Updated: %dm ago", mins)
	} else if elapsed < 24*time.Hour {
		hours := int(elapsed.Hours())
		return fmt.Sprintf("Updated: %dh ago", hours)
	} else {
		// For older updates, show the actual time
		return fmt.Sprintf("Updated: %s", m.lastRefreshTime.Format("15:04"))
	}
}

func Start(database *db.DB, clients *google.Clients, llmClient llm.Client, plannerService *planner.Planner, cfg *config.Config) error {
	// Validate remote mode configuration
	if cfg.Remote.URL != "" && cfg.Remote.AuthKey == "" {
		return fmt.Errorf("remote mode is configured (url=%s) but auth_key is missing\n\nPlease set FOCUS_AGENT_AUTH_KEY environment variable in ~/.env and restart your shell", cfg.Remote.URL)
	}

	// Create log buffer to capture background logs
	logBuffer := NewLogBuffer(10) // Keep last 10 log messages

	// Redirect log output to buffer
	originalOutput := log.Writer()
	log.SetOutput(logBuffer)
	defer log.SetOutput(originalOutput)

	// Also redirect stderr to dev/null to prevent any direct stderr writes from disrupting TUI
	originalStderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stderr = devNull
		defer func() {
			os.Stderr = originalStderr
			devNull.Close()
		}()
	}

	m := NewModel(database, clients, llmClient, plannerService, cfg, logBuffer)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
