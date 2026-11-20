package kbwire

import "time"

// ProjectSource identifies a source shared by project views. Delivery is the
// source's own state. Environment verification requires separate evidence.
type ProjectSource struct {
	ID                      string     `json:"id"`
	URL                     string     `json:"url,omitempty"`
	Refs                    []string   `json:"refs,omitempty"`
	DeliveryState           string     `json:"delivery_state,omitempty"`
	DeliveryAt              *time.Time `json:"delivery_at,omitempty"`
	EnvironmentVerification string     `json:"environment_verification,omitempty"`
}

// ProjectEvidenceRef identifies one input and the revision of it used in a rewrite.
type ProjectEvidenceRef struct {
	Ref      string `json:"ref"`
	Position string `json:"position"`
	SourceID string `json:"source_id,omitempty"`
}

// ProjectCutoff describes the exact evidence window supplied to the rewrite.
// Timeline offsets are byte positions in log.md, starting at zero and ending exclusively.
type ProjectCutoff struct {
	TimelineFrom      int64                `json:"timeline_from"`
	TimelineThrough   int64                `json:"timeline_through"`
	TimelineTruncated bool                 `json:"timeline_truncated,omitempty"`
	Sources           []ProjectEvidenceRef `json:"sources,omitempty"`
}

// ProjectFieldFreshness describes a conclusion independently of the project's update time.
type ProjectFieldFreshness struct {
	Writer                 string         `json:"writer,omitempty"`
	Source                 string         `json:"source,omitempty"`
	Cutoff                 *ProjectCutoff `json:"cutoff,omitempty"`
	LockReason             string         `json:"lock_reason,omitempty"`
	ReviewBy               string         `json:"review_by"`
	NeedsReview            bool           `json:"needs_review"`
	ReviewReason           string         `json:"review_reason,omitempty"`
	UncoveredEvidenceCount *int           `json:"uncovered_evidence_count,omitempty"`
}

// ProjectFreshness is the common projection for project list and detail reads.
type ProjectFreshness struct {
	NeedsReview bool                             `json:"needs_review"`
	TimelineEnd int64                            `json:"timeline_end"`
	Evidence    []ProjectEvidenceRef             `json:"evidence,omitempty"`
	Fields      map[string]ProjectFieldFreshness `json:"fields"`
}
