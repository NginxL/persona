package uistream

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Diff emits deterministic stable-key edits without regenerating callback IDs.
// The target must use the same session/epoch/root and the protocol's next version
// (or the same version for unchanged non-event work). Identical trees may also
// advance exactly one revision to consume an accepted event. Page metadata uses root replacement;
// tiles use whole grid/carousel replacement. The bool recommends a full snapshot
// when a delta is not smaller or topology/operation budgets need root fallback.
// A returned delta remains valid and lossless even when a snapshot is preferred.
// If even root replacement exceeds the frame budget, return (nil, true, nil):
// only the already-validated target snapshot can be sent.
func Diff(from, to *Tree) (*Delta, bool, error) {
	if from == nil || from.root == nil || to == nil || to.root == nil {
		return nil, false, errors.New("missing diff tree")
	}
	if from.Session() != to.Session() || from.Epoch() != to.Epoch() || from.RootKey() != to.RootKey() {
		return nil, false, errors.New("diff identity mismatch: open a fresh snapshot")
	}
	same := bytes.Equal(from.viewJSON(), to.viewJSON())
	want := from.Version()
	if !same || to.Version() != want {
		if want == VersionMost {
			return nil, false, errors.New("rotate epoch before version exhaustion")
		}
		want++
	}
	if to.Version() != want {
		return nil, false, errors.New("diff target version mismatch")
	}
	header := wire.UIStreamDelta{Protocol: 1, Session: from.Session(), Epoch: from.Epoch(), BaseVersion: from.Version(), Version: want}
	d := &Delta{frame: header, ops: make([]map[string]json.RawMessage, 0)}
	if same {
		return d, false, nil
	}
	rootReplacement := func() *Delta {
		return &Delta{frame: header, ops: []map[string]json.RawMessage{operation("replace", "key", from.RootKey(), "view", to.viewJSON())}}
	}
	fallback := false
	if !equalProps(from.root, to.root) {
		d = rootReplacement()
	} else {
		staged := from.root.clone()
		emit := func(op map[string]json.RawMessage) error {
			if err := reduce(staged, op); err != nil {
				return err
			}
			if err := checkTopology(staged); err != nil {
				return err
			}
			d.ops = append(d.ops, op)
			if len(d.ops) > OperationsMost {
				return errors.New("operation budget exceeded")
			}
			return nil
		}
		if err := diffChildren(staged, to.root, emit); err != nil || !bytes.Equal(staged.json(), to.viewJSON()) {
			d = rootReplacement()
			fallback = true
		}
	}
	raw, err := d.MarshalJSON()
	if err != nil {
		return nil, false, err
	}
	if len(raw) > FrameMost {
		d = rootReplacement()
		fallback = true
		raw, err = d.MarshalJSON()
		if err != nil {
			return nil, false, err
		}
		if len(raw) > FrameMost {
			return nil, true, nil
		}
	}
	// Use the same raw boundary as received frames (including nested shape checks).
	checked, err := DecodeDelta(raw)
	if err != nil {
		return nil, false, err
	}
	snapshot, _ := to.MarshalJSON()
	if err := reproduces(from, checked, to, snapshot); err != nil {
		return nil, false, err
	}
	return checked, fallback || len(raw) >= len(snapshot), nil
}

// reproduces replays d on from and requires the result to be to, whose frame
// is snapshot. A replay whose frame is snapshot byte for byte is to's twin,
// which DecodeSnapshot already checked (check reads only the frame and the
// tree its view describes), so it is not checked again; any other replay is
// checked as Apply checks it before its view is compared.
func reproduces(from *Tree, d *Delta, to *Tree, snapshot []byte) error {
	result, err := replay(from, d)
	if err != nil {
		return err
	}
	result.view = result.root.json()
	if frame, _ := result.MarshalJSON(); bytes.Equal(frame, snapshot) {
		return nil
	}
	result.view = nil // check fills it in
	if err := result.check(nil); err != nil {
		return rejected(err)
	}
	if !bytes.Equal(result.viewJSON(), to.viewJSON()) {
		return errors.New("diff did not reproduce target")
	}
	return nil
}

func diffChildren(staged, target *node, emit func(map[string]json.RawMessage) error) error {
	// Remove disappearing components before allocating replacements/new sections.
	// Retained components stay available to move out of a disappearing section.
	desired := map[string]*node{}
	for _, s := range target.children {
		for _, c := range s.children {
			desired[c.key] = c
		}
	}
	for _, s := range append([]*node(nil), staged.children...) {
		for _, c := range append([]*node(nil), s.children...) {
			if desired[c.key] == nil {
				if err := emit(operation("remove", "key", c.key)); err != nil {
					return err
				}
			}
		}
	}
	for _, s := range target.children {
		found, _, _ := staged.find(s.key)
		if found == nil {
			shell := &node{key: s.key, kind: s.kind, props: s.props}
			if err := emit(operation("insert", "parent", staged.key, "section", shell.json())); err != nil {
				return err
			}
		} else if found.kind != "section" {
			return errors.New("key changed topology kind")
		}
	}
	// Reverse traversal plus before-sibling addresses gives deterministic ordering,
	// while unchanged adjacency needs no operation.
	for _, s := range target.children {
		before := ""
		for i := len(s.children) - 1; i >= 0; i-- {
			desired := s.children[i]
			current, parent, index := staged.find(desired.key)
			if current == nil {
				op := operation("insert", "parent", s.key, "item", desired.json())
				if before != "" {
					op["before"] = mustJSON(before)
				}
				if err := emit(op); err != nil {
					return err
				}
			} else {
				if current.kind != "item" {
					return errors.New("key changed topology kind")
				}
				adjacent := parent.key == s.key && (before == "" && index == len(parent.children)-1 || before != "" && index+1 < len(parent.children) && parent.children[index+1].key == before)
				if !adjacent {
					op := operation("move", "key", current.key, "parent", s.key)
					if before != "" {
						op["before"] = mustJSON(before)
					}
					if err := emit(op); err != nil {
						return err
					}
				}
				if !bytes.Equal(current.json(), desired.json()) {
					if err := emit(operation("replace", "key", current.key, "item", desired.json())); err != nil {
						return err
					}
				}
			}
			before = desired.key
		}
	}
	desiredSections := map[string]bool{}
	for _, s := range target.children {
		desiredSections[s.key] = true
	}
	for _, s := range append([]*node(nil), staged.children...) {
		if !desiredSections[s.key] {
			if err := emit(operation("remove", "key", s.key)); err != nil {
				return err
			}
		}
	}
	before := ""
	for i := len(target.children) - 1; i >= 0; i-- {
		desired := target.children[i]
		current, parent, index := staged.find(desired.key)
		if !equalProps(current, desired) {
			replacement := &node{key: desired.key, kind: desired.kind, props: desired.props, children: current.children}
			if err := emit(operation("replace", "key", current.key, "section", replacement.json())); err != nil {
				return err
			}
		}
		adjacent := before == "" && index == len(parent.children)-1 || before != "" && index+1 < len(parent.children) && parent.children[index+1].key == before
		if !adjacent {
			op := operation("move", "key", desired.key, "parent", staged.key)
			if before != "" {
				op["before"] = mustJSON(before)
			}
			if err := emit(op); err != nil {
				return err
			}
		}
		before = desired.key
	}
	return nil
}
func equalProps(a, b *node) bool { return bytes.Equal(mustJSON(a.props), mustJSON(b.props)) }
func operation(op string, pairs ...any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{"op": mustJSON(op)}
	for i := 0; i < len(pairs); i += 2 {
		if raw, ok := pairs[i+1].(json.RawMessage); ok {
			out[pairs[i].(string)] = raw
		} else {
			out[pairs[i].(string)] = mustJSON(pairs[i+1])
		}
	}
	return out
}
