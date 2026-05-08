package pods

import (
	"fmt"
)

// CLIArgs holds parsed command-line arguments.
type CLIArgs struct {
	ShowHelp    bool
	ShowVersion bool

	Command    string
	Subcommand string

	// Global / shared
	PodName string

	// pods setup
	SetupName string
	SetupSSH  string
	SetupOpts SetupOptions

	// start
	StartModelID string
	StartName    string
	StartOpts    StartOptions

	// stop / logs
	TargetName string

	// ssh / shell
	SSHCommand string

	// agent
	AgentMessages []string
	AgentContinue bool
}

// ParseCLIArgs parses os.Args-style slice.
func ParseCLIArgs(argv []string) (CLIArgs, error) {
	var args CLIArgs
	if len(argv) == 0 {
		args.ShowHelp = true
		return args, nil
	}

	if argv[0] == "--help" || argv[0] == "-h" {
		args.ShowHelp = true
		return args, nil
	}
	if argv[0] == "--version" || argv[0] == "-v" {
		args.ShowVersion = true
		return args, nil
	}

	args.Command = argv[0]
	rest := argv[1:]

	// Extract --pod override from anywhere.
	rest, args.PodName = extractFlag(rest, "--pod")

	switch args.Command {
	case "pods":
		if len(rest) == 0 {
			args.Subcommand = "list"
			return args, nil
		}
		args.Subcommand = rest[0]
		switch args.Subcommand {
		case "setup":
			if len(rest) < 3 {
				return args, fmt.Errorf("usage: pods setup <name> \"<ssh>\" [options]")
			}
			args.SetupName = rest[1]
			args.SetupSSH = rest[2]
			opts := rest[3:]
			opts, args.SetupOpts.Mount = extractFlag(opts, "--mount")
			opts, args.SetupOpts.ModelsPath = extractFlag(opts, "--models-path")
			_, vllm := extractFlag(opts, "--vllm")
			if vllm == "release" || vllm == "nightly" || vllm == "gpt-oss" {
				args.SetupOpts.VLLM = vllm
			} else if vllm != "" {
				return args, fmt.Errorf("invalid --vllm: %q (valid: release, nightly, gpt-oss)", vllm)
			}
		case "active":
			if len(rest) < 2 {
				return args, fmt.Errorf("usage: pods active <name>")
			}
			args.TargetName = rest[1]
		case "remove":
			if len(rest) < 2 {
				return args, fmt.Errorf("usage: pods remove <name>")
			}
			args.TargetName = rest[1]
		case "list":
			// no args
		default:
			return args, fmt.Errorf("unknown pods subcommand: %q", args.Subcommand)
		}
	case "start":
		if len(rest) == 0 {
			args.Subcommand = "show-models"
			return args, nil
		}
		args.StartModelID = rest[0]
		parseStartOptions(rest[1:], &args.StartOpts)
		args.StartName = args.StartOpts.startName
	case "stop":
		if len(rest) > 0 {
			args.TargetName = rest[0]
		}
	case "list":
		args.Subcommand = "list-models"
	case "logs":
		if len(rest) < 1 {
			return args, fmt.Errorf("usage: logs <name>")
		}
		args.TargetName = rest[0]
	case "shell":
		if len(rest) > 0 {
			args.TargetName = rest[0]
		}
	case "ssh":
		if len(rest) == 1 {
			args.SSHCommand = rest[0]
		} else if len(rest) == 2 {
			args.TargetName = rest[0]
			args.SSHCommand = rest[1]
		} else {
			return args, fmt.Errorf("usage: ssh [<name>] \"<command>\"")
		}
	case "agent":
		if len(rest) < 1 {
			return args, fmt.Errorf("usage: agent <name> [messages...]")
		}
		args.TargetName = rest[0]
		msgRest := rest[1:]
		msgRest, _ = extractFlagBool(msgRest, "--continue")
		msgRest, _ = extractFlagBool(msgRest, "-c")
		args.AgentMessages = msgRest
	default:
		return args, fmt.Errorf("unknown command: %q", args.Command)
	}
	return args, nil
}

func parseStartOptions(args []string, opts *StartOptions) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				opts.startName = args[i+1]
				i++
			}
		case "--memory":
			if i+1 < len(args) {
				opts.Memory = args[i+1]
				i++
			}
		case "--context":
			if i+1 < len(args) {
				opts.Context = args[i+1]
				i++
			}
		case "--gpus":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &opts.GPUs)
				i++
			}
		case "--vllm":
			// Collect all remaining args as vllm args.
			if i+1 < len(args) {
				opts.VLLMArgs = append([]string(nil), args[i+1:]...)
				return
			}
		}
	}
}

func extractFlag(args []string, flag string) ([]string, string) {
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			val := args[i+1]
			out := make([]string, 0, len(args)-2)
			out = append(out, args[:i]...)
			out = append(out, args[i+2:]...)
			return out, val
		}
	}
	return args, ""
}

func extractFlagBool(args []string, flag string) ([]string, bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			out := make([]string, 0, len(args)-1)
			out = append(out, args[:i]...)
			out = append(out, args[i+1:]...)
			return out, true
		}
	}
	return args, false
}

// HelpText is the usage string.
const HelpText = `y-pods - Manage vLLM deployments on GPU pods

Pod Management:
  y-pods pods setup <name> "<ssh>" --mount "<mount>"    Setup pod with mount
    --vllm release|nightly|gpt-oss
  y-pods pods                                           List all pods (* = active)
  y-pods pods active <name>                             Switch active pod
  y-pods pods remove <name>                             Remove pod from config
  y-pods shell [<name>]                                 Open shell on pod
  y-pods ssh [<name>] "<command>"                       Run SSH command on pod

Model Management:
  y-pods start <model> --name <name> [options]          Start a model
    --memory <percent>   GPU memory allocation
    --context <size>     Context window (4k, 8k, 16k, 32k, 64k, 128k)
    --gpus <count>       Number of GPUs
    --vllm <args...>     Pass remaining args to vLLM
  y-pods stop [<name>]                                  Stop model (or all)
  y-pods list                                           List running models
  y-pods logs <name>                                    Stream model logs
  y-pods agent <name> ["<message>"...]                  Chat with model
    --continue, -c       Continue previous session

Environment:
  HF_TOKEN         HuggingFace token
  PI_API_KEY       API key for vLLM endpoints
  Y_PODS_CONFIG_DIR  Config directory (default: ~/.config/y-pods)
`
