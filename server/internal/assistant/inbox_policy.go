package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// inboxReadOnlyTools are the assistant-owned tools an Inbox handling chat may call itself. Everything not listed is
// denied by default: a newly added outbound, interruption, delegation, scheduling or settings tool must not become an
// Inbox bypass merely because this policy was written before it. kb_update is the documented internal-memory exception;
// it cannot send to another person. Suggestions and drafts remain available, and their confirmed execution uses runTool.
var inboxReadOnlyTools = map[string]bool{
	"inbox_suggest": true, "inbox_draft": true, "inbox_folders": true,
	"kb_search": true, "kb_list": true, "kb_get": true, "kb_update": true, "kb_history_status": true,
	"machines_list": true, "agents_list": true, "agent_read": true,
	"conversations_list": true, "conversation_read": true, "view_get": true, "task_scope": true,
	"repositories_list": true, "repository_get": true, "todo_repositories_get": true, "todo_list": true, "todo_get": true,
	"list_followups": true, "list_places": true, "model_profiles_list": true, "model_profile_get": true,
}

// Phone tools have no ReadTools/Actions metadata. Keep an explicit read-only catalog and fail closed for new or mixed
// tools: phone_notify, messages/calls, alarms, shortcuts, media, clipboard writes and device controls all stay behind
// inbox_suggest. These reads do not notify or change the phone (maps_route, unlike maps_navigate, opens nothing).
var inboxReadOnlyPhoneTools = map[string]bool{
	"phone_contacts_search": true, "phone_calendar_events": true, "phone_reminders_list": true,
	"phone_location_current": true, "phone_location_search": true, "phone_maps_search_nearby": true, "phone_maps_route": true,
	"phone_health_summary": true, "phone_workouts_recent": true, "phone_workout_detail": true,
	"phone_photos_recent": true, "phone_photo_get": true, "phone_device_info": true, "phone_clipboard_get": true,
	"phone_alarms_list": true, "phone_shortcuts_list": true, "phone_music_snapshot": true, "phone_home_list": true,
	"phone_notifications_list": true, "phone_write_queue_list": true,
}

// isInboxModelCall uses the node's attested origin first, covering the window before assistant.send returns and the
// manager records it. The manager's own origin book covers older nodes and direct Tool calls. A recorded Inbox origin
// wins over a missing or different hint, so a downgrade cannot turn an existing handling chat into an ordinary one.
func (s *Service) isInboxModelCall(ctx context.Context, machine, session, originHint string) bool {
	if originHint == OriginInbox {
		return true
	}
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(ctx) {
		return false // a new node still fails closed through its hint; do not break every ordinary call on a store error
	}
	o := b.m[session]
	return o != nil && o.Origin == OriginInbox && (o.MachineID == "" || s.sameMachine(machine, o.MachineID))
}

// managerOriginDecision returns the only origin attestation a node may trust. A new conversation carries the manager's
// own start origin; an existing one requires a successful origin-book read. A store error is unknown, never ordinary.
func (s *Service) managerOriginDecision(ctx context.Context, machine, session, startOrigin string, newConversation bool) (string, bool) {
	if newConversation {
		return startOrigin, true
	}
	b := &s.origins
	b.mu.Lock()
	if !s.originsLocked(ctx) {
		b.mu.Unlock()
		return "", false
	}
	o := b.m[session]
	if o == nil {
		b.mu.Unlock()
		// Backfill is asynchronous. A missing record is not positive evidence of an ordinary conversation: a legacy
		// Inbox handling chat can reach this path before its first backfill completes. A current guest registration is
		// separate manager-owned evidence; guests never share the assistant's origin book.
		_, guest, err := s.guestOf(ctx, machine, session)
		if err != nil {
			return "", false
		}
		return "", guest
	}
	origin, originMachine := o.Origin, o.MachineID
	b.mu.Unlock()
	if origin == OriginInbox {
		if originMachine == "" || s.sameMachine(machine, originMachine) {
			return OriginInbox, true
		}
		return "", false
	}
	return "", true
}

// inboxModelRefusal applies only to model/node tool entry points, never runTool. Owner-confirmed reply, Agent and to-do
// execution deliberately takes the latter path as the owner's action.
func (s *Service) inboxModelRefusal(ctx context.Context, machine, session, originHint, name string) (proto.ToolResult, bool) {
	if !s.isInboxModelCall(ctx, machine, session, originHint) {
		return proto.ToolResult{}, false
	}
	if inboxReadOnlyTools[name] || inboxReadOnlyPhoneTools[name] {
		return proto.ToolResult{}, false
	}
	if s.ext != nil {
		if known, readOnly := s.ext.ToolAccess(name); known && readOnly {
			return proto.ToolResult{}, false
		}
	}
	// Unknown tools are denied too. In particular, a connector Action is never mistaken for a read because its name
	// happens not to end in _send, and future extension/phone tools cannot silently widen the outbound surface.
	if strings.TrimSpace(name) == "" {
		name = "this tool"
	}
	return proto.TextResult("Inbox conversations cannot call "+name+" directly because it may send, interrupt, delegate, "+
		"or change external state. Use inbox_suggest; the owner-confirmed action will run separately.", true), true
}
