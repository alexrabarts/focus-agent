package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/google"
)

var (
	configFile = flag.String("config", os.ExpandEnv("$HOME/.focus-agent/config.yaml"), "Path to configuration file")
)

func main() {
	flag.Parse()

	// Load config
	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize Google clients
	ctx := context.Background()
	clients, err := google.NewClients(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize Google clients: %v", err)
	}

	if clients.Chat == nil {
		log.Fatalf("Chat client not initialized - check config and scopes")
	}

	userEmail := strings.ToLower(cfg.Google.UserEmail)
	log.Printf("Looking for Chat DM space for user: %s", userEmail)

	// List all spaces
	pageToken := ""
	foundSpaces := 0

	for {
		call := clients.Chat.Service.Spaces.List()
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Context(ctx).Do()
		if err != nil {
			log.Fatalf("Failed to list spaces: %v", err)
		}

		log.Printf("Found %d spaces in this page", len(resp.Spaces))

		for _, space := range resp.Spaces {
			foundSpaces++
			log.Printf("\nSpace #%d:", foundSpaces)
			log.Printf("  Name: %s", space.Name)
			log.Printf("  Type: %s", space.SpaceType)
			log.Printf("  DisplayName: %s", space.DisplayName)
			log.Printf("  SingleUserBotDm: %v", space.SingleUserBotDm)

			// Check if this is a DM
			if space.SpaceType == "DIRECT_MESSAGE" {
				log.Printf("  ✓ This is a DM space")

				if space.SingleUserBotDm {
					log.Printf("  ✓ This is a single-user bot DM")

					// Try to verify membership
					memberResource := fmt.Sprintf("%s/members/users/%s", space.Name, url.PathEscape(userEmail))
					member, err := clients.Chat.Service.Spaces.Members.Get(memberResource).Context(ctx).Do()
					if err != nil {
						log.Printf("  ✗ Could not verify user membership: %v", err)
					} else {
						log.Printf("  ✓ User is a member: %s", member.Name)
						log.Printf("\n🎯 FOUND IT! Use this space ID in your config:")
						log.Printf("   space_id: %s", space.Name)
					}
				}
			}
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	log.Printf("\nTotal spaces found: %d", foundSpaces)
}
