package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database   Database   `yaml:"database"`
	Google     Google     `yaml:"google"`
	Gemini     Gemini     `yaml:"gemini"`
	Chat       Chat       `yaml:"chat"`
	API        API        `yaml:"api"`
	Remote     Remote     `yaml:"remote"`
	TUI        TUI        `yaml:"tui"`
	Schedule   Schedule   `yaml:"schedule"`
	Planner    Planner    `yaml:"planner"`
	Limits     Limits     `yaml:"limits"`
	Priorities Priorities `yaml:"priorities"`
}

type Database struct {
	Path string `yaml:"path"`
}

type Google struct {
	ClientID       string   `yaml:"client_id"`
	ClientSecret   string   `yaml:"client_secret"`
	RedirectURL    string   `yaml:"redirect_url"`
	TokenFile      string   `yaml:"token_file"`
	Scopes         []string `yaml:"scopes"`
	UserEmail      string   `yaml:"user_email,omitempty"` // Populated from Gmail profile
	PollingMinutes struct {
		Gmail    int `yaml:"gmail"`
		Drive    int `yaml:"drive"`
		Calendar int `yaml:"calendar"`
		Tasks    int `yaml:"tasks"`
	} `yaml:"polling_minutes"`
}

type Gemini struct {
	APIKey           string         `yaml:"api_key"`
	Model            string         `yaml:"model"`
	MaxTokens        int            `yaml:"max_tokens"`
	Temperature      float32        `yaml:"temperature"`
	CacheHours       int            `yaml:"cache_hours"`
	RateLimits       map[string]int `yaml:"rate_limits"`        // Requests per minute per model
	DefaultRateLimit int            `yaml:"default_rate_limit"` // Fallback for unknown models
	RetryOnRateLimit bool           `yaml:"retry_on_rate_limit"`
	MaxRetries       int            `yaml:"max_retries"`
	BaseRetryDelay   int            `yaml:"base_retry_delay_seconds"`
}

type Chat struct {
	WebhookURL string `yaml:"webhook_url"`
	SpaceID    string `yaml:"space_id"`
	ThreadKey  string `yaml:"thread_key"`
}

type API struct {
	Enabled bool   `yaml:"enabled"`
	Port    int    `yaml:"port"`
	AuthKey string `yaml:"auth_key"`
}

type Remote struct {
	URL     string `yaml:"url"`
	AuthKey string `yaml:"auth_key"`
}

type TUI struct {
	AutoRefreshSeconds int `yaml:"auto_refresh_seconds"`
}

type Schedule struct {
	DailyBriefTime  string `yaml:"daily_brief_time"` // "07:45"
	ReplanTime      string `yaml:"replan_time"`      // "13:00"
	FollowUpMinutes int    `yaml:"followup_minutes"` // 60
	Timezone        string `yaml:"timezone"`         // "America/Los_Angeles"
}

type Planner struct {
	Weights struct {
		Impact      float64 `yaml:"impact"`
		Urgency     float64 `yaml:"urgency"`
		Stakeholder float64 `yaml:"stakeholder"`
		Effort      float64 `yaml:"effort"`
	} `yaml:"weights"`
	MaxTasksPerBrief int `yaml:"max_tasks_per_brief"`
	FocusBlockHours  int `yaml:"focus_block_hours"`
}

type Limits struct {
	// Gmail limits
	MaxThreadsPerSync     int  `yaml:"max_threads_per_sync"`
	MaxAIProcessingPerRun int  `yaml:"max_ai_processing_per_run"`
	UnreadOnly            bool `yaml:"unread_only"`
	DaysOfHistory         int  `yaml:"days_of_history"`

	// Drive limits
	MaxDocumentsPerSync int `yaml:"max_documents_per_sync"`
	DriveDaysOfHistory  int `yaml:"drive_days_of_history"`

	// Calendar limits
	CalendarDaysAhead int `yaml:"calendar_days_ahead"`

	// Tasks limits
	MaxTaskLists int `yaml:"max_task_lists"`
}

type Priorities struct {
	// OKRs (Objectives and Key Results)
	OKRs []string `yaml:"okrs"`

	// Strategic focus areas
	FocusAreas []string `yaml:"focus_areas"`

	// Key stakeholder email addresses
	KeyStakeholders []string `yaml:"key_stakeholders"`

	// Key projects
	KeyProjects []string `yaml:"key_projects"`
}

func Load(path string) (*Config, error) {
	// Expand home directory
	if path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	// Create default config if it doesn't exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := createDefaultConfig(path); err != nil {
			return nil, fmt.Errorf("failed to create default config: %w", err)
		}
		return nil, fmt.Errorf("config file created at %s - please update it with your settings", path)
	}

	// Read config file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse YAML
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Apply defaults
	applyDefaults(&config)

	// Validate
	if err := validate(&config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &config, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Database.Path == "" {
		cfg.Database.Path = os.ExpandEnv("$HOME/.focus-agent/data.db")
	}

	if cfg.Google.RedirectURL == "" {
		cfg.Google.RedirectURL = "http://localhost:8080/callback"
	}

	if cfg.Google.TokenFile == "" {
		cfg.Google.TokenFile = os.ExpandEnv("$HOME/.focus-agent/token.json")
	}

	requiredScopes := []string{
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/calendar.readonly",
		"https://www.googleapis.com/auth/tasks.readonly",
		"https://www.googleapis.com/auth/chat.messages",
		"https://www.googleapis.com/auth/chat.spaces.readonly",
		"https://www.googleapis.com/auth/chat.memberships.readonly",
	}

	if len(cfg.Google.Scopes) == 0 {
		cfg.Google.Scopes = append([]string{}, requiredScopes...)
	} else {
		existing := make(map[string]struct{}, len(cfg.Google.Scopes))
		for _, scope := range cfg.Google.Scopes {
			existing[scope] = struct{}{}
		}
		for _, scope := range requiredScopes {
			if _, ok := existing[scope]; !ok {
				cfg.Google.Scopes = append(cfg.Google.Scopes, scope)
			}
		}
	}

	// Polling defaults
	if cfg.Google.PollingMinutes.Gmail == 0 {
		cfg.Google.PollingMinutes.Gmail = 5
	}
	if cfg.Google.PollingMinutes.Drive == 0 {
		cfg.Google.PollingMinutes.Drive = 10
	}
	if cfg.Google.PollingMinutes.Calendar == 0 {
		cfg.Google.PollingMinutes.Calendar = 15
	}
	if cfg.Google.PollingMinutes.Tasks == 0 {
		cfg.Google.PollingMinutes.Tasks = 15
	}

	// Gemini defaults
	if cfg.Gemini.Model == "" {
		cfg.Gemini.Model = "gemini-2.5-flash"
	}
	if cfg.Gemini.MaxTokens == 0 {
		cfg.Gemini.MaxTokens = 2000
	}
	if cfg.Gemini.Temperature == 0 {
		cfg.Gemini.Temperature = 0.3
	}
	if cfg.Gemini.CacheHours == 0 {
		cfg.Gemini.CacheHours = 24
	}
	// Rate limit defaults
	if cfg.Gemini.RateLimits == nil {
		cfg.Gemini.RateLimits = map[string]int{
			"gemini-2.5-flash":      10, // Free tier: 10 RPM
			"gemini-2.5-pro":        2,  // Free tier: 2 RPM
			"gemini-2.0-flash":      10, // Free tier: 10 RPM
			"gemini-2.0-flash-lite": 15, // Free tier: 15 RPM
		}
	}
	if cfg.Gemini.DefaultRateLimit == 0 {
		cfg.Gemini.DefaultRateLimit = 5
	}
	if !cfg.Gemini.RetryOnRateLimit {
		cfg.Gemini.RetryOnRateLimit = true
	}
	if cfg.Gemini.MaxRetries == 0 {
		cfg.Gemini.MaxRetries = 3
	}
	if cfg.Gemini.BaseRetryDelay == 0 {
		cfg.Gemini.BaseRetryDelay = 60
	}

	// API defaults
	if cfg.API.Port == 0 {
		cfg.API.Port = 8081
	}

	// TUI defaults
	if cfg.TUI.AutoRefreshSeconds == 0 {
		cfg.TUI.AutoRefreshSeconds = 30
	}

	// Schedule defaults
	if cfg.Schedule.DailyBriefTime == "" {
		cfg.Schedule.DailyBriefTime = "07:45"
	}
	if cfg.Schedule.ReplanTime == "" {
		cfg.Schedule.ReplanTime = "13:00"
	}
	if cfg.Schedule.FollowUpMinutes == 0 {
		cfg.Schedule.FollowUpMinutes = 60
	}
	if cfg.Schedule.Timezone == "" {
		cfg.Schedule.Timezone = "America/Los_Angeles"
	}

	// Planner defaults
	if cfg.Planner.Weights.Impact == 0 {
		cfg.Planner.Weights.Impact = 0.4
	}
	if cfg.Planner.Weights.Urgency == 0 {
		cfg.Planner.Weights.Urgency = 0.35
	}
	if cfg.Planner.Weights.Stakeholder == 0 {
		cfg.Planner.Weights.Stakeholder = 0.15
	}
	if cfg.Planner.Weights.Effort == 0 {
		cfg.Planner.Weights.Effort = 0.1
	}
	if cfg.Planner.MaxTasksPerBrief == 0 {
		cfg.Planner.MaxTasksPerBrief = 10
	}
	if cfg.Planner.FocusBlockHours == 0 {
		cfg.Planner.FocusBlockHours = 2
	}

	// Limits defaults
	if cfg.Limits.MaxThreadsPerSync == 0 {
		cfg.Limits.MaxThreadsPerSync = 50
	}
	if cfg.Limits.MaxAIProcessingPerRun == 0 {
		cfg.Limits.MaxAIProcessingPerRun = 10
	}
	if cfg.Limits.DaysOfHistory == 0 {
		cfg.Limits.DaysOfHistory = 7
	}
	if cfg.Limits.MaxDocumentsPerSync == 0 {
		cfg.Limits.MaxDocumentsPerSync = 50
	}
	if cfg.Limits.DriveDaysOfHistory == 0 {
		cfg.Limits.DriveDaysOfHistory = 7
	}
	if cfg.Limits.CalendarDaysAhead == 0 {
		cfg.Limits.CalendarDaysAhead = 30
	}
	if cfg.Limits.MaxTaskLists == 0 {
		cfg.Limits.MaxTaskLists = 10
	}
}

func validate(cfg *Config) error {
	if cfg.Google.ClientID == "" {
		return fmt.Errorf("google.client_id is required")
	}
	if cfg.Google.ClientSecret == "" {
		return fmt.Errorf("google.client_secret is required")
	}
	if cfg.Gemini.APIKey == "" {
		return fmt.Errorf("gemini.api_key is required")
	}
	if cfg.Chat.WebhookURL == "" {
		return fmt.Errorf("chat.webhook_url is required")
	}
	return nil
}

func createDefaultConfig(path string) error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Create example config
	exampleConfig := `# Focus Agent Configuration

database:
  path: ~/.focus-agent/data.db

google:
  # Get these from Google Cloud Console
  client_id: YOUR_CLIENT_ID_HERE
  client_secret: YOUR_CLIENT_SECRET_HERE
  redirect_url: http://localhost:8080/callback
  token_file: ~/.focus-agent/token.json

  # OAuth scopes (default read-only)
  scopes:
    - https://www.googleapis.com/auth/gmail.readonly
    - https://www.googleapis.com/auth/drive.readonly
    - https://www.googleapis.com/auth/calendar.readonly
    - https://www.googleapis.com/auth/tasks.readonly

  # Polling intervals in minutes
  polling_minutes:
    gmail: 5
    drive: 10
    calendar: 15
    tasks: 15

gemini:
  # Get from AI Studio: https://aistudio.google.com/app/apikey
  api_key: YOUR_GEMINI_API_KEY_HERE
  model: gemini-1.5-flash
  max_tokens: 2000
  temperature: 0.3
  cache_hours: 24

chat:
  # Google Chat webhook URL
  webhook_url: YOUR_WEBHOOK_URL_HERE
  space_id: YOUR_SPACE_ID
  thread_key: focus-agent

schedule:
  daily_brief_time: "07:45"
  replan_time: "13:00"
  followup_minutes: 60
  timezone: America/Los_Angeles

planner:
  # Task scoring weights
  weights:
    impact: 0.4
    urgency: 0.35
    stakeholder: 0.15
    effort: 0.1

  max_tasks_per_brief: 10
  focus_block_hours: 2
`

	if err := os.WriteFile(path, []byte(exampleConfig), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
