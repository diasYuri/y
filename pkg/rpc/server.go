//go:build feature_rpc

package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

// ServerConfig configures the JSON-RPC server.
type ServerConfig struct {
	Addr         string
	Log          io.Writer
	Provider     providers.Provider
	ToolRegistry *tools.Registry
	Model        ai.Model
	SystemPrompt string
}

// Server is a JSON-RPC HTTP server that exposes agent functionality.
type Server struct {
	cfg        ServerConfig
	listener   net.Listener
	server     *http.Server
	sessions   map[string]*session
	sessionsMu sync.RWMutex
	mu         sync.Mutex
	running    bool
	events     *eventBus
}

// session holds per-client transcript state and a persistent agent.
type session struct {
	id         string
	transcript []ai.Message
	mu         sync.RWMutex
	createdAt  time.Time
	updatedAt  time.Time
}

// sessionAgent builds (or rebuilds) an agent from the session transcript.
func (s *Server) sessionAgent(sess *session, sinks ...agent.EventSink) *agent.Agent {
	sess.mu.RLock()
	msgs := make([]ai.Message, len(sess.transcript))
	copy(msgs, sess.transcript)
	sess.mu.RUnlock()

	opts := []agent.Option{}
	if s.cfg.SystemPrompt != "" {
		opts = append(opts, agent.WithSystemPrompt(s.cfg.SystemPrompt))
	}
	if s.cfg.Model.ID != "" {
		opts = append(opts, agent.WithModel(s.cfg.Model))
	}
	for _, sink := range sinks {
		if sink != nil {
			opts = append(opts, agent.WithEventSink(sink))
		}
	}

	var registry agent.ToolRegistry
	if s.cfg.ToolRegistry != nil {
		registry = s.cfg.ToolRegistry
	}
	ag := agent.New(s.cfg.Provider, registry, opts...)
	if len(msgs) > 0 {
		ag.Reset(msgs...)
	}
	return ag
}

// NewServer creates an RPC server from config.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		cfg:      cfg,
		sessions: make(map[string]*session),
		events:   newEventBus(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", s.handleRPC)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/health", s.handleHealth)
	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s
}

// ListenAndServe starts the server and blocks.
func (s *Server) ListenAndServe() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}
	addr := s.cfg.Addr
	if addr == "" {
		addr = ":0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = ln
	s.running = true
	s.mu.Unlock()
	return s.server.Serve(ln)
}

// Addr returns the listener address. Returns nil if not started.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// handleRPC handles JSON-RPC POST requests.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, newErrorResponse(nil, newError(ErrParseError, "failed to read body")))
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, newErrorResponse(nil, newError(ErrParseError, err.Error())))
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSON(w, newErrorResponse(req.ID, newError(ErrInvalidRequest, "jsonrpc version must be 2.0")))
		return
	}
	if req.Method == "" {
		writeJSON(w, newErrorResponse(req.ID, newError(ErrInvalidRequest, "method is required")))
		return
	}

	result, rpcErr := s.dispatch(r.Context(), req.Method, req.Params)
	if req.IsNotification() {
		if rpcErr != nil {
			logRPCError(s.cfg.Log, req.Method, rpcErr)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if rpcErr != nil {
		writeJSON(w, newErrorResponse(req.ID, rpcErr))
		return
	}
	writeJSON(w, newResponse(req.ID, result))
}

// handleEvents serves Server-Sent Events for streaming agent events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sub := s.events.Subscribe()
	defer s.events.Unsubscribe(sub)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ":ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleHealth returns a simple health check response.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

func logRPCError(w io.Writer, method string, err *ErrorObj) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "rpc error: method=%s code=%d message=%s\n", method, err.Code, err.Message)
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *ErrorObj) {
	switch method {
	case "models":
		return s.handleModels(ctx)
	case "tools":
		return s.handleTools(ctx)
	case "chat":
		return s.handleChat(ctx, params)
	case "continue":
		return s.handleContinue(ctx, params)
	case "steer":
		return s.handleSteer(params)
	case "abort":
		return s.handleAbort(params)
	case "transcript":
		return s.handleTranscript(params)
	case "clear":
		return s.handleClear(params)
	default:
		return nil, newError(ErrMethodNotFound, fmt.Sprintf("method %q not found", method))
	}
}
