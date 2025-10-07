package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/alexrabarts/focus-agent/internal/db"
)

type StatsModel struct {
	database *db.DB
	stats    Stats
	loading  bool
	err      error
}

type Stats struct {
	ThreadCount      int
	MessageCount     int
	DocCount         int
	EventCount       int
	TaskCount        int
	PendingTasks     int
	CompletedToday   int
	HighPriorityTasks int
	LastGmailSync    *time.Time
	LastDriveSync    *time.Time
	LastCalendarSync *time.Time
	LastTasksSync    *time.Time
}

type statsLoadedMsg struct {
	stats Stats
	err   error
}

func NewStatsModel(database *db.DB) StatsModel {
	return StatsModel{
		database: database,
		loading:  true,
	}
}

func (m StatsModel) fetchStats() tea.Cmd {
	return func() tea.Msg {
		var stats Stats

		// Count records
		m.database.QueryRow("SELECT COUNT(*) FROM threads").Scan(&stats.ThreadCount)
		m.database.QueryRow("SELECT COUNT(*) FROM messages").Scan(&stats.MessageCount)
		m.database.QueryRow("SELECT COUNT(*) FROM docs").Scan(&stats.DocCount)
		m.database.QueryRow("SELECT COUNT(*) FROM events").Scan(&stats.EventCount)
		m.database.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&stats.TaskCount)
		m.database.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending'").Scan(&stats.PendingTasks)
		m.database.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending' AND score >= 4.0").Scan(&stats.HighPriorityTasks)

		// Completed today
		today := time.Now().Format("2006-01-02")
		m.database.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'completed' AND DATE(completed_at) = ?", today).Scan(&stats.CompletedToday)

		// Last sync times (from sync_status table if it exists)
		var gmailSyncStr, driveSyncStr, calendarSyncStr, tasksSyncStr string
		err := m.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'gmail' ORDER BY last_sync DESC LIMIT 1").Scan(&gmailSyncStr)
		if err == nil {
			t, _ := time.Parse(time.RFC3339, gmailSyncStr)
			stats.LastGmailSync = &t
		}

		err = m.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'drive' ORDER BY last_sync DESC LIMIT 1").Scan(&driveSyncStr)
		if err == nil {
			t, _ := time.Parse(time.RFC3339, driveSyncStr)
			stats.LastDriveSync = &t
		}

		err = m.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'calendar' ORDER BY last_sync DESC LIMIT 1").Scan(&calendarSyncStr)
		if err == nil {
			t, _ := time.Parse(time.RFC3339, calendarSyncStr)
			stats.LastCalendarSync = &t
		}

		err = m.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'tasks' ORDER BY last_sync DESC LIMIT 1").Scan(&tasksSyncStr)
		if err == nil {
			t, _ := time.Parse(time.RFC3339, tasksSyncStr)
			stats.LastTasksSync = &t
		}

		return statsLoadedMsg{stats: stats}
	}
}

func (m StatsModel) Update(msg tea.Msg) (StatsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case statsLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.stats = msg.stats
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "r":
			m.loading = true
			return m, m.fetchStats()
		}
	}

	return m, nil
}

func (m StatsModel) View() string {
	if m.loading {
		return "Loading stats..."
	}

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Padding(1)
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}

	var b strings.Builder

	// Data Synced section
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("86")).
		Padding(0, 1)

	itemStyle := lipgloss.NewStyle().
		Padding(0, 2)

	b.WriteString(headerStyle.Render("📊 Data Synced") + "\n\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Email Threads: %d", m.stats.ThreadCount)) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Messages: %d", m.stats.MessageCount)) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Documents: %d", m.stats.DocCount)) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Calendar Events: %d", m.stats.EventCount)) + "\n")
	b.WriteString("\n")

	// Tasks section
	b.WriteString(headerStyle.Render("✅ Tasks") + "\n\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Total Tasks: %d", m.stats.TaskCount)) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Pending: %d", m.stats.PendingTasks)) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("High Priority: %d", m.stats.HighPriorityTasks)) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Completed Today: %d", m.stats.CompletedToday)) + "\n")
	b.WriteString("\n")

	// Last Sync section
	b.WriteString(headerStyle.Render("🔄 Last Sync") + "\n\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Gmail: %s", m.formatTime(m.stats.LastGmailSync))) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Drive: %s", m.formatTime(m.stats.LastDriveSync))) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Calendar: %s", m.formatTime(m.stats.LastCalendarSync))) + "\n")
	b.WriteString(itemStyle.Render(fmt.Sprintf("Tasks: %s", m.formatTime(m.stats.LastTasksSync))) + "\n")

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("r: refresh"))

	return b.String()
}

func (m StatsModel) formatTime(t *time.Time) string {
	if t == nil {
		return "Never"
	}

	diff := time.Since(*t)

	if diff < time.Minute {
		return "Just now"
	} else if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	} else if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	} else {
		return t.Format("Jan 2, 3:04 PM")
	}
}
