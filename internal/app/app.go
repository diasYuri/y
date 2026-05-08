package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/yuri/y/internal/buildinfo"
	"github.com/yuri/y/internal/config"
	"github.com/yuri/y/internal/diagnostics"
	"github.com/yuri/y/internal/feature"
)

// BuildInfo is the build metadata needed during CLI bootstrap.
type BuildInfo = buildinfo.Info

// Run executes the y CLI bootstrap and returns a process exit code.
func Run(stdout, stderr io.Writer, args []string, info BuildInfo) int {
	compiled := feature.NewRegistry()
	if err := feature.RegisterCompiledFeatures(compiled); err != nil {
		fmt.Fprintf(stderr, "y: failed to register compiled features: %v\n", err)
		return 1
	}

	if len(args) == 0 {
		printUsage(stdout, info, compiled)
		return 0
	}

	switch args[0] {
	case "-h", "--help", "help":
		printUsage(stdout, info, compiled)
		return 0
	case "-v", "--version":
		fmt.Fprintln(stdout, info.Version)
		return 0
	case "version":
		return runVersion(stdout, stderr, args[1:], info, compiled)
	case "features":
		return runFeatures(stdout, stderr, args[1:], compiled)
	case "config":
		return runConfig(stdout, stderr, args[1:], compiled)
	case "init":
		return runInit(stdout, stderr, args[1:])
	case "doctor":
		return runDoctor(stdout, stderr, args[1:], info, compiled)
	case "session":
		return runSession(stdout, stderr, args[1:])
	case "extension", "extensions":
		return runExtension(stdout, stderr, args[1:], info, compiled)
	case "auth":
		return runAuth(stdout, stderr, args[1:], compiled)
	case "run":
		return runRun(stdout, stderr, os.Stdin, isTerminal(os.Stdin), args[1:], info, compiled)
	case "rpc":
		return runRPC(stdout, stderr, args[1:])
	case "lsp":
		return runLSP(stdout, stderr, args[1:])
	case "chat":
		return runChat(stdout, stderr, os.Stdin, isTerminal(os.Stdin), args[1:], info, compiled)
	default:
		fmt.Fprintf(stderr, "y: unknown command %q\n", args[0])
		fmt.Fprintln(stderr, "Run `y --help` for usage.")
		return 2
	}
}

func printUsage(w io.Writer, info BuildInfo, compiled *feature.Registry) {
	fmt.Fprintf(w, "y %s\n\n", info.Version)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y [command]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  help             Show this help.")
	fmt.Fprintln(w, "  version          Show build version.")
	fmt.Fprintln(w, "  features         List compiled and unavailable capabilities.")
	fmt.Fprintln(w, "  config validate  Validate declarative configuration.")
	fmt.Fprintln(w, "  config show      Display parsed configuration.")
	fmt.Fprintln(w, "  init             Generate a default configuration file.")
	fmt.Fprintln(w, "  doctor           Run basic environment diagnostics.")
	fmt.Fprintln(w, "  session list     List saved sessions for the current workspace.")
	fmt.Fprintln(w, "  session show     Print a saved session transcript as JSONL.")
	if compiled != nil && compiled.Has(feature.KindFeature, "wasm_extensions") {
		fmt.Fprintln(w, "  extension        Manage WASM extensions (list, info, validate, enable, disable).")
	}
	fmt.Fprintln(w, "  auth             Manage OAuth credentials (login, logout, list).")
	fmt.Fprintln(w, "  run              Execute one prompt headlessly and stream text.")
	fmt.Fprintln(w, "  rpc              Start a JSON-RPC server for programmatic access.")
	fmt.Fprintln(w, "  lsp              Connect to a language server and initialize.")
	fmt.Fprintln(w, "  chat             Start a basic stdin/stdout chat loop.")
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func runFeatures(stdout, stderr io.Writer, args []string, compiled *feature.Registry) int {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			printFeaturesUsage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "y features: unexpected argument %q\n", args[0])
		return 2
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tID\tCOMPILED\tBUILD_TAG\tDESCRIPTION")
	for _, status := range compiled.Status() {
		compiledText := "no"
		if status.Compiled {
			compiledText = "yes"
		}
		buildTag := status.BuildTag
		if buildTag == "" {
			buildTag = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", status.Kind, status.ID, compiledText, buildTag, status.Description)
	}
	_ = tw.Flush()
	return 0
}

func runInit(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  y init [path]")
		return 0
	}

	path := "y.toml"
	if len(args) > 0 {
		path = args[0]
	}

	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stderr, "y init: file %q already exists\n", path)
		return 1
	}

	data := config.GenerateDefault()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		fmt.Fprintf(stderr, "y init: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created %s\n", path)
	return 0
}

func runConfig(stdout, stderr io.Writer, args []string, compiled *feature.Registry) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printConfigUsage(stdout)
		return 0
	}

	switch args[0] {
	case "validate":
		return runConfigValidate(stdout, stderr, args[1:], compiled)
	case "show":
		return runConfigShow(stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "y config: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runConfigValidate(stdout, stderr io.Writer, args []string, compiled *feature.Registry) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printConfigUsage(stdout)
		return 0
	}

	path, ok := parseConfigValidateArgs(stderr, args)
	if !ok {
		return 2
	}

	var cfg config.Config
	if path != "" {
		loaded, err := config.LoadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "y config validate: %v\n", err)
			return 1
		}
		cfg = loaded
	}

	if err := config.Validate(cfg, compiled); err != nil {
		fmt.Fprintf(stderr, "y config validate: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "configuration valid")
	return 0
}

func runConfigShow(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, "  y config show [path]")
		return 0
	}

	path := "y.toml"
	if len(args) > 0 {
		path = args[0]
	}

	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "y config show: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Configuration from %s\n\n", path)

	if len(cfg.Features) > 0 {
		fmt.Fprintln(stdout, "Features:")
		for name, enabled := range cfg.Features {
			status := "disabled"
			if enabled {
				status = "enabled"
			}
			fmt.Fprintf(stdout, "  %s: %s\n", name, status)
		}
		fmt.Fprintln(stdout)
	}

	if len(cfg.Providers) > 0 {
		fmt.Fprintln(stdout, "Providers:")
		for name, enabled := range cfg.Providers {
			status := "disabled"
			if enabled {
				status = "enabled"
			}
			fmt.Fprintf(stdout, "  %s: %s\n", name, status)
		}
		fmt.Fprintln(stdout)
	}

	if len(cfg.Tools) > 0 {
		fmt.Fprintln(stdout, "Tools:")
		for name, enabled := range cfg.Tools {
			status := "disabled"
			if enabled {
				status = "enabled"
			}
			fmt.Fprintf(stdout, "  %s: %s\n", name, status)
		}
		fmt.Fprintln(stdout)
	}

	if len(cfg.Limits) > 0 {
		fmt.Fprintln(stdout, "Limits:")
		for name, value := range cfg.Limits {
			fmt.Fprintf(stdout, "  %s: %d\n", name, value)
		}
		fmt.Fprintln(stdout)
	}

	fmt.Fprintf(stdout, "Offline mode: %v\n", cfg.OfflineMode)
	fmt.Fprintf(stdout, "Telemetry:    %v\n", cfg.Telemetry)
	return 0
}

func runDoctor(stdout, stderr io.Writer, args []string, info BuildInfo, compiled *feature.Registry) int {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			printDoctorUsage(stdout)
			return 0
		case "--json":
			jsonOutput = true
		default:
			fmt.Fprintf(stderr, "y doctor: unexpected argument %q\n", arg)
			return 2
		}
	}

	report := diagnostics.Doctor(info, compiled)
	if jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "y doctor: failed to encode JSON: %v\n", err)
			return 1
		}
		return 0
	}

	printDoctorText(stdout, report)
	return 0
}

func printDoctorText(w io.Writer, report diagnostics.DoctorReport) {
	fmt.Fprintln(w, "Y doctor")
	fmt.Fprintf(w, "status: %s\n", report.Status)
	fmt.Fprintf(w, "version: %s\n", report.Build.Version)
	fmt.Fprintf(w, "commit: %s\n", report.Build.Commit)
	fmt.Fprintf(w, "date: %s\n", report.Build.Date)
	tags := "-"
	if len(report.Build.Tags) > 0 {
		tags = strings.Join(report.Build.Tags, ",")
	}
	fmt.Fprintf(w, "tags: %s\n", tags)
	fmt.Fprintf(w, "go: %s\n", report.Runtime.GoVersion)
	fmt.Fprintf(w, "platform: %s/%s\n", report.Runtime.GOOS, report.Runtime.GOARCH)
	fmt.Fprintf(w, "compiled capabilities: %d/%d\n", report.Capabilities.CompiledCount, report.Capabilities.KnownCount)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Checks:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tID\tMESSAGE")
	for _, check := range report.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", check.Status, check.ID, check.Message)
	}
	_ = tw.Flush()
}

func parseConfigValidateArgs(stderr io.Writer, args []string) (string, bool) {
	var path string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y config validate: --config requires a path")
				return "", false
			}
			i++
			if path != "" {
				fmt.Fprintln(stderr, "y config validate: config path specified more than once")
				return "", false
			}
			path = args[i]
		default:
			if path != "" {
				fmt.Fprintf(stderr, "y config validate: unexpected argument %q\n", arg)
				return "", false
			}
			path = arg
		}
	}
	return path, true
}

func printFeaturesUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y features")
}

func printConfigUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y config validate [--config path]")
	fmt.Fprintln(w, "  y config show [path]")
}

func printDoctorUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y doctor [--json]")
}

func runVersion(stdout, stderr io.Writer, args []string, info BuildInfo, compiled *feature.Registry) int {
	jsonOutput := false
	verbose := false
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			fmt.Fprintln(stdout, "Usage:")
			fmt.Fprintln(stdout, "  y version")
			fmt.Fprintln(stdout, "  y version --verbose")
			fmt.Fprintln(stdout, "  y version --json")
			return 0
		case "--json":
			jsonOutput = true
		case "--verbose", "-v":
			verbose = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(stderr, "y version: unknown flag %q\n", arg)
				return exitCodeUsage
			}
			fmt.Fprintf(stderr, "y version: unexpected argument %q\n", arg)
			return exitCodeUsage
		}
	}

	if jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(info)
		return 0
	}

	if verbose {
		fmt.Fprintf(stdout, "y %s\n", info.Version)
		fmt.Fprintf(stdout, "  commit:   %s\n", info.Commit)
		fmt.Fprintf(stdout, "  date:     %s\n", info.Date)
		fmt.Fprintf(stdout, "  go:       %s\n", runtime.Version())
		fmt.Fprintf(stdout, "  os/arch:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
		if len(info.Tags) > 0 {
			fmt.Fprintf(stdout, "  tags:     %s\n", strings.Join(info.Tags, ", "))
		} else {
			fmt.Fprintln(stdout, "  tags:     -")
		}
		return 0
	}

	fmt.Fprintln(stdout, info.Version)
	return 0
}
