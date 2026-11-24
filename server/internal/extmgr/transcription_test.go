package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extwire"
)

// serveTranscriptionExtensionForTest is a transcription contribution's program (PERSONA_TEST_PLUGIN=transcription):
// /transcription/<cid>/info and a job socket for each cid of TRANSCRIPTION_CIDS, 404 for any other. The socket first
// says what it was asked with ("seen"), then echoes every frame; {"type":"big"} is answered with a 200 KiB frame.
func serveTranscriptionExtensionForTest() {
	ln, err := net.Listen("unix", os.Getenv("PERSONA_PLUGIN_SOCKET"))
	if err != nil {
		log.Fatal(err)
	}
	cids := strings.Split(os.Getenv("TRANSCRIPTION_CIDS"), ",")
	ours := func(w http.ResponseWriter, r *http.Request) bool {
		if !slices.Contains(cids, r.PathValue("cid")) {
			http.NotFound(w, r)
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /echo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path})
	})
	mux.HandleFunc("GET /transcription/{cid}/info", func(w http.ResponseWriter, r *http.Request) {
		if ours(w, r) {
			_ = json.NewEncoder(w).Encode(extwire.TranscriptionInfo{Protocol: extwire.TranscriptionProtocol,
				Features: []string{extwire.TranscriptionLive, extwire.TranscriptionReplay}})
		}
	})
	mux.HandleFunc("GET /transcription/{cid}/ws", func(w http.ResponseWriter, r *http.Request) {
		if !ours(w, r) {
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		seen, _ := json.Marshal(map[string]string{"type": "seen", "path": r.URL.Path, "query": r.URL.RawQuery,
			"auth": r.Header.Get("Authorization"), "role": r.Header.Get(extwire.HeaderRole)})
		if c.Write(ctx, websocket.MessageText, seen) != nil {
			return
		}
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if string(b) == `{"type":"big"}` {
				b = []byte(`{"type":"sentence","text":"` + strings.Repeat("x", 200<<10) + `"}`)
			}
			if c.Write(ctx, typ, b) != nil {
				return
			}
		}
	})
	_ = http.Serve(ln, mux)
}

// transcriptionHub runs the built-in voice extension as "talk" (its program the fake above, contributing gpt-live),
// hub.json's transcription extension "tx" (and "txoff", switched off), and "other", a voice extension that does not
// transcribe.
func transcriptionHub(t *testing.T) *Manager {
	t.Helper()
	voice := needsVoiceModule(t)
	self, _ := os.Executable()
	builtin := voiceEntry("talk")
	builtin.Env = map[string]string{"PERSONA_TEST_PLUGIN": "transcription", "TRANSCRIPTION_CIDS": voice.TranscriptionContribution}
	fake := func(id string) ExtensionConfig {
		return ExtensionConfig{ID: id, Kind: "transcription", Command: self, Env: map[string]string{"PERSONA_TEST_PLUGIN": "transcription", "TRANSCRIPTION_CIDS": id}}
	}
	off := fake("txoff")
	off.Disabled = true
	other := ExtensionConfig{ID: "other", Kind: "voice", Command: self, Env: map[string]string{"PERSONA_TEST_PLUGIN": "transcription", "TRANSCRIPTION_CIDS": "other"}}
	h := voiceHub(t, newExtensionStore(), builtin, fake("tx"), off, other)
	for _, id := range []string{"talk", "tx", "other"} {
		waitVoiceStatus(t, h, id, host.StateRunning)
	}
	return h
}

// The manager reaches the transcription contributions over their own sockets: the built-in voice extension's for
// "", another only by its "<extension id>/<contribution id>", never one that is switched off, not running, or not a
// transcription contribution.
func TestTranscriptionContributions(t *testing.T) {
	h := transcriptionHub(t)
	ctx := context.Background()

	for ref, want := range map[string]bool{"": true, "talk/gpt-live": true, "tx/tx": true, "txoff/txoff": true,
		"tx/nope": false, "talk/talk": false, "other/other": false, "nope/x": false, "tx": false, "tx/": false, "/tx": false,
		"Tx/tx": false, "tx/tx/x": false} {
		if got := h.IsTranscriptionContribution(ref); got != want {
			t.Errorf("IsTranscriptionContribution(%q) = %v", ref, got)
		}
	}

	// The installation a choice is pinned to: the extension's, stable while it stays installed; none for a ref that
	// names no transcription contribution.
	if got := h.TranscriptionInstall("tx/tx"); got != "hub.json:tx" || h.TranscriptionInstall("talk/gpt-live") != "builtin:talk" {
		t.Errorf("TranscriptionInstall(tx/tx) = %q", got)
	}
	for _, ref := range []string{"tx/nope", "other/other", "nope/x", "bad"} {
		if got := h.TranscriptionInstall(ref); got != "" {
			t.Errorf("TranscriptionInstall(%q) = %q", ref, got)
		}
	}

	for _, ref := range []string{"", "talk/gpt-live", "tx/tx"} {
		info, err := h.TranscriptionInfo(ctx, ref)
		if err != nil || info.Protocol != extwire.TranscriptionProtocol || !slices.Equal(info.Features, []string{"live", "replay"}) || info.Setup != "" {
			t.Fatalf("info %q: %+v %v", ref, info, err)
		}
	}
	for _, ref := range []string{"txoff/txoff", "tx/nope", "other/other", "nope/x", "bad"} {
		if _, err := h.TranscriptionInfo(ctx, ref); !errors.Is(err, ErrNoTranscriber) {
			t.Errorf("info %q: %v", ref, err)
		}
		if _, _, err := h.TranscriptionDial(ctx, ref); !errors.Is(err, ErrNoTranscriber) {
			t.Errorf("dial %q: %v", ref, err)
		}
	}

	// One job: the extension's request is open until done, which ends it once; the manager's dial carries no
	// credential, and a job's frames may be larger than a WebSocket's default.
	hp := h.extensions.get("tx")
	conn, done, err := h.TranscriptionDial(ctx, "tx/tx")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if hp.proc.Idle() {
		t.Fatal("the job does not hold the extension's request")
	}
	_, raw, err := conn.Read(ctx)
	var seen map[string]string
	if err != nil || json.Unmarshal(raw, &seen) != nil || seen["path"] != "/transcription/tx/ws" || seen["query"] != "" || seen["auth"] != "" || seen["role"] != "" {
		t.Fatalf("seen %s %v", raw, err)
	}
	for _, m := range []struct {
		typ  websocket.MessageType
		data []byte
	}{{websocket.MessageBinary, []byte{1, 0, 255, 127}}, {websocket.MessageText, []byte(`{"type":"start","mode":"live","at":0}`)}} {
		if err := conn.Write(ctx, m.typ, m.data); err != nil {
			t.Fatal(err)
		}
		if typ, b, err := conn.Read(ctx); err != nil || typ != m.typ || string(b) != string(m.data) {
			t.Fatalf("echo %v %q %v", typ, b, err)
		}
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"big"}`)); err != nil {
		t.Fatal(err)
	}
	if _, b, err := conn.Read(ctx); err != nil || len(b) < 200<<10 {
		t.Fatalf("a large frame: %d %v", len(b), err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	done()
	done()
	if !hp.proc.Idle() {
		t.Fatal("done did not end the extension's request")
	}

	// The built-in's contribution switched off: no transcriber for "", though it is still a contribution there.
	mux := h.routes()
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/talk/-/contributions/transcription/gpt-live", h.cfg.Token, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the contribution off", func() bool {
		_, err := h.TranscriptionInfo(ctx, "")
		return errors.Is(err, ErrNoTranscriber)
	})
	if !h.IsTranscriptionContribution("") || !h.IsTranscriptionContribution("talk/gpt-live") {
		t.Fatal("a switched-off contribution is still the extension's")
	}

	// Not running.
	h.extensions.get("tx").proc.Stop()
	if _, err := h.TranscriptionInfo(ctx, "tx/tx"); !errors.Is(err, ErrNoTranscriber) {
		t.Fatalf("stopped: %v", err)
	}
}

// The installation a choice is pinned to never moves while the extension stays installed: not when the manager
// restarts (the database keeps microseconds, an install's clock nanoseconds, and a built-in's row is first written
// meanwhile), not when its market renames it. Installing it again moves it.
func TestTranscriptionInstallSurvivesARestart(t *testing.T) {
	manifest := func(id string) json.RawMessage {
		return json.RawMessage(`{"manifest_version": 1, "id": "` + id + `", "name": "ASR", "version": "1.0.0",
			"engines": {"plugin_protocol": 1}, "runtime": {"command": "bin/asr", "rpc": "none", "http": true},
			"activation_events": ["on_startup"], "contributes": {"transcription": [{"id": "asr", "name": "ASR"}]}}`)
	}
	h := newTestManager(t)
	h.extensions = newExtensions(h.cfg.dir, nil, h.logf)
	// load is the registry as the manager reads it when it starts: its built-in "talk", then the rows.
	load := func(rows ...extension.Record) {
		t.Helper()
		store := newExtensionStore()
		for _, r := range rows {
			store.rows[r.ID] = r
		}
		reg := extension.NewRegistry(h.extensions.root, store, nil, 0)
		m, err := extension.Parse(manifest("talk"))
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.AddBuiltin(m, "", true); err != nil {
			t.Fatal(err)
		}
		if err := reg.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.extensions.reg = reg
	}
	installed := time.Date(2026, 11, 13, 10, 0, 0, 123456789, time.UTC)
	acme := extension.Record{ID: "acme", Source: extension.SourceInstalled, Version: "1.0.0", Manifest: manifest("acme"),
		Enabled: true, InstalledAt: installed}
	talk := extension.Record{ID: "talk", Source: extension.SourceBuiltin, Version: "1.0.0", Enabled: true, InstalledAt: installed}

	// As the install left it in memory; the built-in has no row yet.
	load(acme)
	pinned, builtin := h.TranscriptionInstall("acme/asr"), h.TranscriptionInstall("talk/asr")
	if pinned == "" || builtin == "" {
		t.Fatalf("pins %q %q", pinned, builtin)
	}

	// Restarted: the row as the database reads it back (to the microsecond, in its zone), the built-in's written.
	reread := acme
	reread.InstalledAt = installed.Truncate(time.Microsecond).In(time.FixedZone("UTC+8", 8*3600))
	load(reread, talk)
	if got := h.TranscriptionInstall("acme/asr"); got != pinned {
		t.Errorf("after a restart %q, pinned %q", got, pinned)
	}
	if got := h.TranscriptionInstall("talk/asr"); got != builtin {
		t.Errorf("the built-in after a restart %q, pinned %q", got, builtin)
	}

	// Renamed by its market: the old id still names it, and it is the same installation.
	renamed := reread
	renamed.ID, renamed.StorageID, renamed.PreviousID, renamed.Aliases = "acme-asr", "acme", "acme", []string{"acme"}
	renamed.Manifest = manifest("acme-asr")
	load(renamed, talk)
	if got := h.TranscriptionInstall("acme/asr"); got != pinned {
		t.Errorf("after a rename %q, pinned %q", got, pinned)
	}

	// Uninstalled and installed again: another installation.
	again := acme
	again.InstalledAt = installed.Add(time.Minute)
	load(again, talk)
	if got := h.TranscriptionInstall("acme/asr"); got == "" || got == pinned {
		t.Errorf("installed again %q, pinned %q", got, pinned)
	}
}

// The apps' forward to an extension never reaches /transcription/, however it is spelled, under the old paths too
// and on a WebSocket; the extension's other paths are forwarded as before.
func TestTranscriptionNotForwardedToApps(t *testing.T) {
	h := transcriptionHub(t)
	mux := h.routes()
	master := h.cfg.Token
	phone := deviceToken("phone", roleFull)
	if code, out := call(t, mux, "GET", "/api/v1/extensions/tx/echo", master, ""); code != 200 || out["path"] != "/echo" {
		t.Fatalf("the forward: %d %v", code, out)
	}
	for _, path := range []string{
		"/api/v1/extensions/tx/transcription/tx/info",
		"/api/v1/extensions/talk/transcription/gpt-live/info",
		"/api/v1/plugins/tx/transcription/tx/info",
		"/api/v1/extensions/tx/transcription",
		"/api/v1/extensions/tx/transcription/",
		"/api/v1/extensions/tx/%74ranscription/tx/info",
		"/api/v1/extensions/tx/transcription%2Ftx%2Finfo",
		"/api/v1/extensions/tx/Transcription/tx/info",
		"/api/v1/extensions/tx/TRANSCRIPTION/tx/ws",
	} {
		for _, tok := range []string{master, phone} {
			// The forward's own refusal, not the program's 404 (it answers /transcription/tx/info).
			if code, out := call(t, mux, "GET", path, tok, ""); code != http.StatusNotFound || out["code"] != "not_found" {
				t.Errorf("%s: %d %v", path, code, out)
			}
		}
	}
	// Dot segments and doubled slashes are never forwarded either (the mux redirects them; the check cleans them).
	for _, path := range []string{"/api/v1/extensions/tx//transcription/tx/info", "/api/v1/extensions/tx/x/../transcription/tx/info"} {
		if code, _ := call(t, mux, "GET", path, master, ""); code == http.StatusOK {
			t.Errorf("%s: forwarded", path)
		}
	}
	for rest, want := range map[string]bool{"/transcription/tx/info": true, "//transcription/x": true, "/a/../transcription/x": true,
		"/transcription": true, "/Transcription/": true, "/transcriptions/x": false, "/echo/transcription/x": false, "/": false, "": false} {
		if transcriptionPath(rest) != want {
			t.Errorf("transcriptionPath(%q) != %v", rest, want)
		}
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	if c, res, err := websocket.Dial(context.Background(), ws+"/api/v1/extensions/tx/transcription/tx/ws?token="+master, nil); err == nil {
		c.CloseNow()
		t.Fatal("a job socket through the apps' forward")
	} else if res == nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("%v %v", res, err)
	}
}

// The built-in voice extension contributes gpt-live to transcription beside its voice, and stays of kind voice.
func TestVoiceManifestTranscribes(t *testing.T) {
	voice := needsVoiceModule(t)
	h := newTestManager(t)
	h.version = "1.2.3"
	for _, id := range []string{"voice", "talk"} {
		raw, _ := json.Marshal(voiceManifest(h, voice, id, ""))
		m, err := extension.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if m.Kind() != "voice" || !slices.Equal(m.Items("voice"), []string{id}) || !slices.Equal(m.Items("transcription"), []string{voice.TranscriptionContribution}) {
			t.Fatalf("%s: %v", id, m.Contributions())
		}
		if title := extension.ContributionTitle(m, "transcription", voice.TranscriptionContribution); title != "GPT-Live" {
			t.Fatalf("title %q", title)
		}
	}
}

// A meeting's audio socket (assistantwire.SocketRoutes), relayed here as the extensions' sockets are, is the
// assistant's to answer, with who is calling in the hub's headers. It does not count in Serving: it lasts as long
// as the meeting records and must not hold a redeploy. Without the assistant it is 404.
func TestAssistantSocketIsNotServing(t *testing.T) {
	h := newTestManager(t)
	ctx := context.Background()
	type seen struct {
		role, device, auth, query string
		serving                   int64
	}
	got := make(chan seen, 1)
	h.opts.AssistantSocket = func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.Header.Get(extwire.HeaderRole), r.Header.Get(extwire.HeaderDevice), r.Header.Get("Authorization"), r.URL.RawQuery, h.Serving()}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), typ, b) != nil {
				return
			}
		}
	}
	mux := h.routes()
	counted := make(chan int64, 1)
	h.apiMux.HandleFunc("GET /api/v1/serving-now", func(w http.ResponseWriter, r *http.Request) { counted <- h.Serving() })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")

	phone := deviceToken("phone", roleFull)
	c, _, err := websocket.Dial(ctx, ws+"/api/v1/recordings/r1/audio?at=1&token="+phone, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if s := <-got; s.role != roleFull || s.device != "phone" || s.auth != "" || s.query != "at=1" || s.serving != 0 {
		t.Fatalf("%+v", s)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte{0, 1}); err != nil {
		t.Fatal(err)
	}
	if typ, b, err := c.Read(ctx); err != nil || typ != websocket.MessageBinary || string(b) != "\x00\x01" {
		t.Fatalf("%v %q %v", typ, b, err)
	}
	if n := h.Serving(); n != 0 {
		t.Fatalf("an open audio socket counts: %d", n)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/serving-now", h.cfg.Token, ""); code != 200 || <-counted != 1 {
		t.Fatal("another request counts")
	}
	c.Close(websocket.StatusNormalClosure, "")

	h.opts.AssistantSocket = nil
	if code, _ := call(t, mux, "GET", "/api/v1/recordings/r1/audio", h.cfg.Token, ""); code != http.StatusNotFound {
		t.Fatalf("without the assistant: %d", code)
	}
	if !isAssistantSocket(httptest.NewRequest("GET", "/api/v1/recordings/x/audio", nil)) ||
		isAssistantSocket(httptest.NewRequest("PUT", "/api/v1/recordings/x/audio", nil)) ||
		isAssistantSocket(httptest.NewRequest("GET", "/api/v1/recordings/x", nil)) {
		t.Fatal("isAssistantSocket")
	}
}
