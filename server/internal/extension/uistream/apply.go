package uistream

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Apply stages a complete batch on a private topology copy. Any failure leaves
// both inputs untouched and returns ErrSnapshotRequired. Nonempty batches advance
// exactly one version; empty batches may retain it or advance exactly one as an
// accepted-event fence. They never encode a root replacement just for the fence.
func Apply(t *Tree, d *Delta) (*Tree, error) {
	next, err := replay(t, d)
	if err != nil {
		return nil, err
	}
	if err := next.check(nil); err != nil {
		return nil, rejected(err)
	}
	return next, nil
}

// replay is Apply without the final check of the whole result: the delta's
// identity, version and every operation are still checked as they apply.
func replay(t *Tree, d *Delta) (*Tree, error) {
	if t == nil || t.root == nil || d == nil {
		return nil, rejected(errors.New("missing tree or delta"))
	}
	if d.frame.Session != t.Session() || d.frame.Epoch != t.Epoch() || d.frame.BaseVersion != t.Version() {
		return nil, rejected(errors.New("delta identity or base mismatch"))
	}
	want := t.Version()
	if len(d.ops) > 0 || d.frame.Version != want {
		if want == VersionMost {
			return nil, rejected(errors.New("rotate epoch before version exhaustion"))
		}
		want++
	}
	if d.frame.Version != want {
		return nil, rejected(errors.New("delta version is not the next version"))
	}
	next := &Tree{header: t.header, root: t.root.clone()}
	next.header.Version = want
	for i, op := range d.ops {
		if err := reduce(next.root, op); err != nil {
			return nil, rejected(fmt.Errorf("operation %d: %w", i, err))
		}
		if err := checkTopology(next.root); err != nil {
			return nil, rejected(fmt.Errorf("operation %d: %w", i, err))
		}
	}
	return next, nil
}

func reduce(root *node, op map[string]json.RawMessage) error {
	kind, key := text(op["op"]), text(op["key"])
	switch kind {
	case wire.UIStreamOperationReplace:
		old, parent, index := root.find(key)
		if old == nil || old.kind == "tile" {
			return errors.New("replace target missing or a tile")
		}
		payload := operationNode(op, root.key)
		if payload == nil || payload.kind != old.kind || payload.key != key {
			return errors.New("replacement key or kind mismatch")
		}
		if parent == nil {
			*root = *payload
		} else {
			parent.children[index] = payload
		}
	case wire.UIStreamOperationInsert:
		payload := operationNode(op, root.key)
		if payload == nil {
			return errors.New("insert payload missing")
		}
		parent, _, _ := root.find(text(op["parent"]))
		if !accepts(parent, payload) {
			return errors.New("wrong insert parent or payload kind")
		}
		index, err := beforeIndex(parent, text(op["before"]))
		if err != nil {
			return err
		}
		parent.children = insertAt(parent.children, index, payload)
	case wire.UIStreamOperationRemove:
		old, parent, index := root.find(key)
		if old == nil || parent == nil || old.kind == "tile" {
			return errors.New("remove target missing, root or tile")
		}
		parent.children = removeAt(parent.children, index)
	case wire.UIStreamOperationMove:
		old, parent, index := root.find(key)
		if old == nil || parent == nil || old.kind == "tile" {
			return errors.New("move target missing, root or tile")
		}
		dest, _, _ := root.find(text(op["parent"]))
		if !accepts(dest, old) {
			return errors.New("wrong move destination")
		}
		if descendant, _, _ := old.find(dest.key); descendant != nil {
			return errors.New("cyclic move")
		}
		before := text(op["before"])
		if before == key {
			return errors.New("move before itself")
		}
		if _, err := beforeIndex(dest, before); err != nil {
			return err
		}
		parent.children = removeAt(parent.children, index)
		to, err := beforeIndex(dest, before)
		if err != nil {
			return err
		}
		dest.children = insertAt(dest.children, to, old)
	default:
		return errors.New("unknown operation")
	}
	return nil
}
func operationNode(op map[string]json.RawMessage, rootKey string) *node {
	for _, kind := range []string{"view", "section", "item"} {
		if raw, ok := op[kind]; ok {
			return parseNode(raw, kind, rootKey)
		}
	}
	return nil
}
func accepts(parent, child *node) bool {
	return parent != nil && child != nil && (parent.kind == "view" && child.kind == "section" || parent.kind == "section" && child.kind == "item")
}
func beforeIndex(parent *node, before string) (int, error) {
	if before == "" {
		return len(parent.children), nil
	}
	for i, c := range parent.children {
		if c.key == before {
			return i, nil
		}
	}
	return 0, errors.New("before is not a destination sibling")
}
func insertAt(children []*node, index int, n *node) []*node {
	children = append(children, nil)
	copy(children[index+1:], children[index:])
	children[index] = n
	return children
}
func removeAt(children []*node, index int) []*node {
	copy(children[index:], children[index+1:])
	children[len(children)-1] = nil
	return children[:len(children)-1]
}
func checkTopology(root *node) error {
	seen := map[string]bool{}
	count := 0
	var visit func(*node, int) error
	visit = func(n *node, depth int) error {
		count++
		if count > NodesMost || depth > DepthMost {
			return errors.New("staged tree exceeds bounds")
		}
		if seen[n.key] {
			return fmt.Errorf("duplicate key %q", n.key)
		}
		seen[n.key] = true
		for _, c := range n.children {
			if err := visit(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root, 1)
}
