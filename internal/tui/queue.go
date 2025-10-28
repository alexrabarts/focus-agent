package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/scheduler"
)

type QueueModel struct {
	database     *db.DB
	apiClient    *APIClient
	scheduler    *scheduler.Scheduler // For local mode processing
	queueItems   []QueueItem
	cursor       int
	offset       int // For scrolling
	loading      bool
	processing   bool
	err          error
	selectedItem *QueueItem // Currently selected item for detail view
	detailScroll int        // Scroll position in detail view
	viewport     viewport.Model
	ready        bool
}

type QueueItem struct {
	ThreadID  string
	Subject   string
	From      string
	Timestamp time.Time
}

type queueLoadedMsg struct {
	items []QueueItem
	err   error
}

type processTriggeredMsg struct {
	success bool
	err     error
}

type tickProcessMsg struct{}

type processingTimeoutMsg struct{}

func NewQueueModel(database *db.DB, apiClient *APIClient, sched *scheduler.Scheduler) QueueModel {
	return QueueModel{
		database:  database,
		apiClient: apiClient,
		scheduler: sched,
		loading:   true,
		viewport:  viewport.New(80, 20),
	}
}

// SetSize updates the viewport dimensions
func (m *QueueModel) SetSize(width, height int) {
	m.viewport.Width = width
	m.viewport.Height = height
	m.ready = true
}

func (m QueueModel) fetchQueue() tea.Cmd {
	return func() tea.Msg {
		var items []QueueItem
		var err error

		if m.apiClient != nil {
			// Use remote API
			items, err = m.apiClient.GetQueue()
		} else {
			// Query local database for threads without summaries
			query := `
				SELECT DISTINCT t.id, ANY_VALUE(m.subject) as subject, ANY_VALUE(m.from_addr) as from_addr, MAX(m.ts) as ts
				FROM threads t
				JOIN messages m ON t.id = m.thread_id
				WHERE t.summary IS NULL OR t.summary = ''
				GROUP BY t.id
				ORDER BY MAX(m.ts) DESC
				LIMIT 500
			`

			rows, queryErr := m.database.Query(query)
			if queryErr != nil {
				return queueLoadedMsg{err: queryErr}
			}
			defer rows.Close()

			for rows.Next() {
				var item QueueItem
				var tsUnix int64
				if scanErr := rows.Scan(&item.ThreadID, &item.Subject, &item.From, &tsUnix); scanErr != nil {
					continue
				}
				item.Timestamp = time.Unix(tsUnix, 0)
				items = append(items, item)
			}
		}

		return queueLoadedMsg{items: items, err: err}
	}
}

func (m QueueModel) triggerProcessing() tea.Cmd {
	return func() tea.Msg {
		if m.apiClient != nil {
			// Use remote API
			err := m.apiClient.TriggerProcessing()
			return processTriggeredMsg{success: err == nil, err: err}
		}

		// For local mode, use the scheduler directly
		if m.scheduler != nil {
			// If we have a selected item, process just that thread
			if m.selectedItem != nil {
				err := m.scheduler.ProcessSingleThread(m.selectedItem.ThreadID)
				return processTriggeredMsg{success: err == nil, err: err}
			}
			// If in list view, process the selected item
			if len(m.queueItems) > 0 && m.cursor < len(m.queueItems) {
				err := m.scheduler.ProcessSingleThread(m.queueItems[m.cursor].ThreadID)
				return processTriggeredMsg{success: err == nil, err: err}
			}
			return processTriggeredMsg{success: false, err: fmt.Errorf("no thread selected")}
		}

		return processTriggeredMsg{
			success: false,
			err:     fmt.Errorf("no scheduler available for processing"),
		}
	}
}

func (m QueueModel) Update(msg tea.Msg) (QueueModel, tea.Cmd) {
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case queueLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.queueItems = msg.items
		return m, nil

	case tickProcessMsg:
		// Now actually trigger the processing after the UI has rendered
		return m, m.triggerProcessing()

	case processTriggeredMsg:
		m.processing = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// Processing completed successfully
		// Refresh the queue to show the updated state
		// If we were in detail view, go back to list
		m.selectedItem = nil
		m.detailScroll = 0
		return m, m.fetchQueue()

	case processingTimeoutMsg:
		// No longer needed for single thread processing, but keep for compatibility
		m.processing = false
		return m, m.fetchQueue()

	case tea.KeyMsg:
		// If in detail view, handle detail-specific keys
		if m.selectedItem != nil {
			switch msg.String() {
			case "esc", "q":
				// Return to list view
				m.selectedItem = nil
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
			if m.cursor < len(m.queueItems)-1 {
				m.cursor++
				// Scroll down if needed (max 10 items visible)
				if m.cursor >= m.offset+10 {
					m.offset = m.cursor - 9
				}
			}
		case "enter":
			// Open queue item detail view
			if len(m.queueItems) > 0 && m.cursor < len(m.queueItems) {
				m.selectedItem = &m.queueItems[m.cursor]
				m.detailScroll = 0
			}
		case "r":
			// Refresh queue
			m.loading = true
			return m, m.fetchQueue()
		case "p":
			// Trigger processing
			if !m.processing && len(m.queueItems) > 0 {
				m.processing = true
				// Return immediately to render the processing state, then trigger processing after a brief delay
				return m, tea.Tick(time.Millisecond*100, func(t time.Time) tea.Msg {
					return tickProcessMsg{}
				})
			}
		}
	}

	return m, vpCmd
}

func (m QueueModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	if m.loading {
		return "Loading queue..."
	}

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Padding(1)
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}

	// If in detail view, show thread detail
	if m.selectedItem != nil {
		content := m.renderQueueItemDetail()
		m.viewport.SetContent(content)
		return m.viewport.View()
	}

	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1)

	// Header with count and estimated cost
	queueCount := len(m.queueItems)
	estimatedTokens := queueCount * 500 // Conservative estimate per thread
	estimatedCost := float64(estimatedTokens) * 0.0000002 // $0.20 per 1M tokens for Gemini Flash

	status := ""
	if m.processing {
		status = " 🔄 Processing..."
	} else if queueCount == 0 {
		status = " ✅ Queue Empty"
	}

	b.WriteString(headerStyle.Render(fmt.Sprintf("📋 AI Processing Queue (%d threads)%s", queueCount, status)) + "\n\n")

	if queueCount == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(1)
		b.WriteString(emptyStyle.Render("No threads waiting for AI processing.") + "\n\n")
		b.WriteString(emptyStyle.Render("All email threads have been summarized!") + "\n")
	} else {
		// Show cost estimate
		costStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")).
			Padding(0, 2)

		b.WriteString(costStyle.Render(fmt.Sprintf("Estimated: ~%d tokens (~$%.4f)", estimatedTokens, estimatedCost)) + "\n\n")

		// Show queue items (limited to 10 visible items for scrolling)
		maxVisible := 10
		endIdx := m.offset + maxVisible
		if endIdx > len(m.queueItems) {
			endIdx = len(m.queueItems)
		}

		for i := m.offset; i < endIdx; i++ {
			b.WriteString(m.renderQueueItem(m.queueItems[i], i == m.cursor))
			b.WriteString("\n\n")
		}

		// Show scroll indicator if there are more items
		if len(m.queueItems) > maxVisible {
			scrollInfo := fmt.Sprintf("\n  Showing %d-%d of %d threads (↑/↓ to scroll)",
				m.offset+1, endIdx, len(m.queueItems))
			scrollStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("241")).
				Italic(true)
			b.WriteString(scrollStyle.Render(scrollInfo))
		}
	}

	// Help text
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString("\n")
	if queueCount > 0 {
		b.WriteString(helpStyle.Render("↑/↓: navigate | enter: view details | r: refresh | p: process selected"))
	} else {
		b.WriteString(helpStyle.Render("r: refresh"))
	}

	content := b.String()
	m.viewport.SetContent(content)
	return m.viewport.View()
}

func (m QueueModel) renderQueueItem(item QueueItem, selected bool) string {
	itemStyle := lipgloss.NewStyle().
		Padding(0, 2)

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Padding(0, 2)

	cursor := "  "
	if selected {
		cursor = "→ "
	}

	// Truncate subject if too long
	subject := item.Subject
	if len(subject) > 60 {
		subject = subject[:57] + "..."
	}

	// Truncate from if too long
	from := item.From
	if len(from) > 35 {
		from = from[:32] + "..."
	}

	// Format relative time
	timeStr := formatRelativeTime(item.Timestamp)

	// Build item display
	itemText := fmt.Sprintf("%s%s\n    From: %s | %s", cursor, subject, from, timeStr)

	if selected {
		return selectedStyle.Render(itemText)
	}
	return itemStyle.Render(itemText)
}

func (m QueueModel) renderQueueItemDetail() string {
	var b strings.Builder

	// Get messages for this thread
	var messages []*db.Message
	if m.apiClient != nil {
		messages, _ = m.apiClient.GetThreadMessages(m.selectedItem.ThreadID)
	} else {
		messages, _ = m.database.GetThreadMessages(m.selectedItem.ThreadID)
	}

	// Header with subject
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1)

	b.WriteString(headerStyle.Render(fmt.Sprintf("📧 %s", m.selectedItem.Subject)) + "\n")

	// Gmail link
	gmailURL := fmt.Sprintf("https://mail.google.com/mail/u/0/#inbox/%s", m.selectedItem.ThreadID)
	hyperlink := makeHyperlink(gmailURL, "🔗 View in Gmail")
	b.WriteString(fmt.Sprintf("  \x1b[38;5;39m\x1b[4m%s\x1b[0m\n\n", hyperlink))

	// Show processing status or prompt
	if m.processing {
		processingStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("45")).
			Bold(true).
			Padding(0, 2)
		b.WriteString(processingStyle.Render("🔄 Processing this thread with AI...") + "\n")
		b.WriteString(processingStyle.Render("   Generating summary and extracting tasks...") + "\n\n")
	} else {
		noteStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")).
			Italic(true).
			Padding(0, 2)
		b.WriteString(noteStyle.Render("⚠️  This thread hasn't been processed by AI yet.") + "\n")
		b.WriteString(noteStyle.Render("   Return to list (esc/q) and press 'p' to process.") + "\n\n")
	}

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

func formatRelativeTime(t time.Time) string {
	diff := time.Since(t)

	if diff < time.Minute {
		return "Just now"
	} else if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", mins)
	} else if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	} else if diff < 7*24*time.Hour {
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	} else {
		return t.Format("Jan 2")
	}
}
