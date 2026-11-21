package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A dispatch guard limits only the conversations its targets name. It is off by default: no stored policy, an
// explicitly disabled one and an expired one leave both the tool list and every start path as they were. An enabled
// policy that cannot be read or validated fails closed for dispatch tools, with one fixed refusal that carries none
// of the policy, target, tool input or underlying error.
const (
	dispatchGuardSetting     = "assistant.dispatch_guard"
	dispatchGuardRefusalText = "Dispatch is refused by the configured guard."
	dispatchGuardMaxTargets  = 32
	dispatchGuardMaxIDRunes  = 256
	dispatchGuardMaxExpiry   = time.Hour
	dispatchGuardRoute       = "/api/v1/assistant/dispatch-guard"
)

var dispatchGuardToolNames = append([]string(nil), assistantwire.DispatchGuardTools...)
var dispatchGuardNow = time.Now

// DispatchGuardTarget names one assistant conversation on one machine and the dispatch tools it may not call.
type DispatchGuardTarget struct {
	MachineID string   `json:"machine_id"`
	SessionID string   `json:"session_id"`
	Deny      []string `json:"deny"`
}

// DispatchGuard is the stored policy. ExpiresAt is required for an enabled policy written by an extension, and must
// be at most one hour ahead. A full credential may store one without an expiry.
type DispatchGuard struct {
	Enabled   bool                  `json:"enabled"`
	ExpiresAt *time.Time            `json:"expires_at,omitempty"`
	Targets   []DispatchGuardTarget `json:"targets"`
}

type dispatchGuardBook struct {
	mu    sync.Mutex
	set   bool
	value DispatchGuard
}

func (g DispatchGuard) clone() DispatchGuard {
	out := g
	out.Targets = make([]DispatchGuardTarget, len(g.Targets))
	for i, target := range g.Targets {
		out.Targets[i] = target
		out.Targets[i].Deny = append([]string(nil), target.Deny...)
	}
	return out
}

func validGuardID(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && len([]rune(s)) <= dispatchGuardMaxIDRunes &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// validate checks a policy before storing or using it. allowNoExpiry distinguishes full credentials from extensions.
func (g DispatchGuard) validate(allowNoExpiry bool, now time.Time) error {
	if g.Enabled && len(g.Targets) == 0 {
		return errors.New("an enabled dispatch guard needs at least one target")
	}
	if len(g.Targets) > dispatchGuardMaxTargets {
		return fmt.Errorf("dispatch_guard.targets has %d entries; at most %d", len(g.Targets), dispatchGuardMaxTargets)
	}
	seenTargets := map[string]bool{}
	for i, target := range g.Targets {
		if !validGuardID(target.MachineID) || !validGuardID(target.SessionID) {
			return fmt.Errorf("dispatch_guard.targets[%d] needs non-empty machine_id and session_id", i)
		}
		key := target.MachineID + "\x00" + target.SessionID
		if seenTargets[key] {
			return fmt.Errorf("dispatch_guard.targets[%d] repeats a target", i)
		}
		seenTargets[key] = true
		if len(target.Deny) == 0 {
			return fmt.Errorf("dispatch_guard.targets[%d].deny is empty", i)
		}
		seenTools := map[string]bool{}
		for _, name := range target.Deny {
			if !slices.Contains(dispatchGuardToolNames, name) || seenTools[name] {
				return fmt.Errorf("dispatch_guard.targets[%d].deny has an unsupported or repeated entry", i)
			}
			seenTools[name] = true
		}
	}
	if g.ExpiresAt != nil {
		if !g.ExpiresAt.After(now) {
			return errors.New("dispatch_guard.expires_at must be in the future")
		}
		if !allowNoExpiry && g.ExpiresAt.After(now.Add(dispatchGuardMaxExpiry)) {
			return errors.New("dispatch_guard.expires_at must be at most one hour ahead")
		}
	} else if !allowNoExpiry && g.Enabled && len(g.Targets) > 0 {
		return errors.New("an enabled dispatch guard written by an extension needs expires_at")
	}
	return nil
}

func (s *Service) loadDispatchGuard(ctx context.Context) (DispatchGuard, bool, error) {
	if st := s.store(); st != nil {
		var g DispatchGuard
		ok, err := st.Setting(ctx, dispatchGuardSetting, &g)
		return g, ok, err
	}
	book := &s.guard
	book.mu.Lock()
	defer book.mu.Unlock()
	return book.value.clone(), book.set, nil
}

func (s *Service) saveDispatchGuard(ctx context.Context, g DispatchGuard) error {
	g = g.clone()
	if g.Targets == nil {
		g.Targets = []DispatchGuardTarget{}
	}
	if st := s.store(); st != nil {
		return st.PutSetting(ctx, dispatchGuardSetting, g)
	}
	book := &s.guard
	book.mu.Lock()
	book.value, book.set = g, true
	book.mu.Unlock()
	return nil
}

func allDispatchGuardTools() map[string]bool {
	out := make(map[string]bool, len(dispatchGuardToolNames))
	for _, name := range dispatchGuardToolNames {
		out[name] = true
	}
	return out
}

// guardDenied is the one decision shared by the visible tool list and call admission. A read or validation failure
// denies every dispatch tool; unrelated tools and non-target conversations with a valid policy are left alone.
func (s *Service) guardDenied(ctx context.Context, machine, session string) map[string]bool {
	g, ok, err := s.loadDispatchGuard(ctx)
	now := dispatchGuardNow().UTC()
	if err != nil {
		s.logf("assistant: dispatch guard unavailable; dispatch tools fail closed")
		return allDispatchGuardTools()
	}
	if !ok || !g.Enabled || g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
		return nil
	}
	if err := g.validate(true, now); err != nil {
		s.logf("assistant: invalid enabled dispatch guard; dispatch tools fail closed")
		return allDispatchGuardTools()
	}
	denied := map[string]bool{}
	for _, target := range g.Targets {
		if target.MachineID != machine || target.SessionID != session {
			continue
		}
		for _, name := range target.Deny {
			denied[name] = true
		}
	}
	return denied
}

// guardRefusal is the model-entry admission check. It runs before the called tool parses its arguments and never
// includes the tool name, arguments, target or policy in its error.
func (s *Service) guardRefusal(ctx context.Context, machine, session, tool string) error {
	if !slices.Contains(dispatchGuardToolNames, tool) {
		return nil
	}
	if s.guardDenied(ctx, machine, session)[tool] {
		return errors.New(i18n.T(s.lang(ctx), "assistant-dispatch_guard.refused"))
	}
	return nil
}

// guardTools narrows an actual tool list with the same decision as guardRefusal. Empty targets are unchanged.
func (s *Service) guardTools(ctx context.Context, machine, session string, tools []proto.Tool) []proto.Tool {
	if machine == "" && session == "" {
		return tools
	}
	denied := s.guardDeniedForDiscovery(ctx, machine, session)
	if len(denied) == 0 {
		return tools
	}
	out := make([]proto.Tool, 0, len(tools))
	for _, tool := range tools {
		if !denied[tool.Name] {
			out = append(out, tool)
		}
	}
	return out
}

// ToolsFor is the assistant's own tool list for one conversation.
func (s *Service) ToolsFor(ctx context.Context, machine, session string) []proto.Tool {
	return s.guardTools(ctx, machine, session, s.Tools(ctx))
}

// rejectDuplicateGuardKeys validates every object occurrence before RawMessage decoding can discard an earlier
// duplicate wrapper or targets value. A malformed earlier occurrence is never allowed to reach normalization.
func rejectDuplicateGuardKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkGuardJSONValue(dec); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return i18n.Err("assistant-dispatch_guard.invalid_json")
		}
		return i18n.Err("assistant-dispatch_guard.invalid_json")
	}
	return nil
}

func walkGuardJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return i18n.Err("assistant-dispatch_guard.invalid_json")
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			rawKey, err := dec.Token()
			if err != nil {
				return i18n.Err("assistant-dispatch_guard.invalid_json")
			}
			key, ok := rawKey.(string)
			if !ok {
				return i18n.Err("assistant-dispatch_guard.invalid_json")
			}
			if seen[key] {
				return i18n.Err("assistant-dispatch_guard.duplicate_key", i18n.Args{"key": key})
			}
			seen[key] = true
			if err := walkGuardJSONValue(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return i18n.Err("assistant-dispatch_guard.invalid_json")
		}
	case '[':
		for dec.More() {
			if err := walkGuardJSONValue(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return i18n.Err("assistant-dispatch_guard.invalid_json")
		}
	}
	return nil
}

// strictGuardJSON decodes one JSON value and rejects duplicate, unknown and differently cased fields at every level,
// including targets, before any occurrence can be decoded and dropped.
func strictGuardJSON(data []byte, out any) error {
	if err := rejectDuplicateGuardKeys(data); err != nil {
		return err
	}
	if err := rejectNonCanonicalGuardKeys(data, reflect.TypeOf(out)); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return i18n.Err("assistant-dispatch_guard.invalid_json")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return i18n.Err("assistant-dispatch_guard.invalid_json")
		}
		return i18n.Err("assistant-dispatch_guard.invalid_json")
	}
	return nil
}

var (
	guardRawMessageType = reflect.TypeFor[json.RawMessage]()
	guardTimeType       = reflect.TypeFor[time.Time]()
)

// rejectNonCanonicalGuardKeys requires every object key to be the exact JSON name of a field in the schema being
// decoded. encoding/json matches field names case-insensitively, so a differently cased key such as
// "DISPATCH_GUARD" or "TARGETS" would be another occurrence of the same field and could replace an earlier malformed
// occurrence before normalization sees it; DisallowUnknownFields does not catch that. A json.RawMessage value is
// checked against its own schema when it is decoded in turn.
func rejectNonCanonicalGuardKeys(data []byte, t reflect.Type) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return i18n.Err("assistant-dispatch_guard.invalid_json")
	}
	return checkCanonicalGuardKeys(v, t)
}

func checkCanonicalGuardKeys(v any, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == guardRawMessageType || t == guardTimeType {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil // a value of the wrong type is left to the decoder
		}
		for key, value := range obj {
			field, ok := canonicalGuardField(t, key)
			if !ok {
				return i18n.Err("assistant-dispatch_guard.invalid_json")
			}
			if err := checkCanonicalGuardKeys(value, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		items, ok := v.([]any)
		if !ok {
			return nil
		}
		for _, item := range items {
			if err := checkCanonicalGuardKeys(item, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// canonicalGuardField finds the field whose JSON name is exactly key.
func canonicalGuardField(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// normalizeDispatchGuard distinguishes a missing/null policy from an explicit disable or empty-target clear.
// Empty targets always clear, even when enabled is left true in the request; only non-empty policies need enabled.
func normalizeDispatchGuard(data []byte) (DispatchGuard, error) {
	var in struct {
		Enabled   *bool           `json:"enabled"`
		ExpiresAt *time.Time      `json:"expires_at"`
		Targets   json.RawMessage `json:"targets"`
	}
	if err := strictGuardJSON(data, &in); err != nil {
		return DispatchGuard{}, err
	}
	var g DispatchGuard
	if in.Enabled != nil {
		g.Enabled = *in.Enabled
	}
	g.ExpiresAt = in.ExpiresAt
	if len(in.Targets) == 0 {
		if in.Enabled == nil || g.Enabled {
			return DispatchGuard{}, i18n.Err("assistant-dispatch_guard.targets_required")
		}
		g.Targets = []DispatchGuardTarget{}
		g.ExpiresAt = nil // an explicit disable is also a clear
		return g, nil
	}
	if bytes.Equal(bytes.TrimSpace(in.Targets), []byte("null")) {
		return DispatchGuard{}, i18n.Err("assistant-dispatch_guard.targets_null")
	}
	if err := strictGuardJSON(in.Targets, &g.Targets); err != nil {
		return DispatchGuard{}, err
	}
	if len(g.Targets) == 0 {
		g.Enabled = false
		g.ExpiresAt = nil
		g.Targets = []DispatchGuardTarget{}
		return g, nil
	}
	if in.Enabled == nil {
		return DispatchGuard{}, i18n.Err("assistant-dispatch_guard.enabled_required")
	}
	return g, nil
}

// guardDecisionCapture keeps one effective decision shared by filtering and the revision returned for that list.
// A later admission check uses a new capture, while the response always uses the capture sampled by guardTools.
type guardDecisionCapture struct {
	denied  map[string]bool
	sampled bool
}

type guardDecisionKey struct{}

func (s *Service) guardDeniedForDiscovery(ctx context.Context, machine, session string) map[string]bool {
	if capture, ok := ctx.Value(guardDecisionKey{}).(*guardDecisionCapture); ok {
		if !capture.sampled {
			capture.denied = s.guardDenied(ctx, machine, session)
			capture.sampled = true
		}
		return capture.denied
	}
	return s.guardDenied(ctx, machine, session)
}

// guardDiscoveryCursor includes the effective per-target filter state, not only explicit policy writes. Natural
// expiry and a read failure changing into recovery therefore invalidate a previous revision-aware tool list.
func (s *Service) guardDiscoveryCursor(ctx context.Context, machine, session string, assistant bool, base string) string {
	if !assistant {
		return base
	}
	denied := s.guardDeniedForDiscovery(ctx, machine, session)
	if len(denied) == 0 {
		return base
	}
	names := make([]string, 0, len(denied))
	for name := range denied {
		names = append(names, name)
	}
	slices.Sort(names)
	return base + ":guard=" + strings.Join(names, ",")
}

func (s *Service) dispatchGuardRoutes() {
	s.handle("GET "+dispatchGuardRoute, func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get(RoleHeader)
		if !fullRole(r) && role != roleExtension {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-dispatch_guard.read_only_get"))
			return
		}
		g, _, err := s.loadDispatchGuard(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, i18n.Err("assistant-dispatch_guard.read_failed"))
			return
		}
		if g.Targets == nil {
			g.Targets = []DispatchGuardTarget{}
		}
		apierr.WriteJSON(w, map[string]any{"dispatch_guard": g})
	})
	s.handle("PUT "+dispatchGuardRoute, func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get(RoleHeader)
		full, extension := fullRole(r), role == roleExtension
		if !full && !extension {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-dispatch_guard.read_only_write"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		var in struct {
			Guard json.RawMessage `json:"dispatch_guard"`
		}
		if err := strictGuardJSON(body, &in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if len(in.Guard) == 0 || bytes.Equal(bytes.TrimSpace(in.Guard), []byte("null")) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-dispatch_guard.required"))
			return
		}
		guard, err := normalizeDispatchGuard(in.Guard)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := guard.validate(full, dispatchGuardNow().UTC()); err != nil {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-dispatch_guard.invalid_policy"))
			return
		}
		if err := s.saveDispatchGuard(r.Context(), guard); err != nil {
			apierr.Write(w, http.StatusInternalServerError, i18n.Err("assistant-dispatch_guard.save_failed"))
			return
		}
		s.mcpChanges.notify() // existing sessions must not keep a list made before this policy change
		if guard.Targets == nil {
			guard.Targets = []DispatchGuardTarget{}
		}
		apierr.WriteJSON(w, map[string]any{"dispatch_guard": guard})
	})
}
