package assistantskills

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestImportUsesExistingDefaultAndConfiguredUserDir(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PERSONA_SKILLS_DIR", "")
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	for _, root := range []string{filepath.Join(home, ".persona"), filepath.Join(realTempDir(t), "custom-state")} {
		if root != filepath.Join(home, ".persona") {
			t.Setenv("PERSONA_HOME", root)
		}
		i := &Importer{}
		target := ImportTarget{Scope: "example-manager", Cwd: t.TempDir()}
		archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})
		preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
		if err != nil || preview.Destination != filepath.Join(root, "skills", "example-skill") {
			t.Fatalf("resolver: %+v, %v", preview, err)
		}
		if _, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportPreservesEmptyResourcesAndRejectsPortablePathAliases(t *testing.T) {
	i, target := importFixture(t)
	archive := skillZIP(t, zipEntry{"package/SKILL.md", importManifest, 0},
		zipEntry{"package/resources/empty/", "", os.ModeDir | 0o755})
	preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(installed.Destination, "resources", "empty"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty resource directory: %v", err)
	}
	for _, name := range []string{"COM¹.txt", "lpt²", "conin$", strings.Repeat("a", 256)} {
		_, err := inspectArchive(context.Background(), skillZIP(t, zipEntry{"SKILL.md", importManifest, 0}, zipEntry{name, "", 0}))
		importErrorCode(t, err, "skill_zip_path")
	}
}

func TestImportYAMLDescriptionMatchesExistingList(t *testing.T) {
	for _, field := range []string{`"description": "First\nsecond"`, "description: >-\n  First\n  second"} {
		i, target := importFixture(t)
		archive := skillZIP(t, zipEntry{"SKILL.md", "---\nname: example-skill\n" + field + "\n---\n", 0})
		preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference}); err != nil {
			t.Fatal(err)
		}
		list := List(UserDir(), nil)
		if len(list) == 0 || list[0].Description != "First second" {
			t.Fatalf("YAML list description: %+v", list)
		}
	}
}

func TestImportAtomicPublicationAcrossImporters(t *testing.T) {
	i, target := importFixture(t)
	j := &Importer{}
	archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})
	a, _ := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	b, _ := j.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	hook := func() { ready <- struct{}{}; <-release }
	i.beforePublish, j.beforePublish = hook, hook
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, job := range []struct {
		i   *Importer
		ref string
	}{{i, a.Reference}, {j, b.Reference}} {
		wg.Go(func() {
			_, err := job.i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: job.ref})
			errs <- err
		})
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			importErrorCode(t, err, "skill_conflict")
		}
	}
	if success != 1 {
		t.Fatalf("atomic publication successes = %d", success)
	}
	assertNoImportStaging(t)
}

func TestImportCancellationAndExistingEmptyDirectoryAtCommit(t *testing.T) {
	for _, scenario := range []string{"disconnect", "empty directory", "environment removed"} {
		t.Run(scenario, func(t *testing.T) {
			i, target := importFixture(t)
			archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})
			preview, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			i.beforePublish = func() {
				switch scenario {
				case "disconnect":
					cancel()
				case "empty directory":
					if err := os.Mkdir(preview.Destination, 0o700); err != nil {
						t.Fatal(err)
					}
				case "environment removed":
					t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
				}
			}
			_, err = i.Import(ctx, target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference})
			if scenario == "disconnect" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("disconnect: %v", err)
				}
			} else if scenario == "empty directory" {
				importErrorCode(t, err, "skill_conflict")
				entries, err := os.ReadDir(preview.Destination)
				if err != nil || len(entries) != 0 {
					t.Fatal("existing empty directory overwritten")
				}
			} else {
				importErrorCode(t, err, "skill_environment_changed")
			}
			if hasSkill(filepath.Dir(preview.Destination), preview.Name) {
				t.Fatal("failed operation published files")
			}
			entries, _ := os.ReadDir(filepath.Dir(preview.Destination))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".import-") {
					t.Fatal("failure leaked staging")
				}
			}
		})
	}
}

func TestImportActualCRCWorkAndExpandedLimits(t *testing.T) {
	i, target := importFixture(t)
	// Store makes the content corruption deterministic, exercising CRC failure rather than a broken ZIP header.
	var raw bytes.Buffer
	z := zip.NewWriter(&raw)
	f, _ := z.CreateHeader(&zip.FileHeader{Name: "SKILL.md", Method: zip.Store})
	_, _ = io.WriteString(f, importManifest)
	_ = z.Close()
	b := raw.Bytes()
	p := bytes.Index(b, []byte("A synthetic"))
	b[p] = 'B'
	_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: b})
	importErrorCode(t, err, "skill_zip_invalid")
	// Highly compressible files below the per-file limit still exhaust the allowed extraction work ratio.
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: skillZIP(t,
		zipEntry{"SKILL.md", importManifest, 0}, zipEntry{"bomb", strings.Repeat("a", 1<<20), 0})})
	importErrorCode(t, err, "skill_zip_limits")
	// Repeating a small random dictionary keeps each stream's ratio below 200, while total expanded bytes exceed 16 MiB.
	dictionary := make([]byte, 128<<10)
	_, _ = rand.Read(dictionary)
	body := bytes.Repeat(dictionary, 8)
	var entries []zipEntry
	entries = append(entries, zipEntry{"SKILL.md", importManifest, 0})
	for n := range 17 {
		entries = append(entries, zipEntry{string(rune('a' + n)), string(body), 0})
	}
	archive := skillZIP(t, entries...)
	// The dictionary is larger than Deflate's window, so use a compressible but non-bomb stream instead.
	if len(archive) > MaxImportZIP {
		dictionary = dictionary[:4096]
		body = bytes.Repeat(dictionary, 256)
		entries = entries[:1]
		for n := range 17 {
			entries = append(entries, zipEntry{string(rune('a' + n)), string(body), 0})
		}
		archive = skillZIP(t, entries...)
	}
	if len(archive) > MaxImportZIP {
		t.Fatal("expanded-limit fixture hit the upload limit")
	}
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	importErrorCode(t, err, "skill_zip_limits")
	assertNoImportStaging(t)
}

func TestImportRootIdentityProtectedLocalSkillsAndUndoReplacement(t *testing.T) {
	i, target := importFixture(t)
	archive := skillZIP(t, zipEntry{"SKILL.md", importManifest, 0})
	preview, _ := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	root := UserDir()
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference})
	importErrorCode(t, err, "skill_environment_changed")
	for _, runtime := range []string{".claude", ".agents"} {
		local := filepath.Join(target.Cwd, runtime, "skills", "example-skill")
		if err := os.MkdirAll(local, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
		importErrorCode(t, err, "skill_conflict")
		if err := os.Remove(local); err != nil {
			t.Fatal(err)
		}
	}
	preview, _ = i.Import(context.Background(), target, ImportRequest{Op: "preview", Archive: archive})
	installed, err := i.Import(context.Background(), target, ImportRequest{Op: "install", Archive: archive, Reference: preview.Reference})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(installed.Destination, installed.Destination+"-saved"); err != nil {
		t.Fatal(err)
	}
	skill(t, root, "example-skill", "Replacement")
	_, err = i.Import(context.Background(), target, ImportRequest{Op: "undo", Reference: installed.Reference})
	importErrorCode(t, err, "skill_undo_changed")
}
