package managerconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// A manager's own list of computers (docs/assistant/managers.md "Each manager's computers"): the entries it connects,
// kept in its manager.json so that a manager starting while the hub is away connects them at once. They come from the
// hub's hand-over (devicewire.Config.Machines): the first one is taken over (MachinesFromHubKey records when), and as
// long as the hub edits the lists (adding, removing or moving a computer), each later hand-over is written over it.

// MachinesKey is the list in manager.json: each entry as the hub's hand-over had it (hub.json's form).
const MachinesKey = "machines"

// MachinesFromHubKey records that MachinesKey was taken over from the hub's hand-over, and what else of the hand-over
// connecting them needs before the hub answers. Without it the list is not read: the manager waits for the hub's.
const MachinesFromHubKey = "machines_from_hub"

// MachinesFromHub is MachinesFromHubKey's value.
type MachinesFromHub struct {
	// At is when the list was first taken over.
	At time.Time `json:"at"`
	// Mesh: the hub was in a mesh group at the last hand-over: which of its computers are its to connect, and when,
	// are the group's, so the manager waits for the hub rather than connect the list.
	Mesh bool `json:"mesh,omitempty"`
	// TailcatDERPMapURL is the hub's tailcat DERP map at the last hand-over (the nodes were set up with the same).
	TailcatDERPMapURL string `json:"tailcat_derp_map_url,omitempty"`
	// PrivateNetwork: the hub was in office network mode at the last hand-over, so no machine is dialed over tailcat
	// before it answers either.
	PrivateNetwork bool `json:"private_network,omitempty"`
	// HubID is the hub that handed it over: a manager that joined another hub since does not read it.
	HubID string `json:"hub_id,omitempty"`
	// Own: the list was taken over as the manager's own (the hub handed MachinesOwn): it is the list from then on, and
	// the hub edits it (PutMachine, RemoveMachine); a hand-over is no longer written over it.
	Own bool `json:"own,omitempty"`
}

// OwnMachines is the list file keeps and its record; ok false: none taken over yet, no file, or another hub's (the
// manager's hub_id is not the one that handed it over). A list that does not read (not an array of objects each with
// its own id) is an error.
func OwnMachines(file string) (list []json.RawMessage, from MachinesFromHub, ok bool, err error) {
	b, err := readConfig(file)
	if err != nil || b == nil {
		return nil, from, false, err
	}
	o, err := ParseObject(b)
	if err != nil {
		return nil, from, false, fmt.Errorf("%s: %w", file, err)
	}
	rawFrom, has := o.Get(MachinesFromHubKey)
	if !has {
		return nil, from, false, nil
	}
	if err := json.Unmarshal(rawFrom, &from); err != nil {
		return nil, from, false, fmt.Errorf("%s: %s: %w", file, MachinesFromHubKey, err)
	}
	var hubID string
	if raw, ok := o.Get("hub_id"); ok && json.Unmarshal(raw, &hubID) == nil && hubID != "" && from.HubID != "" && hubID != from.HubID {
		return nil, from, false, nil
	}
	raw, _ := o.Get(MachinesKey)
	if list, err = machineList(raw); err != nil {
		return nil, from, false, fmt.Errorf("%s: %s: %w", file, MachinesKey, err)
	}
	return list, from, true, nil
}

// machineList reads a list of entries: an array (null: none) of objects, each with an id of its own (readable's).
func machineList(raw json.RawMessage) ([]json.RawMessage, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out, bad := readable(list)
	if bad != nil {
		return nil, bad
	}
	return out, nil
}

// KeepMachines writes list (the hub's hand-over) and from into file when they are not what it has; from.At is the
// first time's. Entries that do not read (no id, or an id twice) are left out, and said in skipped. A list there that
// does not read is written over (the hub's is the list), and said so in replaced.
func KeepMachines(file string, list []json.RawMessage, from MachinesFromHub) (changed bool, skipped, replaced error, err error) {
	list, skipped = readable(list)
	err = editIf(file, func(o *Object) (bool, error) {
		var was MachinesFromHub
		if raw, ok := o.Get(MachinesFromHubKey); ok {
			if json.Unmarshal(raw, &was) == nil && !was.At.IsZero() {
				from.At = was.At
			}
		}
		if from.At.IsZero() {
			from.At = time.Now().UTC()
		}
		if raw, ok := o.Get(MachinesKey); ok {
			if _, err := machineList(raw); err != nil {
				replaced = err
			}
		}
		newList, err := compactList(list)
		if err != nil {
			return false, err
		}
		newFrom, err := MarshalNoEscape(from)
		if err != nil {
			return false, err
		}
		oldList, _ := o.Get(MachinesKey)
		oldFrom, _ := o.Get(MachinesFromHubKey)
		if sameJSON(oldList, newList) && sameJSON(oldFrom, newFrom) {
			return false, nil
		}
		o.SetRaw(MachinesKey, newList)
		o.SetRaw(MachinesFromHubKey, newFrom)
		changed = true
		return true, nil
	})
	return changed, skipped, replaced, err
}

// readable is list without the entries that do not read (not an object, no id, an id twice), and what was left out.
func readable(list []json.RawMessage) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(list))
	seen := map[string]bool{}
	var bad []string
	for i, e := range list {
		var head struct {
			ID string `json:"id"`
		}
		b := bytes.TrimSpace(e)
		var keys map[string]json.RawMessage
		switch {
		case len(b) == 0 || b[0] != '{' || !json.Valid(b) || json.Unmarshal(b, &keys) != nil ||
			json.Unmarshal(keys["id"], &head.ID) != nil || strings.TrimSpace(head.ID) != head.ID || head.ID == "":
			bad = append(bad, fmt.Sprintf("entry %d is not a machine with an id", i))
		case seen[head.ID]:
			bad = append(bad, fmt.Sprintf("machine %q is listed twice", head.ID))
		default:
			seen[head.ID] = true
			out = append(out, b)
		}
	}
	if len(bad) > 0 {
		return out, errors.New(strings.Join(bad, "; "))
	}
	return out, nil
}

// compactList is list as one JSON array, each entry compact.
func compactList(list []json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, e := range list {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := json.Compact(&b, e); err != nil {
			return nil, err
		}
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

// ErrNotOwnList: the list has not been taken over as the manager's own (MachinesFromHub.Own), so there is none to
// edit.
var ErrNotOwnList = errors.New("no list of computers taken over as this manager's own")

// EntryOf is entry's id, an error when it is no entry a list keeps (readable's rule: an object with a lowercase "id"
// of its own, not padded).
func EntryOf(entry json.RawMessage) (string, error) {
	ok, bad := readable([]json.RawMessage{entry})
	if bad != nil || len(ok) != 1 {
		return "", errors.New("not a machine with an id")
	}
	return entryID(ok[0]), nil
}

// PutMachine puts entry in file's list: added at the end, or in place of the one with its id; out is the list then.
// The list must have been taken over as the manager's own, and read.
func PutMachine(file string, entry json.RawMessage) (out []json.RawMessage, err error) {
	ok, bad := readable([]json.RawMessage{entry})
	if bad != nil || len(ok) != 1 {
		return nil, errors.New("not a machine with an id")
	}
	entry = ok[0]
	id := entryID(entry)
	err = editIf(file, func(o *Object) (bool, error) {
		list, err := takenOverList(o)
		if err != nil {
			return false, err
		}
		defer func() { out = list }()
		put := false
		for i, e := range list {
			if entryID(e) == id {
				list[i], put = entry, true
			}
		}
		if !put {
			list = append(list, entry)
		}
		raw, err := compactList(list)
		if err != nil {
			return false, err
		}
		old, _ := o.Get(MachinesKey)
		if sameJSON(old, raw) {
			return false, nil
		}
		o.SetRaw(MachinesKey, raw)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveMachine takes the machine with id out of file's list; out is the list then, removed: it had it.
func RemoveMachine(file, id string) (out []json.RawMessage, removed bool, err error) {
	err = editIf(file, func(o *Object) (bool, error) {
		list, err := takenOverList(o)
		if err != nil {
			return false, err
		}
		out = make([]json.RawMessage, 0, len(list))
		for _, e := range list {
			if entryID(e) == id {
				removed = true
				continue
			}
			out = append(out, e)
		}
		if !removed {
			return false, nil
		}
		raw, err := compactList(out)
		if err != nil {
			return false, err
		}
		o.SetRaw(MachinesKey, raw)
		return true, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, removed, nil
}

// takenOverList is o's list, an error when it was not taken over as the manager's own or does not read (an edit would
// lose it).
func takenOverList(o *Object) ([]json.RawMessage, error) {
	var from MachinesFromHub
	if raw, ok := o.Get(MachinesFromHubKey); !ok || json.Unmarshal(raw, &from) != nil || !from.Own {
		return nil, ErrNotOwnList
	}
	raw, _ := o.Get(MachinesKey)
	list, err := machineList(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MachinesKey, err)
	}
	return list, nil
}

// entryID is an entry's id ("" when it has none).
func entryID(e json.RawMessage) string {
	var head struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(e, &head)
	return head.ID
}

// SetAsideOtherHubs keeps a copy of file's own list beside it when it is another hub's than hubID (the manager
// joined another hub, or the hub's id changed: its database made again) and about to be replaced: path is the copy
// (manager.json.machines-<time>, 0600: the entries' secrets with it), "" when there was nothing to keep.
func SetAsideOtherHubs(file, hubID string) (path string, err error) {
	b, err := readConfig(file)
	if err != nil || b == nil {
		return "", err
	}
	o, err := ParseObject(b)
	if err != nil {
		return "", err
	}
	rawFrom, ok := o.Get(MachinesFromHubKey)
	if !ok {
		return "", nil
	}
	var from MachinesFromHub
	if json.Unmarshal(rawFrom, &from) != nil || !from.Own || from.HubID == "" || hubID == "" || from.HubID == hubID {
		return "", nil
	}
	list, _ := o.Get(MachinesKey)
	var entries []json.RawMessage
	if json.Unmarshal(list, &entries) != nil || len(entries) == 0 {
		return "", nil
	}
	aside := &Object{vals: map[string]json.RawMessage{}}
	aside.SetRaw(MachinesKey, list)
	aside.SetRaw(MachinesFromHubKey, rawFrom)
	out, err := aside.Indented()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(file); err == nil { // beside the file itself, as editIf writes it
		file = real
	}
	path = file + ".machines-" + time.Now().UTC().Format("20060102T150405Z")
	return path, WriteFileAtomic(path, out, 0o600)
}
