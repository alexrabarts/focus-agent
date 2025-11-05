package llm

import (
	"fmt"
	"log"
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
// UPDATED: Added noise reduction + enhanced date/stakeholder extraction
func (p *PromptBuilder) BuildTaskExtraction(content string) string {
	return fmt.Sprintf(`Extract action items from this content that I (%s) need to do or respond to.

⚠️  CRITICAL: NOISE REDUCTION RULES (CHECK FIRST)

1. AUTOMATED SENDERS - RETURN ZERO TASKS IF SENDER IS:
   - noreply@, no-reply@, donotreply@, payments-noreply@
   - notifications@, alerts@, support@, marketing@, newsletter@
   - System emails: receipts, confirmations, password resets
   → These are informational. Return empty list.

2. LIMIT TO 1-2 TASKS MAXIMUM:
   - One email = at most 1-2 tasks (consolidate duplicates)
   - Payment notification = 1 task ("Resolve payment issue"), NOT 13 variations
   - NO timestamp titles (e.g., "Oct 2, 12:00 PM - ...")
   - NO meta-observations (e.g., "No clear requests identified")

3. DELEGATION CHECK:
   - If I'm asking someone else to do something → Don't extract as MY task
   - Delegation phrases: "Can you...", "Please [name]..."
   - Exception: "Follow up with [person]" = MY task

INCLUDE tasks where I'm responsible:
- Requests directed at me ("you should review", "please confirm")
- Invitations requiring response
- Requests for input, approval, or response
- Deadlines affecting me
- Tasks with no owner (assume mine)

SKIP (these are 70%% of historical noise):
- Meeting invitations ("Accept meeting", "Confirm availability")
- Event planning for others ("Arrange venue", "Send invitations")
- Purely informational ("FYI", "for your information", "no action needed")
- Marketing emails (product announcements, sales pitches, newsletters, webinars)
- Social invitations ("Join us celebrating", "Team lunch", "Birthday board")
- Decorations ("Enjoy Halloween decorations", "Check out new artwork")
- Congratulatory messages ("Thanks!", "Great job!")

For each task:

- title: Action verb + object (20-60 chars) - NOT timestamp, NOT meta-comment

- due_date: Extract ANY temporal reference:
  * Explicit dates: "Oct 30", "12/15", "2025-01-15", "January 15th"
  * Relative dates: "tomorrow", "in 2 days", "next Monday", "end of week"
  * Deadline phrases: "by Friday", "before EOD", "by end of day"
  * Event-relative: "before the meeting", "after launch"
  * Urgency signals: "URGENT" → "today", "ASAP" → "within 24 hours"
  * Format as natural language (e.g., "Friday", "Oct 30", "tomorrow")
  * If NO temporal reference, use "N/A"

- impact: (1-5) - **USE FULL RANGE**
  * 1 = Nice to have (blog draft, organize files)
  * 2 = Minor (low-priority question)
  * 3 = Moderate, affects team (meeting coordination)
  * 4 = Significant, affects business (payment issue, client work)
  * 5 = Critical, company-wide (outage, security, executive escalation)

- urgency: (1-5) - **CALIBRATE TO REAL DEADLINES**
  * 1 = No deadline ("when you can")
  * 2 = Weeks away
  * 3 = This/next week
  * 4 = Tomorrow/next day
  * 5 = Today/overdue/blocking

- effort: S (< 1h), M (1-4h), L (> 4h)

- stakeholder: Person's name ONLY (extract from From header, email body, signatures)
  * FIRST: Check From header
  * SECOND: Look for @mentions, "Sarah asked", "Tim needs"
  * THIRD: Parse signatures for full names
  * Extract FULL NAME: "Sarah Chen" not "Sarah"
  * Normalize emails: "s.chen@company.com" → "Sarah Chen"
  * ONLY use N/A if no person identifiable
  * VALID: "Sarah Chen", "Tim Davis"
  * INVALID: "Finance Team", "Customer Support", "Marketing"

- project: Related context (if mentioned)

Content:
%s

Format (pipe-delimited):
1. Title: [Action] | Due: [Deadline] | Impact: [1-5] | Urgency: [1-5] | Effort: [S/M/L] | Stakeholder: [Name] | Project: [Context]

If no actionable tasks, return empty list.

YOUR EXTRACTED TASKS:`, p.userEmail, content)
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

// BuildTaskExtractionWithMetadata creates thread-aware extraction with automated sender filtering
func (p *PromptBuilder) BuildTaskExtractionWithMetadata(messages []*db.Message) string {
	if len(messages) == 0 {
		return "No messages provided. Return empty list."
	}

	// Detect automated senders from most recent message
	lastMsg := messages[len(messages)-1]
	fromAddr := strings.ToLower(lastMsg.From)

	// Automated sender patterns that should NOT generate tasks
	automatedSenders := []string{
		"noreply@", "no-reply@", "donotreply@", "do-not-reply@",
		"notifications@", "notify@", "alerts@",
		"payments-noreply@", "receipts@", "billing@",
		"support@", "help@", "customercare@",
		"marketing@", "newsletter@", "news@",
		"automated@", "auto@", "system@",
	}

	isAutomatedSender := false
	for _, pattern := range automatedSenders {
		if strings.Contains(fromAddr, pattern) {
			isAutomatedSender = true
			break
		}
	}

	// If automated sender, skip task extraction
	if isAutomatedSender {
		log.Printf("Skipping automated sender: %s", lastMsg.From)
		return "This is an automated system notification. Do not extract any tasks. Return empty list."
	}

	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("Extract action items for %s from this email thread.\n\n", p.userEmail))

	// Add thread context
	prompt.WriteString("=== EMAIL THREAD ===\n")
	prompt.WriteString(fmt.Sprintf("Thread contains %d message(s)\n\n", len(messages)))

	for i, msg := range messages {
		prompt.WriteString(fmt.Sprintf("[Message %d - %s]\n", i+1, msg.Timestamp.Format("Jan 2, 3:04 PM")))
		prompt.WriteString(fmt.Sprintf("From: %s\n", msg.From))
		prompt.WriteString(fmt.Sprintf("To: %s\n", msg.To))
		prompt.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))

		content := msg.Snippet
		if content == "" {
			content = msg.Body
		}
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		prompt.WriteString(fmt.Sprintf("Content: %s\n\n", content))
	}

	prompt.WriteString("\n=== CRITICAL NOISE REDUCTION RULES (CHECK FIRST) ===\n\n")

	prompt.WriteString("1. THREAD CONSOLIDATION (HIGHEST PRIORITY):\n")
	if len(messages) > 1 {
		prompt.WriteString(fmt.Sprintf("   ⚠️  This thread has %d messages\n", len(messages)))
		prompt.WriteString("   - Extract at MOST 1-2 SUMMARY tasks for the ENTIRE thread\n")
		prompt.WriteString("   - DO NOT create one task per message\n")
		prompt.WriteString("   - DO NOT use timestamps as task titles\n")
		prompt.WriteString("   - BAD: '1. Oct 2, 12:00 PM - Chris says...', '2. Oct 3, 2:15 PM - Alex responds...'\n")
		prompt.WriteString("   - GOOD: '1. Title: Prepare NDA for client review | Due: Thursday | ...'\n")
		prompt.WriteString("   - Look at the FINAL outcome of the conversation, not every message\n\n")
	}

	prompt.WriteString("2. DELEGATION DETECTION:\n")
	prompt.WriteString(fmt.Sprintf("   - If %s is the sender (From: field), check if delegating TO others\n", p.userEmail))
	prompt.WriteString("   - Delegation phrases: 'Can you...', 'Please [name]...', '[Person]: do X'\n")
	prompt.WriteString("   - If Alex asks 'Georgie, can you finalize the report?' → That's Georgie's task, not Alex's\n")
	prompt.WriteString("   - SKIP tasks where Alex is delegating work to someone else\n")
	prompt.WriteString("   - INCLUDE tasks where someone is asking Alex to do something\n")
	prompt.WriteString("   - Exception: 'Follow up with [person]' IS a task for Alex\n\n")

	prompt.WriteString("3. DUPLICATE PREVENTION:\n")
	prompt.WriteString("   - If same action mentioned multiple times, extract it ONCE\n")
	prompt.WriteString("   - Example: 'Cross-reference NPS data' appears 4 times → 1 task total\n")
	prompt.WriteString("   - Consolidate variations: 'Update payment', 'Fix billing', 'Review card' → 1 task: 'Resolve payment issue'\n\n")

	prompt.WriteString("4. META-OBSERVATIONS (DO NOT EXTRACT AS TASKS):\n")
	prompt.WriteString("   - NEVER create tasks like:\n")
	prompt.WriteString("     * 'No clear requests identified'\n")
	prompt.WriteString("     * 'Thread requires further review'\n")
	prompt.WriteString("     * 'Action items unclear'\n")
	prompt.WriteString("   - If no actionable tasks exist, return empty list\n\n")

	prompt.WriteString("5. FYI vs ACTIONABLE:\n")
	prompt.WriteString("   - SKIP: 'FYI', 'for your information', 'keeping you in the loop', 'no action needed'\n")
	prompt.WriteString("   - SKIP: Social invitations ('Join us celebrating', 'Happy hour Friday')\n")
	prompt.WriteString("   - SKIP: Decorations ('Enjoy Halloween decorations', 'Check out new artwork')\n")
	prompt.WriteString("   - SKIP: Thank-yous ('Great job!', 'Thanks for your work')\n")
	prompt.WriteString("   - SKIP: Meeting invitations ('Accept meeting', 'Confirm availability')\n\n")

	prompt.WriteString("\n=== TASK FORMAT REQUIREMENTS ===\n\n")

	prompt.WriteString("For each VALID, UNIQUE task:\n\n")

	prompt.WriteString("- title: Action verb + object (20-60 characters)\n")
	prompt.WriteString("  * MUST be an action: 'Review X', 'Prepare Y', 'Respond to Z'\n")
	prompt.WriteString("  * NOT a timestamp: ❌ 'Oct 2, 12:00 PM - Chris needs...'\n")
	prompt.WriteString("  * NOT a meta-comment: ❌ 'No clear requests identified'\n")
	prompt.WriteString("  * GOOD: ✅ 'Review Q4 budget variance report'\n\n")

	prompt.WriteString("- due_date: Extract ANY temporal reference from the email:\n")
	prompt.WriteString("  * Explicit dates: 'Oct 30', '12/15', '2025-01-15', 'January 15th'\n")
	prompt.WriteString("  * Relative dates: 'tomorrow', 'in 2 days', 'next Monday', 'end of week'\n")
	prompt.WriteString("  * Deadline phrases: 'by Friday', 'before EOD', 'by end of day'\n")
	prompt.WriteString("  * Event-relative: 'before the meeting', 'after launch'\n")
	prompt.WriteString("  * Urgency signals: 'URGENT' → 'today', 'ASAP' → 'within 24 hours'\n")
	prompt.WriteString("  * Format as natural language (e.g., 'Friday', 'Oct 30', 'tomorrow')\n")
	prompt.WriteString("  * If NO temporal reference, leave as 'N/A'\n\n")

	prompt.WriteString("- impact: Business impact (1-5) - **FORCE YOURSELF TO USE FULL RANGE**\n")
	prompt.WriteString("  * 1 = Nice to have, minimal consequence (e.g., 'Review blog draft', 'Update profile')\n")
	prompt.WriteString("  * 2 = Minor impact, affects only you (e.g., 'Organize files', 'Read team update')\n")
	prompt.WriteString("  * 3 = Moderate impact, affects team (e.g., 'Review pull request', 'Update project status')\n")
	prompt.WriteString("  * 4 = Significant impact, affects business/revenue (e.g., 'Fix billing issue', 'Client meeting')\n")
	prompt.WriteString("  * 5 = Critical, company-wide (e.g., 'Fix production outage', 'Security incident')\n")
	prompt.WriteString("  * Payment notifications = impact 2 (informational), NOT 5\n\n")

	prompt.WriteString("- urgency: Time sensitivity (1-5) - **CALIBRATE AGAINST REAL DEADLINES**\n")
	prompt.WriteString("  * 1 = No deadline ('when you have time', 'no rush')\n")
	prompt.WriteString("  * 2 = Weeks away (deadline 2-4 weeks out)\n")
	prompt.WriteString("  * 3 = This week or next ('by Friday', 'next Tuesday')\n")
	prompt.WriteString("  * 4 = Tomorrow or next day ('by EOD Wednesday', subject line with '!')\n")
	prompt.WriteString("  * 5 = Today, overdue, blocking ('URGENT', 'ASAP', '!!!', 'blocking deployment')\n")
	prompt.WriteString("  * Detect urgency signals in subject line and email body\n\n")

	prompt.WriteString("- effort: Estimated time\n")
	prompt.WriteString("  * S = < 1 hour (email reply, quick decision, calendar action)\n")
	prompt.WriteString("  * M = 1-4 hours (doc review, meeting prep, analysis)\n")
	prompt.WriteString("  * L = > 4 hours (training, technical deep-dive, multi-session work)\n\n")

	prompt.WriteString("- stakeholder: Extract the person who needs this done:\n")
	prompt.WriteString("  * FIRST: Check From header - who sent the email?\n")
	prompt.WriteString("  * SECOND: Look for names in email body (@mentions, 'Sarah asked', 'Tim needs')\n")
	prompt.WriteString("  * THIRD: Parse email signatures for full names\n")
	prompt.WriteString("  * FOURTH: Check To/CC headers for context\n")
	prompt.WriteString("  * Extract FULL NAME when possible (e.g., 'Sarah Chen' not 'Sarah')\n")
	prompt.WriteString("  * Normalize email addresses (e.g., 's.chen@company.com' → 'Sarah Chen')\n")
	prompt.WriteString("  * ONLY use 'N/A' if no person identifiable\n")
	prompt.WriteString("  * VALID: 'Sarah Chen', 'Tim Davis'\n")
	prompt.WriteString("  * INVALID: 'Finance Team', 'Customer Support', 'Marketing'\n\n")

	prompt.WriteString("- project: Related project/initiative (if mentioned)\n\n")

	prompt.WriteString("\n=== OUTPUT FORMAT ===\n")
	prompt.WriteString("Numbered list with pipe-delimited fields:\n")
	prompt.WriteString("1. Title: [Action] | Due: [Deadline] | Impact: [1-5] | Urgency: [1-5] | Effort: [S/M/L] | Stakeholder: [Name] | Project: [Context]\n\n")

	prompt.WriteString("If no actionable tasks, return empty list.\n\n")

	prompt.WriteString("YOUR EXTRACTED TASKS:\n")

	return prompt.String()
}

// BuildTaskEnrichment creates a prompt for enriching task descriptions with full email context
// UPDATED: PRESERVE + ADD approach, 400-600 chars, enhanced date/stakeholder extraction
func (p *PromptBuilder) BuildTaskEnrichment(task *db.Task, messages []*db.Message) string {
	var prompt strings.Builder

	prompt.WriteString("You are enriching a task description by ADDING context from the email thread.\n")
	prompt.WriteString("IMPORTANT: Do NOT summarize or replace the existing description. ADD new details to it.\n\n")

	prompt.WriteString("TASK TO ENRICH:\n")
	prompt.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	if task.Description != "" {
		prompt.WriteString(fmt.Sprintf("Current Description: %s\n", task.Description))
		prompt.WriteString("(PRESERVE this and add more details below)\n")
	}
	if task.DueTS != nil {
		prompt.WriteString(fmt.Sprintf("Due Date: %s\n", task.DueTS.Format("Jan 2, 2006")))
	}
	if task.Project != "" {
		prompt.WriteString(fmt.Sprintf("Project: %s\n", task.Project))
	}
	if task.Stakeholder != "" {
		prompt.WriteString(fmt.Sprintf("Stakeholder: %s\n", task.Stakeholder))
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
		prompt.WriteString(fmt.Sprintf("To: %s\n", msg.To))
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
	prompt.WriteString("Write an enriched description (400-600 characters) that ADDS the following details:\n\n")

	prompt.WriteString("1. WHAT is being asked for and WHY it matters\n")
	prompt.WriteString("   - The specific deliverable or action needed\n")
	prompt.WriteString("   - The business context or problem it addresses\n")
	prompt.WriteString("   - Why this is important right now\n\n")

	prompt.WriteString("2. WHO is involved and their role\n")
	prompt.WriteString("   - Extract requester name from 'From:' header (use FULL NAME from signature if available)\n")
	prompt.WriteString("   - Parse email address to name if needed (e.g., s.chen@company.com → Sarah Chen)\n")
	prompt.WriteString("   - Look for names mentioned in email body\n")
	prompt.WriteString("   - Check To/CC headers for other stakeholders\n")
	prompt.WriteString("   - Include role/title if mentioned (e.g., 'Sarah Chen (VP Finance)')\n\n")

	prompt.WriteString("3. WHEN and timeline context\n")
	prompt.WriteString("   - Extract ANY date/time reference from the emails:\n")
	prompt.WriteString("     * Explicit dates: 'Oct 30', '12/15/2024', 'January 15th'\n")
	prompt.WriteString("     * Relative dates: 'tomorrow', 'next Friday', 'end of week', 'in 2 days'\n")
	prompt.WriteString("     * Deadline phrases: 'by EOD', 'before the meeting', 'no later than'\n")
	prompt.WriteString("     * Event-relative: 'before board meeting', 'after launch'\n")
	prompt.WriteString("   - Explain the reason for the timeline (e.g., 'for board meeting on Oct 30')\n")
	prompt.WriteString("   - Note any urgency signals: 'URGENT', 'ASAP', '!!!' in subject\n\n")

	prompt.WriteString("4. HOW this connects to broader work\n")
	prompt.WriteString("   - Related projects or initiatives mentioned\n")
	prompt.WriteString("   - Dependencies or prerequisites\n")
	prompt.WriteString("   - Expected outcomes or success criteria\n\n")

	prompt.WriteString("5. ORIGINAL EMAIL SNIPPET\n")
	prompt.WriteString("   - Include a brief 1-2 sentence quote from the most relevant email\n")
	prompt.WriteString("   - Format as: 'Original request: \"[quote]\"'\n")
	prompt.WriteString("   - Choose the snippet that best captures the task request\n")
	prompt.WriteString("   - Keep the quote under 150 characters\n\n")

	prompt.WriteString("STYLE GUIDELINES:\n")
	prompt.WriteString("- Target length: 400-600 characters (NOT sentences - characters)\n")
	prompt.WriteString("- If current description exists, START with it, then add: 'Additional context: ...'\n")
	prompt.WriteString("- If current description is missing/short, write a complete new description\n")
	prompt.WriteString("- Write in clear, professional language\n")
	prompt.WriteString("- Be specific with names, dates, numbers, and concrete details\n")
	prompt.WriteString("- Focus on actionable context that helps prioritize and execute\n")
	prompt.WriteString("- Avoid speculation - only include information from the thread\n")
	prompt.WriteString("- Add the email snippet at the end on a new line\n\n")

	prompt.WriteString("Now write the enriched description (400-600 characters + email snippet, no preamble):\n")

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

// BuildTaskExtractionWithConversationFlow creates an advanced prompt with conversation awareness
// UPDATED: Now delegates to BuildTaskExtractionWithMetadata for consistency and noise reduction
func (p *PromptBuilder) BuildTaskExtractionWithConversationFlow(messages []*db.Message) string {
	// Delegate to metadata-aware extraction for noise reduction + data extraction
	return p.BuildTaskExtractionWithMetadata(messages)
}
