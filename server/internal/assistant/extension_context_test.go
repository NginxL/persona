package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// answerInboxContext makes the extension manager answer plugins.inbox_context with one extension's block and
// sentences; it counts the calls.
func answerInboxContext(h *inboxHub, never ...string) *int {
	n := new(int)
	h.f.answerApp(AppPlugins, PluginsInboxContext, func(json.RawMessage) (any, error) {
		*n++
		return assistantwire.ExtensionContextResult{Blocks: []assistantwire.ExtensionContextBlock{
			{Extension: "notes", Name: "Notes", Text: "The user answers colleagues briefly and in their own language."}}, NeverQuote: never}, nil
	})
	return n
}

// What an extension wrote for the Inbox goes into the triage and draft prompts, after the user's own rules and notes;
// it is asked for once a minute, not for every item.
func TestGoldenInboxExtensionContext(t *testing.T) {
	ctx := context.Background()
	h := goldenInbox(t)
	asked := answerInboxContext(h)
	model, calls := captureModel(t, `{"tier":"action","summary":"s","reason":"r"}`)
	h.in.model = model
	items := goldenItems(t)
	it := items["dm"]
	h.inbox.items[it.ID] = it
	h.triageOne(ctx, it)
	goldenCall(t, "inbox_triage.extension_context", onlyCall(t, calls()))

	draft, drafts := captureModel(t, "好的")
	h.in.models[purposeDraft] = draft
	if _, err := h.draftReply(ctx, it.ID, ""); err != nil {
		t.Fatal(err)
	}
	goldenCall(t, "inbox_draft.extension_context", onlyCall(t, drafts()))
	if *asked != 1 {
		t.Fatalf("asked %d times within a minute", *asked)
	}
}

// sequenceModel answers each call with the next of replies (the last again once they run out).
func sequenceModel(t *testing.T, replies ...string) (*inboxModelCalls, func()) {
	t.Helper()
	c := &inboxModelCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System string `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		c.mu.Lock()
		reply := replies[min(len(c.systems), len(replies)-1)]
		c.systems = append(c.systems, in.System)
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}})
	}))
	c.url = srv.URL
	return c, srv.Close
}

type inboxModelCalls struct {
	mu      sync.Mutex
	url     string
	systems []string
}

// A draft that repeats a sentence an extension marked never to quote (ignoring case, width, spaces and punctuation)
// is written once more, told so; the rewrite is what the user gets. If the rewrite repeats one too, there is no draft.
// The sentences themselves are never in a prompt; a short one does not count.
func TestInboxDraftNeverQuote(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		ctx := context.Background()
		never := []string{"Thinks the vendor is unreliable", "ok"}
		for _, c := range []struct {
			name    string
			replies []string
			want    string
			err     error
			calls   int
		}{
			{"clean", []string{"OK, I'll send it Friday."}, "OK, I'll send it Friday.", nil, 1},
			{"rewritten", []string{"Sure. (THINKS the vendor is unreliable!)", "Sure, Friday works."}, "Sure, Friday works.", nil, 2},
			{"withheld", []string{"thinks the vendor is ＵＮＲＥＬＩＡＢＬＥ", "Thinks  the vendor, is unreliable."}, "", errDraftWithheld, 2},
		} {
			h := goldenInbox(t)
			h.f.hello.Locale = locale
			answerInboxContext(h, never...)
			calls, stop := sequenceModel(t, c.replies...)
			h.in.models[purposeDraft] = modelAt(t, calls.url)
			it := goldenItems(t)["dm"]
			h.inbox.items[it.ID] = it
			got, err := h.draftReply(ctx, it.ID, "")
			stop()
			if got != c.want || !errors.Is(err, c.err) || len(calls.systems) != c.calls {
				t.Fatalf("%s: %q %v after %d calls", c.name, got, err, len(calls.systems))
			}
			for i, system := range calls.systems {
				if strings.Contains(system, "unreliable") {
					t.Fatalf("%s: a never-quote sentence is in the prompt", c.name)
				}
				if rewrite := strings.Contains(system, "上一版草稿照搬了") || strings.Contains(system, "The previous draft copied") || strings.Contains(system, "前の下書きは外部に出せない"); rewrite != (i == 1) {
					t.Fatalf("%s: call %d asks to rewrite: %v", c.name, i, rewrite)
				}
			}
		}
	}
}

func TestQuotesAny(t *testing.T) {
	for _, c := range []struct {
		text, want string
	}{
		{"他觉得供应商不太可靠，先别答应。", "觉得供应商不太可靠"},
		{"他觉得供应商 不太 可靠", "觉得供应商不太可靠"},
		{"供应商挺好", ""},
		{"好的", ""},
	} {
		if got := quotesAny(c.text, []string{"好的", "觉得供应商不太可靠"}); got != c.want {
			t.Errorf("%q: %q, want %q", c.text, got, c.want)
		}
	}
}
