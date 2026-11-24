package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const desktopSeedThread = "aaaaaaaa-0000-4000-8000-000000000111"

type desktopSeedRecord struct {
	Args  []string          `json:"args"`
	Cwd   string            `json:"cwd"`
	Env   map[string]string `json:"env"`
	Calls []map[string]any  `json:"calls"`
}

// The copied test executable stands in for a native CLI. It runs before the test harness (which clears agent
// environment variables), never reads credentials, and records only the fields asserted below.
func init() {
	mode := os.Getenv("PERSONA_TEST_DESKTOP_CHILD")
	if mode == "" || (filepath.Base(os.Args[0]) != "desktop-seed-helper.exe" && !(mode == "claude-models" && filepath.Base(os.Args[0]) == "node.exe")) {
		return
	}
	record := desktopSeedRecord{Args: os.Args[1:], Env: map[string]string{}}
	record.Cwd, _ = os.Getwd()
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "PERSONA_SESSION_ID", "CODEX_HOME", "KEEP_DESKTOP_TEST"} {
		record.Env[name] = os.Getenv(name)
	}
	writeRecord := func() {
		body, _ := json.Marshal(record)
		if os.WriteFile(os.Getenv("PERSONA_TEST_DESKTOP_RECORD"), body, 0o600) != nil {
			os.Exit(3)
		}
	}
	if mode == "claude" {
		writeRecord()
		os.Exit(0)
	}
	if mode == "claude-models" {
		lines := bufio.NewScanner(os.Stdin)
		if !lines.Scan() {
			os.Exit(8)
		}
		var call map[string]any
		if json.Unmarshal(lines.Bytes(), &call) != nil {
			os.Exit(9)
		}
		record.Calls = append(record.Calls, call)
		writeRecord()
		fmt.Println(claudeInitAnswer)
		os.Exit(0)
	}
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		var call map[string]any
		if json.Unmarshal(lines.Bytes(), &call) != nil {
			os.Exit(4)
		}
		record.Calls = append(record.Calls, call)
		writeRecord()
		id, request := call["id"]
		if !request {
			continue
		}
		result := any(map[string]any{})
		switch call["method"] {
		case "project/list":
			result = map[string]any{"data": []any{
				map[string]any{"id": "project-test", "name": "Example", "roots": []any{map[string]any{"path": record.Cwd}}},
				map[string]any{"id": "project-chosen", "name": "Chosen", "position": 10, "roots": []any{map[string]any{"path": record.Cwd}}},
			}, "nextCursor": nil}
		case "model/list":
			params, _ := call["params"].(map[string]any)
			if mode != "codex-models" || params["includeHidden"] != true {
				os.Exit(6)
			}
			switch params["cursor"] {
			case nil:
				result = map[string]any{"data": []any{map[string]any{
					"model": "gpt-x", "hidden": true, "defaultReasoningEffort": "low",
					"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "ultra"}},
				}}, "nextCursor": "models-next"}
			case "models-next":
				result = map[string]any{"data": []any{map[string]any{
					"model": "gpt-plain", "supportedReasoningEfforts": []any{},
				}}, "nextCursor": nil}
			default:
				os.Exit(7)
			}
		case "thread/start":
			if mode == "codex-lost" {
				os.Exit(0)
			}
			result = map[string]any{"thread": map[string]any{"id": desktopSeedThread}}
		case "thread/name/set":
			writeRecord()
		case "initialize":
		default:
			os.Exit(5) // a turn or resume here is a regression
		}
		b, _ := json.Marshal(map[string]any{"id": id, "result": result})
		fmt.Println(string(b))
	}
	os.Exit(0)
}

func windowsDesktopSeedCLI(t *testing.T, mode string) (string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "desktop-seed-helper.exe")
	if err := os.WriteFile(program, body, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "seen.json")
	t.Setenv("PERSONA_TEST_DESKTOP_CHILD", mode)
	t.Setenv("PERSONA_TEST_DESKTOP_RECORD", record)
	return program, record
}

func readWindowsDesktopSeed(t *testing.T, file string) desktopSeedRecord {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var record desktopSeedRecord
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestWindowsDesktopSeedArguments(t *testing.T) {
	cli, seen := windowsDesktopSeedCLI(t, "claude")
	dir := t.TempDir()
	t.Setenv("Claude_Config_Dir", `C:\other profile`)
	t.Setenv("ClaudeCode", "1")
	t.Setenv("Claude_Code_Entrypoint", "claude-desktop")
	t.Setenv("Persona_Session_Id", "a-parent-session")
	t.Setenv("KEEP_DESKTOP_TEST", "yes")
	title := `A "quoted" title & %PATH% 中文`
	if err := seedDesktopSession(context.Background(), cli, dir, desktopSeedThread, title); err != nil {
		t.Fatal(err)
	}
	record := readWindowsDesktopSeed(t, seen)
	if !reflect.DeepEqual(record.Args, []string{"-p", "--session-id", desktopSeedThread, "/rename " + title}) || !strings.EqualFold(record.Cwd, dir) {
		t.Fatalf("seed arguments or directory changed: %+v", record)
	}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "PERSONA_SESSION_ID"} {
		if record.Env[key] != "" {
			t.Fatalf("inherited %s", key)
		}
	}
	if record.Env["KEEP_DESKTOP_TEST"] != "yes" {
		t.Fatal("unrelated environment was removed")
	}
}

func TestWindowsCodexMetadataSession(t *testing.T) {
	cli, seen := windowsDesktopSeedCLI(t, "codex")
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("Codex_Home", `C:\old-profile`)
	title := `Fix "CSV" & export 中文`
	thread, err := startCodexAppChat(context.Background(), cli, home, dir, title)
	if err != nil || thread != desktopSeedThread {
		t.Fatalf("seed = %s, %v", thread, err)
	}
	record := readWindowsDesktopSeed(t, seen)
	if !reflect.DeepEqual(record.Args, []string{"app-server"}) || record.Env["CODEX_HOME"] != home || !strings.EqualFold(record.Cwd, dir) {
		t.Fatalf("server environment or arguments changed: %+v", record)
	}
	var methods []string
	for _, call := range record.Calls {
		method := call["method"].(string)
		methods = append(methods, method)
		params, _ := call["params"].(map[string]any)
		switch method {
		case "initialize":
			if params["clientInfo"].(map[string]any)["name"] != codexAppClient {
				t.Fatal("seed did not identify the desktop client")
			}
			if capabilities, _ := params["capabilities"].(map[string]any); capabilities["experimentalApi"] != true {
				t.Fatal("native project APIs require the experimental capability")
			}
		case "thread/start":
			if params["cwd"] != dir || params["projectId"] != "project-test" {
				t.Fatalf("new chat is not in the picked native project: %v", params)
			}
		case "thread/name/set":
			if params["name"] != title || params["threadId"] != desktopSeedThread {
				t.Fatalf("name params = %v", params)
			}
		}
	}
	if !reflect.DeepEqual(methods, []string{"initialize", "initialized", "project/list", "thread/start", "thread/name/set"}) {
		t.Fatalf("unexpected metadata requests: %v", methods)
	}
}

func TestWindowsDesktopSeedRejectsUnknownBatchFiles(t *testing.T) {
	batch := filepath.Join(t.TempDir(), "untrusted.cmd")
	if err := os.WriteFile(batch, []byte("exit 0\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedDesktopSession(context.Background(), batch, t.TempDir(), desktopSeedThread, `a & echo no`); err == nil {
		t.Fatal("Claude seed accepted an unknown shell wrapper")
	}
	if err := withCodexAppServer(context.Background(), batch, t.TempDir(), t.TempDir(), func(codexAppCall) error { return nil }); err == nil {
		t.Fatal("Codex metadata client accepted an unknown shell wrapper")
	}
}

func TestWindowsCodexMetadataUsesExactSelectedProject(t *testing.T) {
	for _, chosen := range []string{"project-chosen", "project-missing"} {
		t.Run(chosen, func(t *testing.T) {
			cli, seen := windowsDesktopSeedCLI(t, "codex")
			thread, err := startCodexAppChatInProject(context.Background(), cli, t.TempDir(), t.TempDir(), "title", chosen)
			record := readWindowsDesktopSeed(t, seen)
			if chosen == "project-missing" {
				if err == nil || thread != "" {
					t.Fatalf("missing project accepted: %s, %v", thread, err)
				}
				for _, call := range record.Calls {
					if call["method"] == "thread/start" {
						t.Fatal("created a thread before rejecting the selected project")
					}
				}
				return
			}
			if err != nil || thread != desktopSeedThread {
				t.Fatalf("explicit project creation: %s, %v", thread, err)
			}
			for _, call := range record.Calls {
				if call["method"] == "thread/start" && call["params"].(map[string]any)["projectId"] != chosen {
					t.Fatal("a different project with the same root replaced the explicit selection")
				}
			}
		})
	}
}

func TestWindowsCodexCreationResponseLossIsUncertain(t *testing.T) {
	cli, _ := windowsDesktopSeedCLI(t, "codex-lost")
	thread, err := startCodexAppChat(context.Background(), cli, t.TempDir(), t.TempDir(), "title")
	if thread != "" || !maybeDelivered(err) {
		t.Fatalf("lost creation response allowed an automatic retry: %s, %v", thread, err)
	}
}

// The native Windows child exercises the same real stdio client as the Unix shell fixture, including pagination.
func TestWindowsReadCodexAppModelsAsksTheAppsCodex(t *testing.T) {
	cli, seen := windowsDesktopSeedCLI(t, "codex-models")
	oldCLI := codexAppCLI
	codexAppCLI = func() (string, error) { return cli, nil }
	t.Cleanup(func() { codexAppCLI = oldCLI })
	home := t.TempDir()
	t.Setenv("Codex_Home", `C:\old-profile`)
	models, err := readCodexAppModels(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if m := models["gpt-x"]; !reflect.DeepEqual(m.levels, []string{"low", "ultra"}) || m.def != "low" {
		t.Fatalf("hidden model's efforts = %+v", models)
	}
	if m, found := models["gpt-plain"]; !found || len(m.levels) != 0 || m.def != "" || len(models) != 2 {
		t.Fatalf("the second model page was not read: %+v", models)
	}
	record := readWindowsDesktopSeed(t, seen)
	if !reflect.DeepEqual(record.Args, []string{"app-server"}) || record.Env["CODEX_HOME"] != home || !strings.EqualFold(record.Cwd, os.TempDir()) {
		t.Fatalf("model client environment or arguments changed: %+v", record)
	}
	var methods []string
	pages := 0
	for _, call := range record.Calls {
		method := call["method"].(string)
		methods = append(methods, method)
		params, _ := call["params"].(map[string]any)
		switch method {
		case "initialize":
			client, _ := params["clientInfo"].(map[string]any)
			capabilities, _ := params["capabilities"].(map[string]any)
			if client["name"] != codexAppClient || capabilities["experimentalApi"] != true {
				t.Fatalf("model client initialization changed: %v", params)
			}
		case "model/list":
			want := map[string]any{"includeHidden": true}
			if pages > 0 {
				want["cursor"] = "models-next"
			}
			if !reflect.DeepEqual(params, want) {
				t.Fatalf("model page %d params = %v, want %v", pages, params, want)
			}
			pages++
		}
	}
	if !reflect.DeepEqual(methods, []string{"initialize", "initialized", "model/list", "model/list"}) {
		t.Fatalf("unexpected model requests: %v", methods)
	}
}

// A desktop model query follows an npm shim's JavaScript entry with structured arguments; cmd.exe never runs.
func TestWindowsClaudeDesktopModelsUseTheSafeNpmEntry(t *testing.T) {
	helper, seen := windowsDesktopSeedCLI(t, "claude-models")
	root := t.TempDir()
	shim := filepath.Join(root, "claude.cmd")
	entry := filepath.Join(root, "node_modules", "@anthropic-ai", "claude-code", "cli.js")
	if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string][]byte{shim: []byte("@exit /b 86\r\n"), entry: []byte("test entry")} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node.exe"), body, 0o700); err != nil {
		t.Fatal(err)
	}
	oldCLI := desktopClaudeCLI
	desktopClaudeCLI = func() (string, error) { return shim, nil }
	t.Cleanup(func() { desktopClaudeCLI = oldCLI })
	n, _ := testNode(t)
	models, err := n.listDesktopCLIModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0].ID != "default" || models[0].Resolved != "claude-opus-5-5[1m]" {
		t.Fatalf("models = %+v", models)
	}
	record := readWindowsDesktopSeed(t, seen)
	if len(record.Args) != 9 || record.Args[0] != entry || !reflect.DeepEqual(record.Args[1:8], []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--settings"}) {
		t.Fatalf("model command arguments = %v", record.Args)
	}
	if len(record.Calls) != 1 || record.Calls[0]["type"] != "control_request" {
		t.Fatalf("initialize calls = %+v", record.Calls)
	}
}
