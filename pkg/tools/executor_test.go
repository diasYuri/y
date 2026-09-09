package tools

import (
	"context"
	"testing"
)

func TestExecutorHandlerForwardsCancellationAndProgress(t *testing.T) {
	seenProgress := false
	handler := ExecutorHandler{Executor: ExecutorFunc(func(ctx context.Context, request ToolRequest) (ToolResponse, error) {
		if request.Progress != nil {
			request.Progress(ToolProgress{Text: "working"})
		}
		return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: "done"}}}, ctx.Err()
	})}
	response, err := handler.Handle(context.Background(), ToolRequest{Progress: func(progress ToolProgress) { seenProgress = progress.Text == "working" }})
	if err != nil || response.Content[0].Text != "done" || !seenProgress {
		t.Fatalf("response=%#v err=%v progress=%v", response, err, seenProgress)
	}
}
