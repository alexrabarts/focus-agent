package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

type TasksModel struct {
	database *db.DB
	planner  *planner.Planner
	tasks    []*db.Task
	cursor   int
	loading  bool
	err      error
}

type tasksLoadedMsg struct {
	tasks []*db.Task
	err   error
}

func NewTasksModel(database *db.DB, planner *planner.Planner) TasksModel {
	return TasksModel{
		database: database,
		planner:  planner,
		loading:  true,
	}
}

func (m TasksModel) fetchTasks() tea.Cmd {
	return func() tea.Msg {
		tasks, err := m.database.GetPendingTasks(50)
		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

func (m TasksModel) Update(msg tea.Msg) (TasksModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tasksLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.tasks = msg.tasks
		return m, nil

	case tea.KeyMsg:
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
			// Complete task
			if m.cursor < len(m.tasks) {
				task := m.tasks[m.cursor]
				return m, m.completeTask(task)
			}
		case "r":
			// Refresh tasks
			m.loading = true
			return m, m.fetchTasks()
		}
	}

	return m, nil
}

func (m TasksModel) completeTask(task *db.Task) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.planner.CompleteTask(ctx, task.ID); err != nil {
			return tasksLoadedMsg{err: err}
		}
		// Refetch tasks after completion
		tasks, err := m.database.GetPendingTasks(50)
		return tasksLoadedMsg{tasks: tasks, err: err}
	}
}

func (m TasksModel) View() string {
	if m.loading {
		return "Loading tasks..."
	}

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Padding(1)
		return errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
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
	b.WriteString(helpStyle.Render("enter: complete task | r: refresh"))

	return b.String()
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
