package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
)

// Exercise the actual launch boundary, including guest restrictions. The fake runtimes never call a model or read a real home.
func TestAssistantMCPRegistrationAtStart(t *testing.T) {
	for _, runtime := range []string{"cli", "guest", "windows-cli", "windows-guest"} {
		for _, tc := range []struct {
			name    string
			persona bool
			resume  bool
		}{
			{name: "unknown-or-old-hub"},
			{name: "negotiated-new", persona: true},
			{name: "negotiated-resume", persona: true, resume: true},
			{name: "old-hub-resume", resume: true},
		} {
			t.Run(runtime+"/"+tc.name, func(t *testing.T) {
				f := cliTest(t)
				assistantMCPTestCache(t)
				engine := strings.TrimPrefix(runtime, "windows-")
				fileConfig := engine != runtime
				if fileConfig {
					windowsAssistantMCPTest(t)
				}
				n := f.n
				n.hubPersonaContracts.Store(tc.persona)
				const id = "11111111-2222-3333-4444-555555555555"
				cwd := filepath.Join(n.home, ".persona", "assistant")
				if engine == "guest" {
					n.state.put(id, sessionState{Guest: []string{"lark_agent_reply"}})
				}
				p, err := n.spawnClaude(id, cwd, "claude", tc.resume, false)
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-p.ready:
				case <-p.exited:
					t.Fatal("fake runtime exited before initialization")
				case <-time.After(5 * time.Second):
					t.Fatal("fake runtime did not initialize")
				}

				var servers map[string]any
				var allowed, builtin []string
				flags := p.cmd.Args
				var config struct {
					Servers map[string]any `json:"mcpServers"`
				}
				raw := []byte(mcpFlag(t, flags, "--mcp-config"))
				if fileConfig {
					path := string(raw)
					if !filepath.IsAbs(path) || strings.Contains(path, `\`) {
						t.Fatalf("not an absolute slash path: %q", path)
					}
					var err error
					raw, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := json.Unmarshal(raw, &config); err != nil {
					t.Fatal(err)
				}
				servers, allowed = config.Servers, strings.Split(mcpFlag(t, flags, "--allowedTools"), ",")
				if engine != "guest" {
					builtin = strings.Split(mcpFlag(t, flags, "--tools"), ",")
				}
				sessionFlag := "--session-id"
				if tc.resume {
					sessionFlag = "--resume"
				}
				if mcpFlag(t, flags, sessionFlag) != id {
					t.Fatalf("wrong session flag: %v", flags)
				}
				if engine == "guest" && (mcpFlag(t, flags, "--tools") != "" || !slices.Contains(flags, "--strict-mcp-config")) {
					t.Fatalf("guest restrictions lost: %v", flags)
				}
				legacy := !tc.persona
				wantCount := 1
				if legacy {
					wantCount++
				}
				if len(servers) != wantCount || servers["persona"] == nil || (servers[compat.MCPServer] != nil) != legacy {
					t.Fatalf("servers = %v, legacy = %v", servers, legacy)
				}
				if engine == "guest" {
					if want := compat.MCPAllowedToolNames([]string{"lark_agent_reply"}, legacy); !slices.Equal(allowed, want) {
						t.Fatalf("guest allowed %v, want %v", allowed, want)
					}
				} else if !slices.Contains(allowed, "mcp__persona") || slices.Contains(allowed, "mcp__"+compat.MCPServer) != legacy || !slices.Contains(allowed, officeRule(n.assistantOfficeScript())) {
					t.Fatalf("allowed = %v, legacy = %v", allowed, legacy)
				}
				wantBuiltin := []string{"Bash", "Read", "Write", "Edit", "Monitor", "Skill", "Agent", "ListAgents", "SendMessage", "TaskStop", "WebFetch", "WebSearch", "ToolSearch"}
				if engine != "guest" && !slices.Equal(builtin, wantBuiltin) {
					t.Fatalf("built-in tools = %v, want %v", builtin, wantBuiltin)
				}
			})
		}
	}
}

func TestCodexAssistantMCPRegistrationAtStart(t *testing.T) {
	n, _ := testNode(t)
	for _, tc := range []struct {
		name    string
		persona bool
	}{
		{name: "unknown-or-old-hub"},
		{name: "negotiated", persona: true},
	} {
		for _, start := range []string{"new", "resume"} {
			t.Run(tc.name+"/"+start, func(t *testing.T) {
				n.hubPersonaContracts.Store(tc.persona)
				args, started := n.codexAssistantArgs("proc-test", n.dir, "")
				// A hub switch after this start's one decision must not split the args from the spare's recorded policy.
				n.hubPersonaContracts.Store(!tc.persona)
				joined := strings.Join(args, " ")
				persona := strings.Contains(joined, "mcp_servers.persona.command")
				legacy := strings.Contains(joined, "mcp_servers."+compat.MCPServer+".command")
				if started != tc.persona || !persona || legacy != !tc.persona {
					t.Fatalf("started=%v persona=%v legacy=%v, negotiated=%v: %s", started, persona, legacy, tc.persona, joined)
				}
			})
		}
	}
}

func mcpFlag(t *testing.T, flags []string, key string) string {
	t.Helper()
	i := slices.Index(flags, key)
	if i < 0 || i+1 >= len(flags) {
		t.Fatalf("missing %s in %v", key, flags)
	}
	return flags[i+1]
}

func TestDoctorMCPRetainsAliasesForAutomaticResume(t *testing.T) {
	e := newDoctorEnv(t)
	e.n.hubPersonaContracts.Store(true)
	if err := e.n.writeClaudeConfig(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(e.opts.Dir, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Servers) != 2 || config.Servers["persona"] == nil || config.Servers[compat.MCPServer] == nil {
		t.Fatalf("shared resume config lost aliases: %s", raw)
	}
	for _, check := range checkGenerated(e.opts) {
		if check.Name == "mcp.json" && (check.Status != DoctorOK || check.State != "current") {
			t.Fatalf("doctor disagrees with generated config: %+v", check)
		}
	}
}
