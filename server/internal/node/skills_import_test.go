package node

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
)

func TestSkillImportNodeRoutingAndDiscovery(t *testing.T) {
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = filepath.Join(n.home, ".persona"), "/opt/example/personad", log.New(io.Discard, "", 0)
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_SKILLS_DIR", filepath.Join(t.TempDir(), "shared-skills"))
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	w, _ := z.Create("package/SKILL.md")
	_, _ = io.WriteString(w, "---\nname: example-upload\ndescription: An isolated fixture.\n---\n")
	w, _ = z.Create("package/scripts/check.sh")
	_, _ = io.WriteString(w, "#!/bin/sh\nexit 98\n") // importing/provisioning must not execute this
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	dir := path.Join(compat.StateDir, identity.AssistantDirName(assistantwire.AssistantID))
	scope := assistantwire.AssistantID + "\x00example-computer"
	call := func(p map[string]any) (assistantskills.ImportResult, error) {
		t.Helper()
		b, _ := json.Marshal(p)
		out, err := n.handle(context.Background(), "skills.import", b)
		if err != nil {
			return assistantskills.ImportResult{}, err
		}
		return out.(assistantskills.ImportResult), nil
	}
	// The assistant's directory is prepared before its messages, as the manager does (assistant.prepare). Since #6806
	// the node provisions skills only into a prepared directory. Preparing before the install leaves the uploaded skill
	// to the provisioning entry points checked below.
	if _, err := n.handle(context.Background(), "assistant.prepare", mustJSON(map[string]any{"dir": dir})); err != nil {
		t.Fatal(err)
	}
	preview, err := call(map[string]any{"op": "preview", "archive": archive.Bytes(), "dir": dir, "scope": scope})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(map[string]any{"op": "install", "archive": archive.Bytes(), "dir": dir, "scope": scope, "reference": preview.Reference}); err != nil {
		t.Fatal(err)
	}
	// Exercise the real node list and both actual provisioning entry points, without starting a paid runtime.
	raw, _ := json.Marshal(map[string]any{"cwd": n.assistantDirOf(dir)})
	out, err := n.handle(context.Background(), "skills.list", raw)
	if err != nil || !slices.ContainsFunc(out.(map[string]any)["skills"].([]assistantskills.Skill),
		func(s assistantskills.Skill) bool { return s.Name == "example-upload" && s.Source == "user" }) {
		t.Fatalf("node list: %v %v", out, err)
	}
	cwd := n.assistantDirOf(dir)
	_, release := n.assistantLarkEnv(cwd, "synthetic-session")
	defer release()
	n.assistantRuntimeSkills(cwd)
	for _, runtime := range []string{".claude", ".agents"} {
		if b, err := os.ReadFile(filepath.Join(cwd, runtime, "skills", "example-upload", "scripts", "check.sh")); err != nil ||
			string(b) != "#!/bin/sh\nexit 98\n" {
			t.Fatalf("%s provisioning: %q %v", runtime, b, err)
		}
	}
	for _, p := range []map[string]any{
		{"op": "preview", "archive": archive.Bytes(), "dir": "../outside", "scope": scope},
		{"op": "preview", "archive": archive.Bytes(), "dir": dir, "scope": "other\x00example-computer"},
		{"op": "preview", "archive": archive.Bytes(), "dir": dir, "scope": scope, "path": "/arbitrary"},
		{"op": "preview", "archive": archive.Bytes(), "dir": dir, "scope": scope, "machine_id": "another"},
	} {
		if _, err := call(p); err == nil {
			t.Fatalf("accepted arbitrary target: %+v", p)
		}
	}
}
