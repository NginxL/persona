package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// These are delivery receipts for the hub's phone transport, not phone data or manager business state.
type phoneWriteQueue struct {
	mu   sync.Mutex
	wake chan struct{}
	done chan struct{}
}

func (h *Hub) startPhoneWrites(ctx context.Context) error {
	h.phoneWrites.mu.Lock()
	defer h.phoneWrites.mu.Unlock()
	if h.phoneWrites.wake != nil {
		return nil
	}
	recoverCtx, cancel := context.WithTimeout(ctx, phoneToolsTimeout)
	err := h.store.RecoverPhoneWrites(recoverCtx)
	cancel()
	if err != nil {
		return err
	}
	h.phoneWrites.wake = make(chan struct{}, 1)
	h.phoneWrites.done = make(chan struct{})
	h.nodeWorkers.Add(1)
	go func() {
		defer h.nodeWorkers.Done()
		defer close(h.phoneWrites.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			// A snapshot pauses the hub's writers; what is due is delivered
			// after resume (quiesce.go).
			if h.quiesce.begin() {
				h.deliverPhoneWrites(ctx)
				h.quiesce.end()
			}
			select {
			case <-ctx.Done():
				return
			case <-h.phoneWrites.wake:
			case <-ticker.C:
			}
		}
	}()
	return nil
}

// Registration never waits for the delivery loop or its database reads.
func (h *Hub) wakePhoneWrites() {
	h.phoneWrites.mu.Lock()
	defer h.phoneWrites.mu.Unlock()
	select {
	case h.phoneWrites.wake <- struct{}{}:
	default:
	}
}

func (h *Hub) enqueuePhoneWrite(ctx context.Context, name string, args json.RawMessage) proto.ToolResult {
	if h.store == nil {
		return proto.TextResult("Phone write queue unavailable: no database.", true)
	}
	// Do not store invented tools or a tool whose registration has been revoked.
	h.mu.Lock()
	known := false
	for _, registration := range h.phoneTools {
		for _, tool := range registration.Tools {
			if tool.Name == name {
				known = true
			}
		}
	}
	h.mu.Unlock()
	if !known {
		return proto.TextResult("This phone write tool has no registered provider. Open the Persona app to register it first.", true)
	}
	ctx, cancel := context.WithTimeout(ctx, phoneToolsTimeout)
	defer cancel()
	write, err := h.store.EnqueuePhoneWrite(ctx, randomHex(16), name, args)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	h.wakePhoneWrites()
	go h.requestPhoneWake(name)
	return phoneWriteResult(write)
}

func phoneQueueOption(name string, args json.RawMessage) (json.RawMessage, bool, error) {
	var object map[string]json.RawMessage
	// Preserve the original call for tools that have no queue option, including its validation on the phone.
	if json.Unmarshal(args, &object) != nil || object == nil {
		return args, false, nil
	}
	option, exists := object["queue_if_offline"]
	if !exists {
		return args, false, nil
	}
	var enabled bool
	if string(option) == "null" || json.Unmarshal(option, &enabled) != nil {
		return nil, false, errors.New("queue_if_offline must be a boolean")
	}
	if enabled && !phoneConfirmedTools[name] {
		return nil, false, errors.New("Only phone writes that require confirmation can be queued")
	}
	delete(object, "queue_if_offline")
	args, err := json.Marshal(object)
	return args, enabled, err
}

func (h *Hub) withPhoneWriteTools(tools []proto.Tool) []proto.Tool {
	for i, tool := range tools {
		if !phoneConfirmedTools[tool.Name] {
			continue
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil {
			continue
		}
		var properties map[string]json.RawMessage
		_ = json.Unmarshal(schema["properties"], &properties)
		if properties == nil {
			properties = map[string]json.RawMessage{}
		}
		properties["queue_if_offline"], _ = json.Marshal(struct {
			Default     bool   `json:"default"`
			Description string `json:"description"`
			JSONType    string `json:"type"`
		}{JSONType: "boolean", Description: prompts.RenderPhoneQueueOption()})
		schema["properties"], _ = json.Marshal(properties)
		tools[i].InputSchema, _ = json.Marshal(schema)
		tools[i].Description = prompts.RenderPhoneQueuedWrite(tool.Description)
	}
	if h.store != nil {
		for _, name := range []string{"phone_write_queue_list", "phone_write_queue_cancel"} {
			tool := prompts.Tool(name)
			tools = append(tools, proto.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
	}
	return tools
}

func (h *Hub) deliverPhoneWrites(ctx context.Context) {
	for ctx.Err() == nil {
		readCtx, cancel := context.WithTimeout(ctx, phoneToolsTimeout)
		writes, err := h.store.QueuedPhoneWrites(readCtx)
		cancel()
		if err != nil {
			h.logf("read phone write queue: %v", err)
			return
		}
		var next *db.PhoneWrite
		var phone *client
		for i := range writes {
			if phone = h.queuedWriteClient(writes[i].Tool); phone != nil { // the primary device only, when it offers the tool
				next = &writes[i]
				break
			}
		}
		if next == nil {
			return
		}
		claimCtx, cancel := context.WithTimeout(ctx, phoneToolsTimeout)
		claimed, err := h.store.ClaimPhoneWrite(claimCtx, next.ID)
		cancel()
		if err != nil {
			h.logf("claim phone write: %v", err)
			return
		}
		if !claimed {
			continue
		} // Cancel won the race; nothing was sent.
		result, state := h.deliverPhoneWrite(ctx, phone, next)
		raw, _ := json.Marshal(result)
		if len(raw) > db.PhoneWriteResultLimit {
			raw, _ = json.Marshal(proto.TextResult("The phone result exceeded the retained result limit (64 KiB). Check the receipt state and verify on the phone before repeating a write.", result.IsError))
		}
		// A disconnected caller or a stopping hub must not erase the outcome of an accepted write.
		finishCtx, cancel := context.WithTimeout(context.Background(), phoneToolsTimeout)
		err = h.store.FinishPhoneWrite(finishCtx, next.ID, state, raw)
		cancel()
		if err != nil {
			h.logf("record phone write result: %v", err)
			return
		}
	}
}

func (h *Hub) deliverPhoneWrite(ctx context.Context, phone *client, write *db.PhoneWrite) (proto.ToolResult, string) {
	id := randomHex(16)
	ch := make(chan proto.ToolResult, 1)
	h.mu.Lock()
	h.calls[id] = ch
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.calls, id); h.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, phoneConfirmWait)
	defer cancel()
	select {
	case <-ctx.Done():
		return phoneWriteUncertain(), "uncertain"
	case <-phone.done:
		return phoneWriteUncertain(), "uncertain"
	default:
	}
	phone.sendJSON(map[string]any{"type": "tool_call", "call_id": id, "name": write.Tool, "args": write.Args})
	select {
	case result := <-ch:
		state := "succeeded"
		if result.IsError {
			state = "failed"
			for _, content := range result.Content {
				if content.Type == "text" && strings.TrimSpace(content.Text) == "The user declined on the phone." {
					state = "declined"
					break
				}
			}
		}
		return result, state
	case <-ctx.Done():
		return phoneWriteUncertain(), "uncertain"
	case <-phone.done:
		return phoneWriteUncertain(), "uncertain"
	}
}

func phoneWriteUncertain() proto.ToolResult {
	return proto.TextResult("The phone disconnected or the confirmation wait ended. The change may already be saved or its approval may still be open. This write will not be sent again; check on the phone before creating another write.", true)
}

type phoneWriteList struct {
	Items        []db.PhoneWrite `json:"items"`
	PendingCount int             `json:"pending_count"`
}

func (h *Hub) listPhoneWrites(ctx context.Context, id string) (phoneWriteList, error) {
	list := phoneWriteList{Items: []db.PhoneWrite{}}
	if h.store == nil {
		return list, errors.New("phone write queue unavailable: no database")
	}
	ctx, cancel := context.WithTimeout(ctx, phoneToolsTimeout)
	defer cancel()
	writes, err := h.store.PhoneWrites(ctx)
	for _, write := range writes {
		if write.State == "queued" || write.State == "delivering" {
			list.PendingCount++
		}
		if id == "" || write.ID == id {
			list.Items = append(list.Items, write)
		}
	}
	return list, err
}

func (h *Hub) cancelPhoneWrite(ctx context.Context, id string) (*db.PhoneWrite, int, error) {
	if id == "" {
		return nil, http.StatusBadRequest, errors.New("id is required")
	}
	if h.store == nil {
		return nil, http.StatusServiceUnavailable, errors.New("phone write queue unavailable: no database")
	}
	ctx, cancel := context.WithTimeout(ctx, phoneToolsTimeout)
	defer cancel()
	write, err := h.store.CancelPhoneWrite(ctx, id)
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	if write == nil {
		return nil, http.StatusNotFound, errors.New("no such phone write")
	}
	if write.State != "cancelled" {
		return write, http.StatusConflict, errors.New("only a queued phone write can be cancelled")
	}
	return write, http.StatusOK, nil
}

func phoneWriteResult(value any) proto.ToolResult {
	raw, _ := json.Marshal(value)
	return proto.TextResult(string(raw), false)
}

func (h *Hub) phoneWriteTool(ctx context.Context, name string, args json.RawMessage) (proto.ToolResult, bool) {
	if name != "phone_write_queue_list" && name != "phone_write_queue_cancel" {
		return proto.ToolResult{}, false
	}
	var input struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(args, &input) != nil {
		return proto.TextResult("Expected JSON arguments with an optional id.", true), true
	}
	if name == "phone_write_queue_list" {
		list, err := h.listPhoneWrites(ctx, input.ID)
		if err != nil {
			return proto.TextResult(err.Error(), true), true
		}
		return phoneWriteResult(list), true
	}
	write, _, err := h.cancelPhoneWrite(ctx, input.ID)
	if err != nil {
		return proto.TextResult(err.Error(), true), true
	}
	return phoneWriteResult(write), true
}

func (h *Hub) phoneWriteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/phone/write-queue", func(w http.ResponseWriter, r *http.Request) {
		list, err := h.listPhoneWrites(r.Context(), r.URL.Query().Get("id"))
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, list)
	})
	mux.HandleFunc("POST /api/v1/phone/write-queue/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		write, status, err := h.cancelPhoneWrite(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, status, err)
			return
		}
		writeJSON(w, write)
	})
}
