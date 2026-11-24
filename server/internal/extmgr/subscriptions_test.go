package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// The subscriptions' routes: anyone reads, only full access changes, and the service's errors keep the API's body.
func TestSubscriptionRoutes(t *testing.T) {
	h := newTestManager(t)
	mux := http.NewServeMux()
	h.subscriptionRoutes(mux)
	call := func(p principal, method, path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		mux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), p)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	full, watch := principal{ID: "dev_phone", Role: roleFull}, principal{ID: "dev_watch", Role: roleAnswer}
	if code, out := call(full, "GET", "/api/v1/subscriptions", ""); code != http.StatusServiceUnavailable || out["code"] != "unavailable" {
		t.Fatalf("before the service runs: %d %v", code, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startSubscriptions(ctx)
	if !h.subscriptionsShown() {
		t.Fatal("the apps are not told about subscriptions, which deliver now (#2039)")
	}
	if f := h.subs.Features(); !f.Deliver || !f.Generate {
		t.Fatalf("features %+v: both deliver (#2039) and generate (#2040) are in", f)
	}
	if !subscriptions.New(subscriptions.Options{Features: subscriptions.Features{Deliver: true}}).Features().Deliver {
		t.Fatal("Features does not say what the service was made with")
	}
	code, out := call(watch, "GET", "/api/v1/subscriptions", "")
	if code != http.StatusOK || out["subscriptions"] == nil || out["features"] == nil {
		t.Fatalf("list from an answer-only device: %d %v", code, out)
	}
	if code, out := call(watch, "GET", "/api/v1/subscriptions/summary", ""); code != http.StatusOK || out["connectors"] == nil {
		t.Fatalf("summary: %d %v", code, out)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/subscriptions"}, {"PUT", "/api/v1/subscriptions/sub_x"}, {"DELETE", "/api/v1/subscriptions/sub_x"},
		{"POST", "/api/v1/subscriptions/sub_x/run"}, {"POST", "/api/v1/subscriptions/-/dry-run"},
	} {
		if code, out := call(watch, c.method, c.path, `{}`); code != http.StatusForbidden || out["code"] != "read_only_device" {
			t.Errorf("%s %s from an answer-only device: %d %v", c.method, c.path, code, out)
		}
	}
	if code, out := call(full, "POST", "/api/v1/subscriptions", `{"connector":"nowhere","source":"x"}`); code != http.StatusNotFound || out["code"] != "not_found" || out["error"] == nil {
		t.Fatalf("an unknown connector: %d %v", code, out)
	}
	if code, out := call(full, "GET", "/api/v1/subscriptions/sub_x", ""); code != http.StatusNotFound || out["code"] != "not_found" {
		t.Fatalf("an unknown subscription: %d %v", code, out)
	}
	if code, out := call(full, "POST", "/api/v1/subscriptions", `{"connector":`); code != http.StatusBadRequest || out["code"] != "bad_request" {
		t.Fatalf("broken JSON: %d %v", code, out)
	}
}

// Drafting (#2040): the route is a change, features say it is in, and the model goes through the assistant on the
// purpose subscription.recipe, its codes read for the pages.
func TestSubscriptionGenerate(t *testing.T) {
	h := newTestManager(t)
	mux := http.NewServeMux()
	h.subscriptionRoutes(mux)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var asked []string
	var got []assistantwire.PurposeComplete
	answer := func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"text":"{}","json":{"tool":"list","args":{}},"model":"m","stop_reason":"end_turn","usage":{"requests":1}}`), nil
	}
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		asked = append(asked, method)
		var p assistantwire.PurposeComplete
		_ = json.Unmarshal(params, &p)
		got = append(got, p)
		return answer(params)
	}
	h.startSubscriptions(ctx)
	call := func(p principal, path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		mux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), p)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	full, watch := principal{ID: "dev_phone", Role: roleFull}, principal{ID: "dev_watch", Role: roleAnswer}
	// features.generate is on in the list the apps read, whatever else the manager's options turn on.
	lrec := httptest.NewRecorder()
	mux.ServeHTTP(lrec, httptest.NewRequest("GET", "/api/v1/subscriptions", nil).WithContext(withPrincipal(context.Background(), principal{ID: "dev_phone", Role: roleFull})))
	var list struct {
		Features subscriptions.Features `json:"features"`
	}
	if json.Unmarshal(lrec.Body.Bytes(), &list) != nil || !list.Features.Generate || !h.subs.Features().Generate {
		t.Fatalf("features.generate is off: %s", lrec.Body.String())
	}
	if code, out := call(watch, "/api/v1/subscriptions/-/generate", `{"connector":"x","source":"y"}`); code != http.StatusForbidden || out["code"] != "read_only_device" {
		t.Fatalf("from an answer-only device: %d %v", code, out)
	}
	for _, c := range []struct {
		path, body string
		code       int
		want       string
	}{
		{"/api/v1/subscriptions/-/generate", ``, http.StatusBadRequest, "bad_request"},
		{"/api/v1/subscriptions/-/generate", `{"connector":`, http.StatusBadRequest, "bad_request"},
		{"/api/v1/subscriptions/-/generate", `{"connector":"nowhere","source":"x"}`, http.StatusNotFound, "not_found"},
		{"/api/v1/subscriptions/sub_x/generate", `{"connector":"ignored","source":"ignored"}`, http.StatusNotFound, "not_found"},
	} {
		if code, out := call(full, c.path, c.body); code != c.code || out["code"] != c.want {
			t.Errorf("%s %s: %d %v", c.path, c.body, code, out)
		}
	}

	m := subscriptionModel{h}
	in := subscriptions.ModelCall{System: "s", Turns: []subscriptions.Turn{{Role: "user", Content: "tools"}}, Schema: json.RawMessage(`{"type":"object"}`),
		MaxTokens: 1024, Label: "subscription pick"}
	raw, err := m.JSON(ctx, in)
	if err != nil || string(raw) != `{"tool":"list","args":{}}` {
		t.Fatalf("%s %v", raw, err)
	}
	p := got[len(got)-1]
	if asked[len(asked)-1] != assistantwire.MethodModelsPurposeComplete || p.Purpose != assistantwire.PurposeSubscriptionRecipe ||
		p.Params.System != "s" || len(p.Params.Messages) != 1 || p.Params.MaxTokens != 1024 || p.Params.TimeoutSeconds != subscriptionModelWait ||
		string(p.Params.JSONSchema) != `{"type":"object"}` || p.Params.Label != "subscription pick" {
		t.Fatalf("the assistant got %v %+v", asked, p)
	}
	answer = func(json.RawMessage) (json.RawMessage, error) {
		return nil, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "nothing to call"}
	}
	if _, err := m.JSON(ctx, in); !errors.Is(err, subscriptions.ErrNoModel) {
		t.Fatalf("nothing set up: %v", err)
	}
	modelFailure := &rpc.Error{Code: assistantwire.CodeModelFailed, Message: "the upstream model is missing"}
	answer = func(json.RawMessage) (json.RawMessage, error) {
		return nil, modelFailure
	}
	if _, err := m.JSON(ctx, in); err != modelFailure || errors.Is(err, subscriptions.ErrNoModel) {
		t.Fatalf("an upstream model failure was rewritten: %v", err)
	}
	until := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	answer = func(json.RawMessage) (json.RawMessage, error) {
		return nil, &rpc.Error{Code: assistantwire.CodeModelPaused, Message: "held", Data: json.RawMessage(`{"reason":"quota","until":"` + until + `"}`)}
	}
	var se *subscriptions.Error
	if _, err := m.JSON(ctx, in); !errors.As(err, &se) || se.Code != subscriptions.CodeModelFailed || se.RetryAfter < 500 {
		t.Fatalf("a held connection: %v", err)
	}
	answer = func(params json.RawMessage) (json.RawMessage, error) {
		if !strings.Contains(string(params), `"purpose":"subscription.recipe"`) {
			return json.RawMessage(`{"usable":false}`), nil
		}
		return json.RawMessage(`{"usable":true,"provider":"Example API","model":"small-model"}`), nil
	}
	if got := h.subs.RecipeModel(ctx); !got.OK || got.Provider != "Example API" || got.Model != "small-model" || asked[len(asked)-1] != assistantwire.MethodModelsUsable {
		t.Fatalf("recipe model %+v (asked %v)", got, asked)
	}
	if got := h.subs.ModelOf(ctx, "kb"); got.OK {
		t.Fatalf("an unusable purpose: %+v", got)
	}
	h.opts.Assistant = nil
	if got := h.subs.RecipeModel(ctx); got.OK {
		t.Fatalf("the assistant away: %+v", got)
	}
	if _, err := m.JSON(ctx, in); err == nil || errors.Is(err, subscriptions.ErrNoModel) {
		t.Fatalf("the assistant away: %v", err)
	}
	// A failed draft carries the draft for the manual editor.
	rec := httptest.NewRecorder()
	writeSubscriptionErr(rec, &subscriptions.Error{Status: http.StatusUnprocessableEntity, Code: subscriptions.CodeNoTitle, Message: "No item has a title.",
		Draft: &subscriptions.Recipe{Tool: "list"}})
	var body map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["code"] != "no_title" || body["recipe"].(map[string]any)["tool"] != "list" {
		t.Fatalf("%s", rec.Body.String())
	}
}

// A subscription is an internal source (#2039): every Inbox round names it and, unless it reads only the Feishu chats,
// hands in its things as a plugin's (triaged first); the knowledge base lists it and reads it through
// connectors.changes. Deleting it with its data asks both to remove theirs, and while the assistant is away the
// subscription stays, switched off, for another try.
func TestSubscriptionsAreInboxAndKnowledgeBaseSources(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	store := subscriptions.NewMemStore()
	now := time.Now().UTC()
	from, mark := now.Add(-time.Minute), now.Add(-30*time.Second)
	sub := subscriptions.Subscription{ID: "sub_example", Connector: "example", Source: "notices", Label: "Notices",
		Recipe: subscriptions.Recipe{Mode: "items", Kind: "sub_notice", GroupBy: "source"}, ToInbox: true, ToKB: true,
		InboxFrom: &from, InboxCatchup: &mark, InboxGroup: "source", KBFrom: &from, Revision: 1, CreatedAt: now.Add(-time.Hour)}
	if err := store.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-2 * time.Minute)
	items := []subscriptions.Item{{UID: "n1", Title: "Office hours moved", Time: &at, Body: "Made-up text", Kind: "sub_notice"},
		{UID: "n2", Title: "Form due", Time: &at, Body: "Made-up text", Kind: "sub_notice"}}
	if _, err := store.CommitRun(ctx, subscriptions.RunCommit{Sub: sub, Revision: 1, Items: items, Now: mark}); err != nil {
		t.Fatal(err)
	}
	h.subs = subscriptions.New(h.subscriptionOptions(store))

	r := h.readInbox(ctx, roundPoll)
	var src *assistantwire.InboxSource
	for i := range r.Sources {
		if r.Sources[i].ID == sub.ID {
			src = &r.Sources[i]
		}
	}
	// One row for the subscription grouped by source (#5671) and the failure notice's end; its count is the Inbox's to read.
	if r.Names[sub.ID] != "Notices" || src == nil || !src.Extension || !src.Counts || src.Kinds["sub_digest"] == "" || len(src.Things) != 2 {
		t.Fatalf("the round: names %v, source %+v", r.Names, src)
	}
	// What it reads and its row's knowledge-base chat, for the AI twin (#6222).
	if src.Via != "example" || src.ViaSource != "notices" || len(src.Chats) != 1 {
		t.Fatalf("what the subscription reads: %q %q %v", src.Via, src.ViaSource, src.Chats)
	}
	if chats := h.readInbox(ctx, roundChats); chats.Names[sub.ID] != "Notices" || len(chats.Sources) != 0 {
		t.Fatalf("a round of the Feishu chats: %+v", chats)
	}
	// A run that failed: the next round says the subscription is behind, as a built-in's failed read does, and still
	// hands its rows (#6189).
	cur, _ := store.GetSubscription(ctx, sub.ID)
	failing := *cur
	failing.RunState = subscriptions.StateFailing
	if ok, err := store.UpdateSubscription(ctx, failing, cur.Revision); !ok || err != nil {
		t.Fatalf("update: %v %v", ok, err)
	}
	found := false
	for _, s := range h.readInbox(ctx, roundPoll).Sources {
		if s.ID == sub.ID {
			found = true
			if s.ReadError != assistantwire.ReadFailed || len(s.Things) != 2 {
				t.Fatalf("a failing subscription's source: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("the failing subscription left the round")
	}

	archives := h.kbArchives(ctx)
	if len(archives.Archives) != 1 || archives.Archives[0].ID != sub.ID {
		t.Fatalf("the knowledge base's archives: %+v", archives)
	}
	raw, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsChanges,
		Params: json.RawMessage(`{"id":"sub_example","cursor":""}`)})
	var res assistantwire.ChangesResult
	_ = json.Unmarshal(raw, &res)
	if err != nil || len(res.Batches) != 1 || len(res.Batches[0].Messages) != 2 || res.Next == "" {
		t.Fatalf("connectors.changes of the subscription: %+v %v", res, err)
	}
	if _, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsArchiveMedia,
		Params: json.RawMessage(`{"id":"sub_example","ref":"x"}`)}); err == nil {
		t.Fatal("a subscription handed over an attachment")
	}

	mux := http.NewServeMux()
	h.subscriptionRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/subscriptions/sub_example?data=1", nil)
	mux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), principal{ID: "dev_phone", Role: roleFull})))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusServiceUnavailable || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("deleting its data with the assistant away: %d %v", rec.Code, out)
	}
	if left, _ := store.GetSubscription(ctx, sub.ID); left == nil || left.ToInbox || left.ToKB {
		t.Fatalf("after the failed removal: %+v", left)
	}
	if a := h.kbArchives(ctx); len(a.Archives) != 0 {
		t.Fatalf("the knowledge base still reads a subscription switched off: %+v", a)
	}
}
