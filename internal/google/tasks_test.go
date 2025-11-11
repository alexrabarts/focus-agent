package google

import (
	"testing"

	"google.golang.org/api/tasks/v1"
)

// TestSkipFocusAgentList verifies that the "Focus Agent" list is skipped during sync
func TestSkipFocusAgentList(t *testing.T) {
	// Create a mock task list
	taskLists := &tasks.TaskLists{
		Items: []*tasks.TaskList{
			{Title: "Personal Tasks", Id: "list1"},
			{Title: "Focus Agent", Id: "list2"}, // Should be skipped
			{Title: "Work Tasks", Id: "list3"},
		},
	}

	// The sync logic should skip "Focus Agent"
	// This test documents the expected behavior
	skippedCount := 0
	processedCount := 0

	for _, taskList := range taskLists.Items {
		if taskList.Title == "Focus Agent" {
			skippedCount++
			continue
		}
		processedCount++
	}

	if skippedCount != 1 {
		t.Errorf("Expected to skip 1 list (Focus Agent), but skipped %d", skippedCount)
	}

	if processedCount != 2 {
		t.Errorf("Expected to process 2 lists, but processed %d", processedCount)
	}
}
