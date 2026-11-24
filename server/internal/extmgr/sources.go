package extmgr

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Moving a source from one manager to another (extwire.MethodSource*, docs/assistant/managers.md): the hub asks the old manager
// to export it, gives what came back to the new one to import, then tells the old one to release it. Mail accounts are
// the only kind so far. A bundle holds the source's secrets in plaintext: it is never logged, and no error quotes it.

const sourceKindMail = "mail"

// sourceCall answers extensions.source_export / source_import / source_release.
func (h *Manager) sourceCall(method string, params json.RawMessage) (any, error) {
	switch method {
	case extwire.MethodSourceImport:
		var b extwire.SourceBundle
		if err := json.Unmarshal(params, &b); err != nil {
			// Not err itself: it could quote the bundle.
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: "extensions.source_import needs a source bundle"}
		}
		return h.importSource(b)
	case extwire.MethodSourceExport, extwire.MethodSourceRelease:
		var r extwire.SourceRef
		if err := json.Unmarshal(params, &r); err != nil || r.Source == "" {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: method + " needs a source"}
		}
		id, err := h.mailSource(r.Source)
		if err != nil {
			return nil, err
		}
		if method == extwire.MethodSourceExport {
			data, err := h.mail.ExportAccount(id)
			if errors.Is(err, mailwire.ErrNoAccount) {
				return nil, noSource(r.Source)
			}
			if err != nil {
				return nil, err
			}
			return extwire.SourceBundle{Source: r.Source, Kind: sourceKindMail, Data: data}, nil
		}
		if err := h.mail.DeleteAccount(id); err != nil {
			return nil, err
		}
		h.logf("source %s released", r.Source)
		return map[string]any{}, nil
	}
	return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: "unknown source method " + method}
}

// importSource takes a source another manager exported.
func (h *Manager) importSource(b extwire.SourceBundle) (any, error) {
	kind, _, _ := strings.Cut(b.Source, ":")
	if b.Kind != sourceKindMail || kind != sourceKindMail {
		return nil, &rpc.Error{Code: extwire.CodeSourceKind, Message: "this kind of source cannot move: " + b.Kind}
	}
	if h.mail == nil {
		return nil, &rpc.Error{Code: extwire.CodeNoSource, Message: "mail is not set up on this manager"}
	}
	a, err := h.mail.ImportAccount(b.Data)
	if errors.Is(err, mailwire.ErrAccountExists) {
		return nil, &rpc.Error{Code: extwire.CodeSourceExists, Message: "this manager has " + b.Source + " already"}
	}
	if err != nil {
		return nil, err
	}
	ref := extwire.SourceRef{Source: sourceKindMail + ":" + a.ID}
	h.logf("source %s imported", ref.Source)
	return ref, nil
}

// mailSource is the account ID of a "mail:<id>" source this manager has (a *rpc.Error with CodeNoSource or
// CodeSourceKind otherwise).
func (h *Manager) mailSource(source string) (string, error) {
	kind, id, _ := strings.Cut(source, ":")
	if kind != sourceKindMail {
		return "", &rpc.Error{Code: extwire.CodeSourceKind, Message: "this kind of source cannot move: " + kind}
	}
	if h.mail == nil || id == "" {
		return "", noSource(source)
	}
	accounts, err := h.mail.ListAccounts()
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.ID == id {
			return id, nil
		}
	}
	return "", noSource(source)
}

func noSource(source string) error {
	return &rpc.Error{Code: extwire.CodeNoSource, Message: "no source " + source + " here"}
}
