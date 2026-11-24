package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestWorkAgentSendImages(t *testing.T) {
	images := []proto.Image{{Mime: "image/png", Data: "AQI="}, {Mime: "image/gif", Data: "AwQ="}}
	for _, kind := range []string{agyAgent, cursorAgent} {
		for _, tc := range []struct {
			name   string
			text   string
			images []proto.Image
			bad    bool
		}{
			{"text and two images", "Look at these.", images, false},
			{"image only", "", images[:1], false},
			{"plain text", "  keep the spaces  ", nil, false},
			{"invalid image", "Look at this.", []proto.Image{{Mime: "image/png", Data: "!"}}, true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				n, f, home := herdrNode(t)
				const pane = "w1:p1"
				agent := agentOf(kind, "", proto.StatusIdle, pane, "w1:t1", "w1", home, kind)
				seedAgents(t, n, f, agent)
				f.reply("agent.get", map[string]any{"agent": agent})
				id := otherSession(host.Agent{Agent: kind, PaneID: pane})
				_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": tc.text, "images": tc.images})

				var prompts []string
				for _, c := range f.calls() {
					switch c.Method {
					case "agent.prompt":
						if c.Params["target"] == pane {
							prompts = append(prompts, str(c.Params["text"]))
						}
					case "pane.send_text": // Windows pastes the prompt before pressing Enter.
						if c.Params["pane_id"] == pane {
							prompts = append(prompts, unpaste(str(c.Params["text"])))
						}
					}
				}
				if tc.bad {
					if err == nil || len(prompts) != 0 {
						t.Errorf("invalid image: error %v, delivered %q", err, prompts)
					}
					return
				}
				if err != nil || len(prompts) != 1 {
					t.Fatalf("send: error %v, delivered %q", err, prompts)
				}
				if len(tc.images) == 0 {
					if prompts[0] != tc.text {
						t.Errorf("plain prompt %q, want %q", prompts[0], tc.text)
					}
					return
				}
				prefix := "<attached_images>\n"
				if tc.text != "" {
					prefix = tc.text + "\n\n" + prefix
				}
				block, open := strings.CutPrefix(prompts[0], prefix)
				block, closed := strings.CutSuffix(block, "\n</attached_images>")
				paths := strings.Split(block, "\n")
				if !open || !closed || len(paths) != len(tc.images) {
					t.Fatalf("prompt %q, want %d attached image paths after %q", prompts[0], len(tc.images), tc.text)
				}
				for i, path := range paths {
					dir := filepath.Dir(path)
					if filepath.Dir(dir) != filepath.Join(n.dir, "uploads") {
						t.Fatalf("attachment path outside the dated upload directory: %q", path)
					}
					if _, err := time.Parse(time.DateOnly, filepath.Base(dir)); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(path)
					want := []string{"\x01\x02", "\x03\x04"}[i]
					if err != nil || string(data) != want {
						t.Errorf("image %d: bytes %x, error %v, want %x", i, data, err, want)
					}
				}
			})
		}
	}
}
