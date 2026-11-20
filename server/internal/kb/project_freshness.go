package kb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

const projectLogWindow = 40

type projectFieldRecord struct {
	Writer       string                `json:"writer"`
	Source       string                `json:"source"`
	ValueHash    string                `json:"value_hash"`
	SummaryHash  string                `json:"summary_hash,omitempty"`
	Cutoff       *kbwire.ProjectCutoff `json:"cutoff,omitempty"`
	TimelineHash string                `json:"timeline_hash,omitempty"`
}

func evidenceHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func evidencePosition(value any) string {
	raw, _ := json.Marshal(value)
	return evidenceHash(raw)
}

// projectTimeline retains the model's tail and counts newer entries by append position.
// Reading a known cutoff only needs the tail and the bytes of that cutoff's window.
type projectTimeline struct {
	lines []string
	count int
	newer map[string]int
	from  int64
	end   int64
	hash  string
	valid map[string]bool
	ok    bool
}

func (s *Store) projectTimeline(p *Project, inspect bool) projectTimeline {
	out := projectTimeline{ok: true, hash: evidenceHash(nil), valid: map[string]bool{}, newer: map[string]int{}}
	s.mu.Lock()
	var settleErr error
	if !inspect {
		_, settleErr = s.settleLogLocked(p.Slug)
	}
	f, err := os.Open(s.path(KindProject, p.Slug, "log.md"))
	s.mu.Unlock()
	if err != nil {
		out.ok = errors.Is(err, os.ErrNotExist)
		return out
	}
	defer f.Close()
	info, err := f.Stat()
	if settleErr != nil || err != nil {
		out.ok = false
		return out
	}
	size := info.Size()
	floor := size
	cutoffs := map[string]int64{}
	if inspect {
		for _, field := range []string{"summary", "one_line"} {
			rec := p.FieldCoverage[field]
			if rec.Cutoff == nil {
				floor = 0
				continue
			}
			from, through := rec.Cutoff.TimelineFrom, rec.Cutoff.TimelineThrough
			if hash, ok := timelineHash(f, from, through, size); ok && hash == rec.TimelineHash {
				out.valid[field] = true
				floor = min(floor, through)
				cutoffs[field] = through
			}
		}
	}
	// linesBack includes an empty segment after the final newline. end is
	// the exclusive byte position including the line's newline, if present.
	pos, newline := size, int64(0)
	err = linesBack(f, size, logChunk, func(line []byte) bool {
		end := pos + newline
		pos -= int64(len(line))
		if bytes.HasPrefix(line, []byte("- ")) {
			if out.end == 0 {
				out.end = end
			}
			out.count++
			for field, through := range cutoffs {
				if end > through {
					out.newer[field]++
				}
			}
			if len(out.lines) < projectLogWindow {
				out.lines = append(out.lines, string(line))
				out.from = pos
			}
		}
		pos--
		newline = 1
		return out.count <= projectLogWindow || end > floor
	})
	slices.Reverse(out.lines)
	out.hash, out.ok = timelineHash(f, out.from, out.end, size)
	out.ok = out.ok && err == nil
	return out
}

func timelineHash(f *os.File, from, end, size int64) (string, bool) {
	if from < 0 || end < from || end > size {
		return "", false
	}
	if from == end {
		return evidenceHash(nil), true
	}
	raw := make([]byte, end-from)
	_, err := f.ReadAt(raw, from)
	return evidenceHash(raw), err == nil
}

// projectInput contains the exact model snapshot alongside the positions it can cover.
type projectInput struct {
	prompts.KBProjectInput
	cutoff       kbwire.ProjectCutoff
	timelineHash string
	timelineOK   bool
}

// sameProjectEvidenceRef compares revisions. SourceID is a navigation label that can be recorded later.
func sameProjectEvidenceRef(a, b kbwire.ProjectEvidenceRef) bool {
	return a.Ref == b.Ref && a.Position == b.Position
}

func sameProjectCutoff(a, b kbwire.ProjectCutoff) bool {
	return a.TimelineFrom == b.TimelineFrom && a.TimelineThrough == b.TimelineThrough &&
		a.TimelineTruncated == b.TimelineTruncated && slices.EqualFunc(a.Sources, b.Sources, sameProjectEvidenceRef)
}

// projectEvidence reads the same bounded input on the read and refresh paths.
// Pending chat messages are reported separately because they were not in the prompt.
func (e *Engine) projectEvidence(p *Project, timeline projectTimeline) (projectInput, []kbwire.ProjectEvidenceRef) {
	in := projectInput{
		KBProjectInput: prompts.KBProjectInput{Pinned: p.Pinned, Name: p.Name, Kind: p.Kind, Aliases: p.Aliases, Log: timeline.lines},
		cutoff: kbwire.ProjectCutoff{
			TimelineFrom: timeline.from, TimelineThrough: timeline.end, TimelineTruncated: timeline.count > projectLogWindow,
		},
		timelineHash: timeline.hash, timelineOK: timeline.ok,
	}
	add := func(ref, id string, value any) {
		in.cutoff.Sources = append(in.cutoff.Sources, kbwire.ProjectEvidenceRef{Ref: ref, SourceID: id, Position: evidencePosition(value)})
	}
	var pending []kbwire.ProjectEvidenceRef
	if p.Pinned != "" {
		add(KindProject+"/"+p.Slug+"/meta.json#pinned", "", p.Pinned)
	}
	for _, cid := range p.Chats[:min(len(p.Chats), 8)] {
		sum := e.Store.Summary(KindChat, cid)
		if r := []rune(sum); len(r) > 1500 {
			sum = string(r[:1500]) + "…"
		}
		ref := KindChat + "/" + cid
		pc := prompts.KBProjectChat{ID: cid, Summary: sum}
		if c, ok := e.Store.Chat(cid); ok {
			id, _ := canonicalSourceURL(c.SourceURL)
			if id == "" {
				id = ref
			}
			pc.State = c.State
			if !c.StateAt.IsZero() {
				pc.StateAt = c.StateAt.In(e.Store.Zone()).Format("2006-01-02")
			}
			if c.State != "" || sum != "" {
				add(ref, id, pc)
			}
			if c.Pending > 0 {
				pending = append(pending, kbwire.ProjectEvidenceRef{
					Ref: ref + "/messages", SourceID: id, Position: evidencePosition([]any{c.LastAt, c.Pending, c.Messages}),
				})
			}
		} else if sum != "" {
			add(ref, ref, pc)
		}
		in.Chats = append(in.Chats, pc)
	}
	in.Moments = e.projectMomentsForPrompt(p, 5)
	for _, moment := range in.Moments {
		ref := "moments/posts/" + moment.ID
		add(ref, ref, moment)
	}
	return in, pending
}

func (p *Project) recordProjectField(field, text, writer, source string, in *projectInput) {
	if p.FieldCoverage == nil {
		p.FieldCoverage = map[string]projectFieldRecord{}
	}
	rec := projectFieldRecord{Writer: writer, Source: source, ValueHash: evidenceHash([]byte(text))}
	if in != nil && in.timelineOK {
		rec.Cutoff, rec.TimelineHash = &in.cutoff, in.timelineHash
	}
	p.FieldCoverage[field] = rec
}

func (e *Engine) projectFreshness(p *Project, summary string) (*kbwire.ProjectFreshness, []string) {
	known := *p
	known.FieldCoverage = map[string]projectFieldRecord{}
	for field, value := range map[string]string{"summary": summary, "one_line": p.OneLine} {
		rec := p.FieldCoverage[field]
		// The file and metadata can fail independently. An old record never attests a different value.
		if rec.ValueHash != "" && rec.ValueHash == evidenceHash([]byte(value)) {
			known.FieldCoverage[field] = rec
		}
	}
	if basis := known.FieldCoverage["one_line"].SummaryHash; basis != "" && basis != evidenceHash([]byte(summary)) {
		known.OneLineNeedsReview = true
	}
	timeline := e.Store.projectTimeline(&known, true)
	in, pending := e.projectEvidence(p, timeline)
	current := append(slices.Clone(in.cutoff.Sources), pending...)
	result := &kbwire.ProjectFreshness{
		TimelineEnd: timeline.end, Evidence: current, Fields: map[string]kbwire.ProjectFieldFreshness{},
	}
	for _, field := range []string{"summary", "one_line"} {
		value := summary
		if field == "one_line" {
			value = p.OneLine
		}
		rec := known.FieldCoverage[field]
		view := kbwire.ProjectFieldFreshness{Writer: rec.Writer, Source: rec.Source, Cutoff: rec.Cutoff, ReviewBy: "persona"}
		if holder := heldBy(&p.Tagging, field); holder != "" {
			view.LockReason, view.ReviewBy = "field_locked", holder
		}
		if value == "" && view.LockReason == "" && len(current) == 0 && timeline.end == 0 && timeline.ok && !p.Dirty && !p.referenceOnly() {
			continue
		}
		view.UncoveredEvidenceCount, view.NeedsReview, view.ReviewReason = projectReview(field, &known, rec, timeline, current)
		result.Fields[field] = view
		result.NeedsReview = result.NeedsReview || view.NeedsReview
	}
	if len(result.Fields) == 0 {
		return nil, timeline.lines
	}
	return result, timeline.lines
}

func projectReview(field string, p *Project, rec projectFieldRecord, timeline projectTimeline, current []kbwire.ProjectEvidenceRef) (*int, bool, string) {
	if p.referenceOnly() {
		return nil, false, referenceOnly
	}
	if !timeline.ok {
		return nil, true, "coverage_unknown"
	}
	count := len(current)
	reason := "coverage_unknown"
	if rec.Cutoff == nil {
		count += timeline.count
	} else {
		count = len(current) - len(rec.Cutoff.Sources)
		for _, ref := range rec.Cutoff.Sources {
			if !slices.ContainsFunc(current, func(candidate kbwire.ProjectEvidenceRef) bool {
				return sameProjectEvidenceRef(ref, candidate)
			}) {
				count++
			}
		}
		reason = "new_evidence"
		if !timeline.valid[field] && rec.Cutoff.TimelineThrough != 0 {
			if field == "one_line" && p.OneLineNeedsReview {
				return nil, true, "summary_edited"
			}
			return nil, true, "evidence_changed"
		}
		count += timeline.newer[field]
	}
	if field == "one_line" && p.OneLineNeedsReview {
		return &count, true, "summary_edited"
	}
	if count > 0 {
		return &count, true, reason
	}
	if rec.Cutoff == nil {
		return &count, false, reason
	}
	if !slices.EqualFunc(rec.Cutoff.Sources, current, sameProjectEvidenceRef) {
		return &count, true, "evidence_changed"
	}
	return &count, false, ""
}

func (e *Engine) projectEntry(p *Project, catalog func() map[string]kbwire.ProjectSource) Entry {
	freshness, _ := e.projectFreshness(p, e.Store.Summary(KindProject, p.Slug))
	return Entry{Kind: KindProject, ID: p.Slug, Name: p.Name, OneLine: p.OneLine, Importance: p.Importance,
		Relevance: p.Relevance, Tags: p.Tags, Updated: p.Updated, Extra: p.Kind + " · " + p.Status,
		ProjectMode: p.Mode, Sources: e.projectSources(p, catalog),
		Freshness: freshness}
}

// projectDetail takes the summary, metadata and timeline under the digest's commit lock.
func (e *Engine) projectDetail(d *Detail) (*Project, error) {
	s := e.Store
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	raw, err := os.ReadFile(s.entityPath(KindProject, d.ID))
	if err != nil {
		return nil, ErrNotFound
	}
	d.Meta = raw
	d.Summary = s.Summary(KindProject, d.ID)
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		d.Log = s.projectTimeline(&Project{Slug: d.ID}, true).lines
		d.Log = d.Log[max(0, len(d.Log)-30):]
		return nil, nil // hand-edited metadata remains readable as text
	}
	if len(p.FieldCoverage) != 0 || p.OneLineNeedsReview {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		delete(fields, "field_coverage")
		delete(fields, "one_line_needs_summary_review")
		d.Meta, _ = json.Marshal(fields)
	}
	p.Slug = d.ID
	d.ProjectMode = p.Mode
	d.Freshness, d.Log = e.projectFreshness(&p, d.Summary)
	d.Log = d.Log[max(0, len(d.Log)-30):]
	return &p, nil
}
