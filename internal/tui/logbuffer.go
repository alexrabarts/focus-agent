package tui

import (
	"strings"
	"sync"
	"time"
)

// LogEntry represents a single log message with timestamp
type LogEntry struct {
	Message   string
	Timestamp time.Time
}

// LogBuffer captures log output and stores recent messages in a circular buffer
type LogBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	maxSize int
	pos     int
}

// NewLogBuffer creates a new log buffer with specified capacity
func NewLogBuffer(maxSize int) *LogBuffer {
	return &LogBuffer{
		entries: make([]LogEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Write implements io.Writer interface
func (lb *LogBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	message := strings.TrimSpace(string(p))
	if message == "" {
		return len(p), nil
	}

	entry := LogEntry{
		Message:   message,
		Timestamp: time.Now(),
	}

	// Add to circular buffer
	if len(lb.entries) < lb.maxSize {
		lb.entries = append(lb.entries, entry)
	} else {
		lb.entries[lb.pos] = entry
		lb.pos = (lb.pos + 1) % lb.maxSize
	}

	return len(p), nil
}

// GetRecent returns the most recent log messages (newest first)
func (lb *LogBuffer) GetRecent(count int) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	if len(lb.entries) == 0 {
		return nil
	}

	// Return up to 'count' most recent entries
	if count > len(lb.entries) {
		count = len(lb.entries)
	}

	result := make([]LogEntry, count)

	// If buffer is not full, entries are in order
	if len(lb.entries) < lb.maxSize {
		for i := 0; i < count; i++ {
			result[i] = lb.entries[len(lb.entries)-1-i]
		}
	} else {
		// Buffer is full, need to account for circular position
		for i := 0; i < count; i++ {
			idx := (lb.pos - 1 - i + lb.maxSize) % lb.maxSize
			result[i] = lb.entries[idx]
		}
	}

	return result
}

// GetLatest returns the most recent log entry, or nil if buffer is empty
func (lb *LogBuffer) GetLatest() *LogEntry {
	recent := lb.GetRecent(1)
	if len(recent) > 0 {
		return &recent[0]
	}
	return nil
}
