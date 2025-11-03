package llm

import (
	"fmt"
	"strings"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// PromptBuilder centralizes all LLM prompts in one place
// This eliminates duplication across gemini.go, ollama.go, hybrid.go
type PromptBuilder struct {
	userEmail string
}

// NewPromptBuilder creates a new prompt builder
func NewPromptBuilder(userEmail string) *PromptBuilder {
	if userEmail == "" {
		userEmail = "the user"
	}
	return &PromptBuilder{userEmail: userEmail}
}

// BuildThreadSummary creates a prompt for summarizing email threads
func (p *PromptBuilder) BuildThreadSummary(messages []*db.Message) string {
	var prompt strings.Builder

	prompt.WriteString("Summarize this email thread concisely. Focus on:\n")
	prompt.WriteString("1. Main topic/issue\n")
	prompt.WriteString("2. Key decisions or action items\n")
	prompt.WriteString("3. Who needs to do what\n")
	prompt.WriteString("4. Deadlines mentioned\n")
	prompt.WriteString("5. Any risks or blockers\n\n")

	prompt.WriteString("Thread:\n")
	for _, msg := range messages {
		prompt.WriteString(fmt.Sprintf("From: %s\n", msg.From))
		prompt.WriteString(fmt.Sprintf("Date: %s\n", msg.Timestamp.Format("Jan 2, 3:04 PM")))
		prompt.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))
		prompt.WriteString(fmt.Sprintf("Content: %s\n\n", msg.Snippet))
	}

	prompt.WriteString("Summary (be concise, max 200 words):")

	return prompt.String()
}

// BuildTaskExtraction creates a prompt for extracting tasks from received emails
func (p *PromptBuilder) BuildTaskExtraction(content string) string {
	return fmt.Sprintf(`Extract action items from this content that I (%s) need to do or respond to.

IMPORTANT RULES:
- INCLUDE tasks where I am responsible or need to take action
- INCLUDE implicit actions directed at me (e.g., "you should review", "recipient needs to", "please confirm")
- INCLUDE invitations, meeting requests, and events I'm invited to
- INCLUDE requests for my input, approval, or response
- INCLUDE deadlines and due dates that affect me
- INCLUDE tasks with no owner specified (assume they're for me)
- SKIP only tasks explicitly assigned to other specific people (e.g., "Andrew: do X", "Maria: review Y")

For each task, provide:
- title: Brief, actionable description (20-60 characters)
- due_date: Deadline if mentioned (e.g., "tomorrow", "Friday", "Oct 30", "by EOD")
- impact: Business impact score (1-5):
  * 1 = Nice to have, minimal consequence if delayed (learning/familiarization, general research, reading documentation, administrative overhead)
  * 2 = Helpful but not urgent (minor improvements, nice-to-have features)
  * 3 = Important, affects day-to-day operations (routine work, team coordination)
  * 4 = High impact, affects key stakeholders or projects (deliverables, client work)
  * 5 = Critical, blocks others or has major business impact (production issues, urgent deadlines)
- urgency: Time sensitivity score (1-5):
  * 1 = No specific deadline, can wait weeks
  * 2 = Deadline in 2-4 weeks
  * 3 = Deadline within 1 week
  * 4 = Deadline in next 3 days
  * 5 = Overdue or due today/tomorrow
- effort: Estimated effort (S/M/L):
  * S = < 1 hour, single action (reply, calendar action, quick decision)
  * M = 1-4 hours, moderate complexity (document review, meeting prep, analysis)
  * L = > 4 hours OR sustained learning (training, reading documentation, multi-session work, technical deep-dives)
- stakeholder: ONLY populate if a specific person with name/title is mentioned (e.g., "Sarah Chen", "VP Finance"). Leave empty for generic terms like "Team", "Users", "N/A"
- project: Related project, initiative, or context (if mentioned)

IMPORTANT FILTERING:
- SKIP meeting invitation tasks (e.g., "Respond to meeting invitation", "Accept meeting", "Confirm availability") - these belong in calendar, not task list
- SKIP purely informational emails where no action is required (e.g., "FYI", "for your information", "just an update", "wanted to share", "keeping you in the loop", "no action needed")
- SKIP emails where you're being informed about something but have no action to take
- SKIP tasks that are requests TO others where you're waiting for them to deliver/provide something (e.g., "Alex is requesting materials from Jules", "asking John to prepare report") - these are tasks for THEM, not you

Content:
%s

Format as a numbered list with pipe-delimited fields. Example:
1. Title: Review Q3 budget variance analysis | Due: Friday EOB | Impact: 5 | Urgency: 5 | Effort: M | Stakeholder: Sarah Chen (VP Finance) | Project: Board Meeting Prep
2. Title: Read Azure security documentation | Due: Next week | Impact: 1 | Urgency: 2 | Effort: L | Stakeholder: | Project: Security Training
3. Title: Provide feedback on draft proposal | Due: Tomorrow | Impact: 4 | Urgency: 5 | Effort: M | Stakeholder: John Smith (Director) | Project: Q4 Planning

Tasks:`, p.userEmail, content)
}

// BuildSentEmailTaskExtraction creates a prompt for extracting self-commitments from sent emails
func (p *PromptBuilder) BuildSentEmailTaskExtraction(content string, recipients []string) string {
	recipientList := "others"
	if len(recipients) > 0 {
		recipientList = strings.Join(recipients, ", ")
	}

	return fmt.Sprintf(`Extract commitments and promises I made in this sent email to %s.

IMPORTANT RULES:
- ONLY extract commitments where I (%s) promised or committed to do something
- Look for commitment patterns like:
  * "I will" / "I'll"
  * "I can" / "I'll make sure"
  * "Let me" / "I'm going to"
  * "I promise" / "I commit to"
  * "I'll send" / "I'll get back to you"
  * "I'll have it ready"
- Include delivery promises (e.g., "I'll send the report")
- Include action promises (e.g., "I'll review this")
- Include timeline promises (e.g., "I'll get back to you by Friday")
- SKIP general statements that aren't commitments (e.g., "That sounds good")

For each commitment, provide:
- Title (brief description of what I committed to)
- Recipient (from: %s)
- Due date/urgency (if mentioned, otherwise leave blank)
- Priority (High/Medium/Low based on context and urgency)

Content:
%s

Format as a numbered list. Example:
1. Title: Send Q3 report to Sarah | Recipient: sarah@example.com | Due: Friday EOD | Priority: High
2. Title: Review proposal for team | Recipient: team@example.com | Due: This week | Priority: Medium

Extract my commitments:`, recipientList, p.userEmail, recipientList, content)
}

// BuildTaskEnrichment creates a prompt for enriching task descriptions with full email context
// ENHANCED: Now includes a quoted snippet from the original email
func (p *PromptBuilder) BuildTaskEnrichment(task *db.Task, messages []*db.Message) string {
	var prompt strings.Builder

	prompt.WriteString("You are helping enrich a task description with full context from an email thread.\n\n")

	prompt.WriteString("TASK TO ENRICH:\n")
	prompt.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	if task.Description != "" {
		prompt.WriteString(fmt.Sprintf("Current Description: %s\n", task.Description))
	}
	if task.DueTS != nil {
		prompt.WriteString(fmt.Sprintf("Due Date: %s\n", task.DueTS.Format("Jan 2, 2006")))
	}
	if task.Project != "" {
		prompt.WriteString(fmt.Sprintf("Project: %s\n", task.Project))
	}
	prompt.WriteString("\n")

	prompt.WriteString("EMAIL THREAD CONTEXT:\n")
	// Show most recent messages first, limit to last 10 for context
	start := 0
	if len(messages) > 10 {
		start = len(messages) - 10
	}
	for i := len(messages) - 1; i >= start; i-- {
		msg := messages[i]
		prompt.WriteString(fmt.Sprintf("\n--- Message from %s (%s) ---\n", msg.From, msg.Timestamp.Format("Jan 2, 3:04 PM")))
		prompt.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))
		// Use snippet if available, otherwise truncate body
		if msg.Snippet != "" && msg.Snippet != msg.Body {
			prompt.WriteString(fmt.Sprintf("Content: %s\n", msg.Snippet))
		} else if msg.Body != "" {
			body := msg.Body
			if len(body) > 500 {
				body = body[:500] + "..."
			}
			prompt.WriteString(fmt.Sprintf("Content: %s\n", body))
		}
	}
	prompt.WriteString("\n")

	prompt.WriteString("INSTRUCTIONS:\n")
	prompt.WriteString("Write a rich, contextual description (2-4 sentences) that captures:\n\n")
	prompt.WriteString("1. WHAT is being asked for and WHY it matters\n")
	prompt.WriteString("   - The specific deliverable or action needed\n")
	prompt.WriteString("   - The business context or problem it addresses\n")
	prompt.WriteString("   - Why this is important right now\n\n")

	prompt.WriteString("2. WHO is involved and their role\n")
	prompt.WriteString("   - Who requested this (name and role if mentioned)\n")
	prompt.WriteString("   - Who else is involved or affected\n")
	prompt.WriteString("   - Any stakeholder concerns or expectations\n\n")

	prompt.WriteString("3. WHEN and timeline context\n")
	prompt.WriteString("   - Specific deadline if mentioned\n")
	prompt.WriteString("   - Reason for the timeline (e.g., 'for board meeting', 'before launch')\n")
	prompt.WriteString("   - Any time-sensitivity or urgency drivers\n\n")

	prompt.WriteString("4. HOW this connects to broader work\n")
	prompt.WriteString("   - Related projects or initiatives\n")
	prompt.WriteString("   - Dependencies or prerequisites\n")
	prompt.WriteString("   - Expected outcomes or success criteria\n\n")

	prompt.WriteString("5. ORIGINAL EMAIL SNIPPET\n")
	prompt.WriteString("   - Include a brief 1-2 sentence quote from the most relevant email\n")
	prompt.WriteString("   - Format as: 'Original request: \"[quote]\"'\n")
	prompt.WriteString("   - Choose the snippet that best captures the task request\n")
	prompt.WriteString("   - Keep the quote under 150 characters\n\n")

	prompt.WriteString("STYLE GUIDELINES:\n")
	prompt.WriteString("- Write in clear, professional language\n")
	prompt.WriteString("- Be specific with names, dates, numbers, and concrete details\n")
	prompt.WriteString("- Focus on actionable context that helps prioritize and execute\n")
	prompt.WriteString("- Avoid speculation - only include information from the thread\n")
	prompt.WriteString("- Keep main description concise: 2-4 sentences\n")
	prompt.WriteString("- Add the email snippet at the end on a new line\n\n")

	prompt.WriteString("EXAMPLE OUTPUT:\n")
	prompt.WriteString(`"Sarah Chen (VP Finance) requested a detailed review of Q3 budget variances by Friday EOB for the board meeting. She's particularly concerned about the 12% overspend in APAC marketing and needs specific recommendations on cost optimization opportunities to present to the board. This connects to our Q4 profitability targets and may affect the marketing automation project timeline.

Original request: \"Hi Alex, we need the Q3 budget variance analysis by Friday EOB for the board meeting. Focus on APAC marketing overspend.\"`)
	prompt.WriteString("\n\n")

	prompt.WriteString("Now write the enriched description (2-4 sentences + email snippet, no preamble):\n")

	return prompt.String()
}

// BuildStrategicAlignment creates a prompt for evaluating task alignment with strategic priorities
func (p *PromptBuilder) BuildStrategicAlignment(task *db.Task, priorities *config.Priorities) string {
	var prompt strings.Builder

	prompt.WriteString("Evaluate how well this task aligns with the following strategic priorities.\n\n")

	prompt.WriteString("TASK:\n")
	prompt.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	if task.Description != "" {
		prompt.WriteString(fmt.Sprintf("Description: %s\n", task.Description))
	}
	if task.Project != "" {
		prompt.WriteString(fmt.Sprintf("Project: %s\n", task.Project))
	}
	if task.Stakeholder != "" {
		prompt.WriteString(fmt.Sprintf("Stakeholder: %s\n", task.Stakeholder))
	}
	prompt.WriteString("\n")

	prompt.WriteString("STRATEGIC PRIORITIES:\n\n")

	if len(priorities.OKRs) > 0 {
		prompt.WriteString("OKRs (Objectives & Key Results):\n")
		for _, okr := range priorities.OKRs {
			prompt.WriteString(fmt.Sprintf("  - %s\n", okr))
		}
		prompt.WriteString("\n")
	}

	if len(priorities.FocusAreas) > 0 {
		prompt.WriteString("Focus Areas:\n")
		for _, area := range priorities.FocusAreas {
			prompt.WriteString(fmt.Sprintf("  - %s\n", area))
		}
		prompt.WriteString("\n")
	}

	if len(priorities.KeyProjects) > 0 {
		prompt.WriteString("Key Projects:\n")
		for _, project := range priorities.KeyProjects {
			prompt.WriteString(fmt.Sprintf("  - %s\n", project))
		}
		prompt.WriteString("\n")
	}

	if len(priorities.KeyStakeholders) > 0 {
		prompt.WriteString("Key Stakeholders (VIP contacts - tasks from these people are high priority):\n")
		for _, stakeholder := range priorities.KeyStakeholders {
			prompt.WriteString(fmt.Sprintf("  - %s\n", stakeholder))
		}
		prompt.WriteString("\n")
	}

	prompt.WriteString("INSTRUCTIONS:\n")
	prompt.WriteString("Evaluate the DIRECT, MEANINGFUL alignment between this task and the strategic priorities.\n\n")
	prompt.WriteString("STRICT MATCHING RULES:\n")
	prompt.WriteString("1. Only match if the task DIRECTLY advances or relates to the priority\n")
	prompt.WriteString("2. Shared keywords alone are NOT sufficient (e.g., 'data team' ≠ 'Data lake project')\n")
	prompt.WriteString("3. Generic administrative tasks (scheduling, coordinating, reporting) should NOT match strategic priorities unless they're specifically about implementing/advancing that priority\n")
	prompt.WriteString("4. Be conservative - when in doubt, DON'T match\n")
	prompt.WriteString("5. Check if the task stakeholder matches any Key Stakeholder (exact email match or similar)\n\n")
	prompt.WriteString("EXAMPLES OF POOR MATCHES TO AVOID:\n")
	prompt.WriteString("- 'Schedule meeting about X' does NOT align with X unless the meeting is to implement/advance X\n")
	prompt.WriteString("- 'Send report to team' does NOT align with 'Improved forecasting' just because both involve data\n")
	prompt.WriteString("- 'Coordinate with data team' does NOT align with 'Data lake' unless specifically about the data lake\n")
	prompt.WriteString("- 'Review budget' does NOT align with 'Profitability' unless it's specifically about improving margins\n\n")
	prompt.WriteString("EXAMPLES OF GOOD MATCHES:\n")
	prompt.WriteString("- 'Implement new CRM dashboard' → 'Scalable systems - CRM implementation'\n")
	prompt.WriteString("- 'Analyze margin trends for cost optimization' → 'Sector Leading Profitability'\n")
	prompt.WriteString("- 'Design brand guidelines for member experience' → 'Known for distinctive service'\n\n")
	prompt.WriteString("Return:\n")
	prompt.WriteString("- score: 0.0 (no alignment) to 5.0 (perfect alignment)\n")
	prompt.WriteString("- okrs: array of OKR names that genuinely align (empty array if none)\n")
	prompt.WriteString("- focus_areas: array of Focus Area names that align (empty array if none)\n")
	prompt.WriteString("- projects: array of Project names that align (empty array if none)\n")
	prompt.WriteString("- key_stakeholder: true if task stakeholder matches any Key Stakeholder, false otherwise\n")
	prompt.WriteString("- reasoning: brief explanation of your evaluation (include why you excluded matches if any)\n")

	return prompt.String()
}

// BuildReply creates a prompt for drafting email replies
func (p *PromptBuilder) BuildReply(thread []*db.Message, goal string) string {
	var prompt strings.Builder

	prompt.WriteString("Draft a concise, professional email reply.\n\n")
	prompt.WriteString(fmt.Sprintf("Goal: %s\n\n", goal))

	prompt.WriteString("Thread context (most recent first):\n")
	for i := len(thread) - 1; i >= 0 && i >= len(thread)-3; i-- {
		msg := thread[i]
		prompt.WriteString(fmt.Sprintf("From: %s\n", msg.From))
		prompt.WriteString(fmt.Sprintf("Content: %s\n\n", msg.Snippet))
	}

	prompt.WriteString("Draft a reply that:\n")
	prompt.WriteString("- Is concise and to the point\n")
	prompt.WriteString("- Maintains professional tone\n")
	prompt.WriteString("- Addresses the goal clearly\n")
	prompt.WriteString("- Uses my typical writing style (direct, friendly)\n\n")

	prompt.WriteString("Reply (max 150 words):")

	return prompt.String()
}

// BuildMeetingPrep creates a prompt for meeting preparation
func (p *PromptBuilder) BuildMeetingPrep(event *db.Event, docs []*db.Document) string {
	var prompt strings.Builder

	prompt.WriteString("Generate a one-page meeting preparation brief.\n\n")

	prompt.WriteString(fmt.Sprintf("Meeting: %s\n", event.Title))
	prompt.WriteString(fmt.Sprintf("Time: %s\n", event.StartTS.Format("Monday, Jan 2, 3:04 PM")))

	if len(event.Attendees) > 0 {
		prompt.WriteString(fmt.Sprintf("Attendees: %s\n", strings.Join(event.Attendees, ", ")))
	}

	if event.Description != "" {
		prompt.WriteString(fmt.Sprintf("Description: %s\n", event.Description))
	}

	if len(docs) > 0 {
		prompt.WriteString("\nRelated documents:\n")
		for _, doc := range docs {
			prompt.WriteString(fmt.Sprintf("- %s\n", doc.Title))
		}
	}

	prompt.WriteString("\nInclude:\n")
	prompt.WriteString("1. Meeting objective and key discussion points\n")
	prompt.WriteString("2. Preparation checklist (what to review beforehand)\n")
	prompt.WriteString("3. Suggested agenda items\n")
	prompt.WriteString("4. Potential questions or concerns to address\n")
	prompt.WriteString("5. Expected outcomes and next steps\n\n")

	prompt.WriteString("Brief (one page):")

	return prompt.String()
}

// BuildTaskExtractionWithConversationFlow creates an advanced prompt for Claude that considers conversation flow
// This is the smarter version that was previously only in hybrid.go
func (p *PromptBuilder) BuildTaskExtractionWithConversationFlow(messages []*db.Message) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("Extract action items for %s from this email conversation.\n\n", p.userEmail))

	prompt.WriteString("CONVERSATION FLOW:\n")
	for i, msg := range messages {
		prompt.WriteString(fmt.Sprintf("\n[Message %d - %s]\n", i+1, msg.Timestamp.Format("Jan 2, 3:04 PM")))
		prompt.WriteString(fmt.Sprintf("From: %s\n", msg.From))
		prompt.WriteString(fmt.Sprintf("To: %s\n", msg.To))
		prompt.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))

		content := msg.Snippet
		if content == "" || content == msg.Body {
			content = msg.Body
		}
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		prompt.WriteString(fmt.Sprintf("Content: %s\n", content))
	}

	prompt.WriteString("\n\nEXTRACTION RULES:\n")
	prompt.WriteString(fmt.Sprintf("- Extract tasks for %s (the recipient/participant)\n", p.userEmail))
	prompt.WriteString("- Include requests, commitments, follow-ups, and action items\n")
	prompt.WriteString("- Consider conversation flow - later messages may cancel or modify earlier requests\n")
	prompt.WriteString("- Skip tasks that were already completed in the thread\n")
	prompt.WriteString("- Skip vague or unclear items\n")
	prompt.WriteString("- Include context about who requested it and why\n\n")

	prompt.WriteString("OUTPUT FORMAT:\n")
	prompt.WriteString("For each task, provide:\n")
	prompt.WriteString("1. Title: Brief, actionable description\n")
	prompt.WriteString("2. Context: Who requested it and why (1 sentence)\n")
	prompt.WriteString("3. Deadline: If mentioned, extract as 'YYYY-MM-DD' or relative ('this week')\n")
	prompt.WriteString("4. Priority: High/Medium/Low based on urgency and importance\n\n")

	prompt.WriteString("Example:\n")
	prompt.WriteString("Task 1:\n")
	prompt.WriteString("Title: Review Q4 forecast model\n")
	prompt.WriteString("Context: Sarah (CFO) needs this for board presentation\n")
	prompt.WriteString("Deadline: Friday EOD\n")
	prompt.WriteString("Priority: High\n\n")

	prompt.WriteString("Extract tasks now:")

	return prompt.String()
}
