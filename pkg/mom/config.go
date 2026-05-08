package mom

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// CLIArgs collects the flags accepted by the y-mom binary.
type CLIArgs struct {
	WorkingDir      string
	Sandbox         SandboxConfig
	DownloadChannel string
	ShowHelp        bool
	ShowVersion     bool
}

// ParseCLIArgs interprets argv (without the program name).
func ParseCLIArgs(argv []string) (CLIArgs, error) {
	args := CLIArgs{Sandbox: SandboxConfig{Kind: SandboxHost}}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			args.ShowHelp = true
			return args, nil
		case arg == "-v" || arg == "--version":
			args.ShowVersion = true
			return args, nil
		case strings.HasPrefix(arg, "--sandbox="):
			cfg, err := ParseSandboxArg(strings.TrimPrefix(arg, "--sandbox="))
			if err != nil {
				return CLIArgs{}, err
			}
			args.Sandbox = cfg
		case arg == "--sandbox":
			if i+1 >= len(argv) {
				return CLIArgs{}, errors.New("--sandbox requires a value")
			}
			i++
			cfg, err := ParseSandboxArg(argv[i])
			if err != nil {
				return CLIArgs{}, err
			}
			args.Sandbox = cfg
		case strings.HasPrefix(arg, "--download="):
			args.DownloadChannel = strings.TrimSpace(strings.TrimPrefix(arg, "--download="))
			if args.DownloadChannel == "" {
				return CLIArgs{}, errors.New("--download requires a channel id")
			}
		case arg == "--download":
			if i+1 >= len(argv) {
				return CLIArgs{}, errors.New("--download requires a channel id")
			}
			i++
			args.DownloadChannel = strings.TrimSpace(argv[i])
			if args.DownloadChannel == "" {
				return CLIArgs{}, errors.New("--download requires a channel id")
			}
		case strings.HasPrefix(arg, "-"):
			return CLIArgs{}, fmt.Errorf("unknown flag %q", arg)
		default:
			if args.WorkingDir != "" {
				return CLIArgs{}, fmt.Errorf("unexpected argument %q", arg)
			}
			abs, err := filepath.Abs(arg)
			if err != nil {
				return CLIArgs{}, err
			}
			args.WorkingDir = abs
		}
	}
	return args, nil
}

// EnvConfig captures the relevant environment variables consumed by y-mom.
type EnvConfig struct {
	SlackAppToken     string
	SlackBotToken     string
	AnthropicAPIKey   string
	OpenAIAPIKey      string
	GoogleAPIKey      string
	DefaultProviderID string
}

// EnvLoader is a function that reads an environment variable.
type EnvLoader func(name string) string

// LoadEnvConfig builds an EnvConfig from the supplied loader. Tests pass a
// simple map-backed loader so they don't depend on os.Getenv.
func LoadEnvConfig(load EnvLoader) EnvConfig {
	if load == nil {
		return EnvConfig{}
	}
	cfg := EnvConfig{
		SlackAppToken:     strings.TrimSpace(load("MOM_SLACK_APP_TOKEN")),
		SlackBotToken:     strings.TrimSpace(load("MOM_SLACK_BOT_TOKEN")),
		AnthropicAPIKey:   strings.TrimSpace(load("ANTHROPIC_API_KEY")),
		OpenAIAPIKey:      strings.TrimSpace(load("OPENAI_API_KEY")),
		GoogleAPIKey:      strings.TrimSpace(load("GOOGLE_API_KEY")),
		DefaultProviderID: strings.TrimSpace(load("Y_MOM_PROVIDER")),
	}
	if cfg.DefaultProviderID == "" {
		switch {
		case cfg.AnthropicAPIKey != "":
			cfg.DefaultProviderID = "anthropic"
		case cfg.OpenAIAPIKey != "":
			cfg.DefaultProviderID = "openai"
		case cfg.GoogleAPIKey != "":
			cfg.DefaultProviderID = "google"
		}
	}
	return cfg
}

// Validate reports configuration issues that would prevent y-mom from running.
func (c EnvConfig) Validate() error {
	missing := make([]string, 0, 2)
	if c.SlackAppToken == "" {
		missing = append(missing, "MOM_SLACK_APP_TOKEN")
	}
	if c.SlackBotToken == "" {
		missing = append(missing, "MOM_SLACK_BOT_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if c.AnthropicAPIKey == "" && c.OpenAIAPIKey == "" && c.GoogleAPIKey == "" {
		return errors.New("missing provider credentials: set ANTHROPIC_API_KEY, OPENAI_API_KEY or GOOGLE_API_KEY")
	}
	return nil
}

// HelpText is the text printed when y-mom is invoked with --help.
const HelpText = `Usage: y-mom [options] <working-directory>
       y-mom --download <channel-id>

Options:
  --sandbox=host                  Run tools on the host (not recommended).
  --sandbox=docker:<container>    Run tools in a long-running container.
  --download <channel-id>         Backfill a channel into the working directory.
  -h, --help                      Show this help.
  -v, --version                   Show build version.

Environment:
  MOM_SLACK_APP_TOKEN  Slack app-level token (xapp-...).
  MOM_SLACK_BOT_TOKEN  Slack bot user OAuth token (xoxb-...).
  ANTHROPIC_API_KEY    Anthropic API key (preferred provider).
  OPENAI_API_KEY       OpenAI API key (fallback provider).
  GOOGLE_API_KEY       Google AI key (fallback provider).
  Y_MOM_PROVIDER       Override the auto-detected provider id.
`
