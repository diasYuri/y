package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebSocketAdapterDispatchesCommand(t *testing.T) {
	adapter := WebSocketAdapter{Handler: HandlerFunc(func(_ context.Context, command Command) (Response, error) {
		if command.Kind != CommandAbort {
			t.Fatalf("command = %#v", command)
		}
		return Response{Accepted: true}, nil
	})}
	server := httptest.NewServer(http.HandlerFunc(adapter.ServeHTTP))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	connection, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	command, err := NewCommand(CommandAbort, nil)
	if err != nil {
		t.Fatal(err)
	}
	command.ID, command.RunID = "cmd", "run"
	if err := connection.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := connection.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.CommandID != "cmd" {
		t.Fatalf("response = %#v", response)
	}
}
