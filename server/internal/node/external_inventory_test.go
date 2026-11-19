//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func externalInventoryRecordCalls(t *testing.T, f *cliFixture, delay bool) {
	t.Helper()
	body := strings.Replace(externalTestAdapter, "inventory) exec", "inventory) printf 'inventory\\n' >> \"$CLI_FAKE_DIR/inventory-calls\"; exec", 1)
	if delay {
		body = strings.Replace(body, "inventory) printf", "inventory) /bin/sleep 0.2; printf", 1)
	}
	writeFile(t, filepath.Join(f.dir, "example-adapter"), body)
}

func externalInventoryEdit(t *testing.T, f *cliFixture, edit func(*proto.ExternalInventory)) {
	t.Helper()
	path := filepath.Join(f.dir, "external-inventory.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var inventory proto.ExternalInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	edit(&inventory)
	data, err = json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func externalInventoryNoNative(t *testing.T, f *cliFixture) {
	t.Helper()
	if f.file("external-argv") != "" || f.file("cli-argv") != "" || f.file("external-stdin") != "" {
		t.Fatal("inventory discovery executed a native session or fell back to a shell launcher")
	}
}

func TestExternalInventoryCapabilityIsDynamicAndReadOnly(t *testing.T) {
	if slices.Contains(nodeproto.Announced, nodeproto.ExternalLauncherInventory) || slices.Contains(nodeproto.Required, nodeproto.ExternalLauncherInventory) {
		t.Fatal("an optional local registration became a default or required capability")
	}
	for _, node := range []*Node{{}, {keeper: newAssistantKeeper()}} {
		if slices.Contains(node.capabilities(), nodeproto.ExternalLauncherInventory) {
			t.Fatal("a node without a trusted registration advertised external inventory")
		}
	}
	f, _, _, _ := externalAssistantFixture(t, false)
	if !slices.Contains(f.n.capabilities(), nodeproto.ExternalLauncherInventory) {
		t.Fatal("a valid local registration did not advertise its independent inventory source")
	}
	if _, ok := f.n.external.lookup(proto.ExternalLauncherName("example", "claude-code_"+strings.Repeat("a", 32))); ok {
		t.Fatal("reading capabilities loaded an external inventory")
	}
	externalInventoryNoNative(t, f)
}

func TestExternalInventoryColdModelsAndGuards(t *testing.T) {
	for _, operation := range []string{"models", "models-configured", "runnable", "known"} {
		t.Run(operation, func(t *testing.T) {
			f, launcher, _, _ := externalAssistantFixture(t, false)
			externalInventoryRecordCalls(t, f, false)
			switch operation {
			case "models", "models-configured":
				raw, err := nodeCall(t, f.n, "launchers.models", map[string]any{"name": launcher, "refresh": true,
					"configured": operation == "models-configured", "agent_type": "codex"})
				if err != nil {
					t.Fatal(err)
				}
				var result proto.LauncherModels
				if json.Unmarshal(raw, &result) != nil || result.Agent != "claude" || len(result.Models) != 1 || result.Models[0].ID != "example-model" {
					t.Fatalf("cold external models = %s", raw)
				}
			case "runnable":
				if err := f.n.runnableLauncher(t.Context(), launcher, false); err != nil {
					t.Fatal(err)
				}
			case "known":
				if !f.n.knownLauncher(t.Context(), launcher) {
					t.Fatal("the locally registered cold target was not resolved")
				}
			}
			if strings.Count(f.file("inventory-calls"), "inventory\n") != 1 {
				t.Fatal("a cold specific lookup did not join exactly one inventory load")
			}
			externalInventoryNoNative(t, f)
		})
	}
}

func TestExternalInventoryRefreshIsSingleFlight(t *testing.T) {
	f, launcher, _, _ := externalAssistantFixture(t, false)
	externalInventoryRecordCalls(t, f, true)
	const count = 8
	start := make(chan struct{})
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.n.externalLauncherModels(t.Context(), launcher, true)
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(f.file("inventory-calls"), "inventory\n") != 1 {
		t.Fatal("concurrent cold refreshes started more than one inventory command")
	}
	externalInventoryNoNative(t, f)
}

func TestExternalInventoryRejectsUnregisteredAndForgedIdentity(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong-adapter", true: "disabled"}[unavailable], func(t *testing.T) {
			f, launcher, _, _ := externalAssistantFixture(t, false)
			externalInventoryRecordCalls(t, f, false)
			want := "adapter_target_not_found"
			if unavailable {
				f.n.external.Close()
				f.n.external = nil
				want = "adapter_disabled"
			} else {
				launcher = proto.ExternalLauncherName("not-the-local-registration", "claude-code_"+strings.Repeat("a", 32))
			}
			_, err := nodeCall(t, f.n, "launchers.models", map[string]any{"name": launcher, "refresh": true,
				"configured": true, "agent_type": "claude", "source": "external"})
			requireExternalCode(t, err, want)
			requireExternalCode(t, f.n.runnableLauncher(t.Context(), launcher, true), want)
			if f.n.knownLauncher(t.Context(), launcher) {
				t.Fatal("a client hint authorized an absent local target")
			}
			if unavailable && f.file("inventory-calls") != "" {
				t.Fatal("a disabled adapter was discovered or executed")
			}
			externalInventoryNoNative(t, f)
		})
	}
}

func TestExternalInventoryScopedListDoesNotReadBadOrSlowRC(t *testing.T) {
	for _, rc := range []string{"exit 1", "/bin/sleep 2; exit 1"} {
		t.Run(rc, func(t *testing.T) {
			f, launcher, _, _ := externalAssistantFixture(t, false)
			writeFile(t, filepath.Join(os.Getenv("ZDOTDIR"), ".zshrc"), "printf 'read\\n' > \"$CLI_FAKE_DIR/rc-read\"\n"+rc+"\n")
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			params, _ := json.Marshal(map[string]any{"source": "external", "host": "paseo"})
			at := time.Now()
			answer, err := f.n.handle(ctx, "launchers.list", params)
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(at) >= time.Second || f.file("rc-read") != "" {
				t.Fatal("source-scoped discovery entered or waited for the login shell")
			}
			raw, _ := json.Marshal(answer)
			var result struct {
				Source    string           `json:"source"`
				Launchers []proto.Launcher `json:"launchers"`
			}
			if json.Unmarshal(raw, &result) != nil || result.Source != "external" || len(result.Launchers) != 1 || result.Launchers[0].Name != launcher {
				t.Fatalf("source-scoped response = %s", raw)
			}
			advertised := result.Launchers[0]
			if advertised.Source != "external" || advertised.External == nil || !advertised.Assistant {
				t.Fatal("the real registry advertisement is filtered from main-chat-capable client pickers")
			}
			externalInventoryNoNative(t, f)
		})
	}
}

func TestExternalInventoryListKeepsOrdinaryFailureSeparate(t *testing.T) {
	f, launcher, _, _ := externalAssistantFixture(t)
	launcherCache.Lock()
	oldAt, oldList := launcherCache.at, launcherCache.list
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = oldAt, oldList
		launcherCache.Unlock()
	})
	writeFile(t, filepath.Join(os.Getenv("ZDOTDIR"), ".zshrc"), "exit 1\n")
	if _, err := nodeCall(t, f.n, "launchers.list", map[string]any{}); err == nil {
		t.Fatal("a warm external catalog hid the ordinary shell failure")
	}
	raw, err := nodeCall(t, f.n, "launchers.list", map[string]any{"source": "external", "refresh": true})
	if err != nil || !strings.Contains(string(raw), launcher) {
		t.Fatalf("the independent external source failed with the shell: %s, %v", raw, err)
	}
	externalInventoryNoNative(t, f)
}

func TestExternalInventoryListNeverMutatesOrdinaryCache(t *testing.T) {
	f, _, _, _ := externalAssistantFixture(t, false)
	backing := make([]proto.Launcher, 4)
	backing[0] = proto.Launcher{Name: "ordinary-provider", Source: "zshrc"}
	backing[1] = proto.Launcher{Name: "unused-capacity-sentinel"}
	before := slices.Clone(backing)
	launcherCache.Lock()
	oldAt, oldList := launcherCache.at, launcherCache.list
	launcherCache.at, launcherCache.list = time.Now(), backing[:1]
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = oldAt, oldList
		launcherCache.Unlock()
	})
	if _, err := nodeCall(t, f.n, "launchers.list", map[string]any{"source": "external", "refresh": true}); err != nil {
		t.Fatal(err)
	}
	raw, err := nodeCall(t, f.n, "launchers.list", map[string]any{})
	if err != nil || !strings.Contains(string(raw), "ordinary-provider") || strings.Contains(string(raw), "persona_ext_") {
		t.Fatalf("ordinary discovery was replaced or mixed with another source: %s, %v", raw, err)
	}
	launcherCache.Lock()
	unchanged := len(launcherCache.list) == 1 && slices.Equal(backing, before)
	launcherCache.Unlock()
	if !unchanged {
		t.Fatal("external discovery mutated the shared ordinary cache or its spare capacity")
	}
}

func TestExternalInventoryScopedListFailureIsNotHealthyEmpty(t *testing.T) {
	f, _, _, _ := externalAssistantFixture(t, false)
	writeFile(t, filepath.Join(f.dir, "external-inventory.json"), "not inventory")
	_, err := nodeCall(t, f.n, "launchers.list", map[string]any{"source": "external", "refresh": true})
	requireExternalCode(t, err, "adapter_inventory_invalid")
	f.n.external.Close()
	f.n.external = nil
	_, err = nodeCall(t, f.n, "launchers.list", map[string]any{"source": "external"})
	requireExternalCode(t, err, "adapter_disabled")
	externalInventoryNoNative(t, f)
}

func TestExternalInventoryModelIDUsesSharedRuleAndExactArg(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t, false)
	const model = "~vendor/model-latest"
	externalInventoryEdit(t, f, func(inventory *proto.ExternalInventory) {
		inventory.Targets[0].Models[0].Default = false
		inventory.Targets[0].Models = append(inventory.Targets[0].Models, proto.AgentModel{ID: model, Name: "Latest model", Default: true})
	})
	models, err := f.n.externalLauncherModels(t.Context(), launcher, true)
	if err != nil || len(models.Models) != 2 || models.Models[0].ID != "example-model" || models.Models[1].ID != model {
		t.Fatalf("a shared-valid alias masked another valid model: %+v, %v", models, err)
	}
	for _, value := range []string{"a+b", "~", "~+model", "--model"} {
		if proto.ValidModelID(value) || externalAdapterModelID(value) {
			t.Fatalf("a noncanonical model id was accepted: %q", value)
		}
	}
	invalid := externalAdapterTestInventory()
	invalid.Targets[0].Models = append(invalid.Targets[0].Models, proto.AgentModel{ID: "a+b", Name: "Invalid model"})
	if externalAdapterValidateInventory(&invalid) == nil {
		t.Fatal("an inventory accepted a model that the manager and node would reject")
	}
	execution.Model = model
	p := externalMergeStart(t, f, launcher, t.TempDir(), execution)
	for i, arg := range p.cmd.Args {
		if arg == "--model" && (i+1 >= len(p.cmd.Args) || p.cmd.Args[i+1] != model) {
			t.Fatal("an external model identity changed between the inventory and raw native arguments")
		}
	}
	if !strings.Contains(f.file("external-argv"), "--model "+model) {
		t.Fatal("the leading tilde was expanded or the selected model never reached the native program")
	}
}

func TestExternalInventorySwitchKeepsCompactionAndAcceptedQueue(t *testing.T) {
	for _, operation := range []string{"manual-compact", "accepted-queue"} {
		for _, change := range []string{"model", "source"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				f, launcher, execution, _ := externalAssistantFixture(t, false)
				externalInventoryEdit(t, f, func(inventory *proto.ExternalInventory) {
					inventory.Targets[0].Models = append(inventory.Targets[0].Models, proto.AgentModel{ID: "another-model", Name: "Another model"})
				})
				body := strings.Replace(externalTestNative,
					`echo '{"type":"result","subtype":"success","model":"example-model","result":"hello"}'`,
					`case "$line" in
					  *'/compact'*) printf 'compacting\n' > "$CLI_FAKE_DIR/compacting" ;;
					  *) echo '{"type":"result","subtype":"success","model":"example-model","result":"hello"}'
					     if [ -f "$CLI_FAKE_DIR/queue-next" ]; then
					       uuid=$(printf '%s' "$line" | sed -n 's/.*"uuid":"\([^"]*\)".*/\1/p')
					       printf '{"type":"command_lifecycle","command_uuid":"%s","state":"queued"}\n' "$uuid"
					     fi ;;
					esac`, 1)
				writeFile(t, filepath.Join(f.dir, "example-native"), body)
				id := execution.External.SessionID
				p := externalMergeStart(t, f, launcher, t.TempDir(), execution)
				if operation == "manual-compact" {
					if err := f.n.assistantCompact(t.Context(), id, false); err != nil {
						t.Fatal(err)
					}
					cliWait(t, "manual compaction input", func() bool { return f.file("compacting") != "" })
				} else {
					writeFile(t, filepath.Join(f.dir, "queue-next"), "1")
					if _, err := nodeCall(t, f.n, "session.send", externalMergeParams(launcher, execution, "accepted queued input")); err != nil {
						t.Fatal(err)
					}
					cliWait(t, "the native accepted queue", func() bool {
						p.mu.Lock()
						defer p.mu.Unlock()
						return !p.busy && slices.Contains(queuedTexts(p.queuedLocked()), "accepted queued input")
					})
				}
				p.mu.Lock()
				workingWithoutTurn := !p.busy && p.workingLocked()
				p.mu.Unlock()
				if !workingWithoutTurn {
					t.Fatal("the fixture did not isolate work outside busy/tasks")
				}
				before := f.file("external-stdin")
				params := externalMergeParams(launcher, execution, "must not reach a replacement process")
				params["model"] = "another-model"
				if change == "source" {
					params["launcher"], params["model"] = "claude", ""
					delete(params, "external")
				}
				_, err := nodeCall(t, f.n, "session.send", params)
				requireExternalCode(t, err, "adapter_busy")
				if f.proc(id) != p || !p.alive() || f.file("external-stdin") != before || f.file("cli-argv") != "" {
					t.Fatal("a switch stopped active native work, delivered input, or started another source")
				}
			})
		}
	}
}
