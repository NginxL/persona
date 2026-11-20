package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// Reading a pasted provider configuration (#5193, docs/assistant/model-config.md): the provider wizard sends what the
// user pasted, the manager reads it as shell lines, a Claude Code settings.json, a curl command, a cindy provider
// import (models_import_formats.go) or a provider import link (models_import_link.go), and answers drafts of
// POST /api/v1/launchers bodies with what each leaves out and why, and the stored providers each would meet. Nothing
// is stored. The env is mapped as importing an alias maps it (newImportGroup, importEnv). The text holds keys: it is
// never logged, and the answer carries none of its secrets but the key itself (the user pasted it into the form it
// stays in).

// What one paste answers at most: drafts (too_many beyond), notes in all (the top-level ones and every draft's),
// variables and headers one draft names as left out; and what it reads at most: shell lines' claude launches and
// variables, cindy's providers past pasteMost drafts (too_many). A text of the body's 256 KiB stays a small answer.
const (
	pasteMost        = 50
	pasteNotesMost   = 200
	pasteSkippedMost = 100
	pasteLaunchMost  = 500
	pasteEnvMost     = 500
)

// The codes of a paste's notes (clients localize them): a note carries names (an alias, a variable, a header, an
// option, a protocol), never a value of the text.
const (
	noteUnrecognized  = "unrecognized"       // nothing in the text looks like a provider configuration
	noteInvalidJSON   = "invalid_json"       // it starts like JSON but does not parse
	noteLineUnused    = "line_unused"        // a shell line that is no comment, alias, export, assignment or claude command
	noteNotClaude     = "not_claude"         // an alias or command that does not start Claude Code
	noteNoGateway     = "no_gateway"         // Claude Code without ANTHROPIC_BASE_URL: its own sign-in, nothing to add
	noteURLShell      = "url_shell"          // the address is a shell expansion: no draft
	noteURLUser       = "url_user"           // the address carries a user name or password: no draft
	noteURLQuery      = "url_query"          // the address carries a query or fragment (it may hold a key): no draft
	noteKeyShell      = "key_shell"          // the key is a shell expansion: left out
	noteNoKey         = "no_key"             // no key found: it is typed
	noteKeyHelper     = "key_helper"         // settings.json's apiKeyHelper runs a command for the key
	noteCurlOption    = "curl_option"        // a curl option not used
	noteCurlPath      = "curl_path"          // a curl URL's path is no endpoint Persona knows
	noteCindyKind     = "cindy_kind"         // a cindy preset or builtin provider
	noteCindyAuth     = "cindy_auth"         // a cindy provider signing in with oauth (no draft) or none (no key)
	noteCindyEndpoint = "cindy_endpoint"     // a cindy endpoint not imported
	noteCindyPath     = "cindy_request_path" // a cindy endpoint's own request path: the draft calls the API's usual one
	noteArgsLeftOut   = "args_left_out"      // the arguments look like they hold a secret or a setting's text: none kept
	noteHeaderUnread  = "header_unreadable"  // a header whose name is no plain token: left out, unnamed
	noteTooMany       = "too_many"           // more than pasteMost drafts: the rest left out
	noteLinkInvalid   = "link_invalid"       // an import link damaged or breaking a v1 rule: nothing used (the field's path)
	noteLinkVersion   = "link_version"       // an import link of another format version (named when a number)
	noteLinkTooLong   = "link_too_long"      // an import link, or the JSON it carries, longer than cindy takes
	noteLinkField     = "link_field"         // an import link with a field v1 does not define: nothing used (its path)
)

// pasteNote is something said about the pasted text or one draft: Line is 1-based, of the text with CRLF made LF.
type pasteNote struct {
	Code string `json:"code"`
	Line int    `json:"line,omitempty"`
	Name string `json:"name,omitempty"`
}

// addNote adds n to notes once, up to pasteNotesMost.
func addNote(notes *[]pasteNote, n pasteNote) {
	if len(*notes) < pasteNotesMost && !slices.Contains(*notes, n) {
		*notes = append(*notes, n)
	}
}

// Why a header of a paste is not a provider's header.
const (
	headerSkipCredential = "credential" // its name looks like a secret's
	headerSkipProtocol   = "protocol"   // a request basic Claude Code or the manager sets itself
	headerSkipUnsafe     = "unsafe"     // its value has a control character, a backslash or a $: no header line holds it as it is
)

// skippedHeader is one header of a paste left out, and why.
type skippedHeader struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// pasteMatch is a stored provider on a draft's base URL; SameKey: it has the draft's key.
type pasteMatch struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	SameKey     bool   `json:"same_key"`
}

// pastedProvider is a draft as POST /api/v1/launchers takes it, empty fields left out. Name, the startup ID, only for
// a shell alias's; DisplayName only for a cindy provider's.
type pastedProvider struct {
	Kind                   string                `json:"kind,omitempty"`
	Agent                  string                `json:"agent,omitempty"`
	API                    string                `json:"api,omitempty"`
	Name                   string                `json:"name,omitempty"`
	DisplayName            string                `json:"display_name,omitempty"`
	BaseURL                string                `json:"base_url,omitempty"`
	Auth                   string                `json:"auth,omitempty"`
	APIKey                 string                `json:"api_key,omitempty"`
	Model                  string                `json:"model,omitempty"`
	Models                 []proto.ProviderModel `json:"models,omitempty"`
	OpusModel              string                `json:"opus_model,omitempty"`
	SonnetModel            string                `json:"sonnet_model,omitempty"`
	HaikuModel             string                `json:"haiku_model,omitempty"`
	FableModel             string                `json:"fable_model,omitempty"`
	SmallModel             string                `json:"small_model,omitempty"`
	SubagentModel          string                `json:"subagent_model,omitempty"`
	SubagentForce          bool                  `json:"subagent_force,omitempty"`
	CustomModel            string                `json:"custom_model,omitempty"`
	CustomModelName        string                `json:"custom_model_name,omitempty"`
	CustomModelDescription string                `json:"custom_model_description,omitempty"`
	Effort                 string                `json:"effort,omitempty"`
	CompactWindow          int                   `json:"compact_window,omitempty"`
	MaxContext             int                   `json:"max_context,omitempty"`
	TimeoutMS              int                   `json:"timeout_ms,omitempty"`
	Proxy                  string                `json:"proxy,omitempty"`
	NoProxy                string                `json:"no_proxy,omitempty"`
	Args                   []string              `json:"args,omitempty"`
	Headers                string                `json:"headers,omitempty"` // "Name: value" lines, credentials left out
	Env                    string                `json:"env,omitempty"`     // importEnv's lines
}

// pasteDraft is one provider a paste would add: what it came from (Label; Aliases for shell aliases), the provider,
// the variables and headers it brings and leaves out, the stored providers on its base URL, and what it says.
type pasteDraft struct {
	Label          string          `json:"label,omitempty"`
	Aliases        []string        `json:"aliases,omitempty"`
	Provider       pastedProvider  `json:"provider"`
	EnvNames       []string        `json:"env_names,omitempty"`
	SkippedEnv     []skippedEnv    `json:"skipped_env,omitempty"`
	SkippedHeaders []skippedHeader `json:"skipped_headers,omitempty"`
	Matches        []pasteMatch    `json:"matches,omitempty"`
	Notes          []pasteNote     `json:"notes,omitempty"`
}

// setKey puts the pasted key in the draft, or says why there is none: a shell expansion is no key, nor is a value
// with a blank or a control character in it (no key has one; it could run on into other text).
func (d *pasteDraft) setKey(key string) {
	switch {
	case shellExpands(key):
		addNote(&d.Notes, pasteNote{Code: noteKeyShell})
	case key == "" || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		addNote(&d.Notes, pasteNote{Code: noteNoKey})
	default:
		d.Provider.APIKey = key
	}
}

// pasteResult is the parse route's answer: what the text was read as ("" for nothing), the drafts, and the notes
// about the text as a whole in text order.
type pasteResult struct {
	Format string       `json:"format"`
	Drafts []pasteDraft `json:"drafts"`
	Notes  []pasteNote  `json:"notes"`
}

func (res *pasteResult) note(code string, line int, name string) {
	addNote(&res.Notes, pasteNote{Code: code, Line: line, Name: name})
}

// parsePaste reads text as what it looks like: an import link (models_import_link.go); JSON (starting with { or [),
// a cindy import or a settings.json; else a curl command when the first command line runs curl; else shell lines.
func parsePaste(text string) pasteResult {
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\uFEFF"), "\r\n", "\n")
	var res pasteResult
	t := strings.TrimSpace(text)
	if link, ok := parseLink(t); ok {
		res = link
	} else if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		res = parsePasteJSON([]byte(t))
	} else {
		lines := shellcmd.Lines(text)
		if start, args, curl := curlLine(lines); curl {
			res = parseCurl(lines, start, args)
		} else {
			res = parseShell(lines)
		}
	}
	if len(res.Drafts) == 0 && len(res.Notes) == 0 {
		res = pasteResult{Notes: []pasteNote{{Code: noteUnrecognized}}}
	}
	// Bounded: drafts past pasteMost (a reader stops at one past them, or says too_many itself), then notes in all.
	tooMany := pasteNote{Code: noteTooMany}
	cut := len(res.Drafts) > pasteMost || slices.Contains(res.Notes, tooMany)
	res.Drafts = res.Drafts[:min(len(res.Drafts), pasteMost)]
	res.Notes = slices.DeleteFunc(res.Notes, func(n pasteNote) bool { return n == tooMany })
	if cut {
		res.Notes = append(res.Notes[:min(len(res.Notes), pasteNotesMost-1)], tooMany)
	}
	res.Notes = res.Notes[:min(len(res.Notes), pasteNotesMost)]
	left := pasteNotesMost - len(res.Notes)
	for i := range res.Drafts {
		d := &res.Drafts[i]
		d.Notes = d.Notes[:min(len(d.Notes), left)]
		left -= len(d.Notes)
		if len(d.Notes) == 0 {
			d.Notes = nil
		}
	}
	if res.Drafts == nil {
		res.Drafts = []pasteDraft{}
	}
	if res.Notes == nil {
		res.Notes = []pasteNote{}
	}
	return res
}

// parsePasteJSON reads a JSON paste: an object with providers, kind or endpoints is a cindy import, one with env,
// apiKeyHelper or model a settings.json.
func parsePasteJSON(data []byte) pasteResult {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return pasteResult{Notes: []pasteNote{{Code: noteInvalidJSON}}}
	}
	obj, _ := v.(map[string]any)
	has := func(k string) bool { _, ok := obj[k]; return ok }
	_, providers := obj["providers"].([]any)
	switch {
	case providers || has("kind") || has("endpoints"):
		return parseCindy(data)
	case has("env") || has("apiKeyHelper") || has("model"):
		return parseSettings(obj)
	}
	return pasteResult{}
}

// pasteAPI is the API a stored provider is called with, as a draft names it: messages for any Messages base (a Claude
// Code or omp launcher, a direct Messages API), a direct API's own; none for a Codex or Antigravity launcher, which no
// paste makes (its models are what that agent is offered, not a direct API's).
func pasteAPI(l storedLauncher) string {
	switch {
	case l.MessagesBase():
		return "messages"
	case l.Kind == "api":
		return l.API
	}
	return ""
}

// matchPaste fills each draft's matches: every stored provider calling the draft's API at its base URL, the ones with
// its key first. Only those providers' keys are opened (open), and only for a draft with a key; they are compared here
// and never leave the manager. A key that is a reference never equals a pasted one.
func matchPaste(drafts []pasteDraft, stored []storedLauncher, open func(sealed string) (string, error)) error {
	for i := range drafts {
		d := &drafts[i]
		var same, other []pasteMatch
		for _, l := range stored {
			if l.LaunchBaseURL() != d.Provider.BaseURL || pasteAPI(l) != d.Provider.API {
				continue
			}
			m := pasteMatch{Name: l.Name, DisplayName: l.DisplayName}
			if d.Provider.APIKey != "" && l.KeySealed != "" {
				key, err := open(l.KeySealed)
				if err != nil {
					return i18n.Err("assistant-models.err_key_decrypt")
				}
				_, ref := extension.ReferenceOf(key)
				m.SameKey = !ref && key == d.Provider.APIKey
			}
			if m.SameKey {
				same = append(same, m)
			} else {
				other = append(other, m)
			}
		}
		d.Matches = append(same, other...)
	}
	return nil
}

// parsePasteRoute answers POST /api/v1/launchers/import/parse {text}: the drafts of the pasted text, the shell aliases'
// named as an import would name them (importName, claimImportName against importTaken), each with its matches.
func (s *Service) parsePasteRoute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	errText := i18n.Err("assistant-models_import.err_text_required")
	if err := decodeBody(w, r, &in, 256<<10); err != nil {
		writeBodyErr(w, err, errText) // a decoding error may quote the text
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		apierr.Write(w, http.StatusBadRequest, errText)
		return
	}
	res := parsePaste(in.Text)
	if len(res.Drafts) > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		s.models.mu.Lock()
		stored, err := s.readStoredLaunchers(ctx)
		s.models.mu.Unlock()
		if err == nil {
			err = matchPaste(res.Drafts, stored, s.openKey)
		}
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		if slices.ContainsFunc(res.Drafts, func(d pasteDraft) bool { return d.Provider.Name != "" }) {
			rc, _ := s.zshrcLaunchersLastSeen(ctx)
			taken := s.importTaken(ctx, rc, stored)
			for i := range res.Drafts {
				if p := &res.Drafts[i].Provider; p.Name != "" {
					p.Name = claimImportName(taken, p.Name)
				}
			}
		}
	}
	apierr.WriteJSON(w, res)
}
