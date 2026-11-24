package extension

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Defined in the contract package extwire (docs/server/architecture.md "Contract packages"); the old names stay.
const (
	ScopeAgentsControl           = extwire.ScopeAgentsControl
	ScopeAgentsPermissionMode    = extwire.ScopeAgentsPermissionMode
	ScopeAgentsRead              = extwire.ScopeAgentsRead
	ScopeAssistantAsk            = extwire.ScopeAssistantAsk
	ScopeCallPrefix              = extwire.ScopeCallPrefix
	ScopeInboxRead               = extwire.ScopeInboxRead
	ScopeInboxWrite              = extwire.ScopeInboxWrite
	ScopeKBRead                  = extwire.ScopeKBRead
	ScopeKBWrite                 = extwire.ScopeKBWrite
	ScopeSentRead                = extwire.ScopeSentRead
	ScopeMachinesRead            = extwire.ScopeMachinesRead
	ScopeMachinesPair            = extwire.ScopeMachinesPair
	ScopeBrowserRun              = extwire.ScopeBrowserRun
	ScopeNotify                  = extwire.ScopeNotify
	ScopeTodosRead               = extwire.ScopeTodosRead
	ScopeTodosWrite              = extwire.ScopeTodosWrite
	ScopeDecisionsRaise          = extwire.ScopeDecisionsRaise
	ScopeLeadersRead             = extwire.ScopeLeadersRead
	ScopeLeadersWrite            = extwire.ScopeLeadersWrite
	ScopeAssistantDispatchPolicy = extwire.ScopeAssistantDispatchPolicy
	ScopeAssistantStartDefaults  = extwire.ScopeAssistantStartDefaults
	ScopeDispatchGuard           = extwire.ScopeDispatchGuard
	ScopeModels                  = extwire.ScopeModels
	ScopeAssistantRead           = extwire.ScopeAssistantRead
	ScopeAssistantMemoryRead     = extwire.ScopeAssistantMemoryRead
	ScopeAssistantEntriesRead    = extwire.ScopeAssistantEntriesRead
	ScopeTranscription           = extwire.ScopeTranscription
	ScopeMarketRefreshOwn        = extwire.ScopeMarketRefreshOwn
)

// HubScopes are the scopes, in the order the install screen lists them.
var HubScopes = []string{ScopeKBRead, ScopeKBWrite, ScopeSentRead, ScopeInboxRead, ScopeInboxWrite, ScopeAgentsRead, ScopeAgentsControl,
	ScopeAgentsPermissionMode, ScopeMachinesRead, ScopeMachinesPair, ScopeBrowserRun, ScopeNotify, ScopeAssistantAsk, ScopeAssistantRead, ScopeAssistantMemoryRead, ScopeAssistantEntriesRead,
	ScopeTodosRead, ScopeTodosWrite, ScopeDecisionsRaise, ScopeLeadersRead, ScopeLeadersWrite, ScopeAssistantDispatchPolicy, ScopeAssistantStartDefaults, ScopeDispatchGuard, ScopeModels,
	ScopeTranscription, ScopeMarketRefreshOwn}

// InEveryScope says whether a token of every scope (an extension granted the hub's full token the old way, hub_api,
// that gets one of every scope instead) has scope s. Scopes whose authority must be agreed to by name are left out:
// agents.permission_mode, assistant.start_defaults (which can raise what task Agents spend), decisions.raise (which can
// interrupt the owner with a new decision), dispatch.guard (which changes which conversations may hand work out),
// machines.pair (which can issue one-time pairing links for new pending computers),
// browser.run (which runs scripts in a paired computer's browser), and
// market.refresh_own (which spends the source marketplace's API quota).
func InEveryScope(s string) bool {
	return s != ScopeAgentsPermissionMode && s != ScopeAssistantStartDefaults && s != ScopeDecisionsRaise && s != ScopeDispatchGuard && s != ScopeMachinesPair && s != ScopeBrowserRun && s != ScopeMarketRefreshOwn
}

// EveryScope is the scopes of a token of every scope (InEveryScope), in HubScopes' order.
func EveryScope() []string {
	return slices.DeleteFunc(slices.Clone(HubScopes), func(s string) bool { return !InEveryScope(s) })
}

var scopeTitles = map[string]string{
	ScopeKBRead:                  "scope_kb_read",
	ScopeKBWrite:                 "scope_kb_write",
	ScopeSentRead:                "scope_sent_read",
	ScopeInboxRead:               "scope_inbox_read",
	ScopeInboxWrite:              "scope_inbox_write",
	ScopeAgentsRead:              "scope_agents_read",
	ScopeAgentsControl:           "scope_agents_control",
	ScopeAgentsPermissionMode:    "scope_agents_permission_mode",
	ScopeMachinesRead:            "scope_machines_read",
	ScopeMachinesPair:            "scope_machines_pair",
	ScopeBrowserRun:              "scope_browser_run",
	ScopeNotify:                  "scope_notify",
	ScopeAssistantAsk:            "scope_assistant_ask",
	ScopeTodosRead:               "scope_todos_read",
	ScopeTodosWrite:              "scope_todos_write",
	ScopeDecisionsRaise:          "scope_decisions_raise",
	ScopeLeadersRead:             "scope_leaders_read",
	ScopeLeadersWrite:            "scope_leaders_write",
	ScopeAssistantDispatchPolicy: "scope_assistant_dispatch_policy",
	ScopeAssistantStartDefaults:  "scope_assistant_start_defaults",
	ScopeDispatchGuard:           "scope_dispatch_guard",
	ScopeModels:                  "scope_models",
	ScopeAssistantRead:           "scope_assistant_read",
	ScopeAssistantMemoryRead:     "scope_assistant_memory_read",
	ScopeAssistantEntriesRead:    "scope_assistant_entries_read",
	ScopeTranscription:           "scope_transcription",
	ScopeMarketRefreshOwn:        "scope_market_refresh_own",
}

// ScopeTitle is how the install screen says scope s; ok false for what is not a scope.
func ScopeTitle(s string) (string, bool) { return ScopeTitleIn(i18n.Default, s) }

// ScopeTitleIn names a known scope in the requested language.
func ScopeTitleIn(l i18n.Lang, s string) (string, bool) {
	if id, ok := strings.CutPrefix(s, ScopeCallPrefix); ok {
		if !idPattern.MatchString(id) {
			return "", false
		}
		return permissionText(l, "scope_call", i18n.Args{"id": id}), true
	}
	key, ok := scopeTitles[s]
	if !ok {
		return "", false
	}
	return permissionText(l, key), true
}

// hubLine is the line for scopes (all an extension asks for, or those an upgrade adds), in HubScopes' order.
func hubLine(l i18n.Lang, title string, scopes []string) PermissionLine {
	var what []string
	for _, known := range HubScopes {
		for _, s := range scopes {
			if s == known {
				t, _ := ScopeTitleIn(l, s)
				what = append(what, t)
			}
		}
	}
	for _, s := range scopes {
		if strings.HasPrefix(s, ScopeCallPrefix) {
			t, _ := ScopeTitleIn(l, s)
			what = append(what, t)
		}
	}
	return PermissionLine{Key: "hub", Title: title, Detail: strings.Join(what, permissionText(l, "sentence_separator"))}
}
