package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yuri/y/internal/auth"
	"github.com/yuri/y/internal/feature"
)

func runAuth(stdout, stderr io.Writer, args []string, compiled *feature.Registry) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printAuthUsage(stdout)
		return 0
	}

	switch args[0] {
	case "login":
		return runAuthLogin(stdout, stderr, args[1:])
	case "logout":
		return runAuthLogout(stdout, stderr, args[1:])
	case "list":
		return runAuthList(stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "y auth: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runAuthLogin(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "y auth login: provider is required")
		return 2
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "y auth login: unexpected argument %q\n", args[1])
		return 2
	}

	providerID := strings.TrimSpace(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Fprintf(stderr, "Starting login for %s...\n", providerID)
	creds, err := auth.Login(ctx, providerID)
	if err != nil {
		fmt.Fprintf(stderr, "y auth login: %v\n", err)
		return 1
	}

	store := auth.NewStore()
	if err := store.Write(creds); err != nil {
		fmt.Fprintf(stderr, "y auth login: failed to save credentials: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Logged in to %s.\n", providerID)
	return 0
}

func runAuthLogout(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "y auth logout: provider is required")
		return 2
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "y auth logout: unexpected argument %q\n", args[1])
		return 2
	}

	providerID := strings.TrimSpace(args[0])
	if err := auth.Logout(providerID); err != nil {
		fmt.Fprintf(stderr, "y auth logout: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Logged out from %s.\n", providerID)
	return 0
}

func runAuthList(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printAuthUsage(stdout)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintf(stderr, "y auth list: unexpected argument %q\n", args[0])
		return 2
	}

	store := auth.NewStore()
	ids, err := store.List()
	if err != nil {
		fmt.Fprintf(stderr, "y auth list: %v\n", err)
		return 1
	}
	if len(ids) == 0 {
		fmt.Fprintln(stdout, "no stored credentials")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tEXPIRES\tTOKEN_TYPE")
	for _, id := range ids {
		creds, err := store.Read(id)
		if err != nil || creds == nil {
			fmt.Fprintf(tw, "%s\tunknown\tunknown\n", id)
			continue
		}
		expires := "never"
		if !creds.ExpiresAt.IsZero() {
			if creds.IsExpired() {
				expires = "expired"
			} else {
				expires = creds.ExpiresAt.Format("2006-01-02 15:04:05")
			}
		}
		tt := creds.TokenType
		if tt == "" {
			tt = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", id, expires, tt)
	}
	_ = tw.Flush()
	return 0
}

func printAuthUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y auth login <provider>")
	fmt.Fprintln(w, "  y auth logout <provider>")
	fmt.Fprintln(w, "  y auth list")
}
