package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/alexrabarts/focus-agent/internal/db"
)

type ThreadsModel struct {
	database  *db.DB
	apiClient *APIClient
	threads   []*db.Thread
	messages  map[string][]*db.Message // thread ID -> messages
	cursor    int
	loading   bool
	err       error
}

type threadsLoadedMsg struct {
	threads []*db.Thread
	err     error
}

func NewThreadsModel(database *db.DB, apiClient *APIClient) ThreadsModel {
	return ThreadsModel{
		database:  database,
		apiClient: apiClient,
		messages:  make(map[string][]*db.Message),
		loading:   true,
	}
}

func (m ThreadsModel) fetchThreads() tea.Cmd {
	return func() tea.Msg {
		var threads []*db.Thread
		var err error

		if m.apiClient != nil {
			// Use remote API
			threads, err = m.apiClient.GetThreads()
		} else {
			// Use local database
			threads, err = m.database.GetThreadsWithSummaries(50)
		}

		return threadsLoadedMsg{threads: threads, err: err}
	}
}

func (m ThreadsModel) Update(msg tea.Msg) (ThreadsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case threadsLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.threads = msg.threads
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.threads)-1 {
				m.cursor++
			}
		case "r":
			// Refresh threads
			m.loading = true
			return m, m.fetchThreads()
		}
	}

	return m, nil
}

func (m ThreadsModel) View() string {
	if m.loading {
		return "Loading threads..."
	}

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Padding(1)
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}

	if len(m.threads) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(1)
		return emptyStyle.Render("No threads with summaries found.")
	}

	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1)
	b.WriteString(headerStyle.Render(fmt.Sprintf("📧 Email Threads with AI Summaries (%d)", len(m.threads))) + "\n\n")

	// Show threads
	for i, thread := range m.threads {
		b.WriteString(m.renderThread(thread, i == m.cursor))
	}

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑/↓: navigate | r: refresh"))

	return b.String()
}

func (m ThreadsModel) renderThread(thread *db.Thread, selected bool) string {
	threadStyle := lipgloss.NewStyle().
		Padding(0, 2)

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Padding(0, 2)

	cursor := "  "
	if selected {
		cursor = "→ "
	}

	// Get the first message to show subject and from
	var subject, from string
	var timestamp time.Time

	// Get messages from cache or database
	messages, ok := m.messages[thread.ID]
	if !ok {
		// Fetch messages for this thread
		if m.apiClient != nil {
			messages, _ = m.apiClient.GetThreadMessages(thread.ID)
		} else {
			messages, _ = m.database.GetThreadMessages(thread.ID)
		}
		if len(messages) > 0 {
			m.messages[thread.ID] = messages
		}
	}

	if len(messages) > 0 {
		subject = messages[0].Subject
		from = messages[0].From
		timestamp = messages[0].Timestamp
	}

	// Truncate subject if too long
	if len(subject) > 50 {
		subject = subject[:47] + "..."
	}

	// Truncate from if too long
	if len(from) > 30 {
		from = from[:27] + "..."
	}

	// Format date
	dateStr := timestamp.Format("Jan 2, 15:04")

	// Truncate summary if too long
	summary := thread.Summary
	if len(summary) > 120 {
		summary = summary[:117] + "..."
	}

	// Build thread display
	var threadText strings.Builder
	threadText.WriteString(fmt.Sprintf("%s%s\n", cursor, subject))
	threadText.WriteString(fmt.Sprintf("    From: %s | %s | Tasks: %d\n", from, dateStr, thread.TaskCount))
	threadText.WriteString(fmt.Sprintf("    %s\n", summary))

	if selected {
		return selectedStyle.Render(threadText.String())
	}
	return threadStyle.Render(threadText.String())
}
