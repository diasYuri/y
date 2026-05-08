package mom

import "time"

// EventType identifies the source of a Slack event.
type EventType string

const (
	EventMention EventType = "mention"
	EventDM      EventType = "dm"
)

// Attachment describes a file attached to a Slack message.
//
// Original is the filename as it arrived from Slack while Local is a path
// relative to the working directory where the bytes are persisted.
type Attachment struct {
	Original string `json:"original"`
	Local    string `json:"local"`
}

// SlackFile is the wire representation of a Slack file payload as forwarded by
// the connector. Only the fields used by mom are exposed.
type SlackFile struct {
	Name        string `json:"name,omitempty"`
	URLDownload string `json:"url_private_download,omitempty"`
	URLPrivate  string `json:"url_private,omitempty"`
}

// SlackUser describes a workspace user.
type SlackUser struct {
	ID          string `json:"id"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
}

// SlackChannel describes a workspace channel or DM.
type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SlackEvent is a normalised inbound Slack event delivered to MomHandler.
type SlackEvent struct {
	Type        EventType    `json:"type"`
	Channel     string       `json:"channel"`
	TS          string       `json:"ts"`
	User        string       `json:"user"`
	Text        string       `json:"text"`
	Files       []SlackFile  `json:"files,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// LoggedMessage is the structure persisted to per-channel log.jsonl.
type LoggedMessage struct {
	Date        string       `json:"date"`
	TS          string       `json:"ts"`
	User        string       `json:"user"`
	UserName    string       `json:"userName,omitempty"`
	DisplayName string       `json:"displayName,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
	IsBot       bool         `json:"isBot"`
}

// SandboxKind selects between executing tools on the host or inside Docker.
type SandboxKind string

const (
	SandboxHost   SandboxKind = "host"
	SandboxDocker SandboxKind = "docker"
)

// SandboxConfig describes the runtime environment used by mom for tool
// execution. Container is only relevant when Kind is SandboxDocker.
type SandboxConfig struct {
	Kind      SandboxKind
	Container string
}

// String renders the sandbox in a CLI-friendly form.
func (c SandboxConfig) String() string {
	switch c.Kind {
	case SandboxDocker:
		return "docker:" + c.Container
	default:
		return string(SandboxHost)
	}
}

// EventKind identifies the schedule type of an inbound y-mom event file.
type EventKind string

const (
	EventImmediate EventKind = "immediate"
	EventOneShot   EventKind = "one-shot"
	EventPeriodic  EventKind = "periodic"
)

// MomEvent is a scheduled instruction stored as JSON inside <workdir>/events.
type MomEvent struct {
	Type      EventKind `json:"type"`
	ChannelID string    `json:"channelId"`
	Text      string    `json:"text"`
	At        string    `json:"at,omitempty"`
	Schedule  string    `json:"schedule,omitempty"`
	Timezone  string    `json:"timezone,omitempty"`
}

// RunResult summarises the outcome of an agent run on a channel.
type RunResult struct {
	StopReason   string
	ErrorMessage string
}

// Clock abstracts time.Now so tests can drive scheduling deterministically.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// SystemClock returns the default UTC system clock.
func SystemClock() Clock { return systemClock{} }
