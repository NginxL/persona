package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func externalAdapterTestInventory() proto.ExternalInventory {
	return proto.ExternalInventory{Protocol: proto.ExternalAdapterProtocol, Targets: []proto.ExternalTarget{{
		ID: "claude-code_0123456789abcdef0123456789abcdef", Name: "Local model service", Engine: "claude-code",
		Revision: "sha256:" + strings.Repeat("a", 64), CredentialState: "configured",
		Capabilities: proto.ExternalCapabilities{Contexts: []string{"main-chat"}, Runtimes: []string{"cli"}, Platforms: []string{"posix"}},
		Models: []proto.AgentModel{{ID: "provider/model.v2@preview[1m]", Name: "Local model", Default: true,
			Efforts: []string{"low", "high"}, DefaultEffort: "high"}},
	}}}
}

func externalAdapterTestJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func externalAdapterTestCode(t *testing.T, err error, want string) {
	t.Helper()
	var coded *rpc.Error
	if !errors.As(err, &coded) || coded.Code != want || coded.Message != want || len(coded.Data) != 0 {
		t.Fatalf("error = %#v; want only static code %q", err, want)
	}
}

func TestExternalAdapterDisabled(t *testing.T) {
	t.Setenv(externalAdapterConfigEnv, "")
	t.Setenv("PATH", "")
	r, err := loadExternalAdapterRegistry()
	if err != nil || r != nil {
		t.Fatalf("disabled registration = %v, %v", r, err)
	}
	r.SetOnChange(func() { t.Error("disabled adapter notified") })
	r.Close()
	if got := r.launchers(context.Background(), true); got != nil {
		t.Fatalf("disabled launchers = %#v", got)
	}
	if _, ok := r.lookup(proto.ExternalLauncherName("local", "target")); ok {
		t.Fatal("disabled adapter resolved a target")
	}
	_, err = r.models(context.Background(), "missing", true)
	externalAdapterTestCode(t, err, "adapter_target_not_found")
	if _, err := r.command("missing", nil, "", "", nil, nil); err == nil {
		t.Fatal("disabled adapter prepared a process")
	}
}

func TestExternalAdapterStrictInventoryJSON(t *testing.T) {
	valid := string(externalAdapterTestJSON(t, externalAdapterTestInventory()))
	cases := map[string]string{
		"syntax":               `{"protocol":1,"targets":[`,
		"extra value":          valid + `{}`,
		"trailing garbage":     valid + ` secret-output`,
		"duplicate protocol":   strings.Replace(valid, `"protocol":1`, `"protocol":1,"protocol":1`, 1),
		"duplicate nested id":  strings.Replace(valid, `"engine":"claude-code"`, `"engine":"claude-code","engine":"claude-code"`, 1),
		"wrong case":           strings.Replace(valid, `"protocol"`, `"Protocol"`, 1),
		"unknown top level":    strings.Replace(valid, `"protocol":1`, `"protocol":1,"command":["/untrusted/program"]`, 1),
		"endpoint":             strings.Replace(valid, `"credential_state":"configured"`, `"credential_state":"configured","url":"https://example.com/private"`, 1),
		"environment":          strings.Replace(valid, `"credential_state":"configured"`, `"credential_state":"configured","env":{"EXAMPLE_KEY":"secret-output"}`, 1),
		"arguments":            strings.Replace(valid, `"credential_state":"configured"`, `"credential_state":"configured","args":["--unsafe"]`, 1),
		"unknown model member": strings.Replace(valid, `"default":true`, `"default":true,"api_key":"secret-output"`, 1),
		"invalid utf8":         valid + string([]byte{0xff}),
		"excessive nesting":    strings.Repeat("[", 20) + "0" + strings.Repeat("]", 20),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var inv proto.ExternalInventory
			if externalAdapterDecode([]byte(data), &inv) == nil {
				t.Fatal("accepted invalid inventory")
			}
		})
	}
	var inv proto.ExternalInventory
	if err := externalAdapterDecode([]byte(valid+"\n\t"), &inv); err != nil {
		t.Fatalf("valid inventory: %v", err)
	}
}

func TestExternalAdapterInventoryValidation(t *testing.T) {
	cases := map[string]func(*proto.ExternalInventory){
		"missing targets":   func(v *proto.ExternalInventory) { v.Targets = nil },
		"missing id":        func(v *proto.ExternalInventory) { v.Targets[0].ID = "" },
		"target too long":   func(v *proto.ExternalInventory) { v.Targets[0].ID = strings.Repeat("x", 129) },
		"target argument":   func(v *proto.ExternalInventory) { v.Targets[0].ID = "--target" },
		"target duplicate":  func(v *proto.ExternalInventory) { v.Targets = append(v.Targets, v.Targets[0]) },
		"missing revision":  func(v *proto.ExternalInventory) { v.Targets[0].Revision = "" },
		"revision argument": func(v *proto.ExternalInventory) { v.Targets[0].Revision = "r1 --target other" },
		"missing name":      func(v *proto.ExternalInventory) { v.Targets[0].Name = "" },
		"name too long":     func(v *proto.ExternalInventory) { v.Targets[0].Name = strings.Repeat("x", 161) },
		"name url":          func(v *proto.ExternalInventory) { v.Targets[0].Name = "https://example.com/token" },
		"name environment":  func(v *proto.ExternalInventory) { v.Targets[0].Name = "EXAMPLE_KEY=private" },
		"name argument":     func(v *proto.ExternalInventory) { v.Targets[0].Name = "Local --settings private" },
		"name control":      func(v *proto.ExternalInventory) { v.Targets[0].Name = "Local\nprivate" },
		"name bidi":         func(v *proto.ExternalInventory) { v.Targets[0].Name = "Local‮private" },
		"engine injection":  func(v *proto.ExternalInventory) { v.Targets[0].Engine = "claude-code;exec" },
		"credential invalid": func(v *proto.ExternalInventory) {
			v.Targets[0].CredentialState = "healthy"
		},
		"code arbitrary": func(v *proto.ExternalInventory) { v.Targets[0].UnavailableCode = "private-detail" },
		"code url": func(v *proto.ExternalInventory) {
			v.Targets[0].UnavailableCode = "https://example.com/secret"
		},
		"nil contexts":   func(v *proto.ExternalInventory) { v.Targets[0].Capabilities.Contexts = nil },
		"nil runtimes":   func(v *proto.ExternalInventory) { v.Targets[0].Capabilities.Runtimes = nil },
		"nil platforms":  func(v *proto.ExternalInventory) { v.Targets[0].Capabilities.Platforms = nil },
		"duplicate caps": func(v *proto.ExternalInventory) { v.Targets[0].Capabilities.Runtimes = []string{"cli", "cli"} },
		"caps url": func(v *proto.ExternalInventory) {
			v.Targets[0].Capabilities.Contexts = []string{"https://example.com"}
		},
		"missing models": func(v *proto.ExternalInventory) { v.Targets[0].Models = nil },
		"model duplicate": func(v *proto.ExternalInventory) {
			v.Targets[0].Models = append(v.Targets[0].Models, v.Targets[0].Models[0])
		},
		"model missing id": func(v *proto.ExternalInventory) { v.Targets[0].Models[0].ID = "" },
		"model whitespace": func(v *proto.ExternalInventory) { v.Targets[0].Models[0].ID += " " },
		"model url":        func(v *proto.ExternalInventory) { v.Targets[0].Models[0].ID = "https://example.com/model" },
		"model mailto":     func(v *proto.ExternalInventory) { v.Targets[0].Models[0].ID = "mailto:me@example.com" },
		"model argument":   func(v *proto.ExternalInventory) { v.Targets[0].Models[0].ID = "--settings" },
		"model too long":   func(v *proto.ExternalInventory) { v.Targets[0].Models[0].ID = strings.Repeat("m", 257) },
		"model url detail": func(v *proto.ExternalInventory) {
			v.Targets[0].Models[0].Description = "See https://example.com/private"
		},
		"model resolved injection": func(v *proto.ExternalInventory) { v.Targets[0].Models[0].Resolved = "MODEL=private" },
		"multiple defaults": func(v *proto.ExternalInventory) {
			m := v.Targets[0].Models[0]
			m.ID = "another-model"
			v.Targets[0].Models = append(v.Targets[0].Models, m)
		},
		"custom model": func(v *proto.ExternalInventory) { v.Targets[0].Models[0].Custom = true },
		"effort invalid": func(v *proto.ExternalInventory) {
			v.Targets[0].Models[0].Efforts = []string{"--exec"}
		},
		"effort duplicate": func(v *proto.ExternalInventory) { v.Targets[0].Models[0].Efforts = []string{"high", "high"} },
		"default effort missing": func(v *proto.ExternalInventory) {
			v.Targets[0].Models[0].DefaultEffort = "max"
		},
		"tier duplicate": func(v *proto.ExternalInventory) {
			v.Targets[0].Models[0].ServiceTiers = []proto.ServiceTier{{ID: "fast", Name: "Fast"}, {ID: "fast", Name: "Fast"}}
		},
		"negative price": func(v *proto.ExternalInventory) { v.Targets[0].Models[0].Price = &proto.ModelPrice{Input: -1} },
		"too many targets": func(v *proto.ExternalInventory) {
			target := v.Targets[0]
			v.Targets = make([]proto.ExternalTarget, externalAdapterTargetLimit+1)
			for i := range v.Targets {
				v.Targets[i] = target
				v.Targets[i].ID = fmt.Sprintf("target-%d", i)
			}
		},
		"too many models": func(v *proto.ExternalInventory) {
			v.Targets[0].Models = make([]proto.AgentModel, externalAdapterModelLimit+1)
			for i := range v.Targets[0].Models {
				v.Targets[0].Models[i] = proto.AgentModel{ID: fmt.Sprintf("model-%d", i), Name: "Model"}
			}
		},
		"total models limited": func(v *proto.ExternalInventory) {
			target := v.Targets[0]
			target.Models = make([]proto.AgentModel, externalAdapterModelLimit/2+1)
			for i := range target.Models {
				target.Models[i] = proto.AgentModel{ID: fmt.Sprintf("model-%d", i), Name: "Model"}
			}
			v.Targets[0] = target
			target.ID = "another-target"
			v.Targets = append(v.Targets, target)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := externalAdapterTestInventory()
			change(&v)
			externalAdapterTestCode(t, externalAdapterValidateInventory(&v), "adapter_inventory_invalid")
		})
	}
}

func TestExternalAdapterCapabilityIntersection(t *testing.T) {
	cases := []struct {
		name   string
		change func(*proto.ExternalTarget)
		code   string
	}{
		{"supported subset", func(v *proto.ExternalTarget) {
			v.Capabilities.Contexts = []string{"main-chat", "agent-session"}
			v.Capabilities.Runtimes = []string{"cli", "agentsdk"}
			v.Capabilities.Platforms = []string{"posix", "windows"}
		}, ""},
		{"unsupported engine", func(v *proto.ExternalTarget) { v.Engine = "other-engine" }, "adapter_runtime_unsupported"},
		{"unsupported purpose", func(v *proto.ExternalTarget) { v.Capabilities.Contexts = []string{"agent-session"} }, "adapter_purpose_unsupported"},
		{"unsupported runtime", func(v *proto.ExternalTarget) { v.Capabilities.Runtimes = []string{"agentsdk"} }, "adapter_runtime_unsupported"},
		{"unsupported platform", func(v *proto.ExternalTarget) { v.Capabilities.Platforms = []string{"windows"} }, "adapter_platform_unsupported"},
		{"missing credentials", func(v *proto.ExternalTarget) { v.CredentialState = "missing" }, "adapter_credentials_missing"},
		{"unknown credentials", func(v *proto.ExternalTarget) { v.CredentialState = "unknown" }, ""},
		{"explicit unavailable", func(v *proto.ExternalTarget) { v.UnavailableCode = "adapter_unavailable" }, "adapter_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := externalAdapterTestInventory()
			tc.change(&v.Targets[0])
			credential := v.Targets[0].CredentialState
			if err := externalAdapterValidateInventory(&v); err != nil {
				t.Fatal(err)
			}
			got := v.Targets[0]
			if got.UnavailableCode != tc.code || got.CredentialState != credential {
				t.Fatalf("status = %q, %q; want %q, %q", got.UnavailableCode, got.CredentialState, tc.code, credential)
			}
			for _, c := range []struct {
				values []string
				only   string
			}{{got.Capabilities.Contexts, "main-chat"}, {got.Capabilities.Runtimes, "cli"}, {got.Capabilities.Platforms, "posix"}} {
				if len(c.values) > 1 || len(c.values) == 1 && c.values[0] != c.only {
					t.Fatalf("unsupported capability escaped intersection: %v", c.values)
				}
			}
		})
	}
}

func TestExternalAdapterInventoryLimitsAndRawIDs(t *testing.T) {
	v := externalAdapterTestInventory()
	original := v.Targets[0].Models[0].ID
	v.Targets[0].Models = make([]proto.AgentModel, externalAdapterModelLimit)
	for i := range v.Targets[0].Models {
		v.Targets[0].Models[i] = proto.AgentModel{ID: fmt.Sprintf("provider/model-%d[1m]", i), Name: "Model"}
	}
	v.Targets[0].Models[0].ID = original
	if err := externalAdapterValidateInventory(&v); err != nil || v.Targets[0].Models[0].ID != original {
		t.Fatalf("raw model ID or model bound changed: %v", err)
	}
	v = externalAdapterTestInventory()
	target := v.Targets[0]
	v.Targets = make([]proto.ExternalTarget, externalAdapterTargetLimit)
	for i := range v.Targets {
		v.Targets[i] = externalAdapterCloneTarget(target)
		v.Targets[i].ID = fmt.Sprintf("target-%d", i)
	}
	if err := externalAdapterValidateInventory(&v); err != nil {
		t.Fatalf("valid targets sharing a revision and model ID: %v", err)
	}
	v.Targets = []proto.ExternalTarget{}
	if err := externalAdapterValidateInventory(&v); err != nil {
		t.Fatalf("explicit empty inventory: %v", err)
	}
}

func TestExternalAdapterOutputBufferIsBounded(t *testing.T) {
	canceled := 0
	b := &externalAdapterBuffer{limit: 4, cancel: func() { canceled++ }}
	for _, value := range []string{"abc", "def", strings.Repeat("secret-stderr", 1<<10)} {
		if n, err := b.Write([]byte(value)); err != nil || n != len(value) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if string(b.data) != "abcd" || !b.overflow || canceled != 1 {
		t.Fatalf("bounded output = %q, overflow %v, canceled %d", b.data, b.overflow, canceled)
	}
	stderr := &externalAdapterBuffer{limit: externalAdapterStderrLimit}
	_, _ = stderr.Write([]byte(strings.Repeat("secret-stderr", externalAdapterStderrLimit)))
	if len(stderr.data) != externalAdapterStderrLimit || !stderr.overflow {
		t.Fatal("stderr grew beyond its bound")
	}
	if !slices.Equal(stderr.data[:6], []byte("secret")) {
		t.Fatal("fixture did not exercise stderr contents")
	}
}
