package voice

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/modules/voice/voicetest"
)

func getInfo(t *testing.T, srv *httptest.Server, cid, role string) (int, extwire.TranscriptionInfo) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/transcription/"+cid+"/info", nil)
	if role != "" {
		req.Header.Set(extwire.HeaderRole, role)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var info extwire.TranscriptionInfo
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &info); err != nil {
			t.Fatalf("info %s", b)
		}
	}
	return res.StatusCode, info
}

// /transcription/{cid}/info says the wire, the features and what is missing: the voice extension's setup with no
// upstream, live_upstream with none GPT-Live-compatible, interim once a session of the upstream was granted captions.
func TestTranscriptionInfo(t *testing.T) {
	trFast(t)
	for _, tt := range []struct {
		name     string
		cfg      Config
		setup    string
		features string
	}{
		{"no upstream", Config{Setup: SetupKey}, SetupKey, "live,replay"},
		{"only a codex upstream", Config{Providers: []Provider{{ID: "gateway", URL: "wss://example.com/v1/live", Dialect: DialectCodex}}},
			extwire.TSetupLiveUpstream, "live,replay"},
		{"the fake dialect", Config{Providers: []Provider{{ID: "dev", Dialect: DialectFake}}}, "", "live,replay,interim"},
		{"a live upstream after a codex default", Config{Providers: []Provider{
			{ID: "gateway", URL: "wss://example.com/v1/live", Dialect: DialectCodex},
			{ID: "dev", Dialect: DialectFake}}, Default: "gateway/gpt-live-1-codex"}, "", "live,replay,interim"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, info := getInfo(t, bareTrPlugin(t, tt.cfg), TranscriptionContribution, roleFull)
			if status != http.StatusOK || info.Protocol != 1 || info.Setup != tt.setup || strings.Join(info.Features, ",") != tt.features {
				t.Fatalf("%d %+v", status, info)
			}
		})
	}

	_, srv, _, _ := trLive(t, voicetest.Script{Sentences: trScript})
	if _, info := getInfo(t, srv, TranscriptionContribution, ""); strings.Join(info.Features, ",") != "live,replay" || info.Setup != "" {
		t.Fatalf("before a job: %+v", info)
	}
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	tc.done()
	if _, info := getInfo(t, srv, TranscriptionContribution, ""); strings.Join(info.Features, ",") != "live,replay,interim" {
		t.Fatalf("after captions were granted: %+v", info)
	}
}

// Only the manager reaches the routes: another contribution's id is 404, a caller the apps' forward names other than
// a full device is 403, on both routes.
func TestTranscriptionRoutesRefuse(t *testing.T) {
	srv := bareTrPlugin(t, Config{Providers: []Provider{{ID: "dev", Dialect: DialectFake}}})
	if status, _ := getInfo(t, srv, "other", ""); status != http.StatusNotFound {
		t.Errorf("another contribution: %d", status)
	}
	for _, role := range []string{"answer", "extension"} {
		if status, _ := getInfo(t, srv, TranscriptionContribution, role); status != http.StatusForbidden {
			t.Errorf("%s: %d", role, status)
		}
		if _, res, err := dialTr(t, srv, TranscriptionContribution, http.Header{extwire.HeaderRole: {role}}); err == nil ||
			res == nil || res.StatusCode != http.StatusForbidden {
			t.Errorf("socket for %s: %v %v", role, res, err)
		}
	}
	if _, res, err := dialTr(t, srv, "other", nil); err == nil || res == nil || res.StatusCode != http.StatusNotFound {
		t.Errorf("socket for another contribution: %v %v", res, err)
	}
	// The voice point's own routes stay as they are.
	res, err := http.Get(srv.URL + "/info")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	_ = json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if info["protocol"] != float64(infoProtocol) {
		t.Errorf("/info %v", info)
	}
}

// The glossary is taken as spellings: one line each, cut, without blanks or repeats, at most trGlossaryMost; a long
// one is cut from its end until the instructions fit.
func TestTranscriptionGlossary(t *testing.T) {
	in := []string{"  Alice  ", "", "export\ncap", "ALICE", strings.Repeat("x", 100)}
	got := cleanGlossary(in)
	if len(got) != 3 || got[0] != "Alice" || got[1] != "export cap" || runeLen(got[2]) != trGlossaryTermMost {
		t.Fatalf("%q", got)
	}
	var many []string
	for i := range 400 {
		many = append(many, strings.Repeat(string(rune('a'+i%26)), 70)+string(rune('A'+i/26)))
	}
	j := &trJob{lang: "en", textLang: "en-US", glossary: cleanGlossary(many)}
	if len(j.glossary) != trGlossaryMost {
		t.Fatalf("%d terms", len(j.glossary))
	}
	ins := j.instructions(trVerbatim, []string{"The last sentence."})
	if runeLen(ins) > instructionsMost || !strings.Contains(ins, j.glossary[0]) || strings.Contains(ins, j.glossary[trGlossaryMost-1]) {
		t.Fatalf("%d runes", runeLen(ins))
	}
}
