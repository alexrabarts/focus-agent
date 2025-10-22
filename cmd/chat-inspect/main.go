package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/google"
)

func main() {
	configFile := flag.String("config", os.ExpandEnv("$HOME/.focus-agent/config.yaml"), "Path to configuration file")
	listSpaces := flag.Bool("list", false, "List direct message spaces accessible to the authenticated user")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx := context.Background()
	clients, err := google.NewClients(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to initialize Google clients: %v", err)
	}

	if *listSpaces {
		printSpaces(ctx, clients)
		return
	}

	if flag.NArg() == 0 {
		log.Fatalf("usage: %s [flags] <space-id>\n       %s -list", os.Args[0], os.Args[0])
	}

	spaceID := flag.Arg(0)
	if !strings.HasPrefix(spaceID, "spaces/") {
		spaceID = fmt.Sprintf("spaces/%s", spaceID)
	}

	printSpaceDetails(ctx, clients, spaceID)
}

func printSpaces(ctx context.Context, clients *google.Clients) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SPACE\tTYPE\tSINGLE_USER_DM\tDISPLAY_NAME\t")

	pageToken := ""
	for {
		call := clients.Chat.Service.Spaces.List().Filter("spaceType = \"DIRECT_MESSAGE\"")
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Context(ctx).Do()
		if err != nil {
			log.Fatalf("failed to list spaces: %v", err)
		}

		for _, space := range resp.Spaces {
			fmt.Fprintf(w, "%s\t%s\t%t\t%s\t\n",
				space.Name,
				space.SpaceType,
				space.SingleUserBotDm,
				space.DisplayName)
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	w.Flush()
}

func printSpaceDetails(ctx context.Context, clients *google.Clients, spaceID string) {
	space, err := clients.Chat.Service.Spaces.Get(spaceID).Context(ctx).Do()
	if err != nil {
		log.Fatalf("failed to get space details: %v", err)
	}

	fmt.Printf("Space: %s\n", space.Name)
	fmt.Printf("DisplayName: %s\n", space.DisplayName)
	fmt.Printf("Type: %s (SingleUserBotDM=%v)\n", space.SpaceType, space.SingleUserBotDm)
	fmt.Printf("Created: %s\n", space.CreateTime)
	fmt.Printf("History: %s\n", space.SpaceHistoryState)
	fmt.Printf("URI: %s\n", space.SpaceUri)
	fmt.Println()

	fmt.Println("Members:")
	call := clients.Chat.Service.Spaces.Members.List(spaceID)
	for {
		resp, err := call.Context(ctx).Do()
		if err != nil {
			log.Fatalf("failed to list members: %v", err)
		}

		for _, member := range resp.Memberships {
			m := member.Member
			fmt.Printf("- %s (%s) name=%s state=%s role=%s\n", m.DisplayName, m.Type, m.Name, member.State, member.Role)
		}

		if resp.NextPageToken == "" {
			break
		}
		call.PageToken(resp.NextPageToken)
	}
}
