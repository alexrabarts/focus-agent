package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

type TasksModel struct {
	database            *db.DB
	planner             *planner.Planner
	apiClient           *APIClient
	tasks               []*db.Task
	cursor              int
	loading             bool
	err                 error
	lastCompletedTaskID string
	selectedTask        *db.Task // Currently selected task for detail view
	detailScroll        int      // Scroll position in detail view
	maxScroll           int      // Maximum scroll position for current task
	viewport            viewport.Model
	ready               bool
}

type tasksLoadedMsg struct {
	tasks []*db.Task
	err   error
}

func NewTasksModel(database *db.DB, planner *planner.Planner, apiClient *APIClient) TasksModel {
	return TasksModel{
		database:  database,
		planner:   planner,
		apiClient: apiClient,
		loading:   true,
		viewport:  viewport.New(80, 20), // Default size, will be updated
	}
}

// SetSize updates the viewport dimensions
func (m *TasksModel) SetSize(width, height int) {
	m.viewport.Width = width
	m.viewport.Height = height
	m.ready = true
}

func (m TasksModel) fetchTasks() tea.Cmd {
	return func() tea.Msg {
		var tasks []*db.Task
		var err error

		if m.apiClient != nil {
			// Use remote API
			tasks, err = m.apiClient.GetTasks()
		} else {
			// Use local database
			tasks, err = m.database.GetPendingTasks(50)
		}

		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

func (m *TasksModel) Update(msg tea.Msg) (*TasksModel, tea.Cmd) {
	// Update viewport
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tasksLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.tasks = msg.tasks
		return m, nil

	case tea.KeyMsg:
		// If in detail view, handle detail-specific keys
		if m.selectedTask != nil {
			switch msg.String() {
			case "esc", "q":
				// Return to list view
				m.selectedTask = nil
				m.detailScroll = 0
				return m, nil
			case "up", "k":
				// Scroll up first, then navigate to previous task
				if m.detailScroll > 0 {
					m.detailScroll--
				} else if m.cursor > 0 {
					// At top of content, go to previous task
					m.cursor--
					m.selectedTask = m.tasks[m.cursor]
					m.detailScroll = 0
				}
			case "down", "j":
				// Scroll down first, then navigate to next task
				if m.detailScroll < m.maxScroll {
					m.detailScroll++
				} else if m.cursor < len(m.tasks)-1 {
					// At bottom of content, go to next task
					m.cursor++
					m.selectedTask = m.tasks[m.cursor]
					m.detailScroll = 0
				}
			case "c":
				// Complete task from detail view
				task := m.selectedTask
				m.lastCompletedTaskID = task.ID
				m.selectedTask = nil // Return to list
				m.detailScroll = 0
				return m, m.completeTask(task)
			}
			return m, nil
		}

		// List view key handling
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		case "enter":
			// Open task detail view
			if m.cursor < len(m.tasks) {
				m.selectedTask = m.tasks[m.cursor]
				m.detailScroll = 0
			}
		case "c":
			// Complete task from list view
			if m.cursor < len(m.tasks) {
				task := m.tasks[m.cursor]
				m.lastCompletedTaskID = task.ID // Store for undo
				return m, m.completeTask(task)
			}
		case "u":
			// Undo last completion
			if m.lastCompletedTaskID != "" {
				taskID := m.lastCompletedTaskID
				m.lastCompletedTaskID = "" // Clear undo state
				return m, m.uncompleteTask(taskID)
			}
		case "r":
			// Refresh tasks
			m.loading = true
			return m, m.fetchTasks()
		}
	}

	return m, vpCmd
}

func (m TasksModel) completeTask(task *db.Task) tea.Cmd {
	return func() tea.Msg {
		var err error

		if m.apiClient != nil {
			// Use remote API
			err = m.apiClient.CompleteTask(task.ID)
		} else {
			// Use local planner
			ctx := context.Background()
			err = m.planner.CompleteTask(ctx, task.ID)
		}

		if err != nil {
			return tasksLoadedMsg{err: err}
		}

		// Refetch tasks after completion
		var tasks []*db.Task
		if m.apiClient != nil {
			tasks, err = m.apiClient.GetTasks()
		} else {
			tasks, err = m.database.GetPendingTasks(50)
		}

		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

func (m TasksModel) uncompleteTask(taskID string) tea.Cmd {
	return func() tea.Msg {
		var err error

		if m.apiClient != nil {
			// Use remote API
			err = m.apiClient.UncompleteTask(taskID)
		} else {
			// Use local planner
			ctx := context.Background()
			err = m.planner.UncompleteTask(ctx, taskID)
		}

		if err != nil {
			return tasksLoadedMsg{err: err}
		}

		// Refetch tasks after uncompleting
		var tasks []*db.Task
		if m.apiClient != nil {
			tasks, err = m.apiClient.GetTasks()
		} else {
			tasks, err = m.database.GetPendingTasks(50)
		}

		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

func (m *TasksModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	if m.loading {
		return "Loading tasks..."
	}

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Padding(1)
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
	}

	// If in detail view, show task detail
	if m.selectedTask != nil {
		content := m.renderTaskDetail()
		m.viewport.SetContent(content)
		return m.viewport.View()
	}

	if len(m.tasks) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(1)
		return emptyStyle.Render("No tasks found. All caught up!")
	}

	var b strings.Builder

	// Group tasks by priority
	highPriority := []*db.Task{}
	mediumPriority := []*db.Task{}
	lowPriority := []*db.Task{}

	for _, task := range m.tasks {
		switch {
		case task.Score >= 4.0:
			highPriority = append(highPriority, task)
		case task.Score >= 2.5:
			mediumPriority = append(mediumPriority, task)
		default:
			lowPriority = append(lowPriority, task)
		}
	}

	taskIndex := 0

	// High Priority
	if len(highPriority) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196")).
			Padding(0, 1)
		b.WriteString(headerStyle.Render("🔴 High Priority") + "\n")

		for _, task := range highPriority {
			b.WriteString(m.renderTask(task, taskIndex == m.cursor))
			taskIndex++
		}
		b.WriteString("\n")
	}

	// Medium Priority
	if len(mediumPriority) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("226")).
			Padding(0, 1)
		b.WriteString(headerStyle.Render("🟡 Medium Priority") + "\n")

		for _, task := range mediumPriority {
			b.WriteString(m.renderTask(task, taskIndex == m.cursor))
			taskIndex++
		}
		b.WriteString("\n")
	}

	// Low Priority
	if len(lowPriority) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("46")).
			Padding(0, 1)
		b.WriteString(headerStyle.Render("🟢 Low Priority") + "\n")

		for _, task := range lowPriority {
			b.WriteString(m.renderTask(task, taskIndex == m.cursor))
			taskIndex++
		}
	}

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	b.WriteString("\n")
	helpText := "enter: view details | c: complete task | r: refresh"
	if m.lastCompletedTaskID != "" {
		helpText += " | u: undo"
	}
	b.WriteString(helpStyle.Render(helpText))

	// Set viewport content and return viewport view
	content := b.String()
	m.viewport.SetContent(content)
	return m.viewport.View()
}

func (m TasksModel) renderTask(task *db.Task, selected bool) string {
	taskStyle := lipgloss.NewStyle().
		Padding(0, 2)

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Padding(0, 2)

	cursor := "  "
	if selected {
		cursor = "→ "
	}

	// Truncate title if too long
	title := task.Title
	if len(title) > 60 {
		title = title[:57] + "..."
	}

	// Show project/source
	meta := ""
	if task.Project != "" {
		meta = fmt.Sprintf(" (%s)", task.Project)
	} else if task.Source != "" {
		meta = fmt.Sprintf(" [%s]", task.Source)
	}

	taskText := fmt.Sprintf("%s%s%s - Score: %.1f", cursor, title, meta, task.Score)

	if selected {
		return selectedStyle.Render(taskText) + "\n"
	}
	return taskStyle.Render(taskText) + "\n"
}

func (m *TasksModel) renderTaskDetail() string {
	var b strings.Builder
	task := m.selectedTask

	// Header with task title (wrapped)
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1).
		Width(90)

	b.WriteString(headerStyle.Render(fmt.Sprintf("✅ %s", task.Title)) + "\n\n")

	// Task Information section
	infoTitleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("208")).
		Padding(0, 1)

	infoStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Padding(0, 2)

	b.WriteString(infoTitleStyle.Render("📋 Task Information:") + "\n")

	// Source with clickable link
	sourceText := fmt.Sprintf("Source: %s", task.Source)
	b.WriteString(infoStyle.Render(sourceText) + "\n")

	if task.SourceID != "" {
		var linkURL string
		var linkText string

		switch task.Source {
		case "gmail":
			linkURL = fmt.Sprintf("https://mail.google.com/mail/u/0/#inbox/%s", task.SourceID)
			linkText = "🔗 View email"
		case "google_calendar":
			linkURL = fmt.Sprintf("https://calendar.google.com/calendar/r/eventedit/%s", task.SourceID)
			linkText = "🔗 View event"
		case "google_tasks":
			linkURL = "https://tasks.google.com"
			linkText = "🔗 View in Google Tasks"
		}

		if linkURL != "" {
			// Create hyperlink with OSC 8
			hyperlink := makeHyperlink(linkURL, linkText)
			// Apply color and underline styling
			b.WriteString(fmt.Sprintf("  \x1b[38;5;39m\x1b[4m%s\x1b[0m\n", hyperlink))
		}
	}

	// Project
	if task.Project != "" {
		b.WriteString(infoStyle.Render(fmt.Sprintf("Project: %s", task.Project)) + "\n")
	}

	// Due date
	if task.DueTS != nil {
		dueStr := task.DueTS.Format("Mon, Jan 2, 2006 15:04")
		b.WriteString(infoStyle.Render(fmt.Sprintf("Due: %s", dueStr)) + "\n")
	}

	// Status
	b.WriteString(infoStyle.Render(fmt.Sprintf("Status: %s", task.Status)) + "\n")

	// Timestamps
	b.WriteString(infoStyle.Render(fmt.Sprintf("Created: %s", task.CreatedAt.Format("Jan 2, 15:04"))) + "\n")

	// Description if exists
	if task.Description != "" {
		b.WriteString("\n")
		b.WriteString(infoTitleStyle.Render("📝 Description:") + "\n")
		descStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Width(90).  // Set max width for wrapping
			Padding(0, 2)
		b.WriteString(descStyle.Render(task.Description) + "\n")
	}

	// Score Breakdown section
	b.WriteString("\n")
	scoreTitleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("45")).
		Padding(0, 1)

	b.WriteString(scoreTitleStyle.Render("🎯 Priority Score Breakdown:") + "\n")

	scoreStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Padding(0, 2)

	// Calculate individual components
	impact := float64(task.Impact)
	if impact == 0 {
		impact = 3
	}
	urgency := float64(task.Urgency)
	if urgency == 0 {
		urgency = 3
	}

	effortFactor := 1.0
	effortLabel := "Medium"
	switch task.Effort {
	case "S":
		effortFactor = 0.5
		effortLabel = "Small"
	case "L":
		effortFactor = 1.5
		effortLabel = "Large"
	}

	stakeholderWeight := 1.0
	stakeholderLabel := "Internal"
	switch task.Stakeholder {
	case "external":
		stakeholderWeight = 1.5
		stakeholderLabel = "External"
	case "executive":
		stakeholderWeight = 2.0
		stakeholderLabel = "Executive"
	}

	// Parse strategic alignment from matched priorities
	strategicScore := 0.0
	strategicDetail := ""
	if task.MatchedPriorities != "" && task.MatchedPriorities != "{}" && task.MatchedPriorities != "null" {
		var matches db.PriorityMatches
		if err := json.Unmarshal([]byte(task.MatchedPriorities), &matches); err == nil {
			matchCount := len(matches.OKRs) + len(matches.FocusAreas) + len(matches.Projects)
			if matches.KeyStakeholder {
				matchCount++
			}

			if matchCount > 0 {
				// Score from 0-5 based on number of matches (capped at 5)
				strategicScore = float64(matchCount)
				if strategicScore > 5 {
					strategicScore = 5
				}

				// Build detail string
				parts := []string{}
				if len(matches.OKRs) > 0 {
					parts = append(parts, fmt.Sprintf("%d OKR(s)", len(matches.OKRs)))
				}
				if len(matches.FocusAreas) > 0 {
					parts = append(parts, fmt.Sprintf("%d focus area(s)", len(matches.FocusAreas)))
				}
				if len(matches.Projects) > 0 {
					parts = append(parts, fmt.Sprintf("%d project(s)", len(matches.Projects)))
				}
				if matches.KeyStakeholder {
					parts = append(parts, "key stakeholder")
				}
				strategicDetail = strings.Join(parts, ", ")
			}
		}
	}
	if strategicDetail == "" {
		strategicDetail = "No strategic matches"
	}

	// Calculate contributions
	impactContribution := 0.3 * impact
	urgencyContribution := 0.25 * urgency
	effortContribution := -0.1 * effortFactor
	stakeholderContribution := 0.15 * stakeholderWeight
	strategicContribution := 0.2 * strategicScore

	// Impact description
	impactDesc := ""
	switch task.Impact {
	case 5:
		impactDesc = "Critical/transformational impact"
	case 4:
		impactDesc = "Major impact on goals"
	case 3:
		impactDesc = "Moderate impact"
	case 2:
		impactDesc = "Minor impact"
	case 1:
		impactDesc = "Minimal impact"
	}

	// Urgency description
	urgencyDesc := ""
	switch task.Urgency {
	case 5:
		urgencyDesc = "Immediate/time-critical"
	case 4:
		urgencyDesc = "Very time-sensitive"
	case 3:
		urgencyDesc = "Moderately urgent"
	case 2:
		urgencyDesc = "Low urgency"
	case 1:
		urgencyDesc = "No time pressure"
	}

	// Effort description
	effortDesc := ""
	switch task.Effort {
	case "S":
		effortDesc = "Quick win (<2 hours)"
	case "M":
		effortDesc = "Half day (2-4 hours)"
	case "L":
		effortDesc = "Full day+ (>4 hours)"
	}

	b.WriteString(scoreStyle.Render("Formula: 0.3×impact + 0.25×urgency + 0.2×strategic - 0.1×effort + 0.15×stakeholder") + "\n\n")
	b.WriteString(scoreStyle.Render(fmt.Sprintf("├─ Impact: %.0f/5 - %s (weight: 0.3) → +%.2f", impact, impactDesc, impactContribution)) + "\n")
	b.WriteString(scoreStyle.Render(fmt.Sprintf("├─ Urgency: %.0f/5 - %s (weight: 0.25) → +%.2f", urgency, urgencyDesc, urgencyContribution)) + "\n")
	b.WriteString(scoreStyle.Render(fmt.Sprintf("├─ Strategic Alignment: %.1f/5 - %s (weight: 0.2) → +%.2f", strategicScore, strategicDetail, strategicContribution)) + "\n")
	b.WriteString(scoreStyle.Render(fmt.Sprintf("├─ Effort: %s - %s (%.1f, weight: -0.1) → %.2f", effortLabel, effortDesc, effortFactor, effortContribution)) + "\n")

	// Show stakeholder detail if there is one
	if task.Stakeholder != "" {
		b.WriteString(scoreStyle.Render(fmt.Sprintf("└─ Stakeholder: %s - %s (%.1f, weight: 0.15) → +%.2f", stakeholderLabel, task.Stakeholder, stakeholderWeight, stakeholderContribution)) + "\n")
	} else {
		b.WriteString(scoreStyle.Render(fmt.Sprintf("└─ Stakeholder: %s (%.1f, weight: 0.15) → +%.2f", stakeholderLabel, stakeholderWeight, stakeholderContribution)) + "\n")
	}
	b.WriteString(scoreStyle.Render("──────────────────────────────────────────") + "\n")
	b.WriteString(scoreStyle.Render(fmt.Sprintf("Total Score: %.2f/5", task.Score)) + "\n")

	// Strategic Alignment Matches section
	if task.MatchedPriorities != "" && task.MatchedPriorities != "{}" && task.MatchedPriorities != "null" {
		var matches db.PriorityMatches
		if err := json.Unmarshal([]byte(task.MatchedPriorities), &matches); err == nil {
			hasMatches := len(matches.OKRs) > 0 || len(matches.FocusAreas) > 0 || len(matches.Projects) > 0 || matches.KeyStakeholder

			if hasMatches {
				b.WriteString("\n")
				alignmentTitleStyle := lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("11")).
					Padding(0, 1)

				b.WriteString(alignmentTitleStyle.Render("📊 Strategic Alignment:") + "\n")

				alignmentStyle := lipgloss.NewStyle().
					Foreground(lipgloss.Color("250")).
					Padding(0, 2)

				if len(matches.OKRs) > 0 {
					b.WriteString(alignmentStyle.Render("OKRs:") + "\n")
					for _, okr := range matches.OKRs {
						b.WriteString(alignmentStyle.Render(fmt.Sprintf("  • %s", okr)) + "\n")
					}
				}

				if len(matches.FocusAreas) > 0 {
					b.WriteString(alignmentStyle.Render("Focus Areas:") + "\n")
					for _, area := range matches.FocusAreas {
						b.WriteString(alignmentStyle.Render(fmt.Sprintf("  • %s", area)) + "\n")
					}
				}

				if len(matches.Projects) > 0 {
					b.WriteString(alignmentStyle.Render("Projects:") + "\n")
					for _, project := range matches.Projects {
						b.WriteString(alignmentStyle.Render(fmt.Sprintf("  • %s", project)) + "\n")
					}
				}

				if matches.KeyStakeholder {
					b.WriteString(alignmentStyle.Render("✓ From Key Stakeholder") + "\n")
				}
			}
		}
	}

	// Source Context section (for AI tasks)
	if task.Source == "ai" && task.SourceID != "" {
		b.WriteString("\n")
		contextTitleStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("13")).
			Padding(0, 1)

		b.WriteString(contextTitleStyle.Render("🔗 Source Context:") + "\n")

		contextStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Padding(0, 2)

		b.WriteString(contextStyle.Render(fmt.Sprintf("Extracted from email thread: %s", task.SourceID)) + "\n")

		// Try to fetch thread summary
		var thread *db.Thread
		if m.apiClient != nil {
			// Would need to add API endpoint for this
			b.WriteString(contextStyle.Render("(Thread summary not available in remote mode)") + "\n")
		} else {
			thread, _ = m.database.GetThreadByID(task.SourceID)
			if thread != nil && thread.Summary != "" {
				// Render full summary with word wrapping
				summaryStyle := lipgloss.NewStyle().
					Foreground(lipgloss.Color("250")).
					Width(90).  // Set max width for wrapping
					Padding(0, 2)
				b.WriteString(summaryStyle.Render(thread.Summary) + "\n")
			}
		}
	}

	// Help text
	b.WriteString("\n")
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(1, 0, 0, 1)

	// Calculate max scroll for smart navigation
	fullContent := b.String()
	totalLines := strings.Count(fullContent, "\n")
	viewportHeight := 20 // Assume 20 lines visible
	m.maxScroll = totalLines - viewportHeight
	if m.maxScroll < 0 {
		m.maxScroll = 0
	}

	// Simple help text - let terminal handle scrolling naturally
	helpText := "↑/↓: prev/next task | c: complete | esc/q: back to list"
	if m.maxScroll > 5 {
		helpText = "↑/↓: scroll then navigate | c: complete | esc/q: back"
	}
	b.WriteString(helpStyle.Render(helpText))

	return b.String()
}
