package assistant

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestEmptyConversationStartsIdle(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		images     []proto.Image
		status     string
		state      string
	}{
		{name: "empty", status: proto.StatusIdle, state: proto.StateIdle},
		{name: "whitespace", text: " \t\n", status: proto.StatusIdle, state: proto.StateIdle},
		{name: "text", text: "Hello", status: proto.StatusWorking, state: proto.StateWorking},
		{name: "image only", images: []proto.Image{{Mime: "image/png", Data: "aW1hZ2U="}}, status: proto.StatusWorking, state: proto.StateWorking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.settings = &Settings{MachineID: "server"}
			s := newStorelessService(t, f)
			got, err := s.Start(context.Background(), StartParams{Text: tc.text, Images: tc.images, Main: true})
			if err != nil {
				t.Fatal(err)
			}
			if got.SessionID == "" || got.Status != tc.status || got.State != tc.state {
				t.Fatalf("start: id=%q, status=%q, state=%q; want %q, %q", got.SessionID, got.Status, got.State, tc.status, tc.state)
			}
			if s.settings(context.Background()).Main != got.SessionID {
				t.Fatal("empty creation did not install the main conversation")
			}
		})
	}
}
