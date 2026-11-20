package kb

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// commitProject checks the frozen model input and writes both files under the same persistence lock.
// Observers run after both writes, so a callback that forgets a source cannot be followed by a stale write.
func (e *Engine) commitProject(snapshot *Project, input projectInput, d projectDigest) (err error) {
	s := e.Store
	s.metaMu.Lock()
	var current *Project
	var summaryChanged, metadataChanged bool
	defer func() {
		s.metaMu.Unlock()
		if metadataChanged {
			e.index(KindProject, current.Slug, current.Name, current.Tagging, current.OneLine, current.Updated)
		}
		if summaryChanged {
			s.notify(KindProject, snapshot.Slug, "summary")
		}
		if metadataChanged {
			s.notify(KindProject, snapshot.Slug, "metadata")
		}
	}()
	var ok bool
	current, ok = s.Project(snapshot.Slug)
	if !ok {
		return nil
	}
	if current.referenceOnly() {
		return ErrReferenceOnly
	}
	before, _ := json.Marshal(current)
	frozen, _ := json.Marshal(snapshot)
	// Compare source snapshots in the same instruction language. A preference change
	// while the model works is not a change to the source data it read.
	currentInput := e.projectInput(current)
	currentInput.Language = input.Language
	if !bytes.Equal(before, frozen) || !sameProjectCutoff(currentInput.cutoff, input.cutoff) ||
		currentInput.timelineHash != input.timelineHash || currentInput.timelineOK != input.timelineOK ||
		prompts.RenderKBProjectInput(currentInput.KBProjectInput) != prompts.RenderKBProjectInput(input.KBProjectInput) {
		return nil
	}
	wroteOneLine := oneLine(d.OneLine) != "" && !current.fieldLocked("one_line")
	if wroteOneLine {
		current.OneLine = oneLine(d.OneLine)
	}
	if d.Goal != "" && !current.fieldLocked("goal") {
		current.Goal = oneLine(d.Goal)
	}
	if (d.Kind == "short" || d.Kind == "long") && !current.fieldLocked("project_kind") {
		current.Kind = d.Kind
	}
	// A finished project stays finished through a rewrite: only news revives it (evolveProject), and FinishProjects
	// may have finished it on the sources' facts (#9427).
	if (d.Status == "active" || d.Status == "paused" || d.Status == "done") && current.Status != "ignored" && current.Status != "done" && !current.fieldLocked("status") {
		current.Status = d.Status
	}
	protected := current.Tagging
	now := time.Now().UTC()
	applyTags(&current.Tagging, d.Importance, int(d.Relevance), d.Tags, now)
	if protected.fieldLocked("importance") {
		current.Importance = protected.Importance
	}
	if protected.fieldLocked("relevance") {
		current.Relevance = protected.Relevance
	}
	if protected.fieldLocked("tags") {
		current.Tags = protected.Tags
	}
	if kw := cleanKeywords(d.Keywords); len(kw) > 0 && !current.fieldLocked("keywords") {
		current.Keywords = kw
	}
	current.Dirty = false
	current.Updated = now
	summary := input.Summary
	if strings.TrimSpace(d.Summary) != "" && !current.fieldLocked("summary") {
		path := s.path(KindProject, current.Slug, "summary.md")
		next := []byte(strings.TrimSpace("# "+current.Name+"\n\n"+d.Summary) + "\n")
		old, _ := os.ReadFile(path)
		if !bytes.Equal(old, next) {
			if err = writeFile(path, next); err != nil {
				return err
			}
			summaryChanged = true
			if !wroteOneLine && current.OneLine != "" {
				current.OneLineNeedsReview = true
			}
		}
		summary = string(next)
		current.recordProjectField("summary", summary, "persona", "kb.project", &input)
	}
	if wroteOneLine {
		current.recordProjectField("one_line", current.OneLine, "persona", "kb.project", &input)
		rec := current.FieldCoverage["one_line"]
		rec.SummaryHash = evidenceHash([]byte(summary))
		current.FieldCoverage["one_line"] = rec
		current.OneLineNeedsReview = false
	}
	if err = writeJSON(s.entityPath(KindProject, current.Slug), current); err != nil {
		return err
	}
	metadataChanged = true
	return nil
}
