package google

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/api/chat/v1"
	"google.golang.org/api/googleapi"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// ChatClient handles Google Chat API operations
type ChatClient struct {
	Service    *chat.Service
	Config     *config.Config
	httpClient *http.Client
	dmSpace    string // Cached DM space name
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
	Text    string   `json:"text"`
	OnClick *OnClick `json:"onClick"`
}

// OnClick represents the action when a button is clicked
type OnClick struct {
	OpenLink *OpenLink `json:"openLink,omitempty"`
}

// OpenLink represents a link to open
type OpenLink struct {
	URL string `json:"url"`
}

// getDMSpace finds or creates a DM space with the Focus Agent bot
func (c *ChatClient) getDMSpace(ctx context.Context) (string, error) {
	// Return cached space if available
	if c.dmSpace != "" {
		return c.dmSpace, nil
	}

	// Use configured space if provided (e.g., manually captured DM)
	if configured := strings.TrimSpace(c.Config.Chat.SpaceID); configured != "" {
		if !strings.HasPrefix(configured, "spaces/") {
			configured = fmt.Sprintf("spaces/%s", configured)
		}
		c.dmSpace = configured
		log.Printf("Using configured Chat DM space: %s", c.dmSpace)
		return c.dmSpace, nil
	}

	userEmail := strings.ToLower(c.Config.Google.UserEmail)
	if userEmail == "" {
		return "", fmt.Errorf("google user email is not configured; complete Gmail setup first")
	}

	// List spaces to find existing DM with the bot
	pageToken := ""
	for {
		call := c.Service.Spaces.List().
			Filter("spaceType = \"DIRECT_MESSAGE\"")
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("failed to list spaces: %w", err)
		}

		for _, space := range resp.Spaces {
			// Only consider single user bot DMs
			if space.SpaceType != "DIRECT_MESSAGE" || !space.SingleUserBotDm {
				continue
			}

			// Verify the DM is with this app
			if _, err := c.Service.Spaces.Members.Get(fmt.Sprintf("%s/members/app", space.Name)).Context(ctx).Do(); err != nil {
				if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == http.StatusNotFound {
					continue
				}
				log.Printf("Skipping Chat space %s: app membership lookup failed: %v", space.Name, err)
				continue
			}

			// Confirm the human member matches the authenticated user
			memberResource := fmt.Sprintf("%s/members/users/%s", space.Name, url.PathEscape(userEmail))
			if _, err := c.Service.Spaces.Members.Get(memberResource).Context(ctx).Do(); err != nil {
				if gerr, ok := err.(*googleapi.Error); ok {
					switch gerr.Code {
					case http.StatusForbidden:
						return "", fmt.Errorf("insufficient permissions to inspect Chat memberships (enable chat.memberships.readonly scope): %w", err)
					case http.StatusNotFound:
						// Not the DM we need
						continue
					}
				}
				log.Printf("Skipping Chat space %s: user membership lookup failed: %v", space.Name, err)
				continue
			}

			c.dmSpace = space.Name
			log.Printf("Bound Focus Agent DM space %s to %s", c.dmSpace, userEmail)
			return c.dmSpace, nil
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return "", fmt.Errorf("no DM space found for %s - send a DM to Focus Agent to initialize the conversation", userEmail)
}

// SendMessage sends a message to Google Chat via the API
func (c *ChatClient) SendMessage(ctx context.Context, message *ChatMessage) error {
	// Get the DM space
	spaceName, err := c.getDMSpace(ctx)
	if err != nil {
		return fmt.Errorf("failed to get DM space: %w", err)
	}

	// Convert our message format to Chat API message format
	chatMessage := &chat.Message{}

	if message.Text != "" {
		chatMessage.Text = message.Text
	}

	if len(message.Cards) > 0 {
		// Convert cards to Chat API format
		cardsV2 := make([]*chat.CardWithId, len(message.Cards))
		for i, card := range message.Cards {
			cardsV2[i] = c.convertToAPICard(&card)
		}
		chatMessage.CardsV2 = cardsV2
	}

	// Create the message
	createCall := c.Service.Spaces.Messages.Create(spaceName, chatMessage)
	_, err = createCall.Do()
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// convertToAPICard converts our internal card format to Chat API CardWithId format
func (c *ChatClient) convertToAPICard(card *ChatCard) *chat.CardWithId {
	// This is a simplified conversion - we'll need to map our card structure
	// to the Chat API's card format
	apiCard := &chat.GoogleAppsCardV1Card{}

	if card.Header != nil {
		apiCard.Header = &chat.GoogleAppsCardV1CardHeader{
			Title:    card.Header.Title,
			Subtitle: card.Header.Subtitle,
		}
	}

	// Convert sections
	if len(card.Sections) > 0 {
		sections := make([]*chat.GoogleAppsCardV1Section, len(card.Sections))
		for i, section := range card.Sections {
			apiSection := &chat.GoogleAppsCardV1Section{
				Header: section.Header,
			}

			// Convert widgets
			widgets := make([]*chat.GoogleAppsCardV1Widget, len(section.Widgets))
			for j, widget := range section.Widgets {
				apiWidget := &chat.GoogleAppsCardV1Widget{}

				if widget.TextParagraph != nil {
					apiWidget.TextParagraph = &chat.GoogleAppsCardV1TextParagraph{
						Text: widget.TextParagraph.Text,
					}
				}

				if widget.KeyValue != nil {
					apiWidget.DecoratedText = &chat.GoogleAppsCardV1DecoratedText{
						TopLabel:    widget.KeyValue.TopLabel,
						Text:        widget.KeyValue.Content,
						BottomLabel: widget.KeyValue.BottomLabel,
					}
				}

				widgets[j] = apiWidget
			}
			apiSection.Widgets = widgets
			sections[i] = apiSection
		}
		apiCard.Sections = sections
	}

	return &chat.CardWithId{
		Card: apiCard,
	}
}

// SendDailyBrief sends the daily brief as text (cards not supported with user credentials)
func (c *ChatClient) SendDailyBrief(ctx context.Context, tasks []*db.Task, events []*db.Event) error {
	text := c.createDailyBriefText(tasks, events)
	message := &ChatMessage{
		Text: text,
	}

	return c.SendMessage(ctx, message)
}

// createDailyBriefText creates a plain text daily brief
func (c *ChatClient) createDailyBriefText(tasks []*db.Task, events []*db.Event) string {
	now := time.Now()
	var brief strings.Builder

	// Header
	brief.WriteString(fmt.Sprintf("*Daily Brief - %s*\n", now.Format("Monday, January 2")))
	brief.WriteString("_Your focus plan for today_\n\n")

	// Top Tasks section
	if len(tasks) > 0 {
		brief.WriteString("📋 *Top Priority Tasks*\n")
		for idx, task := range tasks {
			if idx >= 5 {
				break
			}
			priority := c.getPriorityIndicator(task.Score)
			dueStr := ""
			if task.DueTS != nil {
				dueStr = fmt.Sprintf(" • Due: %s", task.DueTS.Format("3:04 PM"))
			}
			brief.WriteString(fmt.Sprintf("\n%s %s\n", priority, task.Title))

			sourceLabel, sourceLink, _ := c.getTaskSourceInfo(task)
			if sourceLink != "" {
				brief.WriteString(fmt.Sprintf("<%s|%s>%s\n", sourceLink, sourceLabel, dueStr))
			} else {
				brief.WriteString(fmt.Sprintf("%s%s\n", sourceLabel, dueStr))
			}
		}
	}

	return brief.String()
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
			if i >= 5 {
				break // Limit to top 5 tasks
			}

			// Format task with priority indicator
			priority := c.getPriorityIndicator(task.Score)
			dueStr := ""
			if task.DueTS != nil {
				dueStr = fmt.Sprintf(" • Due: %s", task.DueTS.Format("3:04 PM"))
			}

			sourceLabel, sourceLink, buttonText := c.getTaskSourceInfo(task)
			taskWidgets = append(taskWidgets, CardWidget{
				KeyValue: &KeyValue{
					TopLabel:         priority,
					Content:          task.Title,
					BottomLabel:      fmt.Sprintf("%s%s", sourceLabel, dueStr),
					ContentMultiline: true,
				},
			})

			if sourceLink != "" {
				taskWidgets[len(taskWidgets)-1].KeyValue.Button = &Button{
					TextButton: &TextButton{
						Text: buttonText,
						OnClick: &OnClick{
							OpenLink: &OpenLink{URL: sourceLink},
						},
					},
				}
			}
		}

		card.Sections = append(card.Sections, CardSection{
			Header:  "📋 Top Priority Tasks",
			Widgets: taskWidgets,
		})
	}

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

// getTaskSourceInfo returns a human-readable source label, hyperlink, and button text
func (c *ChatClient) getTaskSourceInfo(task *db.Task) (string, string, string) {
	switch task.Source {
	case "gmail":
		if task.SourceID != "" {
			return "Gmail", fmt.Sprintf("https://mail.google.com/mail/u/0/#inbox/%s", task.SourceID), "Open in Gmail"
		}
		return "Gmail", "", ""
	case "gtasks", "google_tasks":
		return "Google Tasks", "https://tasks.google.com", "Open in Tasks"
	default:
		return humanizeSource(task.Source), "", ""
	}
}

func humanizeSource(src string) string {
	if src == "" {
		return "Unknown"
	}

	parts := strings.Split(src, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
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
