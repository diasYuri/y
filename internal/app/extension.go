//go:build feature_wasm_ext

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yuri/y/internal/feature"
	"github.com/yuri/y/internal/storage"
	"github.com/yuri/y/pkg/extensions/wasm"
)

// runExtension dispatches the `y extension` subcommands. It is only
// available in builds compiled with feature_wasm_ext.
func runExtension(stdout, stderr io.Writer, args []string, info BuildInfo, compiled *feature.Registry) int {
	if len(args) == 0 {
		printExtensionUsage(stdout)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		printExtensionUsage(stdout)
		return 0
	case "list":
		return runExtensionList(stdout, stderr, args[1:], info, compiled)
	case "info":
		return runExtensionInfo(stdout, stderr, args[1:], info, compiled)
	case "validate":
		return runExtensionValidate(stdout, stderr, args[1:])
	case "enable":
		return runExtensionToggle(stdout, stderr, args[1:], true)
	case "disable":
		return runExtensionToggle(stdout, stderr, args[1:], false)
	default:
		fmt.Fprintf(stderr, "y extension: unknown subcommand %q\n", args[0])
		return exitCodeUsage
	}
}

func runExtensionList(stdout, stderr io.Writer, args []string, info BuildInfo, compiled *feature.Registry) int {
	dirs, _, ok := parseExtensionListArgs(stderr, args)
	if !ok {
		return exitCodeUsage
	}
	manager, infos, err := discoverExtensions(dirs, info, compiled)
	if err != nil {
		fmt.Fprintf(stderr, "y extension list: %v\n", err)
		return exitCodeExecution
	}
	defer manager.Close(context.Background())

	if len(infos) == 0 {
		fmt.Fprintln(stdout, "no extensions discovered")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tVERSION\tSTATUS\tDIR")
	for _, ext := range infos {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			ext.Manifest.ID, ext.Manifest.Name, ext.Manifest.Version,
			ext.Status, ext.Dir)
	}
	_ = tw.Flush()
	return 0
}

func runExtensionInfo(stdout, stderr io.Writer, args []string, info BuildInfo, compiled *feature.Registry) int {
	dirs, positional, ok := parseExtensionListArgs(stderr, args)
	if !ok {
		return exitCodeUsage
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "y extension info: expected exactly one extension id")
		return exitCodeUsage
	}
	manager, _, err := discoverExtensions(dirs, info, compiled)
	if err != nil {
		fmt.Fprintf(stderr, "y extension info: %v\n", err)
		return exitCodeExecution
	}
	defer manager.Close(context.Background())

	ext, err := manager.Get(positional[0])
	if err != nil {
		if errors.Is(err, wasm.ErrExtensionNotFound) {
			fmt.Fprintf(stderr, "y extension info: %v\n", err)
			return exitCodeUsage
		}
		fmt.Fprintf(stderr, "y extension info: %v\n", err)
		return exitCodeExecution
	}
	printExtensionInfo(stdout, ext)
	return 0
}

func runExtensionValidate(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  y extension validate <path>")
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "<path> may be either an extension directory or its extension.toml manifest.")
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "y extension validate: unexpected argument %q\n", args[1])
		return exitCodeUsage
	}
	manifestPath := args[0]
	stat, err := os.Stat(manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "y extension validate: %v\n", err)
		return exitCodeExecution
	}
	if stat.IsDir() {
		manifestPath = filepath.Join(manifestPath, wasm.ManifestFileName)
	}
	manifest, err := wasm.ReadManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "y extension validate: %v\n", err)
		return exitCodeConfig
	}
	fmt.Fprintf(stdout, "manifest valid: %s %s (api %s)\n",
		manifest.ID, manifest.Version, manifest.APIVersion)
	if len(manifest.Tools) > 0 {
		fmt.Fprintf(stdout, "tools: %s\n", strings.Join(toolNames(manifest), ", "))
	}
	if caps := manifestCapabilityNames(manifest); len(caps) > 0 {
		fmt.Fprintf(stdout, "capabilities requested: %s\n", strings.Join(caps, ", "))
	}
	return 0
}

func runExtensionToggle(stdout, stderr io.Writer, args []string, enable bool) int {
	verb := "enable"
	if !enable {
		verb = "disable"
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintf(stdout, "  y extension %s <id>\n", verb)
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Updates the [wasm.extensions.<id>] block in the runtime config.")
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintf(stderr, "y extension %s: expected exactly one extension id\n", verb)
		return exitCodeUsage
	}
	id := args[0]
	configPath := storage.DefaultConfigPath()
	updated, err := updateExtensionToggle(configPath, id, enable)
	if err != nil {
		fmt.Fprintf(stderr, "y extension %s: %v\n", verb, err)
		return exitCodeExecution
	}
	state := "disabled"
	if enable {
		state = "enabled"
	}
	suffix := ""
	if !updated {
		suffix = " (no change)"
	}
	fmt.Fprintf(stdout, "%s %s in %s%s\n", id, state, configPath, suffix)
	return 0
}

// discoverExtensions builds a Manager configured with the supplied directory
// override (or the default discovery dirs) and runs Discover. Callers must
// Close the returned manager.
func discoverExtensions(dirs []string, info BuildInfo, compiled *feature.Registry) (wasm.Manager, []wasm.ExtensionInfo, error) {
	if dirs == nil {
		dirs = defaultExtensionDirs()
	}
	cfg := wasm.Config{ExtensionDirs: dirs, HostVersion: info.Version}
	manager := wasm.NewManager(cfg)
	if err := manager.Discover(context.Background()); err != nil {
		manager.Close(context.Background())
		return nil, nil, err
	}
	infos := manager.List()
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Manifest.ID < infos[j].Manifest.ID
	})
	_ = compiled // reserved for future capability checks during discovery
	return manager, infos, nil
}

// defaultExtensionDirs returns the directories scanned when no --dir override
// is supplied.
func defaultExtensionDirs() []string {
	dirs := []string{filepath.Join(storage.DefaultAgentDir(), "extensions")}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, ".y", "extensions"))
	}
	return dirs
}

// parseExtensionListArgs returns the optional --dir directories and any
// remaining positional arguments. The returned dirs slice is nil when the
// caller did not pass --dir.
func parseExtensionListArgs(stderr io.Writer, args []string) ([]string, []string, bool) {
	var dirs []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			fmt.Fprintln(stderr, "Usage:")
			fmt.Fprintln(stderr, "  y extension list [--dir <path>] [--dir <path> ...]")
			fmt.Fprintln(stderr, "  y extension info [--dir <path> ...] <id>")
			return nil, nil, false
		case "--dir":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y extension: --dir requires a path")
				return nil, nil, false
			}
			i++
			dirs = append(dirs, args[i])
		default:
			if strings.HasPrefix(arg, "--dir=") {
				dirs = append(dirs, strings.TrimPrefix(arg, "--dir="))
				continue
			}
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(stderr, "y extension: unknown flag %q\n", arg)
				return nil, nil, false
			}
			positional = append(positional, arg)
		}
	}
	return dirs, positional, true
}

func printExtensionInfo(w io.Writer, ext wasm.ExtensionInfo) {
	m := ext.Manifest
	fmt.Fprintf(w, "id: %s\n", m.ID)
	fmt.Fprintf(w, "name: %s\n", m.Name)
	fmt.Fprintf(w, "version: %s\n", m.Version)
	fmt.Fprintf(w, "api_version: %s\n", m.APIVersion)
	fmt.Fprintf(w, "entry: %s\n", filepath.Join(ext.Dir, m.Entry))
	fmt.Fprintf(w, "status: %s\n", ext.Status)
	if ext.LastErr != "" {
		fmt.Fprintf(w, "last_error: %s\n", ext.LastErr)
	}
	if m.Runtime.MemoryPages != 0 {
		fmt.Fprintf(w, "memory_pages: %d\n", m.Runtime.MemoryPages)
	}
	if m.Runtime.TimeoutMS != 0 {
		fmt.Fprintf(w, "timeout_ms: %d\n", m.Runtime.TimeoutMS)
	}
	if m.Runtime.MinYVersion != "" {
		fmt.Fprintf(w, "min_y_version: %s\n", m.Runtime.MinYVersion)
	}
	if caps := manifestCapabilityNames(m); len(caps) > 0 {
		fmt.Fprintf(w, "capabilities: %s\n", strings.Join(caps, ", "))
	}
	if len(m.Tools) > 0 {
		fmt.Fprintln(w, "tools:")
		for _, tool := range m.Tools {
			if tool.Description != "" {
				fmt.Fprintf(w, "  - %s — %s\n", tool.Name, tool.Description)
			} else {
				fmt.Fprintf(w, "  - %s\n", tool.Name)
			}
		}
	}
}

func manifestCapabilityNames(m wasm.Manifest) []string {
	caps := make([]string, 0, 8)
	if m.Capabilities.YTools {
		caps = append(caps, "y_tools")
	}
	if m.Capabilities.Filesystem {
		caps = append(caps, "filesystem")
	}
	if m.Capabilities.Network {
		caps = append(caps, "network")
	}
	if m.Capabilities.Process {
		caps = append(caps, "process")
	}
	if m.Capabilities.Git {
		caps = append(caps, "git")
	}
	if m.Capabilities.Secrets {
		caps = append(caps, "secrets")
	}
	if m.Capabilities.Storage {
		caps = append(caps, "storage")
	}
	if m.Capabilities.Logs {
		caps = append(caps, "logs")
	}
	return caps
}

func toolNames(m wasm.Manifest) []string {
	out := make([]string, len(m.Tools))
	for i, t := range m.Tools {
		out[i] = t.Name
	}
	return out
}

func printExtensionUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y extension list      [--dir <path>]")
	fmt.Fprintln(w, "  y extension info      [--dir <path>] <id>")
	fmt.Fprintln(w, "  y extension validate  <path>")
	fmt.Fprintln(w, "  y extension enable    <id>")
	fmt.Fprintln(w, "  y extension disable   <id>")
}
