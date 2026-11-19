package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Each round of the background supervisor starts clean (#7315, docs/assistant/unified-entry.md "Each round starts
// clean"). It was one conversation that every wake went on with: every round reread every earlier one, and the
// tools of every kind of work, though a round needs its own signals and what the tasks' ledgers keep. It
// is still one conversation, because the clients' row, its rights over the tasks (backgroundSupervises), the
// announcement's receipt and the persisted relay queue all key on its id; but each round of it resumes at the end of
// its first turn (the node restarts its idle process with --resume-session-at, node/assistant_round.go), so a round
// reads the start brief, the whole snapshot and its own supervision message; the handling rules are the static
// supervision instructions its process starts with (supervision_instructions.go), as every supervisor's. That message
// is rendered Fresh: a line saying what crosses rounds is on the tasks, the round marker and each entry's last review
// (prompts.Supervise.Fresh). The round's process lists only the tools supervision uses (roundToolNames,
// through mcp.tools' tool_set).
//
// assistant.background_supervisor_keeps_context (off by default) brings back the one long conversation from its next
// round: no round is asked for, the message is rendered as before (not Fresh), and a message that asks for no round needs every tool,
// so the node replaces a round's process, once it is idle, by a plain resume with every tool (node/assistant_round.go).
//
// The node says how each round went (superviseRoundAnswered). A round it could not start from the first turn because
// that turn is no longer on the conversation's chain (a compaction, before #7315 or since: no_anchor), and a round that
// restarted the process with a transcript over backgroundTranscriptLimit, get the background supervisor replaced the
// next time a signal is for it while it is idle (backgroundOutgrown, ensureBackground): a new one starts, what was
// queued for the old one moves to it, and the old one is put away like a former main chat, titled "… · until M/D" and
// archived (putAwayBackground). What the answers said is kept in this manager's memory only: after a restart a
// replacement whose rounds still have no anchor can be replaced once more (at most once per backgroundRetry).

// superviseRoundParam marks an assistant.send to the background supervisor that is a round (node/assistant_round.go).
const superviseRoundParam = "supervise_round"

// toolSetSupervise is the round's tool set as its process asks for it (mcp.tools tool_set).
const toolSetSupervise = "supervise"

type toolSetKey struct{}

// withToolSet is ctx carrying the tool set an mcp.tools request asks for (its tool_set), for sessionMCPTools: ctx as
// it is when the request names none.
func withToolSet(ctx context.Context, params json.RawMessage) context.Context {
	var p struct {
		ToolSet string `json:"tool_set"`
	}
	if json.Unmarshal(params, &p) != nil || p.ToolSet == "" {
		return ctx
	}
	return context.WithValue(ctx, toolSetKey{}, p.ToolSet)
}

// toolSetOf is the tool set withToolSet put on ctx, "" for none.
func toolSetOf(ctx context.Context) string {
	set, _ := ctx.Value(toolSetKey{}).(string)
	return set
}

// roundToolNames are the tools a round lists, and why each is in: the ones its supervision work uses (follow-ups,
// computers and agents, an agent's cards, the knowledge base, the tasks' plans, reviews and scope, a conversation's
// words), and the ones its prompts point it to: ask_escalate for a card (the supervision instructions), task_decided for
// a decision_reply entry (its message asks it to confirm the owner's answer with it, and the tool lets the background
// supervisor record it), todo_get where its message says what crosses rounds is kept, and todo_list, agents_list,
// list_followups and kb_get where the snapshot leaves items out. lark_search, or larkcli_search from the lark-cli
// connector, is listed when the install gives it.
// roundTools keeps them in the order of the lists they come from, so the cached prefix stays the same from round to
// round. Every tool a round's own message names is here, and the descriptions of these tools and the background
// supervisor's context name the others only in passing (TestRoundListsTheToolsItsMessageNames, roundPassingMentions
// there); every other tool the supervision instructions name is in roundToolsWithheld with why, and the instructions'
// background line sends that work elsewhere too (TestRoundToolsAgreeWithSupervisionInstructions). Left out as well, and
// why: the phone's, mail, inbox, view, place and routine tools (the owner's own errands; an inbox signal leaves replies
// to the Inbox's own draft); the model, settings, secret, skill and concern tools (the owner's changes, made in the
// main conversation); the repository and other to-do writing tools (the background supervisor is not a direction and
// records no work of its own); and the other agent, conversation, leader and direction tools (a supervised replacement
// starts with task_plan on the same todo, and what needs another conversation is the main conversation's).
var roundToolNames = []string{"notify_user", "supervision_finish", "schedule_followup", "list_followups", "cancel_followup",
	"machines_list", "agents_list", "agent_read", "agent_send", "agent_interrupt", "agent_archive", "interaction_answer",
	"ask_escalate", "kb_search", "kb_get", "kb_update", "conversation_read", "task_plan", "task_review", "task_scope",
	"task_decided", "todo_get", "todo_list", "lark_search", "larkcli_search"}

// roundToolsWithheld are the tools the supervision instructions name that a round does not list, each with why the
// background supervisor has no use for it in a round. The instructions every supervisor reads also tell it so (their
// background line), so the supervision instructions never send a round to a tool it lacks.
var roundToolsWithheld = map[string]string{
	"ask_user":          "an owner decision from the background supervisor is a task_review needs_user conclusion card, never ask_user (the instructions' background line)",
	"conversation_send": "leader_stuck goes to the main conversation (digestTo)",
	"task_start_screen": "failed and started go to the main conversation, and only it remedies a start (remedyTool)",
	"task_start_keys":   "failed and started go to the main conversation, and only it remedies a start (remedyTool)",
	"task_retry":        "failed and started go to the main conversation, and only it retries a start (toolTaskRetry)",
	"agent_supervise":   "only the main conversation adopts an agent opened outside supervision (toolAgentSupervise)",
	"todo_create":       "a direction's own work: the background supervisor is not a direction and records no to-dos",
	"todo_complete":     "a direction's own work: the background supervisor is not a direction and records no to-dos",
	"leader_progress":   "a direction's own work: only a leader conversation reports progress (toolLeaderProgress)",
}

// roundTools are the tools of list a round lists, in list's order.
func roundTools(list []proto.Tool) []proto.Tool {
	out := make([]proto.Tool, 0, len(roundToolNames))
	for _, t := range list {
		if slices.Contains(roundToolNames, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// backgroundTranscriptLimit is how large the background supervisor's transcript grows before it is replaced. The file
// only grows (a compaction appends to it too), and every round now restarts the process, so Claude Code reads and
// parses all of it at each round's start, as the node does to find the anchor and the app's history after a node
// restart, though the round uses only the first turn. Measured with Claude Code 2.1.322 and a local mock of the model
// API on synthetic lines of about 4 KiB (node/assistant_round_live_test.go): a round asks the model 0.24 s after its
// process starts with a small file, 0.47 s at 32 MiB and about 0.8 s at 128 MiB, and the node's anchor scan takes
// 0.09 s and 0.35 s. Real transcripts that large were not measured and may parse slower (docs/lessons/agents.md: the
// app's reader took about 20 s for 317 MB on a cold open, which would be about 2 s for 32 MiB). A round writes about 0.1
// to 0.4 MB (estimated: the snapshot, its message, its tool calls), so at the audited 60 to 75 rounds a day 32 MiB is a
// replacement every few days; without a limit the file would reach a gigabyte within months. Only a round that
// restarted the process counts (superviseRoundAnswered).
const backgroundTranscriptLimit int64 = 32 << 20

// backgroundKeepsContext is assistant.background_supervisor_keeps_context in effect with settings cfg: the background
// supervisor is one long conversation, as before #7315 (off by default).
func backgroundKeepsContext(cfg Settings) bool {
	return cfg.BackgroundSupervisorKeepsContext != nil && *cfg.BackgroundSupervisorKeepsContext
}

// superviseRoundReply is the node's answer to a round's assistant.send (node/assistant_round.go): Context "fresh" (it
// started from the first turn) or "kept" (it went in as before) with Why, and the transcript's size. A node from before
// #7315 answers none of them, and so does any node to the same message asked again under its once_id.
type superviseRoundReply struct {
	SessionID       string `json:"session_id"`
	Context         string `json:"context"`
	Why             string `json:"why"`
	TranscriptBytes int64  `json:"transcript_bytes"`
}

// superviseRounds is what the rounds' answers say about background supervisors, in memory: the ones to replace and why,
// the ones that are a replacement and have not started a round from their first turn yet, when a replacement was last
// tried, and whether an old node was logged.
type superviseRounds struct {
	mu          sync.Mutex
	outgrown    map[string]string // background supervisor → why it is replaced at the next chance (no_anchor, oversized)
	replacement map[string]bool   // a background supervisor started to replace another, until a round of it starts fresh
	replacedAt  time.Time
	oldNode     bool
}

// superviseRoundAnswered notes how round res of background supervisor to went (deliverRelay).
func (s *Service) superviseRoundAnswered(to string, res superviseRoundReply) {
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	if res.Context == "" {
		if !r.oldNode {
			r.oldNode = true
			s.logf("supervise: a background round's answer says nothing of its context (a node from before #7315 keeps the whole conversation, or the same message was asked again)")
		}
		return
	}
	if res.Context == "fresh" {
		s.logf("supervise: background round %s: fresh", short(to))
		delete(r.replacement, to) // it works: a later no_anchor of it (a compaction) replaces it
	} else {
		s.logf("supervise: background round %s: kept (%s)", short(to), res.Why)
	}
	why := ""
	switch {
	case res.Context == "kept" && res.Why == "no_anchor":
		if r.replacement[to] {
			// A replacement whose first turn is no anchor either: Claude Code's transcript is not what the node reads.
			// Replacing it again would make a new conversation every backgroundRetry; it keeps the whole context instead.
			s.logf("supervise: the new background supervisor %s has no first turn to start rounds from either; it keeps the whole context", short(to))
			return
		}
		why = "no_anchor"
	case res.TranscriptBytes > backgroundTranscriptLimit && (res.Context == "fresh" || res.Why == "start_failed"):
		// Only where rounds restart the process (backgroundTranscriptLimit): a conversation that keeps its context for
		// good (an older Claude Code, another runtime: unsupported) is neither restarted per round nor spared idle
		// compaction, and replacing it would only throw that context away.
		why = "oversized"
	}
	if why == "" {
		return
	}
	if r.outgrown == nil {
		r.outgrown = map[string]string{}
	}
	r.outgrown[to] = why
}

// backgroundOutgrown is why background supervisor id, which is there, is to be replaced now ("" when it is not): a
// round's answer said it cannot start from its first turn or its transcript is too large, rounds start fresh (the
// switch is off), no replacement was tried within backgroundRetry, and it is idle (nothing of it is lost: no turn
// running, nothing going in). It is then reserved for the replacement (relays.replacing), in one step with the look
// that nothing goes in: from then on no queued message starts going into it, and none goes in straight, until what
// waits for it moved to its replacement (backgroundLost) or the replacement did not start (releaseBackground). A
// message that went in meanwhile would run in an archived conversation that is no longer the background supervisor,
// its task_review refused. background.mu is held.
func (s *Service) backgroundOutgrown(ctx context.Context, cfg Settings, id string) string {
	if backgroundKeepsContext(cfg) {
		return ""
	}
	r := &s.background.rounds
	r.mu.Lock()
	why := r.outgrown[id]
	recent := !r.replacedAt.IsZero() && time.Since(r.replacedAt) < backgroundRetry
	r.mu.Unlock()
	if why == "" || recent {
		return ""
	}
	rl := &s.relays
	rl.mu.Lock()
	// The queue must be readable: what waits there moves to the replacement.
	reserved := s.relayLocked(ctx) && !rl.draining[id]
	if reserved {
		rl.replacing[id] = true
	}
	rl.mu.Unlock()
	if !reserved {
		return ""
	}
	// Looked at once reserved: a message that went in just before shows it working.
	if c, ok := s.session(ctx, cfg.MachineID, id); !ok || c.Status == proto.StatusWorking || c.Starting {
		s.releaseBackground(ctx, cfg.MachineID, id)
		return ""
	}
	return why
}

// releaseBackground lets background supervisor id, reserved for a replacement that is not happening
// (backgroundOutgrown), take messages again, and has what waits for it go in now that may: a drain passed it by
// meanwhile.
func (s *Service) releaseBackground(ctx context.Context, machine, id string) {
	rl := &s.relays
	rl.mu.Lock()
	delete(rl.replacing, id)
	rl.mu.Unlock()
	if c, ok := s.session(ctx, machine, id); ok {
		s.drainRelays(machine, []proto.Session{c})
	}
}

// replaceBackground starts a background supervisor in place of old, which outgrew its first turn (why), and puts old
// away. The new one's id, or old's when it could not start (old goes on as it is: its rounds keep the context, and the
// next try is after backgroundRetry). background.mu is held, and old is reserved (backgroundOutgrown): the caller
// moves what waits for it (backgroundLost), which ends the reservation.
func (s *Service) replaceBackground(ctx context.Context, cfg Settings, old, why string) string {
	r := &s.background.rounds
	r.mu.Lock()
	r.replacedAt = time.Now()
	r.mu.Unlock()
	s.logf("supervise: replacing the background supervisor %s (%s)", short(old), why)
	id, err := s.startBackground(ctx)
	if err != nil {
		s.logf("supervise: the background supervisor's replacement did not start: %v; %s goes on as it is", err, short(old))
		s.releaseBackground(ctx, cfg.MachineID, old)
		return old
	}
	r.mu.Lock()
	delete(r.outgrown, old)
	if why == "no_anchor" {
		if r.replacement == nil {
			r.replacement = map[string]bool{}
		}
		r.replacement[id] = true
	}
	r.mu.Unlock()
	// The owner's model pick for the background supervisor goes on with the conversation that does its work: a pick is
	// never dropped behind the owner's back (model_pick.go). The old one keeps its own in the archive.
	if pick, ok := s.modelPickOf(ctx, cfg.BackgroundSupervisorMachineID, old); ok {
		pick.At = time.Now().UTC()
		if err := s.setModelPick(ctx, id, &pick); err != nil {
			s.logf("supervise: carry the background supervisor's model pick to %s: %v", short(id), err)
		}
	}
	s.putAwayBackground(ctx, cfg.BackgroundSupervisorMachineID, old, time.Now())
	return id
}

// putAwayBackground puts away background supervisor old, replaced (the settings name another already): renamed
// "Background supervisor · until M/D" in the owner's language by the user's hand (autonaming leaves it) and archived,
// as a former main chat is (putAwayMain). Its rounds stay readable there; sending to it brings it back as an ordinary
// conversation.
func (s *Service) putAwayBackground(ctx context.Context, machine, old string, now time.Time) {
	local := now.In(s.location(ctx))
	s.titleSet(machine, old, i18n.T(s.lang(ctx), "assistant-conversations.background_supervisor_former",
		i18n.Args{"month": fmt.Sprint(int(local.Month())), "day": fmt.Sprint(local.Day())}), titleByUser)
	if _, err := s.setArchived(ctx, machine, old, true, titleByUser); err != nil {
		s.logf("supervise: archive the former background supervisor %s: %v", short(old), err)
	}
}

// lastReviewEntry is a task's last review as a Fresh supervision message repeats it (#7315): its verdict, when (local)
// and its reason or note in a clipped line, so a round that does not see the earlier ones neither asks again what was
// decided nor sends back what was sent back already. nil when it was never reviewed. Only Fresh messages show it.
func (s *Service) lastReviewEntry(ctx context.Context, v *Supervision) *prompts.SuperviseLastReview {
	if v == nil || len(v.Reviews) == 0 {
		return nil
	}
	r := v.Reviews[len(v.Reviews)-1]
	note := r.Reason
	if note == "" {
		note = r.Note
	}
	return &prompts.SuperviseLastReview{Verdict: r.Verdict, At: r.At.In(s.location(ctx)).Format("01-02 15:04"), Note: clip(oneLine(note), lastReviewClip)}
}

// lastReviewClip is how many characters of a last review's reason or note a Fresh entry keeps.
const lastReviewClip = 200
