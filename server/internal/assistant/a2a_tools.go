package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func a2aSessionCaller(ref a2aRef) a2aCaller {
	return a2aCaller{Ref: ref, Owner: "session:" + ref.Machine + "/" + ref.Session}
}

func (s *Service) a2aTool(ctx context.Context, ref a2aRef, name string, args json.RawMessage) proto.ToolResult {
	cfg := s.a2aConfig(ctx)
	if !s.a2aAllowed(ctx, cfg, ref) {
		return proto.TextResult(errA2AUnavailable.Error(), true)
	}
	st, err := s.a2aStore()
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	out, err := s.a2aToolCall(ctx, st, cfg, a2aSessionCaller(ref), name, args)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	s.a2aSignal()
	data, err := json.Marshal(out)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return proto.TextResult(string(data), false)
}

type a2aToolArgs struct {
	a2aRef
	Task     string `json:"task_id"`
	ID       string `json:"message_id"`
	Context  string `json:"context_id"`
	Text     string `json:"text"`
	State    string `json:"state"`
	Wake     *bool  `json:"wake"`
	Before   string `json:"before"`
	CWD      string `json:"cwd"`
	Launcher string `json:"launcher"`
}

type a2aTaskView struct {
	ID           string     `json:"task_id"`
	Context      string     `json:"context_id"`
	Source       a2aRef     `json:"source"`
	Target       a2aRef     `json:"target"`
	State        string     `json:"state"`
	Events       []a2aEvent `json:"events"`
	Disconnected bool       `json:"target_disconnected,omitempty"`
}

func (t *a2aTask) view() a2aTaskView {
	return a2aTaskView{ID: t.ID, Context: t.Context, Source: t.source(), Target: t.target(), State: t.State, Events: t.Events, Disconnected: t.Disconnected}
}

func (s *Service) a2aToolCall(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, name string, args json.RawMessage) (any, error) {
	var a a2aToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, errA2AInvalid
	}
	var t *a2aTask
	var err error
	switch name {
	case "a2a_sessions":
		return s.a2aDiscover(ctx, cfg, c.Ref, a.Machine), nil
	case "a2a_get":
		if a.Task == "" {
			return s.a2aInbox(ctx, st, cfg, c, a.Before)
		}
		t, err = s.a2aGet(ctx, st, cfg, c, a.Task)
	case "a2a_send":
		if a.Task == "" {
			var launch *a2aLaunch
			if a.CWD != "" {
				if err := s.guardRefusal(ctx, c.Ref.Machine, c.Ref.Session, "agent_start"); err != nil {
					return nil, err
				}
				if err := s.dispatchToolGuard(ctx, c.Ref.Machine, c.Ref.Session, "agent_start"); err != nil {
					return nil, err
				}
				if err := s.workerRefusal(ctx, c.Ref.Machine, c.Ref.Session); err != nil {
					return nil, err
				}
				launch = &a2aLaunch{CWD: a.CWD, Launcher: a.Launcher, Pending: true}
			} else if err := s.a2aCanSendMessage(ctx, c.Ref, a.a2aRef, a.Text); err != nil {
				return nil, err
			}
			t, err = s.a2aStart(ctx, st, cfg, c, a.a2aRef, a.ID, a.Context, a.Text, a.Wake == nil || *a.Wake, launch)
			break
		}
		t, err = s.a2aMutate(ctx, st, cfg, c, a.Task, func(t *a2aTask) (bool, error) {
			if t.Owner != c.Owner {
				return false, errA2ANotFound
			}
			if err := s.a2aCanSendMessage(ctx, c.Ref, t.target(), a.Text); err != nil {
				return false, err
			}
			return a2aContinue(t, a.ID, a.Text)
		})
	case "a2a_report":
		t, err = s.a2aMutate(ctx, st, cfg, c, a.Task, func(t *a2aTask) (bool, error) {
			return a2aReport(t, c.Ref, a.ID, a.State, a.Text)
		})
	case "a2a_cancel":
		t, err = s.a2aMutate(ctx, st, cfg, c, a.Task, func(t *a2aTask) (bool, error) {
			if t.Owner != c.Owner {
				return false, errA2ANotFound
			}
			return a2aCancel(t)
		})
	default:
		return nil, errA2AInvalid
	}
	if err != nil {
		return nil, err
	}
	return t.view(), nil
}

func (s *Service) a2aCanSendMessage(ctx context.Context, from, to a2aRef, text string) error {
	if err := s.guardRefusal(ctx, from.Machine, from.Session, "conversation_send"); err != nil {
		return err
	}
	return s.dispatchSendGuard(withToolCaller(ctx, from.Machine, from.Session), from.Session, to.Machine, to.Session, text)
}

func a2aReport(t *a2aTask, sender a2aRef, id, state, text string) (bool, error) {
	if sender != t.target() {
		return false, errA2ANotFound
	}
	if !a2aValidID(id) || !a2aValidText(text) {
		return false, errA2AInvalid
	}
	kind := "status"
	switch state {
	case "progress":
		kind, state = "progress", ""
	case a2aInput, a2aComplete, a2aFailed:
	default:
		return false, fmt.Errorf("%w: state must be progress, input_required, completed or failed", errA2AInvalid)
	}
	ev := a2aReturn(t, a2aEvent{ID: id, Kind: kind, State: state, Text: text})
	if !slices.ContainsFunc(t.Events, func(e a2aEvent) bool { return e.ID == id }) {
		for i := len(t.Events) - 1; i >= 0; i-- {
			e := &t.Events[i]
			if e.Kind != "message" {
				continue
			}
			// A report from the verified recipient can arrive ahead of the send's acknowledgment.
			if e.Delivery == a2aSending || e.Delivery == a2aUncertain {
				e.Delivery = a2aDelivered
			}
			if e.Delivery != a2aDelivered {
				return false, errA2AConflict
			}
			if t.State == a2aSubmitted && kind == "progress" {
				t.append(a2aEvent{ID: randomHex(16), Kind: "status", State: a2aWorking})
			}
			break
		}
	}
	return a2aAppendOnce(t, ev)
}

func (s *Service) a2aInbox(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, before string) (any, error) {
	out := []a2aTaskView{}
	all, err := st.A2AList(ctx, c.Owner, c.Ref.Machine, c.Ref.Session, "", "", before, true, nil, 21)
	if err != nil {
		return nil, err
	}
	next := ""
	for i, row := range all {
		if i == 20 {
			next = all[i-1].ID
			break
		}
		t, err := decodeA2A(&row, nil)
		if err != nil {
			return nil, err
		}
		if s.a2aTaskVisible(ctx, cfg, c, t) {
			v := t.view()
			// The full history is available when the caller selects one task.
			v.Events = v.Events[len(v.Events)-1:]
			out = append(out, v)
		}
	}
	return map[string]any{"tasks": out, "next": next}, nil
}

func (s *Service) a2aDiscover(ctx context.Context, cfg assistantwire.A2AConfig, self a2aRef, machine string) any {
	type peer struct {
		a2aRef
		Title  string `json:"title"`
		Agent  string `json:"agent,omitempty"`
		Status string `json:"status"`
	}
	out := []peer{}
	machines := cfg.Machines
	if machines == nil {
		for _, d := range s.devices(ctx) {
			machines = append(machines, d.ID)
		}
	}
	for _, mid := range machines {
		if machine != "" && mid != machine {
			continue
		}
		for _, sess := range s.a2aSessionsOn(ctx, mid) {
			ref := a2aRef{mid, sess.SessionID}
			if ref != self && a2aSendable(sess) && s.a2aAllowed(ctx, cfg, ref) {
				out = append(out, peer{ref, sess.Title, sess.Agent, sess.Status})
			}
		}
	}
	return out
}
