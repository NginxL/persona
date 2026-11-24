package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestForeignScript(t *testing.T) {
	cases := []struct {
		text, target string
		want         bool
	}{
		{"Can we move the design review to Tuesday afternoon? I'm travelling.", "zh-Hans", true},
		{"周四我临时要出差，分享能不能挪到周二下午？", "zh-Hans", false},
		{"关于 fetchAll 和 loadAll 的 naming，我觉得 PR #42 可以合了", "zh-Hans", false}, // Chinese with Latin words
		{"OK", "zh-Hans", false}, // too little to tell
		{"来週の会議はキャンセルになりました。よろしくお願いします。", "zh-Hans", true},
		{"来週の会議はキャンセルになりました。よろしくお願いします。", "ja", false},
		{"下周的会议取消了，请大家相互转告一下这个消息", "ja", true},
		{"Bonjour, la réunion de mardi est annulée pour tout le monde.", "en", false}, // same script
		{"下周的会议取消了，请大家相互转告一下这个消息", "en", true},
	}
	for _, c := range cases {
		if got := foreignScript(c.text, c.target); got != c.want {
			t.Errorf("%q → %s: %v, want %v", c.text, c.target, got, c.want)
		}
	}
}

// translateHub is an inbox whose hub wants mail translated into Chinese (a device asked for it), with mail 7 to read
// and a translation model answering what it is given; translated counts the model's calls.
func translateHub(t *testing.T) (h *inboxHub, en, zh *InboxItem, translated func() int) {
	t.Helper()
	h = newInboxHub(t)
	h.inboxReady(context.Background())
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Translate = []string{"zh-Hans"} })
	model, calls := captureModel(t, `{"subject":"设计评审能改期吗？","text":"你好，周四我要出差。"}`)
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = model
	h.in.mail = &fakeMail{bodies: map[uint32]assistantwire.InboxMailMessage{7: {Subject: "Can we move the design review?",
		Text: "Hi, I'm travelling on Thursday, so I won't make the review."}}}
	h.in.mu.Unlock()
	en = &InboxItem{ID: "mail:a:t:7", Source: "mail", Kind: KindMail, Title: "Can we move the design review?",
		Preview: "Hi, I'm travelling on Thursday, so I won't make the review.", Last: "7", BodyOf: "7", UpdatedAt: time.Now(),
		Mail: &InboxMail{AccountID: "a", Mailbox: "INBOX", UIDs: []uint32{7}}}
	zh = &InboxItem{ID: "mail:a:u:8", Source: "mail", Kind: KindMail, Title: "下周的分享能不能提前到周二？",
		Preview: "周四我临时要出差，分享能不能挪到周二下午？", Last: "8", BodyOf: "8", UpdatedAt: time.Now(),
		Mail: &InboxMail{AccountID: "a", Mailbox: "INBOX", UIDs: []uint32{8}}}
	h.inbox.mu.Lock()
	h.inbox.items[en.ID], h.inbox.items[zh.ID] = en, zh
	h.inbox.mu.Unlock()
	return h, en, zh, func() int { return len(calls()) }
}

// passOver waits for the translation pass to end.
func (h *inboxHub) passOver() {
	h.inbox.translating.Lock()
	h.inbox.translating.Unlock()
}

// translateModelAs gives the translation model another model name (on the same server): another fingerprint.
func (h *inboxHub) translateModelAs(t *testing.T, name string) string {
	t.Helper()
	h.in.mu.Lock()
	c := h.in.models[purposeTranslate].Config()
	c.Model = name
	h.in.models[purposeTranslate] = ai.New(c)
	h.in.mu.Unlock()
	fp, err := h.translateFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestNewMailIsTranslatedBeforeItIsOpened(t *testing.T) {
	h, en, zh, translated := translateHub(t)
	ctx := context.Background()
	fp1, _ := h.translateFingerprint(ctx)
	h.translateInbox(ctx)
	h.passOver()
	tr := en.Translations["zh-Hans"]
	if tr == nil || tr.Subject != "设计评审能改期吗？" || tr.Of != "7" || tr.ModelFingerprint != fp1 {
		t.Fatalf("not translated: %+v", tr)
	}
	if zh.Translations != nil || translated() != 1 {
		t.Fatalf("Chinese mail translated into Chinese (%d asked)", translated())
	}
	// The list carries subject and preview, not the body.
	h.inbox.mu.Lock()
	listed := en.listed()
	h.inbox.mu.Unlock()
	if l := listed.Translations["zh-Hans"]; l == nil || l.Subject == "" || l.Text != "" {
		t.Fatalf("listed: %+v", l)
	}
	raw, _ := json.Marshal(listed)
	if !json.Valid(raw) {
		t.Fatal("bad json")
	}
	// Opening the mail (POST /api/v1/translate/mail) finds the stored translation, by the same model only.
	h.inbox.translations = translateCache{} // not from the cache: from the item
	ref := assistantwire.InboxMailRef{AccountID: "a", UID: 7, Mailbox: "INBOX"}
	if got, err := h.translateMail(ctx, ref, "zh-Hans"); err != nil || got.Subject != "设计评审能改期吗？" || got.ModelFingerprint != fp1 || translated() != 1 {
		t.Fatalf("opened: %+v %v (%d asked)", got, err, translated())
	}
	fp2 := h.translateModelAs(t, "m2")
	if got, err := h.translateMail(ctx, ref, "zh-Hans"); err != nil || got.ModelFingerprint != fp2 || translated() != 2 {
		t.Fatalf("another model's translation was taken: %+v %v", got, err)
	}
	// Translated already: the next pass asks nothing; another model: translated again.
	h.translateInbox(ctx)
	h.passOver()
	if translated() != 2 {
		t.Fatalf("translated again: %d", translated())
	}
	h.translateModelAs(t, "m3")
	h.translateInbox(ctx)
	h.passOver()
	if translated() != 3 {
		t.Fatalf("not translated for the new model: %d", translated())
	}
	// No device wants it: nothing to do.
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Translate = nil })
	en.Translations = nil
	h.translateInbox(ctx)
	h.passOver()
	if translated() != 3 {
		t.Fatal("translated with no target")
	}
}

// Short texts keep their order and are cached by the model that translated them: a changed model is asked again.
func TestTranslateTextsKeepsOrderAndCaches(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	// One server answering reply; the model's name is the fingerprint's part a test changes ("": no model).
	var reply atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply.Load()}}})
	}))
	t.Cleanup(srv.Close)
	set := func(answer, name string) {
		reply.Store(answer)
		h.in.mu.Lock()
		h.in.models[purposeTranslate] = nil
		if name != "" {
			h.in.models[purposeTranslate] = ai.New(ai.Config{BaseURL: srv.URL, APIKey: "k", Model: name})
		}
		h.in.mu.Unlock()
	}
	set(`{"texts":["你好","明天见"]}`, "fp1")
	out, err := h.translateTexts(ctx, []string{"Hello", "", "See you tomorrow"}, "zh-Hans")
	if err != nil || strings.Join(out, "|") != "你好||明天见" {
		t.Fatalf("%q %v", out, err)
	}
	// Cached now: a model that would answer wrongly is not asked.
	set(`{"texts":["错"]}`, "fp1")
	if out, err := h.translateTexts(ctx, []string{"See you tomorrow"}, "zh-Hans"); err != nil || out[0] != "明天见" {
		t.Fatalf("%q %v", out, err)
	}
	// Another model: asked again.
	set(`{"texts":["新模型"]}`, "fp2")
	if out, err := h.translateTexts(ctx, []string{"See you tomorrow"}, "zh-Hans"); err != nil || out[0] != "新模型" {
		t.Fatalf("stale cache: %q %v", out, err)
	}
	if _, err := h.translateTexts(ctx, []string{"x"}, "klingon"); err == nil {
		t.Fatal("unknown language accepted")
	}
	// A count that does not line up is an error, not a shifted list.
	set(`{"texts":["一"]}`, "fp2")
	if _, err := h.translateTexts(ctx, []string{"one", "two"}, "zh-Hans"); err == nil {
		t.Fatal("misaligned answer accepted")
	}
	// No translation model: an error, nothing asked.
	set(`{"texts":["一"]}`, "")
	if _, err := h.translateTexts(ctx, []string{"three"}, "zh-Hans"); err == nil {
		t.Fatal("translated without a model")
	}
}

// The translation's routes are the manager's (relayed by the hub): texts in batches, a mail by reference.
func TestTranslateRoutes(t *testing.T) {
	h, _, _, _ := translateHub(t)
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/translate", `{"texts":[],"target":"zh-Hans"}`); code != http.StatusBadRequest {
		t.Fatalf("no texts: %d", code)
	}
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = fakeModel(t, `{"texts":["你好"]}`)
	h.in.mu.Unlock()
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/translate", `{"texts":["Hello"],"target":"zh-Hans"}`); code != http.StatusOK ||
		fmt.Sprint(out["texts"]) != "[你好]" {
		t.Fatalf("texts: %d %v", code, out)
	}
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/translate/mail", `{"uid":7,"target":"zh-Hans"}`); code != http.StatusBadRequest {
		t.Fatalf("a mail without its account: %d", code)
	}
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = fakeModel(t, `{"subject":"设计评审能改期吗？","text":"你好"}`)
	h.in.mu.Unlock()
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/translate/mail", `{"account_id":"a","uid":7,"target":"zh-Hans"}`); code != http.StatusOK ||
		out["subject"] != "设计评审能改期吗？" || out["model_fingerprint"] != mustFingerprint(t, h) {
		t.Fatalf("a mail: %d %v", code, out)
	}
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/translate/mail", `{"account_id":"a","uid":99,"target":"zh-Hans"}`); code != http.StatusBadGateway {
		t.Fatalf("a mail the hub can't read: %d", code)
	}
	// The hub reads the mail as readable text (an HTML mail's paragraphs and links kept).
	h.in.mu.Lock()
	asked := slices.Clone(h.in.asked)
	h.in.mu.Unlock()
	readable := false
	for _, p := range asked {
		var ref assistantwire.InboxMailRef
		if p.Method == assistantwire.InboxMailBody && json.Unmarshal(p.Params, &ref) == nil && ref.UID == 7 {
			readable = ref.Readable
		}
	}
	if !readable {
		t.Fatal("the mail was not read as readable text")
	}
}

func TestGoldenTranslateTexts(t *testing.T) {
	h := newInboxHub(t)
	model, calls := captureModel(t, `{"texts":["你好","明天见"]}`)
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = model
	h.in.mu.Unlock()
	if _, err := h.translateTexts(context.Background(), []string{"Hello", "", "See you tomorrow"}, "zh-Hans"); err != nil {
		t.Fatal(err)
	}
	goldenCall(t, "translate_texts", onlyCall(t, calls()))
}

func TestGoldenTranslateMail(t *testing.T) {
	h := newInboxHub(t)
	model, calls := captureModel(t, `{"subject":"季度汇报","text":"请在周五前提交。"}`)
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = model
	h.in.mail = &fakeMail{bodies: map[uint32]assistantwire.InboxMailMessage{7: {Subject: "Quarterly report", Text: "  Please submit it by Friday.  "}}}
	h.in.mu.Unlock()
	if _, err := h.translateMail(context.Background(), assistantwire.InboxMailRef{AccountID: "acc1", UID: 7, Mailbox: "INBOX"}, "zh-Hans"); err != nil {
		t.Fatal(err)
	}
	goldenCall(t, "translate_mail", onlyCall(t, calls()))
}

func TestTranslationPassOutlivesTheRefreshRequest(t *testing.T) {
	h, en, _, _ := translateHub(t)
	// The refresh request has already answered by the time the pass runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.translateInbox(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.inbox.mu.Lock()
		done := en.Translations["zh-Hans"] != nil
		h.inbox.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.passOver()
	if tr := en.Translations["zh-Hans"]; tr == nil || tr.Subject != "设计评审能改期吗？" {
		t.Fatalf("pass stopped with the request: %+v", tr)
	}
}

// mustFingerprint is the translation model's fingerprint now.
func mustFingerprint(t *testing.T, h *inboxHub) string {
	t.Helper()
	fp, err := h.translateFingerprint(context.Background())
	if err != nil || fp == "" {
		t.Fatalf("fingerprint %q: %v", fp, err)
	}
	return fp
}

// failingMailHub is the test hub, except that reading a mail fails with err.
type failingMailHub struct {
	Hub
	err error
}

func (h *failingMailHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID == AppInbox && p.Method == assistantwire.InboxMailBody {
		return nil, h.err
	}
	return h.Hub.AppCall(ctx, p)
}

// An unknown account keeps its code and status, wrapped or not, and is named in the request's language without
// asking the model; any other failure to read the mail stays 502 internal.
func TestTranslateMailUnknownAccount(t *testing.T) {
	h, _, _, translated := translateHub(t)
	bridge := &failingMailHub{Hub: h.Service.hub}
	setHub(h.Service, bridge)
	const req = `{"account_id":"none","uid":1,"target":"zh-Hans"}`
	unknown := unknownMailAccount("none") // the mail module's error for an account no longer connected
	for _, err := range []error{unknown, fmt.Errorf("read mail: %w", unknown)} {
		bridge.err = err
		status, body := serveJSON(t, h.Service, "POST", "/api/v1/translate/mail", "ja", req)
		if status != http.StatusNotFound || body["code"] != "mail_account_unknown" || body["error"] != unknown.Text(i18n.Ja) || body["detail"] != nil {
			t.Fatalf("%v: %d %v; want 404 mail_account_unknown in Japanese", err, status, body)
		}
	}
	if translated() != 0 {
		t.Fatalf("unknown account called the translation model %d times", translated())
	}
	bridge.err = errors.New("mail backend unavailable")
	status, body := serveJSON(t, h.Service, "POST", "/api/v1/translate/mail", "en", req)
	if status != http.StatusBadGateway || body["code"] != "internal" || body["error"] != i18n.T(i18n.En, "rpc-errors.failed") || body["detail"] != bridge.err.Error() {
		t.Fatalf("other failure: %d %v", status, body)
	}
}

func TestGoldenTranslationLanguage(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans", "ja"} {
		t.Run(locale, func(t *testing.T) {
			h := newInboxHub(t)
			h.f.hello.Locale = locale
			model, calls := captureModel(t, `{"texts":["Bonjour"],"subject":"Avis","text":"Le 5 juin."}`)
			h.in.mu.Lock()
			h.in.models[purposeTranslate] = model
			h.in.mail = &fakeMail{bodies: map[uint32]assistantwire.InboxMailMessage{7: {Subject: "Notice <X>", Text: "June 5."}}}
			h.in.mu.Unlock()
			ctx := context.Background()
			texts, err := h.translateTexts(ctx, []string{"Hello <X>"}, "fr")
			if err != nil || len(texts) != 1 || texts[0] != "Bonjour" {
				t.Fatalf("batch: %q, %v", texts, err)
			}
			ref := assistantwire.InboxMailRef{AccountID: "acc1", UID: 7, Mailbox: "INBOX"}
			mail, err := h.translateMail(ctx, ref, "fr")
			if err != nil || mail.Subject != "Avis" || mail.Text != "Le 5 juin." {
				t.Fatalf("mail: %+v, %v", mail, err)
			}
			got := calls()
			if len(got) != 2 {
				t.Fatalf("model calls: %d", len(got))
			}
			goldenCall(t, "translate_texts."+locale, got[0])
			goldenCall(t, "translate_mail."+locale, got[1])
		})
	}
}

// unknownMailAccount stands for the mail module's UnknownAccountError: the core reaches it only through its code,
// status and localized text.
type unknownMailAccount string

func (u unknownMailAccount) Error() string { return u.Text(i18n.En) }
func (u unknownMailAccount) Text(l i18n.Lang) string {
	return i18n.T(l, "mail-errors.account_unknown", i18n.Args{"account": string(u)})
}
func (unknownMailAccount) Code() string    { return "mail_account_unknown" }
func (unknownMailAccount) HTTPStatus() int { return http.StatusNotFound }
