package assistantskills

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func skillZIP(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const importManifest = "---\nname: example-skill\ndescription: A synthetic skill.\n---\n# Example\n"

// realTempDir is a temp directory without symlinks in its path: the importer reports the resolved destination, and on
// macOS t.TempDir() is under a symlink.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func importFixture(t *testing.T) (*Importer, ImportTarget) {
	t.Helper()
	t.Setenv("PERSONA_SKILLS_DIR", filepath.Join(realTempDir(t), "skills"))
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	return &Importer{}, ImportTarget{Scope: "example-manager", Cwd: t.TempDir()}
}

func importErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *ImportError
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func assertNoImportStaging(t *testing.T) {
	t.Helper()
	entries, _ := os.ReadDir(UserDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".import-") {
			t.Fatalf("staging leaked: %s", e.Name())
		}
	}
}

func TestImportPreviewInstallListAndRuntimeSync(t *testing.T) {
	for _, prefix := range []string{"", "package/"} {
		t.Run(prefix, func(t *testing.T) {
			i, target := importFixture(t)
			archive := skillZIP(t, zipEntry{prefix + "SKILL.md", importManifest, 0},
				zipEntry{prefix + "scripts/check.sh", "#!/bin/sh\nexit 97\n", 0o755},
				zipEntry{prefix + "references/notes.txt", "resource", 0})
			preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
			if err != nil {
				t.Fatal(err)
			}
			if preview.Name != "example-skill" || preview.Description != "A synthetic skill." ||
				preview.Destination != filepath.Join(UserDir(), "example-skill") || preview.Files != 3 || preview.Reference == "" {
				t.Fatalf("preview: %+v", preview)
			}
			if hasSkill(UserDir(), preview.Name) {
				t.Fatal("preview installed the skill")
			}
			assertNoImportStaging(t)
			installed, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference})
			if err != nil {
				t.Fatal(err)
			}
			if installed.Reference == "" || installed.Reference == preview.Reference || !hasSkill(UserDir(), preview.Name) {
				t.Fatalf("install: %+v", installed)
			}
			if !slices.ContainsFunc(List(UserDir(), nil), func(s Skill) bool { return s.Name == preview.Name && s.Source == "user" }) {
				t.Fatal("new skill missing from existing list")
			}
			// Contract smoke: use the same Sync as the existing Claude and Codex provisioning paths.
			for _, runtime := range []string{".claude", ".agents"} {
				dir := filepath.Join(target.Cwd, runtime, "skills")
				if err := Sync(dir, UserDir(), nil, quiet); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(dir, preview.Name, "references", "notes.txt"))
				if err != nil || string(data) != "resource" {
					t.Fatalf("%s resource: %q %v", runtime, data, err)
				}
			}
			undone, err := i.Import(context.Background(), target, ImportRequest{Op: "undo", Reference: installed.Reference})
			if err != nil {
				t.Fatal(err)
			}
			if hasSkill(UserDir(), preview.Name) || undone.Recovery == "" {
				t.Fatal("Undo did not detach the new installation into recovery")
			}
			if data, err := os.ReadFile(filepath.Join(undone.Recovery, "SKILL.md")); err != nil || string(data) != importManifest {
				t.Fatal("Undo did not preserve recoverable data")
			}
			assertNoImportStaging(t)
		})
	}
}

func TestImportRejectsManifestAndAmbiguousLayouts(t *testing.T) {
	cases := []struct{ name, md, code string }{
		{"missing name", "---\ndescription: x\n---\n", "skill_manifest_name"},
		{"empty name", "---\nname: ' '\ndescription: x\n---\n", "skill_manifest_name"},
		{"numeric name", "---\nname: 123\ndescription: x\n---\n", "skill_manifest_name"},
		{"missing description", "---\nname: example\n---\n", "skill_manifest_description"},
		{"empty description", "---\nname: example\ndescription: ''\n---\n", "skill_manifest_description"},
		{"object description", "---\nname: example\ndescription: {a: b}\n---\n", "skill_manifest_description"},
		{"boolean description", "---\nname: example\ndescription: true\n---\n", "skill_manifest_description"},
		{"no frontmatter", "# Example", "skill_manifest_yaml"},
		{"unclosed frontmatter", "---\nname: example\n", "skill_manifest_yaml"},
		{"malformed YAML", "---\nname: [\n---\n", "skill_manifest_yaml"},
		{"duplicate key", "---\nname: example\nname: other\ndescription: x\n---\n", "skill_manifest_yaml"},
		{"alias", "---\nname: &name example\ndescription: *name\n---\n", "skill_manifest_yaml"},
		{"invalid UTF8", "---\nname: example\ndescription: \xff\n---\n", "skill_manifest_utf8"},
		{"traversal name", "---\nname: ../example\ndescription: x\n---\n", "skill_name_unsafe"},
		{"hidden name", "---\nname: .example\ndescription: x\n---\n", "skill_name_unsafe"},
		{"Windows reserved name", "---\nname: con\ndescription: x\n---\n", "skill_name_unsafe"},
		{"control name", "---\nname: \"a\\u0001b\"\ndescription: x\n---\n", "skill_name_unsafe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, target := importFixture(t)
			_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: skillZIP(t, zipEntry{"SKILL.md", tc.md, 0})})
			importErrorCode(t, err, tc.code)
			assertNoImportStaging(t)
		})
	}
	for _, names := range [][]string{{"skill.md"}, {"Skill.md"}, {"a/b/SKILL.md"}, {"a/SKILL.md", "b/SKILL.md"},
		{"SKILL.md", "a/SKILL.md"}, {"a/SKILL.md", "outside.txt"}, {"SKILL.md", "nested/skill.md"}} {
		t.Run(strings.Join(names, "+"), func(t *testing.T) {
			i, target := importFixture(t)
			var entries []zipEntry
			for _, name := range names {
				entries = append(entries, zipEntry{name, importManifest, 0})
			}
			_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: skillZIP(t, entries...)})
			importErrorCode(t, err, "skill_manifest_layout")
			assertNoImportStaging(t)
		})
	}
}

func TestImportRejectsArchiveHazards(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/drive", "C:relative", "//host/share", `\\host\share`,
		`folder\file`, "a/../b", "./file", "a//b", "a/./b", "file.", "file ", "NUL", "x:stream", "a/\x00b"} {
		t.Run(name, func(t *testing.T) {
			i, target := importFixture(t)
			archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0}, zipEntry{name, "untrusted", 0})
			_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
			importErrorCode(t, err, "skill_zip_path")
			assertNoImportStaging(t)
		})
	}
	for _, entries := range [][]zipEntry{
		{{"SKILL.md", importManifest, 0}, {"SKILL.md", importManifest, 0}},
		{{"SKILL.md", importManifest, 0}, {"notes", "", 0}, {"NOTES", "", 0}},
		{{"SKILL.md", importManifest, 0}, {"a", "", 0}, {"a/file", "", 0}},
		{{"SKILL.md", importManifest, 0}, {"a/", "", os.ModeDir | 0o755}, {"A/file", "", 0}},
		{{"SKILL.md", importManifest, 0}, {"café", "", 0}, {"cafe\u0301", "", 0}},
	} {
		i, target := importFixture(t)
		_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: skillZIP(t, entries...)})
		importErrorCode(t, err, "skill_zip_path")
		assertNoImportStaging(t)
	}
	for _, mode := range []os.FileMode{os.ModeSymlink | 0o777, os.ModeNamedPipe | 0o600, os.ModeDevice | 0o600, os.ModeSocket | 0o600} {
		i, target := importFixture(t)
		_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview",
			Archive: skillZIP(t, zipEntry{"SKILL.md", importManifest, 0}, zipEntry{"special", "/outside", mode})})
		importErrorCode(t, err, "skill_zip_type")
		assertNoImportStaging(t)
	}
	i, target := importFixture(t)
	for _, bad := range [][]byte{[]byte("not a ZIP"), skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})[:40]} {
		_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: bad})
		importErrorCode(t, err, "skill_zip_invalid")
	}
}

func TestImportLimitsAndCancellation(t *testing.T) {
	i, target := importFixture(t)
	packages := [][]byte{
		make([]byte, MaxImportZIP+1),
		skillZIP(t, zipEntry{"SKILL.md", importManifest, 0}, zipEntry{"large", strings.Repeat("x", MaxImportFile+1), 0}),
		skillZIP(t, zipEntry{"SKILL.md", importManifest, 0}, zipEntry{strings.Repeat("a/", MaxImportDepth+1) + "deep", "", 0}),
	}
	var many []zipEntry
	for n := range MaxImportEntries + 1 {
		many = append(many, zipEntry{strings.Repeat("a", n+1), "", 0})
	}
	packages = append(packages, skillZIP(t, many...))
	for _, archive := range packages {
		_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
		importErrorCode(t, err, "skill_zip_limits")
		assertNoImportStaging(t)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := i.Import(ctx, target, ImportRequest{Op: "preview", Archive: skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	assertNoImportStaging(t)
}

func TestImportConflictsReferencesAndConcurrentPublication(t *testing.T) {
	i, target := importFixture(t)
	archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})
	preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "install", Reference: preview.Reference, Archive: append(archive, 0)})
	importErrorCode(t, err, "skill_import_changed")
	other := target
	other.Scope = "another-manager"
	_, err = i.Import(context.Background(), other, ImportRequest{Op: "install", Reference: preview.Reference, Archive: archive})
	importErrorCode(t, err, "skill_environment_changed")
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for range 8 {
		wg.Go(func() {
			_, e := i.Import(context.Background(), target, ImportRequest{Op: "install", Reference: preview.Reference, Archive: archive})
			if e == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("concurrent successes = %d, want 1", successes)
	}
	_, err = (&Importer{}).Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	importErrorCode(t, err, "skill_conflict")
	assertNoImportStaging(t)
	// Built-ins are protected even if gated off; plugin names may not be shadowed either.
	for _, name := range []string{"persona-office", "extension-skill"} {
		if name == "extension-skill" {
			skill(t, PluginDir(), name, "From a plugin")
		}
		md := strings.Replace(importManifest, "example-skill", name, 1)
		_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: skillZIP(t, zipEntry{"SKILL.md", md, 0})})
		importErrorCode(t, err, "skill_conflict")
	}
}

func TestImportCancelExpiryEnvironmentAndUndoChanges(t *testing.T) {
	i, target := importFixture(t)
	archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})
	preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "cancel", Reference: preview.Reference})
	if err != nil {
		t.Fatal(err)
	}
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "install", Reference: preview.Reference, Archive: archive})
	importErrorCode(t, err, "skill_import_expired")
	preview, _ = i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	i.refs[preview.Reference].expires = time.Now().Add(-time.Minute)
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "install", Reference: preview.Reference, Archive: archive})
	importErrorCode(t, err, "skill_import_expired")
	preview, _ = i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	old := UserDir()
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "install", Reference: preview.Reference, Archive: archive})
	importErrorCode(t, err, "skill_environment_changed")
	t.Setenv("PERSONA_SKILLS_DIR", old)
	installed, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Reference: preview.Reference, Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "example-skill", "SKILL.md"), []byte(importManifest+"Edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "undo", Reference: installed.Reference})
	importErrorCode(t, err, "skill_undo_changed")
	if !hasSkill(old, "example-skill") {
		t.Fatal("Undo removed an edited skill")
	}
	assertNoImportStaging(t)
}
