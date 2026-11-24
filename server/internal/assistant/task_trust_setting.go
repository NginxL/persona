package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// tasks.trust in persona_settings (#8095): the kinds of tasks that start without a plan card, each one computer, one
// directory with its subdirectories (task_trust_scope.go) and read or edit. get lists them; add and remove change one
// once the user approves on the phone, as every other setting. The plan card's "Start these directly" and a rule
// suggestion's Always still remember the kind of the plan in front of the user.

// trustSettingValue is add's and remove's value.
type trustSettingValue struct {
	Machine   string `json:"machine"`
	Directory string `json:"directory"`
	Access    string `json:"access"` // edit (the default) | read
}

func (s *Service) trustSetting() ownSetting {
	return ownSetting{Setting: Setting{Key: trustSetting, Title: "Tasks that start without asking", Actions: []string{"get", "add", "remove"},
		About: "the kinds of task_plan tasks that start at once instead of waiting for Start on a plan card: one computer, one " +
			"directory and every directory under it, reading or changing files. The plan card's \"Start these directly\" adds the kind " +
			"in front of the user; add one here only when the user asked for it (name the computer and the folder they named). " +
			"add refuses \"/\", a directory right under it and the computer's home (an older entry for one covers only that exact directory)",
		Value: `add / remove: {"machine": "<computer id or name>", "directory": "<absolute path or ~/…>", "access": "edit" (the default) or "read"}`},
		get: s.getTrustSetting, change: s.changeTrustSetting}
}

// getTrustSetting lists the remembered kinds as the assistant reads them.
func (s *Service) getTrustSetting(ctx context.Context) (string, error) {
	have, err := s.trustedKindsErr(ctx)
	if err != nil {
		return "", err
	}
	if len(have) == 0 {
		return "No kind of task is remembered: every task that changes files waits for Start on its plan card.", nil
	}
	homes := s.trustHomes(ctx)
	keys := make([]string, 0, len(have))
	for k := range have {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString("Tasks that start without asking (machine, directory, access):\n")
	for _, k := range keys {
		machine, dir, mode, ok := splitTrustKey(k)
		if !ok {
			fmt.Fprintf(&b, "- %q: only a task whose machine|directory|access is exactly this\n", k)
			continue
		}
		scope := "and every directory under it"
		if d := trustDir(dir, homes[machine]); d == "" || !trustTreeDir(d, homes[machine]) {
			scope = "only this exact directory"
		}
		fmt.Fprintf(&b, "- %s %s (%s), %s\n", machine, dir, scope, mode)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// changeTrustSetting is add or remove of one kind, waiting for the phone.
func (s *Service) changeTrustSetting(ctx context.Context, action string, raw json.RawMessage) (*ownChange, error) {
	var v trustSettingValue
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errors.New(`value must be {"machine", "directory", "access"}`)
	}
	switch v.Access = strings.TrimSpace(v.Access); v.Access {
	case "":
		v.Access = "edit"
	case "edit", "read":
	default:
		return nil, errors.New(`access must be "edit" or "read"`)
	}
	m, ok := s.machineByName(ctx, strings.TrimSpace(v.Machine))
	if !ok {
		return nil, fmt.Errorf("unknown machine %q (machines: %s)", v.Machine, s.machineIDs(ctx))
	}
	h := trustHome{home: m.Home, real: m.HomeReal}
	dir := trustDir(v.Directory, h)
	if dir == "" {
		return nil, fmt.Errorf("directory %q must be an absolute path or start with ~/", v.Directory)
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	have, err := s.trustedKindsErr(ctx)
	if err != nil {
		return nil, err
	}
	name := m.Name
	if name == "" {
		name = m.ID
	}
	key := m.ID + "|" + dir + "|" + v.Access
	args := i18n.Args{"machine": name, "directory": dir}
	if action == "add" {
		if !trustTreeDir(dir, h) {
			return nil, fmt.Errorf("%s is too broad: name a project's folder, not /, a directory right under it or the home", dir)
		}
		if trustCovers(have, s.trustHomes(ctx), key) {
			return &ownChange{summary: fmt.Sprintf("Already remembered: %s tasks on %s in %s start without asking.", v.Access, name, dir)}, nil
		}
		return &ownChange{cardText: func(l i18n.Lang) string {
			return i18n.T(l, "assistant-settings_tool.change_task_trust_add_"+v.Access, args)
		}, apply: func(ctx context.Context) (string, error) {
			if err := s.editTrust(ctx, func(have map[string]bool) { have[key] = true }); err != nil {
				return "", err
			}
			return fmt.Sprintf("Remembered: tasks that %s files on %s in %s and every directory under it start without asking.",
				map[string]string{"edit": "change", "read": "read"}[v.Access], name, dir), nil
		}}, nil
	}
	// remove: every way the same directory was remembered on that machine with that access
	match := func(k string) bool {
		km, kd, md, ok := splitTrustKey(k)
		return ok && km == m.ID && md == v.Access && trustDirEqual(trustDir(kd, h), dir)
	}
	n := 0
	for k := range have {
		if match(k) {
			n++
		}
	}
	if n == 0 {
		return &ownChange{summary: fmt.Sprintf("Not remembered: no %s entry on %s for %s (get lists them).", v.Access, name, dir)}, nil
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_task_trust_remove_"+v.Access, args)
	}, apply: func(ctx context.Context) (string, error) {
		if err := s.editTrust(ctx, func(have map[string]bool) {
			for k := range have {
				if match(k) {
					delete(have, k)
				}
			}
		}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Forgotten: %s tasks on %s in %s wait for Start again (unless another entry holds them).", v.Access, name, dir), nil
	}}, nil
}

// editTrust changes tasks.trust under trustMu, reading it again first; a failed read changes nothing.
func (s *Service) editTrust(ctx context.Context, edit func(have map[string]bool)) error {
	st := s.store()
	if st == nil {
		return errors.New("no store")
	}
	s.tasks.trustMu.Lock()
	defer s.tasks.trustMu.Unlock()
	have, err := s.trustedKindsErr(ctx)
	if err != nil {
		return err
	}
	edit(have)
	keys := make([]string, 0, len(have))
	for k := range have {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return st.PutSetting(ctx, trustSetting, keys)
}
