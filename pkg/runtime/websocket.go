package runtime

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// WebSocketAdapter carries the same Command/Response envelopes over a
// bidirectional WebSocket. Events remain readable through EventStore/SSE;
// callers that need push can dispatch CommandGetEvents after reconnecting.
type WebSocketAdapter struct {
	Handler  Handler
	Upgrader websocket.Upgrader
}

// ServeHTTP upgrades a request and serves commands until the peer closes the
// connection. Each valid inbound command produces exactly one response.
func (a WebSocketAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.Handler == nil {
		http.Error(w, "runtime command handler is unavailable", http.StatusServiceUnavailable)
		return
	}
	upgrader := a.Upgrader
	if upgrader.CheckOrigin == nil {
		// Origin policy is an application decision. The default accepts same
		// host requests and rejects cross-origin browser requests.
		upgrader.CheckOrigin = func(request *http.Request) bool {
			origin := request.Header.Get("Origin")
			return origin == "" || origin == "http://"+request.Host || origin == "https://"+request.Host
		}
	}
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = connection.Close() }()
	for {
		var command Command
		if err := connection.ReadJSON(&command); err != nil {
			return
		}
		response, err := dispatch(r.Context(), a.Handler, command)
		if err != nil && response.Error == nil {
			response = Response{SchemaVersion: ProtocolVersion, CommandID: command.ID, RunID: command.RunID, Accepted: false, Error: &CommandError{Code: "dispatch_failed", Message: err.Error()}}
		}
		if err := connection.WriteJSON(response); err != nil {
			return
		}
	}
}
