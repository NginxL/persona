package assistant

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Fake hosts and keys only: every address is under example.invalid, every key a placeholder.

// linkCase is a case of spec/fixtures/provider-import-link/cases.json's links (the web reads the same file): the
// format, the notes about the text, and per draft the fields compared, its notes only when the case lists them.
type linkCase struct {
	Name   string        `json:"name,omitempty"`
	Text   string        `json:"text,omitempty"`
	Format string        `json:"format"`
	Drafts []linkCaseOut `json:"drafts"`
	Notes  []pasteNote   `json:"notes"`
}

type linkCaseOut struct {
	Kind        string       `json:"kind"`
	API         string       `json:"api"`
	BaseURL     string       `json:"base_url"`
	DisplayName string       `json:"display_name"`
	Auth        string       `json:"auth"`
	APIKey      string       `json:"api_key"`
	Model       string       `json:"model"`
	Models      []string     `json:"models"`
	Notes       *[]pasteNote `json:"notes,omitempty"`
}

// linkCaseOf is res in want's shape.
func linkCaseOf(res pasteResult, want linkCase) linkCase {
	got := linkCase{Format: res.Format, Notes: res.Notes, Drafts: []linkCaseOut{}}
	for i, d := range res.Drafts {
		p := d.Provider
		out := linkCaseOut{Kind: p.Kind, API: p.API, BaseURL: p.BaseURL, DisplayName: p.DisplayName, Auth: p.Auth,
			APIKey: p.APIKey, Model: p.Model, Models: []string{}}
		for _, m := range p.Models {
			out.Models = append(out.Models, m.ID)
		}
		if i < len(want.Drafts) && want.Drafts[i].Notes != nil {
			notes := append([]pasteNote{}, d.Notes...)
			out.Notes = &notes
		}
		got.Drafts = append(got.Drafts, out)
	}
	return got
}

func TestParseLinkSharedCases(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "provider-import-link", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Links []linkCase `json:"links"`
	}
	if err := json.Unmarshal(b, &fixture); err != nil || len(fixture.Links) == 0 {
		t.Fatalf("cases: %v", err)
	}
	for _, c := range fixture.Links {
		t.Run(c.Name, func(t *testing.T) {
			want := linkCase{Format: c.Format, Drafts: c.Drafts, Notes: c.Notes}
			got := linkCaseOf(parsePaste(c.Text), want)
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			if string(gj) != string(wj) {
				t.Fatalf("got  %s\nwant %s", gj, wj)
			}
		})
	}
}

// The smallest custom provider a link takes, and its one endpoint.
const (
	linkTestEndpoint = `{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1"}`
	linkTestPayload  = `{"kind":"custom","name":"Example","endpoints":[` + linkTestEndpoint + `]}`
)

// webLink is the web's link to payload.
func webLink(payload string) string {
	return "https://persona.example.invalid/settings?tab=models#import=" + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// linkJSONOf is a valid custom payload of exactly n bytes (JSON blanks fill it up).
func linkJSONOf(t *testing.T, n int) string {
	t.Helper()
	const p = linkTestPayload
	if n < len(p) {
		t.Fatalf("%d bytes is shorter than the payload", n)
	}
	return p[:len(p)-1] + strings.Repeat(" ", n-len(p)) + "}"
}

func linkNotes(res pasteResult) string {
	b, _ := json.Marshal(res.Notes)
	return string(b)
}

// The limits: a cindy link of linkMost bytes after cindy:// (the most JSON it can carry) is read and one byte more is
// too long, as is a web link's fragment; the decoded JSON's own limit is checked on its own, since a link within
// linkMost never decodes to more than linkJSONMost.
func TestParseLinkSizes(t *testing.T) {
	const cindyHead = "provider/import?v=1&data="
	most := linkMost - len(cindyHead) // 32 743 characters, 24 557 bytes
	payload := linkJSONOf(t, most*3/4)
	data := base64.RawURLEncoding.EncodeToString([]byte(payload))
	if len(data) != most {
		t.Fatalf("data of %d characters", len(data))
	}
	if res := parsePaste("cindy://" + cindyHead + data); res.Format != "link" || len(res.Drafts) != 1 {
		t.Fatalf("a link of %d bytes: %+v", linkMost, res)
	}
	tooLong := `[{"code":"link_too_long"}]`
	if res := parsePaste("cindy://" + cindyHead + data + "A"); linkNotes(res) != tooLong || len(res.Drafts) != 0 {
		t.Fatalf("a link of %d bytes: %+v", linkMost+1, res)
	}
	frag := "import=" + strings.Repeat("A", linkMost+1-len("import="))
	if res := parsePaste("https://persona.example.invalid/settings#" + frag); linkNotes(res) != tooLong {
		t.Fatalf("a fragment of %d bytes: %+v", len(frag), res)
	}
	if res := parsePaste("https://persona.example.invalid/settings#" + frag[:linkMost]); linkNotes(res) != `[{"code":"link_invalid"}]` {
		t.Fatalf("a fragment of %d bytes of no JSON: %+v", linkMost, res)
	}
	for n, want := range map[int]string{linkJSONMost: "", linkJSONMost + 1: noteLinkTooLong} {
		if _, _, note := linkPayload(base64.RawURLEncoding.EncodeToString([]byte(linkJSONOf(t, n)))); note.Code != want {
			t.Fatalf("%d bytes of JSON: %+v", n, note)
		}
	}
}

// A refused link is used for nothing, and its one note names no value: not the key, not an address, not a field
// name that is no plain word, not a version that is no number.
func TestParseLinkNamesNoValue(t *testing.T) {
	const s = "sk-example-hidden"
	const ep = linkTestEndpoint
	custom := func(fields string) string { return webLink(`{"kind":"custom","name":"Example",` + fields + `}`) }
	links := []string{
		custom(`"auth":{"method":"apiKey","apiKey":"` + s + `\nX"},"endpoints":[` + ep + `]`),
		custom(`"auth":{"method":"apiKey","apiKey":"` + s + `","` + s + `":1},"endpoints":[` + ep + `]`),
		custom(`"endpoints":[` + ep + `],"` + s + ` x":"` + s + `"`),
		custom(`"endpoints":[` + ep + `],"extra":"` + s + `"`),
		custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1?key=` + s + `"}]`),
		custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://` + s + `@example.invalid/v1"}]`),
		custom(`"endpoints":[{"protocol":"` + s + `","baseUrl":"https://example.invalid/v1"}]`),
		custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1","apiKey":"` + s + `\u0000"}]`),
		custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1","headers":{"X-Key":"` + s + `\r\nX: y"}}]`),
		custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1","models":[{"id":"m","` + s + `":1}]}]`),
		custom(`"auth":{"method":"none"},"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/` + s + `"}]`),
		custom(`"auth":{"method":"` + s + `"},"endpoints":[` + ep + `]`),
		webLink(`{"kind":"` + s + `","apiKey":"` + s + `"}`),
		webLink(`{"kind":"builtin","provider":"` + s + `","apiKey":"` + s + `"}`),
		custom(`"endpoints":[`+ep+`]`) + "&v=" + s,
		custom(`"endpoints":[`+ep+`]`) + "&" + s + "=1",
		"cindy://provider/import?v=1&data=" + s,
		"cindy://provider/import?v=1&data=" + base64.RawURLEncoding.EncodeToString([]byte(`["`+s+`"]`)),
	}
	for i, link := range links {
		res := parsePaste(link)
		b, _ := json.Marshal(res)
		if res.Format != "link" || len(res.Drafts) != 0 || len(res.Notes) != 1 || strings.Contains(string(b), s) ||
			strings.Contains(string(b), "example.invalid") || strings.Contains(string(b), "://") {
			t.Errorf("link %d: %s", i, b)
		}
	}
}

// The rules the shared cases leave out.
func TestParseLinkRules(t *testing.T) {
	ep := func(more string) string {
		return `{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1"` + more + `}`
	}
	custom := func(fields string) string { return `{"kind":"custom","name":"Example",` + fields + `}` }
	withEndpoint := func(more string) string { return custom(`"endpoints":[` + ep(more) + `]`) }
	named := func(name string) string {
		return `{"kind":"custom","name":"` + name + `","endpoints":[` + ep("") + `]}`
	}
	var hs []string
	for i := range 25 {
		hs = append(hs, fmt.Sprintf(`"X-H%d":"v"`, i))
	}
	headers := strings.Join(hs, ",")
	ok := `[]`
	invalid := func(name string) string { return `[{"code":"link_invalid","name":"` + name + `"}]` }
	cases := []struct{ name, payload, notes string }{
		{"a built-in key slot", `{"kind":"builtin","provider":"gemini","apiKey":"FAKE-KEY"}`, `[{"code":"cindy_kind","name":"builtin"}]`},
		{"a built-in slot cindy does not have", `{"kind":"builtin","provider":"other","apiKey":"FAKE-KEY"}`, invalid("provider")},
		{"a built-in key over 1 KiB", `{"kind":"builtin","provider":"gemini","apiKey":"` + strings.Repeat("k", 1025) + `"}`, invalid("apiKey")},
		{"a preset without a key", `{"kind":"preset","preset":"example"}`, invalid("apiKey")},
		{"a preset of another kind's field", `{"kind":"preset","preset":"example","apiKey":"FAKE-KEY","name":"x"}`, `[{"code":"link_field","name":"name"}]`},
		{"no kind", `{"name":"Example","endpoints":[` + ep("") + `]}`, invalid("kind")},
		{"a blank name", `{"kind":"custom","name":"  ","endpoints":[` + ep("") + `]}`, invalid("name")},
		{"60 characters of name, as JavaScript counts them", named(strings.Repeat("😀", 30)), ok},
		{"61 of them", named(strings.Repeat("😀", 30) + "x"), invalid("name")},
		{"an id", custom(`"id":"example-1","endpoints":[` + ep("") + `]`), ok},
		{"an id that is no slug", custom(`"id":"Example","endpoints":[` + ep("") + `]`), invalid("id")},
		{"auth that is no object", custom(`"auth":"apiKey","endpoints":[` + ep("") + `]`), invalid("auth")},
		{"another sign-in method", custom(`"auth":{"method":"password"},"endpoints":[` + ep("") + `]`), invalid("auth.method")},
		{"a key with no-key sign-in", custom(`"auth":{"method":"none","apiKey":"FAKE-KEY"},"endpoints":[` + ep("") + `]`), `[{"code":"link_field","name":"auth.apiKey"}]`},
		{"another OAuth flow", custom(`"auth":{"method":"oauth","flow":"implicit"},"endpoints":[` + ep("") + `]`), invalid("auth.flow")},
		{"a device-code field in the code flow", custom(`"auth":{"method":"oauth","deviceAuthorizationUrl":"https://example.invalid/d"},"endpoints":[` + ep("") + `]`),
			`[{"code":"link_field","name":"auth.deviceAuthorizationUrl"}]`},
		{"OAuth for pi", custom(`"auth":{"method":"oauth"},"endpoints":[` + ep(`,"targets":["pi"]`) + `]`), invalid("endpoints[0].targets")},
		{"a key on an OAuth endpoint", custom(`"auth":{"method":"oauth"},"endpoints":[` + ep(`,"apiKey":"FAKE-KEY","headers":{"X-A":"b"}`) + `]`), invalid("endpoints[0].apiKey")},
		{"an endpoint that is no object", custom(`"endpoints":["https://example.invalid/v1"]`), invalid("endpoints[0]")},
		{"targets twice", withEndpoint(`,"targets":["codex","codex"]`), invalid("endpoints[0].targets")},
		{"no targets", withEndpoint(`,"targets":[]`), invalid("endpoints[0].targets")},
		{"null models", withEndpoint(`,"models":null`), `[]`},
		{"257 models", withEndpoint(`,"models":["m` + strings.Repeat(`","m`, 256) + `"]`), invalid("endpoints[0].models")},
		{"a blank model", withEndpoint(`,"models":[" "]`), invalid("endpoints[0].models[0]")},
		{"a model without an id", withEndpoint(`,"models":[{"name":"M"}]`), invalid("endpoints[0].models[0].id")},
		{"a context window that is no whole number", withEndpoint(`,"models":[{"id":"m","contextWindow":1.5}]`), invalid("endpoints[0].models[0].contextWindow")},
		{"a reasoning flag that is no boolean", withEndpoint(`,"models":[{"id":"m","reasoning":"yes"}]`), invalid("endpoints[0].models[0].reasoning")},
		{"a default effort it does not list", withEndpoint(`,"models":[{"id":"m","reasoning":true,"reasoningEfforts":["low"],"reasoningDefaultEffort":"high"}]`),
			invalid("endpoints[0].models[0].reasoningDefaultEffort")},
		{"an effort cindy does not know", withEndpoint(`,"models":[{"id":"m","reasoning":true,"reasoningEfforts":["ultra"]}]`),
			invalid("endpoints[0].models[0].reasoningEfforts")},
		{"a default effort without reasoning", withEndpoint(`,"models":[{"id":"m","reasoningDefaultEffort":"low"}]`),
			invalid("endpoints[0].models[0].reasoningDefaultEffort")},
		{"metadata Persona does not read", withEndpoint(`,"models":[{"id":"m","description":{"any":["thing"]},"defaultEnabled":false}]`), ok},
		{"a request path", withEndpoint(`,"requestPath":"/v1/chat/completions"`), ok},
		{"a request path with a query", withEndpoint(`,"requestPath":"/v1?x=1"`), invalid("endpoints[0].requestPath")},
		{"a models URL on the same origin, its default port written", withEndpoint(`,"modelsUrl":"https://EXAMPLE.invalid:443/v1/models"`), ok},
		{"a models URL on another port", withEndpoint(`,"modelsUrl":"https://example.invalid:8443/v1/models"`), invalid("endpoints[0].modelsUrl")},
		{"a base URL without a host", custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https:///v1"}]`), invalid("endpoints[0].baseUrl")},
		{"a base URL ending in ?", custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1?"}]`), invalid("endpoints[0].baseUrl")},
		{"a port past 65535", custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid:65536/v1"}]`), invalid("endpoints[0].baseUrl")},
		{"the last port", custom(`"endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid:65535/v1"}]`), ok},
		{"a models URL on a port past 65535", withEndpoint(`,"modelsUrl":"https://example.invalid:99999999999999999999/v1/models"`),
			invalid("endpoints[0].modelsUrl")},
		{"a header name that is no token", withEndpoint(`,"headers":{"X Tenant":"a"}`), invalid("endpoints[0].headers")},
		{"no headers", withEndpoint(`,"headers":{}`), invalid("endpoints[0].headers")},
		{"25 headers", withEndpoint(`,"headers":{` + headers + `}`), invalid("endpoints[0].headers")},
		{"no sign-in on localhost and ::1", custom(`"auth":{"method":"none"},"endpoints":[{"protocol":"anthropic-messages","baseUrl":"http://localhost:4000",
			"targets":["claude-code"]},{"protocol":"openai-chat","baseUrl":"http://[::1]:4000/v1","modelsUrl":"http://[::1]:4000/v1/models"}]`),
			`[]`},
		{"no sign-in with models on another host", custom(`"auth":{"method":"none"},"endpoints":[{"protocol":"openai-chat","baseUrl":"http://127.0.0.1/v1",
			"modelsUrl":"http://127.0.0.2/v1/models"}]`), invalid("endpoints[0].modelsUrl")},
		{"Messages and Google tie for codex", custom(`"endpoints":[{"protocol":"anthropic-messages","baseUrl":"https://a.example.invalid"},
			{"protocol":"google-generative-ai","baseUrl":"https://b.example.invalid"}]`), invalid("endpoints")},
		{"Messages and Chat: each agent has its best", custom(`"endpoints":[{"protocol":"anthropic-messages","baseUrl":"https://a.example.invalid"},
			{"protocol":"openai-chat","baseUrl":"https://b.example.invalid/v1"}]`), `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := parsePaste(webLink(c.payload))
			if res.Format != "link" || linkNotes(res) != c.notes {
				b, _ := json.Marshal(res)
				t.Fatalf("got %s, want notes %s", b, c.notes)
			}
		})
	}
}

// A link without a key is the usual one and says nothing of it; a key the draft can't take (a blank inside) is noted.
func TestParseLinkKeyNotes(t *testing.T) {
	for payload, want := range map[string]string{
		linkTestPayload: `null`,
		`{"kind":"custom","name":"Example","auth":{"method":"apiKey","apiKey":"FAKE KEY"},"endpoints":[` + linkTestEndpoint + `]}`: `[{"code":"no_key"}]`,
	} {
		res := parsePaste(webLink(payload))
		if len(res.Drafts) != 1 || res.Drafts[0].Provider.APIKey != "" {
			t.Fatalf("%s: %+v", payload, res)
		}
		if b, _ := json.Marshal(res.Drafts[0].Notes); string(b) != want {
			t.Fatalf("%s: notes %s, want %s", payload, b, want)
		}
	}
}

// What is a link: either scheme in any case; text that starts with no link, or an address with another fragment, is
// left to the other readers. A broken %-escape breaks the link, as does a blank or a line break after its first word
// (a chat app wrapping it): nothing of it is read.
func TestParseLinkRecognized(t *testing.T) {
	data := base64.RawURLEncoding.EncodeToString([]byte(linkTestPayload))
	broken := []string{
		"https://persona.example.invalid/#import=" + data + "%zz",
		"cindy://provider/import?v=1&data=" + data[:40] + "\n" + data[40:],
		"https://persona.example.invalid/settings?tab=models#import=" + data[:40] + " " + data[40:],
		"cindy://provider/import?v=1&data=" + data + " and more",
		"cindy://provider/import?v=1&data=" + data + "\t" + data,
	}
	for text, link := range map[string]bool{
		"CINDY://provider/import?v=1&data=" + data:                     true,
		"HTTPS://persona.example.invalid/settings#import=" + data:      true,
		"http://persona.example.invalid/#v=1&import=" + data:           true,
		"https://persona.example.invalid/#imp%6Frt=" + data:            true,
		"https://persona.example.invalid/#settings-providers&x=import": false,
		"https://persona.example.invalid/?import=" + data:              false,
		"Here it is: cindy://provider/import?v=1&data=" + data:         false,
		"https://persona.example.invalid/settings and #import=" + data: false,
	} {
		if res := parsePaste(text); (res.Format == "link") != link {
			t.Errorf("%.60s…: format %q", text, res.Format)
		}
	}
	for _, text := range broken {
		if res := parsePaste(text); res.Format != "link" || len(res.Drafts) != 0 || linkNotes(res) != `[{"code":"link_invalid"}]` {
			t.Errorf("%.60s…: %s %s", text, res.Format, linkNotes(res))
		}
	}
}

// persona://provider/import is the apps' own scheme for the same link (#5428): the same payload gives the same draft
// as cindy://, in any case, and a refused persona link carries the same note as cindy's.
func TestParseLinkPersonaScheme(t *testing.T) {
	data := base64.RawURLEncoding.EncodeToString([]byte(linkTestPayload))
	cindy := parsePaste("cindy://provider/import?v=1&data=" + data)
	if len(cindy.Drafts) == 0 {
		t.Fatalf("the cindy link gives no draft: %+v", cindy)
	}
	cj, _ := json.Marshal(cindy.Drafts)
	for _, text := range []string{
		"persona://provider/import?v=1&data=" + data,
		"PERSONA://provider/import?v=1&data=" + data,
	} {
		res := parsePaste(text)
		pj, _ := json.Marshal(res.Drafts)
		if res.Format != "link" || string(pj) != string(cj) {
			t.Errorf("%.45s…: format %q, drafts %s, want the cindy link's %s", text, res.Format, pj, cj)
		}
	}
	if res := parsePaste("persona://provider/import?v=2&data=" + data); linkNotes(res) != `[{"code":"link_version","name":"2"}]` {
		t.Errorf("a bad persona version: %s", linkNotes(res))
	}
}

// An endpoint's own request path is noted on the draft, never named: Persona calls the API's usual one.
func TestParseCindyRequestPath(t *testing.T) {
	payload := `{"kind":"custom","name":"Example","auth":{"method":"apiKey","apiKey":"FAKE-KEY"},"endpoints":[` +
		`{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1","requestPath":"/secret-path/chat"}]}`
	for _, text := range []string{payload, webLink(payload)} {
		res := parsePaste(text)
		b, _ := json.Marshal(res)
		if len(res.Drafts) != 1 || !slices.Contains(res.Drafts[0].Notes, pasteNote{Code: noteCindyPath}) || strings.Contains(string(b), "secret-path") {
			t.Fatalf("%s", b)
		}
	}
	if res := parsePaste(strings.Replace(payload, "/secret-path/chat", " ", 1)); len(res.Drafts) != 1 || res.Drafts[0].Notes != nil {
		t.Fatalf("a blank request path is none: %+v", res.Drafts)
	}
}
