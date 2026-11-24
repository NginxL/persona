package toolcli

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSecretStoreInterruptedExits130(t *testing.T) {
	dir := t.TempDir()
	fieldPath := filepath.Join(dir, "secret")
	if err := os.WriteFile(fieldPath, []byte("local-only-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(storeFileManifest{
		Operation: "create",
		Vault:     "Dev",
		Title:     "demo",
		Category:  "API_CREDENTIAL",
		Fields:    []storeFileField{{Name: "credential", File: fieldPath}},
	})
	if err != nil {
		t.Fatal(err)
	}

	old := call
	t.Cleanup(func() { call = old })

	// Case 1: caller cancels ctx while prepare is waiting for phone approval.
	prepCtx, cancelPrep := context.WithCancel(context.Background())
	call = func(ctx context.Context, name string, args any) (proto.ToolResult, error) {
		cancelPrep()
		return proto.ToolResult{}, ctx.Err()
	}
	var out, errOut bytes.Buffer
	code := Main(prepCtx, []string{"secret_store", string(manifest)}, strings.NewReader(""), &out, &errOut)
	if code != exitInterrupted {
		t.Fatalf("prepare interrupt exit = %d, want %d; stderr=%s", code, exitInterrupted, errOut.String())
	}
	if !strings.Contains(errOut.String(), `"kind":"interrupted"`) {
		t.Fatalf("prepare interrupt stderr = %q, want kind interrupted", errOut.String())
	}

	// Case 2: caller cancels ctx while commit is in flight after approval.
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	commitCtx, cancelCommit := context.WithCancel(context.Background())
	call = func(ctx context.Context, name string, args any) (proto.ToolResult, error) {
		raw, _ := json.Marshal(args)
		var in struct {
			Phase  string                 `json:"phase"`
			Target wire.SecretStoreTarget `json:"target"`
		}
		_ = json.Unmarshal(raw, &in)
		if in.Phase == "prepare" {
			b, _ := json.Marshal(wire.SecretStoreResult{
				Target: in.Target,
				KeyID:  "k1",
				Key:    base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()),
			})
			return proto.TextResult(string(b), false), nil
		}
		cancelCommit()
		return proto.ToolResult{}, ctx.Err()
	}
	out.Reset()
	errOut.Reset()
	code = Main(commitCtx, []string{"secret_store", string(manifest)}, strings.NewReader(""), &out, &errOut)
	if code != exitInterrupted {
		t.Fatalf("commit interrupt exit = %d, want %d; stderr=%s", code, exitInterrupted, errOut.String())
	}
	if !strings.Contains(errOut.String(), `"kind":"interrupted"`) || !strings.Contains(errOut.String(), "check metadata before retrying") {
		t.Fatalf("commit interrupt stderr = %q, want kind interrupted with metadata warning", errOut.String())
	}
}

func TestCommandLineNormalizesPlanStderrAndCustomExitKind(t *testing.T) {
	old := call
	t.Cleanup(func() { call = old })

	call = func(_ context.Context, name string, args any) (proto.ToolResult, error) {
		exit := 64
		b, _ := json.Marshal(plan{
			Name:   "ex",
			Exit:   &exit,
			Stderr: "bad flag --foo\n",
		})
		return proto.TextResult(string(b), false), nil
	}

	var out, errOut bytes.Buffer
	code := Main(context.Background(), []string{"--cli", "example", "--format", "text", "bad"}, strings.NewReader(""), &out, &errOut)
	if code != 64 || errOut.String() != "ex: bad flag --foo\n" {
		t.Fatalf("text mode = (%d, %q), want (64, %q)", code, errOut.String(), "ex: bad flag --foo\n")
	}

	out.Reset()
	errOut.Reset()
	code = Main(context.Background(), []string{"--cli", "example", "bad"}, strings.NewReader(""), &out, &errOut)
	var parsed map[string]any
	if err := json.Unmarshal(errOut.Bytes(), &parsed); err != nil {
		t.Fatalf("json stderr unmarshal: %v (%q)", err, errOut.String())
	}
	if code != 64 || parsed["error"] != "bad flag --foo" || parsed["kind"] != "failed" {
		t.Fatalf("json mode = (%d, %v), want code 64, error %q, kind %q", code, parsed, "bad flag --foo", "failed")
	}
}
