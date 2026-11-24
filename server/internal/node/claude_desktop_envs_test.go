package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func claudeEnvironmentFixture(id, name, cwd string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "createdAt": 1, "lastUsedAt": 2,
		"target": map[string]any{"kind": "local", "cwd": cwd},
	}
}

func writeClaudeEnvironmentFixture(t *testing.T, dir string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ccd-session-presets.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadClaudeDesktopEnvironmentsNativeCatalog(t *testing.T) {
	dir := t.TempDir()
	cwd, extra := filepath.Join(dir, "project"), filepath.Join(dir, "library")
	first := claudeEnvironmentFixture("shared-id", "  Named project 中文  ", cwd)
	first["target"] = map[string]any{"kind": "local", "cwd": cwd,
		"additionalFolders": []string{extra, extra, cwd, "relative", ""}}
	writeClaudeEnvironmentFixture(t, dir, map[string]any{"byAccount": map[string]any{
		"account-b": map[string]any{"presets": []any{claudeEnvironmentFixture("shared-id", "Other account", cwd)}},
		"account-a": map[string]any{"presets": []any{first, claudeEnvironmentFixture("shared-id", "Duplicate", cwd)},
			"builtIns": map[string]any{"local": map[string]any{}}, "seededFromDraft": true},
	}})
	got, err := readClaudeDesktopEnvironments(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []claudeDesktopEnvironment{
		{ID: "shared-id", Name: "Named project 中文", AccountID: "account-a", Cwd: cwd, AdditionalFolders: []string{extra}},
		{ID: "shared-id", Name: "Other account", AccountID: "account-b", Cwd: cwd},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native environments = %#v, want %#v", got, want)
	}
}

func TestReadClaudeDesktopEnvironmentsSkipsUnsupportedAndMalformedRows(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "project")
	remote := claudeEnvironmentFixture("ssh", "SSH", cwd)
	remote["target"] = map[string]any{"kind": "ssh", "cwd": cwd, "sshConfigId": "host"}
	cloud := claudeEnvironmentFixture("cloud", "Cloud", cwd)
	cloud["target"] = map[string]any{"kind": "cloud", "worker": map[string]any{"type": "pool", "id": "pool"}, "repos": []any{}}
	wsl := claudeEnvironmentFixture("wsl", "WSL", cwd)
	wsl["target"] = map[string]any{"kind": "local", "cwd": cwd, "wslDistro": "Ubuntu"}
	badFolders := claudeEnvironmentFixture("bad-folders", "Bad folders", cwd)
	badFolders["target"] = map[string]any{"kind": "local", "cwd": cwd, "additionalFolders": []any{3}}
	missingTimestamp := claudeEnvironmentFixture("timestamp", "Missing timestamp", cwd)
	delete(missingTimestamp, "createdAt")
	wrongTimestamp := claudeEnvironmentFixture("wrong-time", "Bad timestamp", cwd)
	wrongTimestamp["lastUsedAt"] = "yesterday"
	writeClaudeEnvironmentFixture(t, dir, map[string]any{"byAccount": map[string]any{
		"broken-account": []any{},
		"empty-account":  map[string]any{},
		" ":              map[string]any{"presets": []any{claudeEnvironmentFixture("hidden", "Blank account", cwd)}},
		"account": map[string]any{"presets": []any{
			remote, cloud, wsl, badFolders, missingTimestamp, wrongTimestamp, nil, true,
			claudeEnvironmentFixture("", "Missing ID", cwd),
			claudeEnvironmentFixture(strings.Repeat("x", 129), "Long ID", cwd),
			claudeEnvironmentFixture("blank-name", " \t\n", cwd),
			claudeEnvironmentFixture("relative", "Relative", "relative"),
			claudeEnvironmentFixture("folderless", "Folderless", ""),
			claudeEnvironmentFixture("valid", "Saved name", cwd),
		}},
	}})
	got, err := readClaudeDesktopEnvironments(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []claudeDesktopEnvironment{{ID: "valid", Name: "Saved name", AccountID: "account", Cwd: cwd}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("valid local environments = %#v, want %#v", got, want)
	}
}

func TestReadClaudeDesktopEnvironmentsDistinguishesMissingEmptyAndInvalid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		missing bool
		wantErr bool
	}{
		{name: "missing", missing: true},
		{name: "initial store", body: `{}`},
		{name: "empty catalog", body: `{"byAccount":{}}`},
		{name: "empty account", body: `{"byAccount":{"account":{"presets":[]}}}`},
		{name: "corrupt", body: `{"byAccount":`, wantErr: true},
		{name: "null", body: `null`, wantErr: true},
		{name: "wrong map type", body: `{"byAccount":[]}`, wantErr: true},
		{name: "renderer version envelope", body: `{"state":{"presets":[]},"version":4}`, wantErr: true},
		{name: "oversized", body: strings.Repeat(" ", maxClaudeDesktopEnvironmentBytes+1), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.missing {
				if err := os.WriteFile(filepath.Join(dir, "ccd-session-presets.json"), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readClaudeDesktopEnvironments(dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && (got == nil) != tc.missing {
				t.Fatalf("environments = %#v, missing %v", got, tc.missing)
			}
		})
	}
	if _, err := readClaudeDesktopEnvironments("relative"); err == nil {
		t.Fatal("relative profile directory was accepted")
	}
}
