package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/yuri/y/internal/storage"
)

func runSession(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printSessionUsage(stdout)
		return 0
	}

	switch args[0] {
	case "list":
		return runSessionList(stdout, stderr, args[1:])
	case "show":
		return runSessionShow(stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "y session: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runSessionList(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printSessionUsage(stdout)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintf(stderr, "y session list: unexpected argument %q\n", args[0])
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "y session list: %v\n", err)
		return 1
	}

	store := storage.NewSessionStore(storage.DefaultAgentDir())
	summaries, err := store.List(context.Background(), cwd)
	if err != nil {
		fmt.Fprintf(stderr, "y session list: %v\n", err)
		return 1
	}
	if len(summaries) == 0 {
		fmt.Fprintln(stdout, "no sessions found")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMESSAGES\tBYTES\tTRUNCATED\tMODIFIED\tPATH")
	for _, summary := range summaries {
		truncated := "no"
		if summary.Truncated {
			truncated = "yes"
		}
		fmt.Fprintf(
			tw,
			"%s\t%d\t%d\t%s\t%s\t%s\n",
			summary.ID,
			summary.MessageCount,
			summary.ByteSize,
			truncated,
			summary.Modified.UTC().Format("2006-01-02 15:04:05Z"),
			summary.Path,
		)
	}
	_ = tw.Flush()
	return 0
}

func runSessionShow(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printSessionUsage(stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "y session show: unexpected argument %q\n", args[1])
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "y session show: %v\n", err)
		return 1
	}

	store := storage.NewSessionStore(storage.DefaultAgentDir())
	target := ""
	if len(args) == 1 {
		target = args[0]
	}
	path, err := store.Resolve(context.Background(), cwd, target)
	if err != nil {
		if os.IsNotExist(err) {
			if target == "" {
				fmt.Fprintln(stderr, "y session show: no sessions found")
			} else {
				fmt.Fprintf(stderr, "y session show: session %q not found\n", target)
			}
			return 1
		}
		fmt.Fprintf(stderr, "y session show: %v\n", err)
		return 1
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		fmt.Fprintf(stderr, "y session show: %v\n", err)
		return 1
	}
	defer file.Close()

	if _, err := io.Copy(stdout, file); err != nil {
		fmt.Fprintf(stderr, "y session show: %v\n", err)
		return 1
	}
	return 0
}

func printSessionUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y session list")
	fmt.Fprintln(w, "  y session show [id|path]")
}
