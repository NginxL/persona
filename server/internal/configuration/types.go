// Package configuration defines the portable settings form contract. Modules supply
// definitions, values and persistence; this package knows no user, manager or storage.
package configuration

import (
	"context"
	"encoding/json"
)

const Version = 1
const MaxBytes = 64 << 10

// Document is a complete, localized snapshot of one externally selected resource.
// ID and Revision are opaque. Revision changes when values, permissions or definitions
// change. Values contains one valid effective value for every field, including defaults.
type Document struct {
	SchemaVersion int                        `json:"schema_version"`
	ID            string                     `json:"id"`
	Revision      string                     `json:"revision"`
	Title         string                     `json:"title"`
	Description   string                     `json:"description,omitempty"`
	Groups        []Group                    `json:"groups"`
	Values        map[string]json.RawMessage `json:"values"`
	Notices       map[string]Notice          `json:"notices,omitempty"`
}

type Group struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Fields      []Field `json:"fields"`
}

type Kind string

const (
	Toggle      Kind = "toggle"
	Text        Kind = "text"
	Select      Kind = "select"
	Multiselect Kind = "multiselect"
	Number      Kind = "number"
)

// Field is a discriminated union: Kind selects exactly its matching options member
// (toggle has none). Default is required and has the kind's JSON value type.
// Descriptions and Details are plain text. ReadOnly is presentation of a restriction
// the owning writer enforces too; it never grants authority.
type Field struct {
	ID             string              `json:"id"`
	Kind           Kind                `json:"kind"`
	Title          string              `json:"title"`
	Description    string              `json:"description,omitempty"`
	Default        json.RawMessage     `json:"default"`
	ReadOnly       bool                `json:"read_only,omitempty"`
	DisabledReason string              `json:"disabled_reason,omitempty"`
	Details        []Detail            `json:"details,omitempty"`
	Text           *TextOptions        `json:"text,omitempty"`
	Select         *SelectOptions      `json:"select,omitempty"`
	Multiselect    *MultiselectOptions `json:"multiselect,omitempty"`
	Number         *NumberOptions      `json:"number,omitempty"`
}

// Detail carries module-owned explanations such as scope, activation and rollback.
// A renderer displays their supplied titles and descriptions without interpreting them.
type Detail struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Notice struct {
	Kind    string `json:"kind"` // info | warning
	Message string `json:"message"`
}

type TextOptions struct {
	Multiline   bool   `json:"multiline,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	MinLength   *int   `json:"min_length,omitempty"` // Unicode scalar values, not UTF-16 units
	MaxLength   *int   `json:"max_length,omitempty"`
}

type Option struct {
	Value       string `json:"value"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type SelectOptions struct {
	Options      []Option `json:"options"`
	Presentation string   `json:"presentation,omitempty"` // list (default) | dropdown | segmented
}

type MultiselectOptions struct {
	Options     []Option `json:"options"`
	MinSelected *int     `json:"min_selected,omitempty"`
	MaxSelected *int     `json:"max_selected,omitempty"`
}

type NumberOptions struct {
	Integer bool     `json:"integer,omitempty"`
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	Step    *float64 `json:"step,omitempty"` // positive UI increment, not a validation constraint
}

// Update changes only named fields. Reset removes their overrides. Both operations
// are atomic together; missing fields remain untouched, and null is never a value.
type Update struct {
	SchemaVersion int                        `json:"schema_version"`
	ID            string                     `json:"id"`
	Revision      string                     `json:"revision"`
	Changes       map[string]json.RawMessage `json:"changes,omitempty"`
	Reset         []string                   `json:"reset,omitempty"`
}

// FieldError is machine-readable; clients localize Code, or display Message when the
// owning module supplies a localized business error. Neither value is executable.
type FieldError struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Error follows the API's error/code convention. A conflict returns no replacement
// values: the caller reloads explicitly and keeps its unsaved draft for reconciliation.
type Error struct {
	Code        string       `json:"code"`
	Message     string       `json:"error"`
	FieldErrors []FieldError `json:"field_errors,omitempty"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Provider is injected by the owning module, already bound to an authenticated
// resource. Update MUST check identity, revision, permissions and CheckUpdate in its
// own transaction, then return the complete canonical document after commit.
// This contract provides no global registry, persistence, resource lookup or access.
type Provider interface {
	Read(context.Context) (Document, error)
	Update(context.Context, Update) (Document, error)
}
