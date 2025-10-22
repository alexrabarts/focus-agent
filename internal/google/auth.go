package google

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/chat/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/tasks/v1"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// Clients holds all Google API clients
type Clients struct {
	Gmail    *GmailClient
	Drive    *DriveClient
	Calendar *CalendarClient
	Tasks    *TasksClient
	Chat     *ChatClient
}

// NewClients creates all Google API clients
func NewClients(ctx context.Context, cfg *config.Config) (*Clients, error) {
	// Get OAuth2 config
	oauth2Config := &oauth2.Config{
		ClientID:     cfg.Google.ClientID,
		ClientSecret: cfg.Google.ClientSecret,
		RedirectURL:  cfg.Google.RedirectURL,
		Scopes:       cfg.Google.Scopes,
		Endpoint:     google.Endpoint,
	}

	// Get token
	token, err := getToken(ctx, oauth2Config, cfg.Google.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Create HTTP client
	httpClient := oauth2Config.Client(ctx, token)

	// Create Gmail service
	gmailService, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gmail service: %w", err)
	}

	// Capture the authenticated user's email address for downstream services
	if cfg.Google.UserEmail == "" {
		profile, err := gmailService.Users.GetProfile("me").Context(ctx).Do()
		if err != nil {
			log.Printf("WARNING: failed to retrieve Gmail profile for user identification: %v", err)
		} else if profile != nil && profile.EmailAddress != "" {
			cfg.Google.UserEmail = strings.ToLower(profile.EmailAddress)
			log.Printf("Detected authenticated Google Workspace user: %s", cfg.Google.UserEmail)
		}
	}

	// Create Drive service
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %w", err)
	}

	// Create Calendar service
	calendarService, err := calendar.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Calendar service: %w", err)
	}

	// Create Tasks service
	tasksService, err := tasks.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Tasks service: %w", err)
	}

	// Create Chat service
	chatService, err := chat.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Chat service: %w", err)
	}

	return &Clients{
		Gmail:    &GmailClient{Service: gmailService, Config: cfg},
		Drive:    &DriveClient{Service: driveService, Config: cfg},
		Calendar: &CalendarClient{Service: calendarService, Config: cfg},
		Tasks:    &TasksClient{Service: tasksService, Config: cfg},
		Chat:     &ChatClient{Service: chatService, Config: cfg, httpClient: httpClient},
	}, nil
}

// getToken retrieves a token from a local file or runs OAuth flow
func getToken(ctx context.Context, config *oauth2.Config, tokenFile string) (*oauth2.Token, error) {
	// Expand home directory
	if tokenFile[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		tokenFile = filepath.Join(home, tokenFile[2:])
	}

	// Try to read token from file
	token, err := tokenFromFile(tokenFile)
	if err == nil {
		// Check if token is expired and refresh if needed
		tokenSource := config.TokenSource(ctx, token)
		newToken, err := tokenSource.Token()
		if err == nil && newToken.AccessToken != token.AccessToken {
			// Token was refreshed, save it
			saveToken(tokenFile, newToken)
			return newToken, nil
		}
		return token, nil
	}

	// Need to run OAuth flow
	log.Printf("Starting OAuth flow...")
	token, err = getTokenFromWeb(config)
	if err != nil {
		return nil, fmt.Errorf("failed to get token from web: %w", err)
	}

	log.Printf("Token received, saving to %s", tokenFile)
	// Save token
	if err := saveToken(tokenFile, token); err != nil {
		log.Printf("ERROR: failed to save token: %v", err)
		return nil, fmt.Errorf("failed to save token: %w", err)
	}

	log.Printf("Token saved successfully!")
	return token, nil
}

// tokenFromFile retrieves a token from a local file
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	token := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(token)
	return token, err
}

// saveToken saves a token to a file path
func saveToken(path string, token *oauth2.Token) error {
	fmt.Printf("Saving credential file to: %s\n", path)

	// Create directory if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(token)
}

// getTokenFromWeb runs the OAuth flow via web browser
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	// Channel to receive the authorization code
	codeCh := make(chan string)
	errCh := make(chan error)

	// Start local server for callback
	server := &http.Server{Addr: ":8080"}

	// Callback handler
	callbackHandler := func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Callback received: %s", r.URL.String())
		code := r.URL.Query().Get("code")
		if code == "" {
			log.Printf("No authorization code in callback (likely browser request). URL: %s", r.URL.String())
			fmt.Fprintf(w, "Error: No authorization code received")
			return
		}

		log.Printf("Authorization code received, length: %d", len(code))
		codeCh <- code
		fmt.Fprintf(w, `
			<html>
			<head><title>Focus Agent - Authorization Successful</title></head>
			<body>
				<h1>Authorization Successful!</h1>
				<p>You can close this window and return to the terminal.</p>
				<script>window.close();</script>
			</body>
			</html>
		`)
	}

	// Handle both /callback and root path for Desktop OAuth clients
	http.HandleFunc("/callback", callbackHandler)
	http.HandleFunc("/", callbackHandler)

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	// Generate auth URL
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser:\n%v\n\n", authURL)

	// Wait for code or error
	var code string
	select {
	case code = <-codeCh:
		log.Printf("Code received from channel, exchanging for token...")
	case err := <-errCh:
		log.Printf("Error from channel: %v", err)
		return nil, err
	}

	// Exchange code for token
	log.Printf("Exchanging authorization code for access token...")
	token, err := config.Exchange(context.Background(), code)
	if err != nil {
		log.Printf("ERROR: Token exchange failed: %v", err)
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}

	log.Printf("Token exchange successful!")
	return token, nil
}

// RefreshToken refreshes an OAuth token
func RefreshToken(ctx context.Context, cfg *config.Config, token *oauth2.Token) (*oauth2.Token, error) {
	oauth2Config := &oauth2.Config{
		ClientID:     cfg.Google.ClientID,
		ClientSecret: cfg.Google.ClientSecret,
		RedirectURL:  cfg.Google.RedirectURL,
		Scopes:       cfg.Google.Scopes,
		Endpoint:     google.Endpoint,
	}

	tokenSource := oauth2Config.TokenSource(ctx, token)
	newToken, err := tokenSource.Token()
	if err != nil {
		return nil, err
	}

	// Save refreshed token
	if newToken.AccessToken != token.AccessToken {
		if err := saveToken(cfg.Google.TokenFile, newToken); err != nil {
			log.Printf("Warning: failed to save refreshed token: %v", err)
		}
	}

	return newToken, nil
}

// Base client structures with common functionality
type baseClient struct {
	DB *db.DB
}

// Helper function to handle pagination
func paginate(pageToken string, fn func(string) (string, error)) error {
	for {
		nextToken, err := fn(pageToken)
		if err != nil {
			return err
		}
		if nextToken == "" {
			break
		}
		pageToken = nextToken
	}
	return nil
}
