package mom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AttachmentDownloader fetches attachment bytes by URL on behalf of the store.
// Real production code uses HTTPDownloader while tests inject an in-memory
// implementation.
type AttachmentDownloader interface {
	Download(ctx context.Context, url string) (io.ReadCloser, error)
}

// HTTPDownloader is the default AttachmentDownloader. It performs an HTTP GET
// using the supplied bot token as a Bearer credential and respects the request
// context for cancellation.
type HTTPDownloader struct {
	Client    *http.Client
	BotToken  string
	UserAgent string
}

// NewHTTPDownloader builds a downloader with sane defaults.
func NewHTTPDownloader(botToken string) *HTTPDownloader {
	return &HTTPDownloader{
		Client:    &http.Client{Timeout: 30 * time.Second},
		BotToken:  botToken,
		UserAgent: "y-mom/0.1",
	}
}

// Download issues a GET request and returns the response body. The caller owns
// closing the body.
func (d *HTTPDownloader) Download(ctx context.Context, url string) (io.ReadCloser, error) {
	if d == nil {
		return nil, errors.New("downloader is nil")
	}
	if d.Client == nil {
		d.Client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if d.BotToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.BotToken)
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d %s", url, resp.StatusCode, resp.Status)
	}
	return resp.Body, nil
}

// StoreConfig configures a ChannelStore.
type StoreConfig struct {
	WorkingDir   string
	BotToken     string
	Downloader   AttachmentDownloader
	Clock        Clock
	DedupeWindow time.Duration
}

// ChannelStore persists channel state on disk: log.jsonl, attachments, and
// scratch directories. The store is concurrency-safe.
type ChannelStore struct {
	workingDir   string
	botToken     string
	downloader   AttachmentDownloader
	clock        Clock
	dedupeWindow time.Duration

	mu              sync.Mutex
	recentlyLogged  map[string]time.Time
	pendingDownload []pendingDownload
	downloading     bool
	downloadDone    chan struct{}
}

type pendingDownload struct {
	channelID string
	localPath string
	url       string
}

// NewChannelStore constructs a new store rooted at cfg.WorkingDir.
func NewChannelStore(cfg StoreConfig) (*ChannelStore, error) {
	if strings.TrimSpace(cfg.WorkingDir) == "" {
		return nil, errors.New("store: working directory is required")
	}
	if err := os.MkdirAll(cfg.WorkingDir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create working dir: %w", err)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = SystemClock()
	}
	window := cfg.DedupeWindow
	if window <= 0 {
		window = 60 * time.Second
	}
	downloader := cfg.Downloader
	if downloader == nil && cfg.BotToken != "" {
		downloader = NewHTTPDownloader(cfg.BotToken)
	}
	return &ChannelStore{
		workingDir:     cfg.WorkingDir,
		botToken:       cfg.BotToken,
		downloader:     downloader,
		clock:          clock,
		dedupeWindow:   window,
		recentlyLogged: make(map[string]time.Time),
	}, nil
}

// WorkingDir returns the configured root.
func (s *ChannelStore) WorkingDir() string { return s.workingDir }

// ChannelDir returns and lazily creates the directory for channelID.
func (s *ChannelStore) ChannelDir(channelID string) (string, error) {
	if channelID == "" {
		return "", errors.New("channel id is required")
	}
	dir := filepath.Join(s.workingDir, channelID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("store: create channel dir: %w", err)
	}
	return dir, nil
}

// EventsDir returns the events directory and ensures it exists.
func (s *ChannelStore) EventsDir() (string, error) {
	dir := filepath.Join(s.workingDir, "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

var safeFilenameRE = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// LocalAttachmentName generates a deterministic local filename for attachments.
func LocalAttachmentName(original, slackTS string) string {
	ms := slackTimestampToMillis(slackTS)
	sanitized := safeFilenameRE.ReplaceAllString(original, "_")
	return fmt.Sprintf("%d_%s", ms, sanitized)
}

func slackTimestampToMillis(ts string) int64 {
	if ts == "" {
		return time.Now().UTC().UnixMilli()
	}
	if strings.Contains(ts, ".") {
		f, err := strconv.ParseFloat(ts, 64)
		if err != nil {
			return time.Now().UTC().UnixMilli()
		}
		return int64(f * 1000)
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Now().UTC().UnixMilli()
	}
	return n
}

// ProcessAttachments queues downloads for the supplied files and returns the
// metadata that should be persisted alongside the message.
func (s *ChannelStore) ProcessAttachments(channelID string, files []SlackFile, slackTS string) []Attachment {
	if len(files) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(files))
	queue := make([]pendingDownload, 0, len(files))
	for _, file := range files {
		url := file.URLDownload
		if url == "" {
			url = file.URLPrivate
		}
		if url == "" || file.Name == "" {
			continue
		}
		filename := LocalAttachmentName(file.Name, slackTS)
		localPath := filepath.ToSlash(filepath.Join(channelID, "attachments", filename))
		out = append(out, Attachment{Original: file.Name, Local: localPath})
		queue = append(queue, pendingDownload{channelID: channelID, localPath: localPath, url: url})
	}
	if len(queue) == 0 {
		return out
	}
	s.mu.Lock()
	s.pendingDownload = append(s.pendingDownload, queue...)
	if !s.downloading {
		s.downloading = true
		s.downloadDone = make(chan struct{})
		go s.processDownloads()
	}
	s.mu.Unlock()
	return out
}

// AwaitDownloads blocks until all pending downloads finish or ctx is cancelled.
// Used in tests and when shutting down.
func (s *ChannelStore) AwaitDownloads(ctx context.Context) error {
	s.mu.Lock()
	done := s.downloadDone
	pending := len(s.pendingDownload)
	s.mu.Unlock()
	if done == nil || pending == 0 && !s.isDownloading() {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *ChannelStore) isDownloading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downloading
}

func (s *ChannelStore) processDownloads() {
	for {
		s.mu.Lock()
		if len(s.pendingDownload) == 0 {
			done := s.downloadDone
			s.downloading = false
			s.downloadDone = nil
			s.mu.Unlock()
			if done != nil {
				close(done)
			}
			return
		}
		next := s.pendingDownload[0]
		s.pendingDownload = s.pendingDownload[1:]
		s.mu.Unlock()
		_ = s.downloadOne(next)
	}
}

func (s *ChannelStore) downloadOne(item pendingDownload) error {
	if s.downloader == nil {
		return errors.New("no downloader configured")
	}
	abs := filepath.Join(s.workingDir, filepath.FromSlash(item.localPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	body, err := s.downloader.Download(ctx, item.url)
	if err != nil {
		return err
	}
	defer body.Close()
	tmp := abs + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, abs)
}

// LogMessage appends message to the channel's log.jsonl. Duplicate messages
// inside the dedupe window are skipped and ok=false is returned.
func (s *ChannelStore) LogMessage(channelID string, message LoggedMessage) (bool, error) {
	dedupeKey := channelID + ":" + message.TS
	s.mu.Lock()
	now := s.clock.Now()
	for k, ts := range s.recentlyLogged {
		if now.Sub(ts) > s.dedupeWindow {
			delete(s.recentlyLogged, k)
		}
	}
	if _, dup := s.recentlyLogged[dedupeKey]; dup {
		s.mu.Unlock()
		return false, nil
	}
	s.recentlyLogged[dedupeKey] = now
	s.mu.Unlock()

	if message.Date == "" {
		message.Date = parseSlackTime(message.TS, now).Format(time.RFC3339Nano)
	}
	if message.Attachments == nil {
		message.Attachments = []Attachment{}
	}

	dir, err := s.ChannelDir(channelID)
	if err != nil {
		return false, err
	}
	logPath := filepath.Join(dir, "log.jsonl")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("store: open log: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(message); err != nil {
		return false, fmt.Errorf("store: encode log: %w", err)
	}
	return true, nil
}

// LogBotResponse logs a bot reply to log.jsonl with isBot=true.
func (s *ChannelStore) LogBotResponse(channelID, text, ts string) error {
	now := s.clock.Now()
	if ts == "" {
		ts = strconv.FormatInt(now.UnixMilli(), 10)
	}
	_, err := s.LogMessage(channelID, LoggedMessage{
		Date:        now.Format(time.RFC3339Nano),
		TS:          ts,
		User:        "bot",
		Text:        text,
		Attachments: []Attachment{},
		IsBot:       true,
	})
	return err
}

// LastTimestamp returns the TS of the last message logged for channelID. If no
// log exists or the file is empty, an empty string is returned.
func (s *ChannelStore) LastTimestamp(channelID string) string {
	logPath := filepath.Join(s.workingDir, channelID, "log.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == "" {
			continue
		}
		var msg LoggedMessage
		if err := json.Unmarshal([]byte(lines[i]), &msg); err != nil {
			continue
		}
		return msg.TS
	}
	return ""
}

func parseSlackTime(ts string, fallback time.Time) time.Time {
	if ts == "" {
		return fallback
	}
	if strings.Contains(ts, ".") {
		f, err := strconv.ParseFloat(ts, 64)
		if err != nil {
			return fallback
		}
		secs := int64(f)
		nanos := int64((f - float64(secs)) * 1e9)
		return time.Unix(secs, nanos).UTC()
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fallback
	}
	return time.UnixMilli(n).UTC()
}
