package google

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// CalendarClient handles Google Calendar API operations
type CalendarClient struct {
	Service *calendar.Service
	Config  *config.Config
}

// CalendarSyncState stores Calendar-specific sync state
type CalendarSyncState struct {
	SyncToken string `json:"sync_token"`
}

// SyncEvents performs incremental sync of calendar events
func (c *CalendarClient) SyncEvents(ctx context.Context, database *db.DB) error {
	// Get sync state
	syncState, err := database.GetSyncState("calendar")
	if err != nil {
		return fmt.Errorf("failed to get sync state: %w", err)
	}

	var state CalendarSyncState
	if syncState.State != "" && syncState.State != "{}" {
		if err := json.Unmarshal([]byte(syncState.State), &state); err != nil {
			log.Printf("Warning: invalid sync state, doing full sync: %v", err)
			state = CalendarSyncState{}
		}
	}

	// Perform sync
	if state.SyncToken != "" {
		// Try incremental sync
		if err := c.incrementalSync(ctx, database, &state); err != nil {
			log.Printf("Incremental sync failed, falling back to full sync: %v", err)
			state.SyncToken = ""
		}
	}

	if state.SyncToken == "" {
		// Full sync
		if err := c.fullSync(ctx, database, &state); err != nil {
			return fmt.Errorf("full sync failed: %w", err)
		}
	}

	// Save sync state
	stateJSON, _ := json.Marshal(state)
	syncState.State = string(stateJSON)
	syncState.LastSync = time.Now()
	syncState.NextSync = time.Now().Add(time.Duration(c.Config.Google.PollingMinutes.Calendar) * time.Minute)

	if err := database.SaveSyncState(syncState); err != nil {
		return fmt.Errorf("failed to save sync state: %w", err)
	}

	return nil
}

// fullSync performs a full synchronization of events
func (c *CalendarClient) fullSync(ctx context.Context, database *db.DB, state *CalendarSyncState) error {
	log.Println("Starting Calendar full sync...")

	// Time range for sync based on config
	now := time.Now()
	timeMin := now.AddDate(0, 0, -7) // Past week for context
	daysAhead := c.Config.Limits.CalendarDaysAhead
	if daysAhead == 0 {
		daysAhead = 30 // Default to 30 days
	}
	timeMax := now.AddDate(0, 0, daysAhead)

	log.Printf("Syncing calendar events from %s to %s (%d days ahead)",
		timeMin.Format("2006-01-02"), timeMax.Format("2006-01-02"), daysAhead)

	// List events
	call := c.Service.Events.List("primary").
		Context(ctx).
		TimeMin(timeMin.Format(time.RFC3339)).
		TimeMax(timeMax.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(250)

	events, err := call.Do()
	if err != nil {
		return fmt.Errorf("failed to list events: %w", err)
	}

	// Process each event
	eventCount := 0
	for _, event := range events.Items {
		if err := c.processEvent(ctx, database, event); err != nil {
			log.Printf("Failed to process event %s: %v", event.Id, err)
			continue
		}
		eventCount++
	}

	// Store sync token for incremental sync
	if events.NextSyncToken != "" {
		state.SyncToken = events.NextSyncToken
	}

	log.Printf("Calendar full sync completed: %d events synced", eventCount)
	return nil
}

// incrementalSync performs incremental sync using sync token
func (c *CalendarClient) incrementalSync(ctx context.Context, database *db.DB, state *CalendarSyncState) error {
	log.Printf("Starting Calendar incremental sync with token %s...", state.SyncToken)

	// Use sync token to get changes
	call := c.Service.Events.List("primary").
		Context(ctx).
		SyncToken(state.SyncToken).
		MaxResults(250)

	events, err := call.Do()
	if err != nil {
		return fmt.Errorf("failed to list events: %w", err)
	}

	// Process each event change
	changeCount := 0
	for _, event := range events.Items {
		if event.Status == "cancelled" {
			// Handle deleted events
			log.Printf("Event cancelled: %s", event.Id)
			// Could delete from DB or mark as cancelled
		} else {
			if err := c.processEvent(ctx, database, event); err != nil {
				log.Printf("Failed to process event %s: %v", event.Id, err)
				continue
			}
			changeCount++
		}
	}

	// Update sync token
	if events.NextSyncToken != "" {
		state.SyncToken = events.NextSyncToken
	}

	log.Printf("Calendar incremental sync completed: %d changes processed", changeCount)
	return nil
}

// processEvent saves or updates an event in the database
func (c *CalendarClient) processEvent(ctx context.Context, database *db.DB, event *calendar.Event) error {
	// Parse event times
	startTime, err := parseEventTime(event.Start)
	if err != nil {
		return fmt.Errorf("failed to parse start time: %w", err)
	}

	endTime, err := parseEventTime(event.End)
	if err != nil {
		return fmt.Errorf("failed to parse end time: %w", err)
	}

	// Extract attendees
	var attendees []string
	for _, attendee := range event.Attendees {
		attendees = append(attendees, attendee.Email)
	}

	// Extract meeting link
	meetingLink := extractMeetingLink(event)

	// Create event record
	eventRecord := &db.Event{
		ID:          event.Id,
		Title:       event.Summary,
		StartTS:     startTime,
		EndTS:       endTime,
		Location:    event.Location,
		Description: event.Description,
		Attendees:   attendees,
		MeetingLink: meetingLink,
		Status:      event.Status,
	}

	// Save to database
	if err := database.SaveEvent(eventRecord); err != nil {
		return fmt.Errorf("failed to save event: %w", err)
	}

	// Note: Tasks are no longer auto-created from calendar events.
	// Events are synced for scheduling context and will appear in the Meetings section of briefs.
	// Real tasks should come from email extraction, Google Tasks, or manual entry.

	return nil
}

// GetUpcomingEvents retrieves upcoming calendar events
func (c *CalendarClient) GetUpcomingEvents(ctx context.Context, hours int) ([]*calendar.Event, error) {
	now := time.Now()
	timeMin := now
	timeMax := now.Add(time.Duration(hours) * time.Hour)

	events, err := c.Service.Events.List("primary").
		Context(ctx).
		TimeMin(timeMin.Format(time.RFC3339)).
		TimeMax(timeMax.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(50).
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	return events.Items, nil
}

// CreateEvent creates a new calendar event
func (c *CalendarClient) CreateEvent(ctx context.Context, title, description string, startTime, endTime time.Time, attendees []string) (*calendar.Event, error) {
	event := &calendar.Event{
		Summary:     title,
		Description: description,
		Start: &calendar.EventDateTime{
			DateTime: startTime.Format(time.RFC3339),
			TimeZone: c.Config.Schedule.Timezone,
		},
		End: &calendar.EventDateTime{
			DateTime: endTime.Format(time.RFC3339),
			TimeZone: c.Config.Schedule.Timezone,
		},
	}

	// Add attendees
	for _, email := range attendees {
		event.Attendees = append(event.Attendees, &calendar.EventAttendee{
			Email: email,
		})
	}

	createdEvent, err := c.Service.Events.Insert("primary", event).
		Context(ctx).
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to create event: %w", err)
	}

	return createdEvent, nil
}

// parseEventTime parses event time from calendar API
func parseEventTime(eventTime *calendar.EventDateTime) (time.Time, error) {
	if eventTime.DateTime != "" {
		return time.Parse(time.RFC3339, eventTime.DateTime)
	}
	if eventTime.Date != "" {
		return time.Parse("2006-01-02", eventTime.Date)
	}
	return time.Time{}, fmt.Errorf("no valid time found")
}

// extractMeetingLink extracts video conference link from event
func extractMeetingLink(event *calendar.Event) string {
	// Check conference data
	if event.ConferenceData != nil && len(event.ConferenceData.EntryPoints) > 0 {
		for _, entry := range event.ConferenceData.EntryPoints {
			if entry.EntryPointType == "video" {
				return entry.Uri
			}
		}
	}

	// Check description for common meeting links
	if event.Description != "" {
		patterns := []string{
			"meet.google.com/",
			"zoom.us/",
			"teams.microsoft.com/",
		}
		for _, pattern := range patterns {
			if idx := strings.Index(event.Description, pattern); idx != -1 {
				// Extract URL
				endIdx := strings.IndexAny(event.Description[idx:], " \n\r\t")
				if endIdx == -1 {
					return event.Description[idx:]
				}
				return event.Description[idx : idx+endIdx]
			}
		}
	}

	// Check location
	if strings.Contains(event.Location, "meet.google.com") ||
		strings.Contains(event.Location, "zoom.us") {
		return event.Location
	}

	return ""
}

// calculateUrgency calculates urgency score based on time until event
func calculateUrgency(eventTime time.Time) int {
	hoursUntil := time.Until(eventTime).Hours()

	switch {
	case hoursUntil <= 1:
		return 5
	case hoursUntil <= 4:
		return 4
	case hoursUntil <= 24:
		return 3
	case hoursUntil <= 72:
		return 2
	default:
		return 1
	}
}