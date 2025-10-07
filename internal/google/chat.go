package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// ChatClient handles Google Chat webhook operations
type ChatClient struct {
	Config *config.Config
}

// ChatMessage represents a Google Chat message
type ChatMessage struct {
	Text   string      `json:"text,omitempty"`
	Cards  []ChatCard  `json:"cards,omitempty"`
	Thread *ChatThread `json:"thread,omitempty"`
}

// ChatThread represents a thread in Google Chat
type ChatThread struct {
	Name string `json:"name,omitempty"`
}

// ChatCard represents a card in Google Chat
type ChatCard struct {
	Header   *CardHeader   `json:"header,omitempty"`
	Sections []CardSection `json:"sections,omitempty"`
}

// CardHeader represents the header of a card
type CardHeader struct {
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle,omitempty"`
	ImageURL   string `json:"imageUrl,omitempty"`
	ImageStyle string `json:"imageStyle,omitempty"`
}

// CardSection represents a section in a card
type CardSection struct {
	Header  string       `json:"header,omitempty"`
	Widgets []CardWidget `json:"widgets"`
}

// CardWidget represents a widget in a card section
type CardWidget struct {
	TextParagraph *TextParagraph `json:"textParagraph,omitempty"`
	KeyValue      *KeyValue      `json:"keyValue,omitempty"`
	Buttons       []Button       `json:"buttons,omitempty"`
}

// TextParagraph represents text content
type TextParagraph struct {
	Text string `json:"text"`
}

// KeyValue represents a key-value pair
type KeyValue struct {
	TopLabel         string  `json:"topLabel,omitempty"`
	Content          string  `json:"content"`
	BottomLabel      string  `json:"bottomLabel,omitempty"`
	Icon             string  `json:"icon,omitempty"`
	Button           *Button `json:"button,omitempty"`
	ContentMultiline bool    `json:"contentMultiline,omitempty"`
}

// Button represents an action button
type Button struct {
	TextButton *TextButton `json:"textButton,omitempty"`
}

// TextButton represents a text button
type TextButton struct {
	Text    string      `json:"text"`
	OnClick *OnClick    `json:"onClick"`
}

// OnClick represents the action when a button is clicked
type OnClick struct {
	OpenLink *OpenLink `json:"openLink,omitempty"`
}

// OpenLink represents a link to open
type OpenLink struct {
	URL string `json:"url"`
}

// SendMessage sends a message to Google Chat
func (c *ChatClient) SendMessage(ctx context.Context, message *ChatMessage) error {
	// Add thread if configured
	if c.Config.Chat.ThreadKey != "" {
		message.Thread = &ChatThread{
			Name: fmt.Sprintf("spaces/%s/threads/%s", c.Config.Chat.SpaceID, c.Config.Chat.ThreadKey),
		}
	}

	// Marshal message to JSON
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Send webhook request
	req, err := http.NewRequestWithContext(ctx, "POST", c.Config.Chat.WebhookURL, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chat webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// SendDailyBrief sends the daily brief card
func (c *ChatClient) SendDailyBrief(ctx context.Context, tasks []*db.Task, events []*db.Event) error {
	card := c.createDailyBriefCard(tasks, events)
	message := &ChatMessage{
		Cards: []ChatCard{card},
	}

	return c.SendMessage(ctx, message)
}

// createDailyBriefCard creates a formatted daily brief card
func (c *ChatClient) createDailyBriefCard(tasks []*db.Task, events []*db.Event) ChatCard {
	now := time.Now()
	card := ChatCard{
		Header: &CardHeader{
			Title:    fmt.Sprintf("Daily Brief - %s", now.Format("Monday, January 2")),
			Subtitle: fmt.Sprintf("Your focus plan for today"),
		},
		Sections: []CardSection{},
	}

	// Top Tasks section
	if len(tasks) > 0 {
		taskWidgets := []CardWidget{}

		for i, task := range tasks {
			if i >= 10 {
				break // Limit to top 10 tasks
			}

			// Format task with priority indicator
			priority := c.getPriorityIndicator(task.Score)
			dueStr := ""
			if task.DueTS != nil {
				dueStr = fmt.Sprintf(" • Due: %s", task.DueTS.Format("3:04 PM"))
			}

			taskWidgets = append(taskWidgets, CardWidget{
				KeyValue: &KeyValue{
					TopLabel:         fmt.Sprintf("%s Task #%d", priority, i+1),
					Content:          task.Title,
					BottomLabel:      fmt.Sprintf("%s%s", task.Source, dueStr),
					ContentMultiline: true,
				},
			})
		}

		card.Sections = append(card.Sections, CardSection{
			Header:  "📋 Top Priority Tasks",
			Widgets: taskWidgets,
		})
	}

	// Today's Meetings section
	if len(events) > 0 {
		eventWidgets := []CardWidget{}

		for _, event := range events {
			// Only show today's events
			if event.StartTS.Day() != now.Day() {
				continue
			}

			timeStr := fmt.Sprintf("%s - %s",
				event.StartTS.Format("3:04 PM"),
				event.EndTS.Format("3:04 PM"))

			meetingWidget := CardWidget{
				KeyValue: &KeyValue{
					Icon:             "EVENT_SEAT",
					Content:          event.Title,
					BottomLabel:      timeStr,
					ContentMultiline: true,
				},
			}

			// Add meeting link button if available
			if event.MeetingLink != "" {
				meetingWidget.KeyValue.Button = &Button{
					TextButton: &TextButton{
						Text: "Join",
						OnClick: &OnClick{
							OpenLink: &OpenLink{
								URL: event.MeetingLink,
							},
						},
					},
				}
			}

			eventWidgets = append(eventWidgets, meetingWidget)
		}

		if len(eventWidgets) > 0 {
			card.Sections = append(card.Sections, CardSection{
				Header:  "📅 Today's Meetings",
				Widgets: eventWidgets,
			})
		}
	}

	// Focus Blocks section
	focusWidgets := []CardWidget{
		{
			TextParagraph: &TextParagraph{
				Text: c.generateFocusBlocks(tasks, events),
			},
		},
	}

	card.Sections = append(card.Sections, CardSection{
		Header:  "🎯 Recommended Focus Blocks",
		Widgets: focusWidgets,
	})

	// Quick Stats section
	statsText := c.generateStats(tasks, events)
	card.Sections = append(card.Sections, CardSection{
		Header: "📊 Quick Stats",
		Widgets: []CardWidget{
			{
				TextParagraph: &TextParagraph{
					Text: statsText,
				},
			},
		},
	})

	return card
}

// SendReplanBrief sends the midday replan brief
func (c *ChatClient) SendReplanBrief(ctx context.Context, completedTasks int, remainingTasks []*db.Task, afternoonEvents []*db.Event) error {
	card := c.createReplanCard(completedTasks, remainingTasks, afternoonEvents)
	message := &ChatMessage{
		Cards: []ChatCard{card},
	}

	return c.SendMessage(ctx, message)
}

// createReplanCard creates a midday replan card
func (c *ChatClient) createReplanCard(completedTasks int, remainingTasks []*db.Task, afternoonEvents []*db.Event) ChatCard {
	now := time.Now()
	card := ChatCard{
		Header: &CardHeader{
			Title:    "Midday Re-plan",
			Subtitle: fmt.Sprintf("Progress check at %s", now.Format("3:04 PM")),
		},
		Sections: []CardSection{},
	}

	// Progress section
	progressWidget := CardWidget{
		KeyValue: &KeyValue{
			Icon:        "CHECK_CIRCLE",
			TopLabel:    "Morning Progress",
			Content:     fmt.Sprintf("%d tasks completed", completedTasks),
			BottomLabel: fmt.Sprintf("%d tasks remaining", len(remainingTasks)),
		},
	}

	card.Sections = append(card.Sections, CardSection{
		Header:  "✅ Progress Update",
		Widgets: []CardWidget{progressWidget},
	})

	// Afternoon priorities
	if len(remainingTasks) > 0 {
		taskWidgets := []CardWidget{}

		for i, task := range remainingTasks {
			if i >= 5 {
				break // Top 5 for afternoon
			}

			priority := c.getPriorityIndicator(task.Score)
			taskWidgets = append(taskWidgets, CardWidget{
				TextParagraph: &TextParagraph{
					Text: fmt.Sprintf("%s %s", priority, task.Title),
				},
			})
		}

		card.Sections = append(card.Sections, CardSection{
			Header:  "🎯 Afternoon Priorities",
			Widgets: taskWidgets,
		})
	}

	return card
}

// SendFollowUpReminder sends a follow-up reminder
func (c *ChatClient) SendFollowUpReminder(ctx context.Context, threads []*db.Thread) error {
	if len(threads) == 0 {
		return nil
	}

	var text strings.Builder
	text.WriteString("⏰ *Follow-up Reminders*\n\n")

	for i, thread := range threads {
		if i >= 5 {
			text.WriteString(fmt.Sprintf("... and %d more threads need attention\n", len(threads)-5))
			break
		}
		text.WriteString(fmt.Sprintf("• Thread: %s\n", thread.Summary))
	}

	message := &ChatMessage{
		Text: text.String(),
	}

	return c.SendMessage(ctx, message)
}

// getPriorityIndicator returns an emoji indicator based on score
// 🔴 High: score ≥ 4.0 (urgent + strategic)
// 🟡 Medium: score 2.5-3.9 (important but not urgent)
// 🟢 Low: score < 2.5 (can defer)
func (c *ChatClient) getPriorityIndicator(score float64) string {
	switch {
	case score >= 4.0:
		return "🔴"
	case score >= 2.5:
		return "🟡"
	default:
		return "🟢"
	}
}

// generateFocusBlocks generates recommended focus time blocks
func (c *ChatClient) generateFocusBlocks(tasks []*db.Task, events []*db.Event) string {
	var blocks strings.Builder

	// Check for meetings and suggest blocks around them
	hasAfternoonMeeting := false

	now := time.Now()
	for _, event := range events {
		if event.StartTS.Day() == now.Day() {
			hour := event.StartTS.Hour()
			if hour >= 12 && hour < 17 {
				hasAfternoonMeeting = true
			}
		}
	}

	// Morning block
	blocks.WriteString("• *9:00 - 11:00 AM*: Deep work on high-priority tasks\n")

	if !hasAfternoonMeeting {
		blocks.WriteString("• *2:00 - 4:00 PM*: Second focus block for complex tasks\n")
	} else {
		blocks.WriteString("• *After meetings*: Quick wins and email triage\n")
	}

	blocks.WriteString("• *4:00 - 5:00 PM*: Review and plan for tomorrow\n")

	return blocks.String()
}

// generateStats generates quick statistics
func (c *ChatClient) generateStats(tasks []*db.Task, events []*db.Event) string {
	var stats strings.Builder

	// Count tasks by urgency
	urgent := 0
	normal := 0
	for _, task := range tasks {
		if task.Urgency >= 4 {
			urgent++
		} else {
			normal++
		}
	}

	todayMeetings := 0
	meetingHours := 0.0
	now := time.Now()

	for _, event := range events {
		if event.StartTS.Day() == now.Day() {
			todayMeetings++
			duration := event.EndTS.Sub(event.StartTS).Hours()
			meetingHours += duration
		}
	}

	stats.WriteString(fmt.Sprintf("• *Tasks*: %d urgent, %d normal priority\n", urgent, normal))
	stats.WriteString(fmt.Sprintf("• *Meetings*: %d meetings (%.1f hours)\n", todayMeetings, meetingHours))
	stats.WriteString(fmt.Sprintf("• *Focus Time*: %.1f hours available\n", 8.0-meetingHours))

	return stats.String()
}