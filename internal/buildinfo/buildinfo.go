package buildinfo

import "strings"

// Info describes metadata injected by release builds.
type Info struct {
	Version string   `json:"version"`
	Commit  string   `json:"commit"`
	Date    string   `json:"date"`
	Tags    []string `json:"tags"`
}

var (
	version = "0.0.0-dev"
	commit  = "unknown"
	date    = "unknown"
	tags    = ""
)

// Current returns build metadata injected by release builds when available.
func Current() Info {
	return Info{
		Version: version,
		Commit:  commit,
		Date:    date,
		Tags:    parseTags(tags),
	}
}

func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}
