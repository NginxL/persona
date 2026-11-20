package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Candidate models for model.complete (model_candidates.go, #10398).

// candidateGateways gives each model in behaviours a provider of its own (gw-<model>, a direct API one offering only
// that model) behind a fake Messages API: "fail" answers 400, "slow" answers after the call gave up, "empty" with no
// text, anything else with that text. It returns the models each gateway was asked for, in order.
func candidateGateways(t *testing.T, s *Service, behaviours map[string]string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	var list []storedLauncher
	names := make([]string, 0, len(behaviours))
	for m := range behaviours {
		names = append(names, m)
	}
	slices.Sort(names)
	for _, model := range names {
		behaviour := behaviours[model]
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var c upstreamCall
			_ = json.NewDecoder(r.Body).Decode(&c)
			mu.Lock()
			asked = append(asked, c.Model)
			mu.Unlock()
			text := behaviour
			switch behaviour {
			case "fail":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"synthetic failure"}}`))
				return
			case "slow":
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
				return
			case "empty":
				text = ""
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": c.Model, "stop_reason": "end_turn",
				"content": []any{map[string]any{"type": "text", "text": text}},
				"usage":   map[string]any{"input_tokens": 100, "output_tokens": 10},
			})
		}))
		t.Cleanup(srv.Close)
		key, err := s.sealKey("synthetic-key")
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, storedLauncher{KeySealed: key, ManagedLauncher: proto.ManagedLauncher{
			Name: "gw-" + model, Kind: "api", BaseURL: srv.URL, Model: "other-" + model,
			Models: []proto.ProviderModel{{ID: model}},
		}})
	}
	putLaunchers(t, s, list...)
	return func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(asked) }
}

func attemptCodes(as []assistantwire.ModelAttempt) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Model + "=" + a.Code
	}
	return out
}

func TestModelCandidatesMoveOnForEachFailure(t *testing.T) {
	s, _, kbCalls := extensionModelService(t, "kb answer")
	asked := candidateGateways(t, s, map[string]string{"m-fail": "fail", "m-slow": "slow", "m-empty": "empty", "m-ok": "the answer"})
	var logMu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	started := time.Now()
	res, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"timeout_seconds":1,"label":"curate",
		"models":["m-fail","m-slow","m-empty","m-nowhere","m-ok"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the answer" || res.Candidate != "m-ok" || res.Model != "m-ok" {
		t.Fatalf("answered by %+v", res)
	}
	want := []string{"m-fail=model_failed", "m-slow=timeout", "m-empty=model_failed", "m-nowhere=model_unavailable"}
	if got := attemptCodes(res.Attempts); !slices.Equal(got, want) {
		t.Fatalf("attempts %v, want %v", got, want)
	}
	if !strings.Contains(res.Attempts[0].Message, "synthetic failure") || !strings.Contains(res.Attempts[3].Message, "offers it") {
		t.Fatalf("attempt messages %+v", res.Attempts)
	}
	if res.Usage.Requests < 2 || res.Usage.InputTokens < 200 {
		t.Fatalf("usage of every attempt: %+v", res.Usage)
	}
	if slices.Contains(asked(), "m-nowhere") || len(kbCalls()) != 0 {
		t.Fatalf("a model no provider offers, or the purpose's own, was asked: %v / %d", asked(), len(kbCalls()))
	}
	if d := time.Since(started); d > 4*time.Second {
		t.Fatalf("the slow candidate was not cut at its timeout: %s", d)
	}
	logMu.Lock()
	log := strings.Join(lines, "\n")
	logMu.Unlock()
	for _, want := range []string{"(curate) candidate 1/5 m-fail via gw-m-fail: failed", "candidate 4/5 m-nowhere via -: unavailable",
		"(curate) answered by candidate m-ok (m-ok) after 4 failed"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the log does not say %q:\n%s", want, log)
		}
	}
}

func TestModelCandidatesMoveOnWhenTheSchemaDoesNotMatch(t *testing.T) {
	s, _, _ := extensionModelService(t, "unused")
	asked := candidateGateways(t, s, map[string]string{"m-prose": "not json at all", "m-json": `{"a":1}`})
	res, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],
		"json_schema":{"type":"object","required":["a"]},"models":["m-prose","m-json"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate != "m-json" || string(res.JSON) != `{"a":1}` {
		t.Fatalf("answered by %+v", res)
	}
	if got := attemptCodes(res.Attempts); !slices.Equal(got, []string{"m-prose=invalid_output"}) {
		t.Fatalf("attempts %v", got)
	}
	// The prose candidate had its second try before the next one was asked.
	if got := asked(); !slices.Equal(got, []string{"m-prose", "m-prose", "m-json"}) {
		t.Fatalf("asked %v", got)
	}
}

func TestModelCandidatesAllFailing(t *testing.T) {
	s, _, _ := extensionModelService(t, "unused")
	candidateGateways(t, s, map[string]string{"m-fail": "fail", "m-empty": "empty"})
	_, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"models":["m-fail","m-empty","m-nowhere"]}`)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeModelCandidatesFailed {
		t.Fatalf("error %v", err)
	}
	for _, m := range []string{"m-fail: model_failed", "m-empty: model_failed", "m-nowhere: model_unavailable"} {
		if !strings.Contains(re.Message, m) {
			t.Fatalf("the message does not say %q: %s", m, re.Message)
		}
	}
	var data assistantwire.ModelCandidatesFailed
	if err := json.Unmarshal(re.Data, &data); err != nil {
		t.Fatal(err)
	}
	if got := attemptCodes(data.Attempts); !slices.Equal(got, []string{"m-fail=model_failed", "m-empty=model_failed", "m-nowhere=model_unavailable"}) {
		t.Fatalf("data %v", got)
	}
	if data.Usage.Requests == 0 {
		t.Fatalf("usage %+v", data.Usage)
	}
}

func TestModelCallWithoutCandidatesIsUnchanged(t *testing.T) {
	s, _, kbCalls := extensionModelService(t, "")
	asked := candidateGateways(t, s, map[string]string{"m-ok": "the answer"})
	// An empty reply is still an answer, on the purpose's own model, with nothing about candidates.
	res, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "" || res.Model != "kb-model-answered" || res.Candidate != "" || res.Attempts != nil || res.Usage.Requests != 1 {
		t.Fatalf("result %+v", res)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "candidate") || strings.Contains(string(raw), "attempts") {
		t.Fatalf("new fields on a call without models: %s", raw)
	}
	if len(kbCalls()) != 1 || len(asked()) != 0 {
		t.Fatalf("calls: purpose %d, gateways %v", len(kbCalls()), asked())
	}
	if p := purposeOf(t, s, "extension.kb-curator"); p.Candidates == nil || p.Candidates.Mode != "extension" || len(p.Candidates.Requested) != 0 {
		t.Fatalf("candidates %+v", p.Candidates)
	}
}

func TestModelCandidatesParams(t *testing.T) {
	s, _, _ := extensionModelService(t, "unused")
	for _, models := range []string{`[]`, `["a","a"]`, `["bad id"]`, `["1","2","3","4","5","6","7","8","9"]`} {
		_, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"models":`+models+`}`)
		if models == `[]` { // empty is no candidates: the purpose's model
			if err != nil {
				t.Fatalf("%s: %v", models, err)
			}
			continue
		}
		if rpcCode(err) != "invalid_params" {
			t.Fatalf("%s: %v", models, err)
		}
	}
}

func TestModelCandidatesUserSwitchesOffOrReplaces(t *testing.T) {
	s, _, kbCalls := extensionModelService(t, "kb answer")
	asked := candidateGateways(t, s, map[string]string{"m-fail": "fail", "m-mine": "mine"})
	call := `{"messages":[{"role":"user","content":"hi"}],"models":["m-fail","m-nowhere"]}`

	// The list the extension sent shows in Settings, each model with where it runs.
	if _, err := completeAs(s, "kb-curator", call); rpcCode(err) != assistantwire.CodeModelCandidatesFailed {
		t.Fatal(err)
	}
	p := purposeOf(t, s, "extension.kb-curator")
	if p.Candidates == nil || len(p.Candidates.Requested) != 1 {
		t.Fatalf("requested %+v", p.Candidates)
	}
	seen := p.Candidates.Requested[0].Models
	if len(seen) != 2 || seen[0].Model != "m-fail" || seen[0].Provider != "launcher.gw-m-fail" || !seen[0].Available || seen[1].Available {
		t.Fatalf("requested %+v", seen)
	}

	// Off: the purpose's own model, as before.
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":0,"candidates":{"mode":"off"}}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	res, err := completeAs(s, "kb-curator", call)
	if err != nil || res.Text != "kb answer" || res.Candidate != "" || len(kbCalls()) != 1 {
		t.Fatalf("off: %+v %v", res, err)
	}

	// Custom: the user's own list for every call, whatever the extension sends.
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":1,"candidates":{"mode":"custom","custom":[{"provider":"launcher.gw-m-mine","model":"m-mine"}]}}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	for _, params := range []string{call, `{"messages":[{"role":"user","content":"hi"}]}`} {
		res, err = completeAs(s, "kb-curator", params)
		if err != nil || res.Text != "mine" || res.Candidate != "m-mine" {
			t.Fatalf("custom: %+v %v", res, err)
		}
	}
	// Changing the purpose's model keeps the user's candidates.
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":2,"model":"kb-other"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if p := purposeOf(t, s, "extension.kb-curator"); p.Candidates.Mode != "custom" || len(p.Candidates.Custom) != 1 || !p.Candidates.Custom[0].Available {
		t.Fatalf("after a model change: %+v", p.Candidates)
	}
	if slices.Contains(asked(), "m-nowhere") {
		t.Fatal("asked a model no provider offers")
	}

	// Refused: an unknown mode, a provider not offered, a list too long; and on any other purpose. null goes back.
	for _, body := range []string{
		`{"revision":3,"candidates":{"mode":"sometimes"}}`,
		`{"revision":3,"candidates":{"mode":"custom","custom":[{"provider":"launcher.nowhere","model":"x"}]}}`,
		`{"revision":3,"candidates":{"mode":"custom","custom":[]}}`,
		`{"revision":3,"candidates":{"mode":"off","custom":[{"provider":"launcher.gw-m-mine"}]}}`,
	} {
		if r := modelRequest(s, "PATCH", "extension.kb-curator", body); r.Code != 400 {
			t.Fatalf("%s: %d %s", body, r.Code, r.Body.String())
		}
	}
	if r := modelRequest(s, "PATCH", "kb", `{"revision":0,"candidates":{"mode":"off"}}`); r.Code != 400 {
		t.Fatalf("kb took candidates: %d", r.Code)
	}
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":3,"candidates":null}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if p := purposeOf(t, s, "extension.kb-curator"); p.Candidates.Mode != "extension" || len(p.Candidates.Custom) != 0 {
		t.Fatalf("after null: %+v", p.Candidates)
	}
}

func TestExtensionCandidateEditorReadsExactChoices(t *testing.T) {
	s, _, _ := extensionModelService(t, "kb answer")
	candidateGateways(t, s, map[string]string{"unit-model": "ok"})
	// These are managed connections, but none can serve a direct candidate.
	providers := s.storedLaunchers(t.Context())
	providers = append(providers,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "no-key", Kind: "api", BaseURL: "https://unused.invalid"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "no-address", Kind: "api"}, KeySealed: providers[0].KeySealed},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cli-only", Agent: "codex"}},
	)
	putLaunchers(t, s, providers...)
	const id = "extension.kb-curator"
	if _, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hello"}],"models":["unit-model"]}`); err != nil {
		t.Fatal(err)
	}
	res := modelRequest(s, "PATCH", id, `{"revision":0,"candidates":{"mode":"custom","custom":[{"provider":"launcher.gw-unit-model","model":""},{"provider":"launcher.gw-unit-model","model":"other-unit-model"}]}}`)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	read := modelRequest(s, "GET", id, "")
	var wire struct {
		Purpose struct {
			ProviderIDs []string `json:"provider_ids"`
			Candidates  struct {
				Direct    []string         `json:"direct_provider_ids"`
				Custom    []map[string]any `json:"custom"`
				Requested []struct {
					Models []map[string]any `json:"models"`
				} `json:"requested"`
			} `json:"candidates"`
		} `json:"purpose"`
	}
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &wire) != nil {
		t.Fatal(read.Code, read.Body.String())
	}
	c := wire.Purpose.Candidates
	if !slices.Equal(c.Direct, []string{"launcher.gw-unit-model"}) || !slices.Contains(wire.Purpose.ProviderIDs, c.Direct[0]) {
		t.Errorf("direct providers: %v, purpose: %v", c.Direct, wire.Purpose.ProviderIDs)
	}
	if len(c.Custom) != 2 || c.Custom[0]["model"] != "other-unit-model" || c.Custom[1]["model"] != "other-unit-model" || c.Custom[0]["configured_model"] != "" || c.Custom[1]["configured_model"] != "other-unit-model" {
		t.Errorf("wire lost the difference between following the provider and an explicit pick: %v", c.Custom)
	}
	if len(c.Requested) != 1 || len(c.Requested[0].Models) != 1 {
		t.Fatalf("requested: %v", c.Requested)
	}
	if _, exists := c.Requested[0].Models[0]["configured_model"]; exists {
		t.Error("an extension request has no user-configured model")
	}

	// A saved provider can become unavailable. Its row stays editable, and clients can distinguish an empty
	// callable list from an older hub that never sent the field.
	putLaunchers(t, s, providers[1:]...)
	wire.Purpose.Candidates.Direct = nil
	read = modelRequest(s, "GET", id, "")
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &wire) != nil {
		t.Fatal(read.Code, read.Body.String())
	}
	c = wire.Purpose.Candidates
	if c.Direct == nil || len(c.Direct) != 0 {
		t.Errorf("expected an explicit empty direct provider list, got %#v", c.Direct)
	}
	if len(c.Custom) != 2 || c.Custom[0]["provider"] != "launcher.gw-unit-model" || c.Custom[0]["configured_model"] != "" || c.Custom[0]["available"] != false {
		t.Errorf("missing provider lost its saved row: %v", c.Custom)
	}
	for _, tc := range []struct{ lang, missing, unoffered string }{
		{"en", "the provider gw-unit-model is gone or cannot be called directly", "no provider in Settings › Model providers offers it"},
		{"zh-Hans", "供应商 gw-unit-model 已移除或无法直接调用", "设置中的模型供应商均未提供此模型"},
		{"ja", "プロバイダー gw-unit-model は削除されたか、直接呼び出せません", "設定済みのモデルプロバイダーはこのモデルを提供していません"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/model-config/purposes/"+id, nil)
			req.Header.Set("Accept-Language", tc.lang)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, withRequestLang(req))
			var out struct{ Purpose ModelPurpose }
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Purpose.Candidates == nil {
				t.Fatal(rec.Code, rec.Body.String())
			}
			rows := out.Purpose.Candidates
			if len(rows.Custom) != 2 || len(rows.Requested) != 1 || len(rows.Requested[0].Models) != 1 {
				t.Fatal(rows)
			}
			custom, requested := rows.Custom[0], rows.Requested[0].Models[0]
			if custom.Problem != tc.missing || requested.Problem != tc.unoffered || requested.Model != "unit-model" || custom.Provider != "launcher.gw-unit-model" {
				t.Errorf("candidate reasons or identifiers: custom=%+v requested=%+v", custom, requested)
			}
		})
	}
}

// One candidate at a time (how an extension that counts failures itself calls): its failure is its own code and data,
// as a call without candidates gives, never model_candidates_failed.
func TestModelSingleCandidateFailsWithItsOwnCode(t *testing.T) {
	s, _, _ := extensionModelService(t, "unused")
	candidateGateways(t, s, map[string]string{"m-fail": "fail", "m-empty": "empty", "m-prose": "prose"})
	for model, want := range map[string]string{"m-fail": "model_failed", "m-empty": "model_failed", "m-nowhere": "model_unavailable", "m-prose": "invalid_output"} {
		params := `{"messages":[{"role":"user","content":"hi"}],"models":["` + model + `"]}`
		if model == "m-prose" {
			params = `{"messages":[{"role":"user","content":"hi"}],"json_schema":{"type":"object"},"models":["m-prose"]}`
		}
		_, err := completeAs(s, "kb-curator", params)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != want {
			t.Fatalf("%s: %v, want %s", model, err, want)
		}
		switch model {
		case "m-nowhere":
			var data assistantwire.ModelNotOffered
			if json.Unmarshal(re.Data, &data) != nil || data.Model != "m-nowhere" {
				t.Fatalf("not offered data %s", re.Data)
			}
		case "m-prose":
			var data assistantwire.ModelInvalidOutput
			if json.Unmarshal(re.Data, &data) != nil || data.Text != "prose" || data.Usage.Requests != 2 {
				t.Fatalf("invalid output data %s", re.Data)
			}
		}
	}
	// A later candidate's attempt list carries each failure's time and usage.
	candidateGateways(t, s, map[string]string{"m-fail": "fail", "m-empty": "empty", "m-ok": "ok"})
	res, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"models":["m-empty","m-ok"]}`)
	if err != nil || len(res.Attempts) != 1 || res.Attempts[0].Usage.Requests != 1 || res.Attempts[0].Seconds < 0 || res.Usage.Requests != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

// servingGateway is a provider named name (a keyed Messages launcher) whose fake API answers "<name> served <model>"
// for any model but "missing" (400), listing listed (none: no models list) with def as its default. asked has the
// models it was asked for.
func servingGateway(t *testing.T, s *Service, name, def string, listed ...string) (storedLauncher, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") { // probes are not calls
			http.NotFound(w, r)
			return
		}
		var c upstreamCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		mu.Lock()
		asked = append(asked, c.Model)
		mu.Unlock()
		if c.Model == "missing" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"synthetic failure"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": c.Model, "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": name + " served " + c.Model}},
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	key, err := s.sealKey("synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	l := storedLauncher{KeySealed: key, ManagedLauncher: proto.ManagedLauncher{Name: name, BaseURL: srv.URL, Model: def}}
	for _, m := range listed {
		l.Models = append(l.Models, proto.ProviderModel{ID: m})
	}
	return l, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(asked) }
}

// #10657: the purpose's own provider without a models list takes the extension's candidates, after any provider that
// lists one explicitly; one that fails there moves on like any candidate.
func TestModelCandidatesOnTheOwnProviderWithoutAList(t *testing.T) {
	s, _, kbCalls := extensionModelService(t, "unused")
	own, ownAsked := servingGateway(t, s, "own", "own-default")
	listed, listedAsked := servingGateway(t, s, "listed", "listed-default", "g-listed")
	other, otherAsked := servingGateway(t, s, "other", "other-default")
	putLaunchers(t, s, own, listed, other)
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":0,"provider":"launcher.own"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	res, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"models":["missing","g-any","g-listed"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate != "g-any" || res.Text != "own served g-any" || len(res.Attempts) != 1 || res.Attempts[0].Code != "model_failed" {
		t.Fatalf("result %+v", res)
	}
	res, err = completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"models":["g-listed"]}`)
	if err != nil || res.Text != "listed served g-listed" {
		t.Fatalf("an explicit listing goes first: %+v %v", res, err)
	}
	if got := ownAsked(); !slices.Equal(got, []string{"missing", "g-any"}) {
		t.Fatalf("own asked %v", got)
	}
	if !slices.Equal(listedAsked(), []string{"g-listed"}) || len(otherAsked()) != 0 || len(kbCalls()) != 0 {
		t.Fatalf("listed %v, other (no list, not the purpose's) %v, purpose %d", listedAsked(), otherAsked(), len(kbCalls()))
	}
	// Settings resolves the same way: the first list (now the older one) has g-any on own and g-listed on listed.
	p := purposeOf(t, s, "extension.kb-curator")
	if len(p.Candidates.Requested) != 2 {
		t.Fatalf("settings view %+v", p.Candidates.Requested)
	}
	first := p.Candidates.Requested[1].Models
	if len(first) != 3 || first[1].Provider != "launcher.own" || !first[1].Available || first[2].Provider != "launcher.listed" {
		t.Fatalf("settings view %+v", first)
	}
}

// #10657: a provider with a list still refuses what it does not list, except the model the purpose is bound to there.
func TestModelCandidatesTheBoundModelOnAListedProvider(t *testing.T) {
	s, _, _ := extensionModelService(t, "unused")
	own, ownAsked := servingGateway(t, s, "own", "own-default", "a")
	putLaunchers(t, s, own)
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":0,"provider":"launcher.own","model":"bound-m"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	res, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"hi"}],"models":["z","bound-m"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate != "bound-m" || len(res.Attempts) != 1 || res.Attempts[0].Code != "model_unavailable" {
		t.Fatalf("result %+v", res)
	}
	if got := ownAsked(); !slices.Equal(got, []string{"bound-m"}) {
		t.Fatalf("asked %v: an unlisted model went to a provider with a list", got)
	}
}
