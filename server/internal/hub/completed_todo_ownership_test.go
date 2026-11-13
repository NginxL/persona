package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCompactSessionKeepsCompletedTodoOwnershipSeparate(t *testing.T) {
	session := proto.Session{MachineID: "box", SessionID: "history"}
	session.SetCompletedTodo(&proto.TodoRef{ID: "done", Title: strings.Repeat("x", 400)})
	raw, err := json.Marshal(runtimeSummary(session))
	if err != nil {
		t.Fatal(err)
	}
	var got proto.Session
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Todo != nil || got.CompletedTodo() == nil || got.CompletedTodo().ID != "done" || len(got.CompletedTodo().Title) > 256 {
		t.Fatalf("compact ownership: %+v", got)
	}
}
