package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/alexrabarts/focus-agent/internal/db"
)

type ThreadsModel struct {
	database       *db.DB
	apiClient      *APIClient
	threads        []*db.Thread
	messages       map[string][]*db.Message // thread ID -> messages
	cursor         int
	offset         int // For scrolling
	loading        bool
	err            error
	selectedThread *db.Thread // Currently selected thread for detail view
	detailScroll   int        // Scroll position in detail view
	viewport       viewport.Model
	ready          bool
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
		viewport:  viewport.New(80, 20),
	}
}

// SetSize updates the viewport dimensions
func (m *ThreadsModel) SetSize(width, height int) {
	m.viewport.Width = width
	m.viewport.Height = height
	m.ready = true
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
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case threadsLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.threads = msg.threads
		return m, nil

	case tea.KeyMsg:
		// If in detail view, handle detail-specific keys
		if m.selectedThread != nil {
			switch msg.String() {
			case "esc", "q":
				// Return to list view
				m.selectedThread = nil
				m.detailScroll = 0
				return m, nil
			case "up", "k":
				if m.detailScroll > 0 {
					m.detailScroll--
				}
			case "down", "j":
				m.detailScroll++
			}
			return m, nil
		}

		// List view key handling
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				// Scroll up if needed
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}
		case "down", "j":
			if m.cursor < len(m.threads)-1 {
				m.cursor++
				// Scroll down if needed (max 8 items visible)
				if m.cursor >= m.offset+8 {
					m.offset = m.cursor - 7
				}
			}
		case "enter":
			// Open thread detail view
			if len(m.threads) > 0 && m.cursor < len(m.threads) {
				m.selectedThread = m.threads[m.cursor]
				m.detailScroll = 0
			}
		case "r":
			// Refresh threads
			m.loading = true
			return m, m.fetchThreads()
		}
	}

	return m, vpCmd
}

func (m ThreadsModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	if m.loading {
		return "Loading threads..."
	}

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Padding(1)
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}

	// If in detail view, show thread detail
	if m.selectedThread != nil {
		content := m.renderThreadDetail()
		m.viewport.SetContent(content)
		return m.viewport.View()
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

	// Show threads (limited to 8 visible items for scrolling)
	maxVisible := 8
	endIdx := m.offset + maxVisible
	if endIdx > len(m.threads) {
		endIdx = len(m.threads)
	}

	for i := m.offset; i < endIdx; i++ {
		b.WriteString(m.renderThread(m.threads[i], i == m.cursor))
		b.WriteString("\n\n")
	}

	// Show scroll indicator if there are more items
	if len(m.threads) > maxVisible {
		scrollInfo := fmt.Sprintf("\n  Showing %d-%d of %d threads (↑/↓ to scroll)",
			m.offset+1, endIdx, len(m.threads))
		scrollStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true)
		b.WriteString(scrollStyle.Render(scrollInfo))
	}

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑/↓: navigate | enter: view details | r: refresh"))

	content := b.String()
	m.viewport.SetContent(content)
	return m.viewport.View()
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

	// Determine priority badge based on score
	var priorityBadge string
	var priorityColor string
	if thread.PriorityScore >= 4.0 {
		priorityBadge = "⚡High"
		priorityColor = "196" // Red
	} else if thread.PriorityScore >= 2.0 {
		priorityBadge = "🔸Med"
		priorityColor = "220" // Yellow
	} else {
		priorityBadge = "⚫Low"
		priorityColor = "241" // Gray
	}

	// Add relevance indicator
	relevanceIndicator := ""
	if thread.RelevantToUser {
		relevanceIndicator = " 📌"
	}

	// Truncate summary if too long (shorter to make room for priority)
	summary := thread.Summary
	if len(summary) > 100 {
		summary = summary[:97] + "..."
	}

	// Build thread display
	var threadText strings.Builder
	threadText.WriteString(fmt.Sprintf("%s%s%s\n", cursor, subject, relevanceIndicator))

	// Color-code priority badge
	priorityStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(priorityColor))
	priorityText := priorityStyle.Render(priorityBadge)

	threadText.WriteString(fmt.Sprintf("    From: %s | %s | Tasks: %d | %s\n", from, dateStr, thread.TaskCount, priorityText))
	threadText.WriteString(fmt.Sprintf("    %s", summary))

	if selected {
		return selectedStyle.Render(threadText.String())
	}
	return threadStyle.Render(threadText.String())
}

func (m ThreadsModel) renderThreadDetail() string {
	var b strings.Builder

	// Get messages for this thread
	messages, ok := m.messages[m.selectedThread.ID]
	if !ok {
		// Fetch messages for this thread
		if m.apiClient != nil {
			messages, _ = m.apiClient.GetThreadMessages(m.selectedThread.ID)
		} else {
			messages, _ = m.database.GetThreadMessages(m.selectedThread.ID)
		}
		if len(messages) > 0 {
			m.messages[m.selectedThread.ID] = messages
		}
	}

	// Header with subject
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1)

	subject := "No subject"
	if len(messages) > 0 {
		subject = messages[0].Subject
	}
	b.WriteString(headerStyle.Render(fmt.Sprintf("📧 %s", subject)) + "\n")

	// Gmail link
	gmailURL := fmt.Sprintf("https://mail.google.com/mail/u/0/#inbox/%s", m.selectedThread.ID)
	linkStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Underline(true)
	b.WriteString("  " + linkStyle.Render(gmailURL) + "\n\n")

	// AI Summary section
	summaryTitleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("208")).
		Padding(0, 1)

	summaryStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Padding(0, 2)

	b.WriteString(summaryTitleStyle.Render("🤖 AI Summary:") + "\n")
	b.WriteString(summaryStyle.Render(m.selectedThread.Summary) + "\n\n")

	// Messages section
	messagesTitleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("45")).
		Padding(0, 1)

	b.WriteString(messagesTitleStyle.Render(fmt.Sprintf("💬 Messages (%d):", len(messages))) + "\n\n")

	// Render messages with scrolling
	maxVisible := 10
	startIdx := m.detailScroll
	endIdx := startIdx + maxVisible
	if endIdx > len(messages) {
		endIdx = len(messages)
	}
	if startIdx >= len(messages) {
		startIdx = len(messages) - 1
		if startIdx < 0 {
			startIdx = 0
		}
	}

	for i := startIdx; i < endIdx; i++ {
		msg := messages[i]

		msgHeaderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("45")).
			Padding(0, 2)

		msgBodyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Padding(0, 3)

		b.WriteString(msgHeaderStyle.Render(fmt.Sprintf("From: %s | %s",
			msg.From, msg.Timestamp.Format("Jan 2, 15:04"))) + "\n")

		// Show snippet or truncated body
		body := msg.Snippet
		if body == "" {
			body = msg.Body
		}
		if len(body) > 200 {
			body = body[:197] + "..."
		}
		b.WriteString(msgBodyStyle.Render(body) + "\n\n")
	}

	// Scroll indicator
	if len(messages) > maxVisible {
		scrollInfo := fmt.Sprintf("  Showing messages %d-%d of %d (↑/↓ to scroll)",
			startIdx+1, endIdx, len(messages))
		scrollStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true)
		b.WriteString(scrollStyle.Render(scrollInfo) + "\n")
	}

	// Help text
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑/↓: scroll messages | esc/q: back to list"))

	return b.String()
}
