package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// linkedHome is a home reached through a link, as on a computer whose /home/me is a link to /data/home/me: home (H)
// is the link, real (R) the folder it names, with links followed. A terminal in a container that mounts the home at
// H sees only H's spelling (#10365).
func linkedHome(t *testing.T) (home, real string) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "data", "home", "u")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(base, "home", "u")
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	return home, resolved(t, real)
}

// writeProgram makes an executable file at p.
func writeProgram(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The node's program installed under the legacy data directory, ~/.persona an alias of it, on a linked home: the
// resolved file is R/<legacy name>/bin/personad, which a terminal that sees only H cannot run; the data directory's
// spelling of the same file is handed out.
func TestAgentProgramPrefersDataDirSpellingOfSameFile(t *testing.T) {
	home, real := linkedHome(t)
	exe := writeProgram(t, filepath.Join(real, compat.StateDir, "bin", "personad"))
	if err := os.Symlink(compat.StateDir, filepath.Join(real, ".persona")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".persona")
	if got, want := agentProgram(exe, home, dir), filepath.Join(dir, "bin", "personad"); got != want {
		t.Fatalf("agentProgram = %q, want %q", got, want)
	}
}

// Without a same file in the data directory, the home's spelling of the program; a program outside the home, a home
// that is no link and a missing program stay as they are.
func TestAgentProgramFallsBackToHomeSpellingThenExe(t *testing.T) {
	home, real := linkedHome(t)
	dir := filepath.Join(home, ".persona")
	writeProgram(t, filepath.Join(real, ".persona", "bin", "personad")) // another build in the data directory
	exe := writeProgram(t, filepath.Join(real, ".local", "bin", "personad"))
	if got, want := agentProgram(exe, home, dir), filepath.Join(home, ".local", "bin", "personad"); got != want {
		t.Fatalf("home spelling: %q, want %q", got, want)
	}
	outside := writeProgram(t, filepath.Join(t.TempDir(), "personad"))
	if got := agentProgram(outside, home, dir); got != outside {
		t.Fatalf("a program outside the home moved: %q", got)
	}
	plain := t.TempDir()
	plainExe := writeProgram(t, filepath.Join(plain, "bin", "personad"))
	if realHome(plain) == "" {
		if got := agentProgram(plainExe, plain, filepath.Join(plain, ".persona")); got != plainExe {
			t.Fatalf("a home that is no link changed the spelling: %q", got)
		}
	}
	missing := filepath.Join(real, "gone", "personad")
	if got := agentProgram(missing, home, dir); got != missing {
		t.Fatalf("a missing program changed: %q", got)
	}
	if got := agentProgram(exe, "", ""); got != exe {
		t.Fatalf("no home: %q", got)
	}
}

// Everything that runs the node's program in a terminal names the visible spelling, and the start probe compares the
// shell's words with that same spelling: a managed launcher's typed command, a managed Codex launcher's argv (and the
// checks that recognise it), and the probe's program.
func TestManagedLauncherCommandAndProbeUseVisibleProgram(t *testing.T) {
	home, real := linkedHome(t)
	exe := writeProgram(t, filepath.Join(real, ".persona", "bin", "personad"))
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), exe: exe, bin: agentProgram(exe, home, filepath.Join(home, ".persona"))}
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", BaseURL: "http://gw.example.com", Model: "m1", APIKey: "k"},
		{Name: "cx", Agent: "codex", BaseURL: "http://gw.example.com/v1", APIKey: "k"}})
	if cmd := n.launcherCommand("gw"); cmd != n.bin+" launch gw" || strings.Contains(cmd, real) {
		t.Fatalf("managed launcher command %q names the resolved path (bin %q)", cmd, n.bin)
	}
	p := n.probeFor(t.Context(), probeID, "gw", []string{"/bin/bash"})
	if p.program != n.bin {
		t.Fatalf("probe program %q, bin %q", p.program, n.bin)
	}
	refused, _ := p.verdict(typed("$ ", n.bin+" launch gw")+"\nbash: "+n.bin+": No such file or directory\n$ ", shellBack)
	var re *rpc.Error
	if !errors.As(refused, &re) || re.Code != proto.CodeLauncherMissing || !strings.Contains(string(re.Data), n.bin) {
		t.Fatalf("a terminal without the program is the launcher missing there: %v %s", refused, refusalJSON(refused))
	}
	argv, ok := n.codexArgv("cx")
	if !ok || len(argv) != 3 || argv[0] != n.bin || argv[1] != "launch" || argv[2] != "cx" {
		t.Fatalf("managed codex argv = %v", argv)
	}
	if n.codexOnSharedDaemon(argv) {
		t.Fatalf("the managed wrapper was taken for a codex on the shared daemon: %v", argv)
	}
}

// The node's info keeps the resolved file as the update target (an update replaces the file, never a link to it),
// while what it hands out is the visible spelling.
func TestNodeInfoExecutableStaysResolved(t *testing.T) {
	home, real := linkedHome(t)
	exe := writeProgram(t, filepath.Join(real, ".persona", "bin", "personad"))
	n, _ := testNode(t)
	n.home, n.dir, n.exe = home, filepath.Join(home, ".persona"), exe
	n.bin = agentProgram(exe, home, n.dir)
	if n.bin == exe {
		t.Fatal("fixture: the visible spelling should differ from the resolved file")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // nothing here needs herdr or claude to answer
	if info := n.info(ctx); info.Executable != exe || info.HomeReal != real {
		t.Fatalf("info executable %q (want %q), home_real %q", info.Executable, exe, info.HomeReal)
	}
	if got := (&Node{bin: "/opt/p/personad"}).info(ctx).Executable; got != "/opt/p/personad" {
		t.Fatalf("a node without exe reports bin: %q", got)
	}
}

// The generated hooks, settings and MCP configs name the visible spelling, and `personad node doctor`, which asks the
// same selfProgram, finds the files the node wrote current: neither flips them back to the other spelling.
func TestDoctorAgreesWithNodeOnProgramSpelling(t *testing.T) {
	home, real := linkedHome(t)
	exe := writeProgram(t, filepath.Join(real, ".persona", "bin", "personad"))
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", "")
	old := selfProgram
	selfProgram = func(home, dir string) (string, string, error) { return exe, agentProgram(exe, home, dir), nil }
	t.Cleanup(func() { selfProgram = old })

	dir := filepath.Join(home, ".persona")
	_, bin, _ := selfProgram(home, dir)
	if err := writeGeneratedConfig(dir, bin); err != nil {
		t.Fatal(err)
	}
	files, err := generatedFiles(bin, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(string(f.Content), real) {
			t.Errorf("%s names the resolved path: %s", f.Rel, f.Content)
		}
	}
	opts, err := DefaultDoctorOptions()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Bin != bin || opts.Dir != dir {
		t.Fatalf("doctor bin %q dir %q, node bin %q dir %q", opts.Bin, opts.Dir, bin, dir)
	}
	for _, c := range checkGenerated(DoctorOptions{Dir: opts.Dir, Bin: opts.Bin}) {
		if c.State != "current" && c.Name != agentPluginGroup {
			t.Errorf("doctor: %s is %s: %v", c.Name, c.State, c.Items)
		}
	}
}
