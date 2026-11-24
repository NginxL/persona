package node

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

type agyPaneBinding struct {
	conversation string
	verified     bool
}

type agyTranscriptKind uint8

const (
	agyTranscriptMissing agyTranscriptKind = iota
	agyTranscriptUnreadable
	agyTranscriptEmpty
	agyTranscriptNormal
	agyTranscriptSubagent
)

type agyCandidateDecision uint8

const (
	agyCandidateSubagent agyCandidateDecision = iota
	agyCandidatePending
	agyCandidateNormal
	agyCandidateUnverified
)

type agyParentSubagentInfo struct {
	file      os.FileInfo
	path      string
	hasInvoke bool
	children  map[string]bool
}

var agyEmbeddedUUIDRe = lazyre.New(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

// reportedAgyConversation is the conversation herdr's antigravity-cli integration reports for a pane's row. Persona's own
// hooks report too (agyhooks.go): agyReportedLocked is the two together.
func reportedAgyConversation(a host.Agent) string {
	s := a.AgentSession
	if a.Agent == agyAgent && s != nil && (s.Source == "herdr:antigravity_cli" || s.Source == agyPresenceSource) &&
		s.Agent == agyAgent && s.Kind == "id" && uuidRe.MatchString(s.Value) {
		return s.Value
	}
	return ""
}

// agyReportedLocked is the conversation the pane's agy row has been reported in, by herdr's integration or by
// Persona's own hook (agyhooks.go). The two must agree: when both speak and differ, nothing is known.
func (n *Node) agyReportedLocked(a host.Agent) string {
	if a.Agent != agyAgent {
		return ""
	}
	herdr := reportedAgyConversation(a)
	hook := n.agyReports[a.PaneID].conversation
	if herdr != "" && hook != "" && herdr != hook {
		return ""
	}
	return cmp.Or(herdr, hook)
}

// agyReported is agyReportedLocked for a caller that does not hold n.mu.
func (n *Node) agyReported(a host.Agent) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.agyReportedLocked(a)
}

// rawAgyConversationLocked checks the latest raw snapshots of every engine as well as the pane row.
// The row ID is pane-derived. Cwd and previously bound session IDs cannot identify
// a native conversation, and ambiguous reports do not authorize a read or resume.
func (n *Node) rawAgyConversationLocked(id, pane string) string {
	a := n.agents[id]
	conversation := n.agyReportedLocked(a)
	if conversation == "" || a.PaneID != pane || id != otherSession(a) {
		return ""
	}
	matches := 0
	for _, agents := range n.hostAgents {
		for _, raw := range agents {
			reported := n.agyReportedLocked(raw)
			if raw.PaneID == pane {
				if reported != conversation {
					return ""
				}
				matches++
			} else if reported == conversation {
				return ""
			}
		}
	}
	if matches != 1 {
		return ""
	}
	return conversation
}

func (n *Node) agyConversationClaimedElsewhereLocked(pane, conversation string) bool {
	for _, agents := range n.hostAgents {
		for _, raw := range agents {
			if raw.PaneID != pane && n.agyReportedLocked(raw) == conversation {
				return true
			}
		}
	}
	return false
}

// isAgySubagentHead reports whether a conversation's opening step indicates a built-in
// subagent conversation rather than a user-initiated or system-wake session.
func isAgySubagentHead(stepType, stepSource string) bool {
	typ := strings.ToUpper(strings.TrimSpace(stepType))
	src := strings.ToUpper(strings.TrimSpace(stepSource))
	if typ == "SYSTEM_MESSAGE" || typ == "CORTEX_STEP_TYPE_SYSTEM_MESSAGE" {
		return true
	}
	// Do not classify source=="SYSTEM" alone (e.g. SYSTEM-sourced USER_INPUT wake/background
	// steps or ERROR_MESSAGE steps) as a subagent: only explicit non-user, non-system
	// USER_INPUT sources (such as SUBAGENT) qualify without a SYSTEM_MESSAGE header.
	if typ == "USER_INPUT" && src != "" && src != "USER_EXPLICIT" && src != "SYSTEM" {
		return true
	}
	return false
}

func probeAgyTranscriptFile(root *os.Root, rel string) agyTranscriptKind {
	info, err := root.Stat(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agyTranscriptMissing
		}
		return agyTranscriptUnreadable
	}
	if !info.Mode().IsRegular() {
		return agyTranscriptMissing
	}
	f, err := root.Open(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agyTranscriptMissing
		}
		return agyTranscriptUnreadable
	}
	defer f.Close()

	r := bufio.NewReader(f)
	for range 64 {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Ignore any trailing line without a newline so in-progress writes wait for completion.
				return agyTranscriptEmpty
			}
			return agyTranscriptUnreadable
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var head struct {
			Type   string `json:"type"`
			Source string `json:"source"`
		}
		if json.Unmarshal(line, &head) != nil {
			continue
		}
		if head.Type == "" && head.Source == "" {
			continue
		}
		if isAgySubagentHead(head.Type, head.Source) {
			return agyTranscriptSubagent
		}
		return agyTranscriptNormal
	}
	return agyTranscriptEmpty
}

func probeAgyConversation(dir, conversation string) agyTranscriptKind {
	if dir == "" || conversation == "" || !uuidRe.MatchString(conversation) {
		return agyTranscriptMissing
	}
	root, err := agyBrain(dir, conversation)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agyTranscriptMissing
		}
		return agyTranscriptUnreadable
	}
	defer root.Close()

	kind := probeAgyTranscriptFile(root, agyTranscriptPath)
	if kind == agyTranscriptMissing || kind == agyTranscriptEmpty {
		if full := probeAgyTranscriptFile(root, agyFullTranscriptPath); full == agyTranscriptSubagent || full == agyTranscriptNormal {
			return full
		}
	}
	return kind
}

func extractAgyParentSubagents(root *os.Root, raw []byte, parent string) (bool, map[string]bool) {
	if !bytes.Contains(raw, []byte("invoke_subagent")) &&
		!bytes.Contains(raw, []byte("send_message")) &&
		!bytes.Contains(raw, []byte("manage_subagents")) {
		return false, nil
	}
	steps, _ := parseAgySteps(raw, parent)
	var hasInvoke bool
	var children map[string]bool
	addChild := func(id string) {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidRe.MatchString(id) {
			return
		}
		if children == nil {
			children = make(map[string]bool)
		}
		children[id] = true
	}

	var pending []string
	var plannerCalls []string
	var plannerSeq int
	var sawSystemBetween bool
	for _, step := range steps {
		switch step.Type {
		case "PLANNER_RESPONSE":
			pending = pending[:0]
			plannerCalls = plannerCalls[:0]
			plannerSeq = step.seq
			sawSystemBetween = false
			for _, tc := range step.ToolCalls {
				if tc == nil {
					pending = append(pending, "")
					plannerCalls = append(plannerCalls, "")
					continue
				}
				pending = append(pending, tc.Name)
				plannerCalls = append(plannerCalls, tc.Name)
				switch tc.Name {
				case "invoke_subagent":
					hasInvoke = true
				case "send_message":
					var args struct {
						Recipient string `json:"Recipient"`
					}
					if json.Unmarshal(tc.Args, &args) == nil && args.Recipient != "" {
						hasInvoke = true
						addChild(args.Recipient)
					}
				case "manage_subagents":
					var args struct {
						ConversationIds []string `json:"ConversationIds"`
					}
					if json.Unmarshal(tc.Args, &args) == nil && len(args.ConversationIds) > 0 {
						hasInvoke = true
						for _, cid := range args.ConversationIds {
							addChild(cid)
						}
					}
				}
			}
		case "SYSTEM_MESSAGE":
			sawSystemBetween = true
		case "USER_INPUT", "ERROR_MESSAGE":
			pending = pending[:0]
			plannerCalls = plannerCalls[:0]
		case "GENERIC":
			callName := ""
			if !sawSystemBetween && len(plannerCalls) > 0 && step.seq > plannerSeq {
				if idx := step.seq - plannerSeq - 1; idx >= 0 && idx < len(plannerCalls) {
					callName = plannerCalls[idx]
				}
			}
			if len(pending) > 0 {
				popped := pending[0]
				pending = pending[1:]
				if callName == "" {
					callName = popped
				}
			}
			if callName != "invoke_subagent" {
				continue
			}
			for _, match := range agyEmbeddedUUIDRe.FindAllString(step.Content, -1) {
				addChild(match)
			}
			if root != nil && step.Error == "" {
				for _, field := range step.TruncatedFields {
					if field == "content" {
						path := filepath.Join(".system_generated", "steps", fmt.Sprint(step.seq-1), "output.txt")
						if output, err := agyReadFile(root, path); err == nil {
							for _, match := range agyEmbeddedUUIDRe.FindAllString(string(output), -1) {
								addChild(match)
							}
						}
						break
					}
				}
			}
		}
	}
	return hasInvoke, children
}

func (n *Node) agyParentSubagents(dir, parent string) agyParentSubagentInfo {
	if dir == "" || parent == "" || !uuidRe.MatchString(parent) {
		return agyParentSubagentInfo{}
	}
	root, err := agyBrain(dir, parent)
	if err != nil {
		return agyParentSubagentInfo{}
	}
	defer root.Close()

	rel := agyTranscriptPath
	info, err := root.Stat(rel)
	if err != nil || !info.Mode().IsRegular() {
		rel = agyFullTranscriptPath
		info, err = root.Stat(rel)
		if err != nil || !info.Mode().IsRegular() {
			return agyParentSubagentInfo{}
		}
	}

	key := filepath.Join(dir, parent)
	n.mu.Lock()
	if n.agyParentSubs != nil {
		if cached, ok := n.agyParentSubs.get(key); ok && cached.path == rel && sameAgyFile(info, cached.file) && time.Since(info.ModTime()) > time.Second {
			n.mu.Unlock()
			return cached
		}
	}
	n.mu.Unlock()

	raw, err := root.ReadFile(rel)
	if err != nil {
		return agyParentSubagentInfo{}
	}
	raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	hasInvoke, children := extractAgyParentSubagents(root, raw, parent)
	entry := agyParentSubagentInfo{file: info, path: rel, hasInvoke: hasInvoke, children: children}

	n.mu.Lock()
	if n.agyParentSubs == nil {
		n.agyParentSubs = newLRU[string, agyParentSubagentInfo](256)
	}
	n.agyParentSubs.put(key, entry)
	n.mu.Unlock()
	return entry
}

func (n *Node) classifyAgyCandidate(dir, parent, candidate string) agyCandidateDecision {
	kind := probeAgyConversation(dir, candidate)
	if kind == agyTranscriptSubagent {
		return agyCandidateSubagent
	}
	parentInfo := n.agyParentSubagents(dir, parent)
	if parentInfo.children[strings.ToLower(candidate)] {
		return agyCandidateSubagent
	}
	switch kind {
	case agyTranscriptNormal:
		return agyCandidateNormal
	case agyTranscriptEmpty, agyTranscriptUnreadable:
		return agyCandidatePending
	default: // agyTranscriptMissing
		if parentInfo.hasInvoke {
			return agyCandidatePending
		}
		return agyCandidateUnverified
	}
}

// agyConversation resolves the pane's active parent agy conversation while ignoring
// built-in subagent conversation IDs reported via inherited HERDR_PANE_ID hooks (#13336).
func (n *Node) agyConversation(id, pane string) string {
	for range 3 {
		n.mu.Lock()
		raw := n.rawAgyConversationLocked(id, pane)
		if raw == "" {
			delete(n.agyBound, pane)
			n.mu.Unlock()
			return ""
		}
		bound := n.agyBound[pane]
		if bound.conversation != "" && n.agyConversationClaimedElsewhereLocked(pane, bound.conversation) {
			bound = agyPaneBinding{}
			delete(n.agyBound, pane)
		}
		if bound.conversation == raw && bound.verified {
			n.mu.Unlock()
			return raw
		}
		n.mu.Unlock()

		dir := n.agyDataRoot()
		var next agyPaneBinding
		var result string
		var keepBound bool

		if bound.conversation == "" || bound.conversation == raw {
			switch probeAgyConversation(dir, raw) {
			case agyTranscriptSubagent:
				next = agyPaneBinding{}
				result = ""
			case agyTranscriptNormal:
				next = agyPaneBinding{conversation: raw, verified: true}
				result = raw
			default:
				next = agyPaneBinding{conversation: raw, verified: false}
				result = raw
			}
		} else {
			switch n.classifyAgyCandidate(dir, bound.conversation, raw) {
			case agyCandidateSubagent, agyCandidatePending:
				keepBound = true
				next = bound
				result = bound.conversation
			case agyCandidateNormal:
				next = agyPaneBinding{conversation: raw, verified: true}
				result = raw
			case agyCandidateUnverified:
				next = agyPaneBinding{conversation: raw, verified: false}
				result = raw
			}
		}

		n.mu.Lock()
		if n.rawAgyConversationLocked(id, pane) != raw {
			n.mu.Unlock()
			continue
		}
		if keepBound && n.agyConversationClaimedElsewhereLocked(pane, bound.conversation) {
			delete(n.agyBound, pane)
			n.mu.Unlock()
			continue
		}
		if next.conversation == "" {
			delete(n.agyBound, pane)
		} else {
			if n.agyBound == nil {
				n.agyBound = make(map[string]agyPaneBinding)
			}
			n.agyBound[pane] = next
		}
		n.mu.Unlock()
		return result
	}
	return ""
}

// agyNotice is a pane's history while its conversation is not known: one notice, in the node's own language like its
// other lines (one watch serves every client of the session); the hub says it in each client's (transcript_text.go).
func agyNotice(id string) agyHistory {
	return agyHistory{token: "agy-pane:" + id, seq: 1, items: []proto.Item{{
		ID: "agy#0", Seq: 1, Role: "system", Kind: "notice", Text: i18n.T(i18n.Default, "node-engine.agy_pane_integration"), TS: time.Now().UTC(),
	}}}
}

type agyWatch struct {
	pane, history, conversation string
	since                       int
	file                        *agyPaneFiles
	stepHashes                  map[int][32]byte
}

type agyPaneFiles struct {
	source                  string
	primary, full, receipts os.FileInfo
}

func sameAgyFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime() == b.ModTime()
}

func agyStepsHistory(conversation string) agyHistory {
	return agyHistory{token: "agy-pane:" + conversation + ":steps:context-resources-v1:lazy-media:checkpoints:wakes:file-links:full-text"}
}

// agyRead projects the compact export when present, filling in verified text
// from the full export. A full-only export has its own cursor and revision cache.
func (n *Node) agyRead(root *os.Root, key, conversation string, previous *agyPaneFiles) (agyHistory, *agyPaneFiles, error) {
	empty := agyStepsHistory(conversation)
	source := agyTranscriptPath
	info, err := root.Stat(source)
	fullFile, fullErr := root.Lstat(agyFullTranscriptPath)
	if errors.Is(err, os.ErrNotExist) {
		source = agyFullTranscriptPath
		info, err = fullFile, fullErr
		fullFile = nil
		empty.token += ":full-export"
		key = filepath.Join(key, source)
	}
	if err != nil {
		return empty, nil, err
	}
	if !info.Mode().IsRegular() {
		return empty, nil, fmt.Errorf("%s is not a regular file", filepath.Base(source))
	}
	receiptsFile, _ := root.Lstat(agyWakeReceipts)
	if receiptsFile != nil && !receiptsFile.Mode().IsRegular() {
		receiptsFile = nil
	}
	if fullFile != nil && !fullFile.Mode().IsRegular() {
		fullFile = nil
	}
	fullUnsettled := fullFile != nil && time.Since(fullFile.ModTime()) <= time.Second
	files := &agyPaneFiles{source: source, primary: info, full: fullFile, receipts: receiptsFile}
	if previous != nil && previous.source == source && sameAgyFile(info, previous.primary) && sameAgyFile(fullFile, previous.full) &&
		sameAgyFile(receiptsFile, previous.receipts) && time.Since(info.ModTime()) > time.Second && !fullUnsettled {
		return agyHistory{}, files, nil
	}
	raw, err := root.ReadFile(source)
	if err != nil {
		return empty, nil, err
	}
	raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	steps, rev := n.agyParse(key, conversation, info, raw)
	if !slices.ContainsFunc(steps, func(step agyStep) bool { return step.Type != "" }) && len(bytes.TrimSpace(raw)) != 0 {
		return empty, nil, errors.New("unrecognized Antigravity transcript format")
	}
	if fullFile != nil && slices.ContainsFunc(steps, agyNeedsFullText) {
		full, err := n.agyFullSteps(root, key, conversation, fullFile, fullUnsettled)
		if err != nil {
			files.full = nil // Retry the optional source on the next poll.
		} else if restoreAgyFullText(steps, full, rev.steps) {
			rev.full = fullFile
		}
	}
	n.mu.Lock()
	if n.agyRevisions == nil {
		n.agyRevisions = newLRU[string, agyRevision](256)
	}
	if old, ok := n.agyRevisions.get(key); ok && old.branch == rev.branch {
		rev.rewrite = old.rewrite
		compactChanged := len(raw) < old.length || sha256.Sum256(raw[:old.length]) != old.hash
		fullChanged := !sameAgyFile(old.full, rev.full) || (rev.full != nil && fullUnsettled)
		if (compactChanged || fullChanged) && agyStepsRewritten(old.steps, rev.steps) {
			rev.rewrite = agyDigest(fmt.Sprintf("%s:%x", old.rewrite, rev.hash))
		}
	}
	n.agyRevisions.put(key, rev)
	n.mu.Unlock()

	var receipts map[string]bool
	if receiptsFile != nil {
		if data, err := root.ReadFile(agyWakeReceipts); err == nil {
			receipts = make(map[string]bool)
			for row := range strings.SplitSeq(string(data), "\n") {
				row = strings.TrimRight(row, "\r")
				if row != "" {
					receipts[row] = true
				}
			}
		}
	}
	h := mapAgySteps(steps, info.ModTime(), receipts)
	h.token = empty.token
	if rev.branch != "" || rev.native != "" || rev.rewrite != "" {
		h.token += ":" + rev.branch + ":" + rev.native + ":" + rev.rewrite
	}
	if h.observedWakeRevision != "" {
		h.token += ":wake:" + h.observedWakeRevision
	}
	return h, files, nil
}

// agyParsesKept is how many conversations' parses agyParse keeps to go on from: the panes being watched.
const agyParsesKept = 8

// agyParsed is how far agyParse read a conversation's transcript: the file, the length and SHA-256 of the complete
// lines it parsed, and the fold after them.
type agyParsed struct {
	file   os.FileInfo
	length int
	hash   [32]byte
	fold   agyFold
}

// agyParse is parseAgySteps(raw, conversation) for transcript file info at key. When the file is the one it parsed last
// time, only grew, and the bytes parsed then hash the same, only the lines after them are parsed, going on from that
// fold: a watched pane re-parsed the whole transcript four times a second while agy wrote to it (#13470). A truncated,
// rewritten or replaced file is parsed whole, as before.
func (n *Node) agyParse(key, conversation string, info os.FileInfo, raw []byte) ([]agyStep, agyRevision) {
	n.mu.Lock()
	if n.agyParses == nil {
		n.agyParses = newLRU[string, *agyParsed](agyParsesKept)
	}
	last, _ := n.agyParses.get(key)
	n.agyParses.put(key, nil) // taken: a reader meanwhile parses on its own
	n.mu.Unlock()

	h := sha256.New()
	fold, from := agyFold{conversation: conversation}, 0
	if last != nil && last.fold.conversation == conversation && os.SameFile(info, last.file) && len(raw) >= last.length {
		h.Write(raw[:last.length])
		if [32]byte(h.Sum(nil)) == last.hash {
			fold, from = last.fold, last.length
		} else {
			h.Reset()
		}
	}
	fold.add(raw[from:])
	h.Write(raw[from:])
	sum := [32]byte(h.Sum(nil))

	n.mu.Lock()
	n.agyParses.put(key, &agyParsed{file: info, length: len(raw), hash: sum, fold: fold})
	n.mu.Unlock()
	return agyOwnSteps(fold.steps), fold.revision(len(raw), sum)
}

// agyOwnSteps copies a fold's steps for the caller to keep and change: the fold stays the transcript's parse, for the
// next read to go on from, which can replace steps in place (a late arrival, a rewind, a running step that finished).
// agyRead restores a truncated reply's full text into its copy (restoreAgyFullText), dropping restored fields from
// TruncatedFields in place, so each step's list is copied too.
func agyOwnSteps(steps []agyStep) []agyStep {
	own := slices.Clone(steps)
	for i := range own {
		own[i].TruncatedFields = slices.Clone(own[i].TruncatedFields)
	}
	return own
}

type agyPageParams struct {
	ItemID     string `json:"item_id"`
	SinceSeq   int    `json:"since_seq"`
	BeforeSeq  int    `json:"before_seq"`
	AfterSeq   *int   `json:"after_seq"`
	Limit      int    `json:"limit"`
	Turns      int    `json:"turns"`
	FromSeq    int    `json:"from_seq"`
	ThroughSeq int    `json:"through_seq"`
	UserInputs bool   `json:"user_inputs"`
	FoldWork   bool   `json:"fold_work"`
}

func agyLightItems(items []proto.Item) []proto.Item {
	out := make([]proto.Item, 0, len(items))
	for _, it := range items {
		out = append(out, transcript.LightItem(it))
	}
	return out
}

func agyIndex(items []proto.Item, seq int) int {
	return sort.Search(len(items), func(i int) bool { return items[i].Seq >= seq })
}

func agyAfter(items []proto.Item, seq int) int {
	return sort.Search(len(items), func(i int) bool { return items[i].Seq > seq })
}

func (h agyHistory) after(since int) (items []proto.Item, span transcript.Span, more bool) {
	items = h.items[agyAfter(h.items, since):]
	span = transcript.Span{History: h.token, From: since, To: max(since, h.seq)}
	if len(items) > 500 {
		// Keep every item from the same native step on the same page.
		end := agyAfter(items, items[499].Seq)
		more = end < len(items)
		items = items[:end]
		if more {
			span.To = items[end-1].Seq
		}
	}
	return agyLightItems(items), span, more
}

func agyUserInput(it proto.Item) bool {
	return it.Role == "user" && (it.Kind == proto.KindText || (it.Kind == proto.KindImage && it.ToolUseID == ""))
}

func (h agyHistory) page(p agyPageParams) any {
	if p.UserInputs {
		index := proto.UserInputIndex{History: h.token, Inputs: []proto.UserInputPosition{}}
		for _, it := range h.items {
			if agyUserInput(it) && (len(index.Inputs) == 0 || index.Inputs[len(index.Inputs)-1].Seq != it.Seq) {
				index.Inputs = append(index.Inputs, proto.UserInputPosition{ID: it.ID, Seq: it.Seq})
			}
		}
		index.Total = len(index.Inputs)
		return index
	}
	items := h.items
	if p.ThroughSeq > 0 {
		from, to := agyIndex(items, p.FromSeq), agyAfter(items, p.ThroughSeq)
		items = items[min(from, to):to]
		if len(items) > 1000 {
			end := agyAfter(items, items[999].Seq)
			return map[string]any{"items": agyLightItems(items[:end]), "more": end < len(items)}
		}
		return map[string]any{"items": agyLightItems(items), "more": false}
	}
	if p.AfterSeq != nil {
		page, span, more := h.after(*p.AfterSeq)
		return withSpan(map[string]any{"items": page, "has_more": false, "more_after": more}, span, true)
	}
	span := transcript.Span{History: h.token, To: h.seq}
	if p.BeforeSeq > 0 {
		items = items[:agyIndex(items, p.BeforeSeq)]
		span.To = 0
		if len(items) > 0 {
			span.To = items[len(items)-1].Seq
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 80
	}
	start := max(0, len(items)-min(limit, 500))
	if p.Turns > 0 {
		turns := min(p.Turns, 10)
		start = max(0, len(items)-2000)
		lastSeq := -1
		for i := len(items) - 1; i >= start; i-- {
			if agyUserInput(items[i]) && items[i].Seq != lastSeq {
				lastSeq = items[i].Seq
				turns--
				if turns == 0 {
					start = i
					break
				}
			}
		}
	}
	if start > 0 {
		start = agyIndex(items, items[start].Seq)
	}
	items = agyLightItems(items[start:])
	if p.FoldWork && p.BeforeSeq == 0 {
		items = transcript.FoldWorkLatest(items)
	} else if p.FoldWork {
		items = transcript.FoldWork(items)
	}
	return withSpan(map[string]any{"items": nonNil(items), "has_more": start > 0}, span, false)
}

func (n *Node) agyPaneCall(ctx context.Context, method, id, pane string, raw json.RawMessage) (any, error, bool) {
	switch method {
	case "session.unwatch":
		n.unwatch(id)
		return okResult(nil), nil, true
	case "session.info":
		return n.agyPaneSessionInfo(id, pane), nil, true
	case "session.items", "session.item", "session.image", "session.watch":
	default:
		return nil, nil, false
	}
	var p agyPageParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err, true
	}
	if method == "session.watch" {
		n.watchAgyPane(rpc.ConnContext(ctx), id, pane, p.SinceSeq)
		return okResult(nil), nil, true
	}

	mu := n.pushLock(id)
	mu.Lock()
	defer mu.Unlock()
	h := agyNotice(id)
	conversation := n.agyConversation(id, pane)
	if conversation != "" {
		h = agyStepsHistory(conversation)
		dir := n.agyDataRoot()
		root, err := agyBrain(dir, conversation)
		if err == nil {
			defer root.Close()
			h, _, err = n.agyRead(root, filepath.Join(dir, conversation), conversation, nil)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read Antigravity transcript: %w", err), true
		}
		if method == "session.image" {
			media, found := h.media[p.ItemID]
			if found && n.agyConversation(id, pane) == conversation {
				image, err := media.image(root)
				if n.agyConversation(id, pane) == conversation {
					return image, err, true
				}
			}
		}
		if method == "session.item" {
			for _, it := range h.items {
				if it.ID != p.ItemID {
					continue
				}
				if step, ok := h.outputs[it.ID]; ok {
					path := filepath.Join(".system_generated", "steps", fmt.Sprint(step), "output.txt")
					if output, err := agyReadFile(root, path); err == nil {
						it.Text = agyToolOutput(string(output))
					}
				}
				if n.agyConversation(id, pane) == conversation {
					return it, nil, true
				}
				break
			}
		}
		if n.agyConversation(id, pane) != conversation {
			h = agyNotice(id)
		}
	}
	if method == "session.item" {
		return nil, &rpc.Error{Code: codeNotFound, Message: "no item"}, true // 404, not a gateway failure (#13470)
	}
	if method == "session.image" {
		return nil, &rpc.Error{Code: codeNotFound, Message: "no image"}, true
	}
	return h.page(p), nil, true
}

func (n *Node) agyPaneSessionInfo(id, pane string) map[string]any {
	info := map[string]any{
		"session_id": id, "agent": agyAgent,
		"context_tokens": 0, "output_tokens": 0, "compactions": 0, "turns": 0,
		"usage_unknown": true,
	}
	if a, ok := n.agent(id); ok {
		info["cwd"] = a.Cwd
	}
	if st, ok := n.state.get(id); ok {
		if st.Launcher != "" {
			info["launcher"] = st.Launcher
		}
		if st.Model != "" {
			info["model"] = st.Model
		}
	}
	if conversation := n.agyConversation(id, pane); conversation != "" {
		var breakdown *proto.ContextBreakdown
		if runtimeRow, _, ok := n.runtimes.rowOf(agyAgent + "-" + conversation); ok {
			if runtimeRow.Model != "" {
				info["model"] = runtimeRow.Model
			}
			if runtimeRow.PermissionMode != "" {
				info["permission_mode"] = runtimeRow.PermissionMode
			}
			if runtimeRow.Effort != "" {
				info["effort"] = runtimeRow.Effort
			}
			if runtimeRow.ContextTokens > 0 || runtimeRow.OutputTokens > 0 {
				info["context_tokens"] = int(runtimeRow.ContextTokens)
				info["output_tokens"] = runtimeRow.OutputTokens
				delete(info, "usage_unknown")
			}
			n.resolveAgyContext(&runtimeRow, conversation)
			breakdown = runtimeRow.ContextBreakdown()
		} else if u, ok := n.agyTranscriptMeta(conversation); ok {
			if u.model != "" {
				info["model"] = u.model
			}
			info["compactions"] = u.compactions
			info["turns"] = u.turns
			if u.hasUsage {
				info["context_tokens"] = u.contextTokens
				info["output_tokens"] = u.outputTokens
				delete(info, "usage_unknown")
			}
			row := proto.Session{SessionID: id, Model: u.model}
			n.setAgyTranscriptContext(&row, u)
			breakdown = row.ContextBreakdown()
		}
		if breakdown != nil {
			info["context_breakdown"] = breakdown
		}
	}
	return info
}

func (n *Node) watchAgyPane(parent context.Context, id, pane string, since int) {
	n.mu.Lock()
	if cur := n.watches[id]; cur != nil {
		n.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w := &watch{cancel: cancel, ctx: ctx, parent: parent, id: id, agy: &agyWatch{pane: pane, since: since}}
	n.watches[id] = w
	if !n.pumping {
		n.pumping = true
		go n.watchPump()
	}
	n.mu.Unlock()
	context.AfterFunc(ctx, func() { n.watchEnded(w) })
	n.pollAgyPane(w)
	w.armed.Store(true)
}

func (n *Node) pollAgyPane(w *watch) {
	mu := n.pushLock(w.id)
	mu.Lock()
	defer mu.Unlock()
	if w.ctx.Err() != nil {
		return
	}
	a := w.agy
	h := agyNotice(w.id)
	conversation := n.agyConversation(w.id, a.pane)
	var info *agyPaneFiles
	if conversation != "" {
		h = agyStepsHistory(conversation)
		dir := n.agyDataRoot()
		root, err := agyBrain(dir, conversation)
		if err == nil {
			defer root.Close()
			var previous *agyPaneFiles
			if a.conversation == conversation {
				previous = a.file
			}
			h, info, err = n.agyRead(root, filepath.Join(dir, conversation), conversation, previous)
		}
		if err != nil && (!errors.Is(err, os.ErrNotExist) || a.file != nil && a.conversation == conversation) {
			return
		}
		if h.token == "" || n.agyConversation(w.id, a.pane) != conversation {
			return
		}
	}
	since := a.since
	reset := a.history != "" && a.history != h.token || since > h.seq
	initial := a.history == ""
	if initial && since == 0 {
		a.since = h.seq // the first page is the subscriber's backlog
		since = h.seq
	}
	if reset {
		since = 0
		a.stepHashes = nil
	} else if !initial && len(a.stepHashes) > 0 && len(h.stepHashes) > 0 {
		// Push in-place updates for steps at or before `since` (such as a RUNNING step
		// finishing with new content) without bumping the history token or resetting the watcher.
		var updated []proto.Item
		for _, it := range h.items {
			if it.Seq > since {
				break
			}
			curHash, ok := h.stepHashes[it.Seq]
			if !ok {
				continue
			}
			if prevHash, seen := a.stepHashes[it.Seq]; !seen || prevHash != curHash {
				updated = append(updated, it)
			}
		}
		if len(updated) > 0 {
			if n.agyConversation(w.id, a.pane) != conversation {
				return
			}
			if n.peer != nil {
				span := transcript.Span{History: h.token, From: since, To: since}
				err := n.peer.NotifyContext(w.ctx, "session.items", withSpan(map[string]any{"session_id": w.id, "items": agyLightItems(updated)}, span, true))
				if err != nil {
					return
				}
			}
		}
	}
	for reset || since < h.seq {
		items, span, more := h.after(since)
		if n.agyConversation(w.id, a.pane) != conversation {
			return
		}
		if n.peer != nil {
			err := n.peer.NotifyContext(w.ctx, "session.items", withSpan(map[string]any{"session_id": w.id, "items": items}, span, true))
			if err != nil {
				return
			}
		}
		a.since, since, reset = span.To, span.To, false
		a.history = h.token
		if !more {
			break
		}
	}
	if len(h.stepHashes) > 0 {
		hashes := make(map[int][32]byte, len(h.stepHashes))
		for seq, sum := range h.stepHashes {
			if seq <= a.since {
				hashes[seq] = sum
			}
		}
		a.stepHashes = hashes
	} else {
		a.stepHashes = nil
	}
	a.history, a.file, a.conversation = h.token, info, conversation
}
