package toolcli

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
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

func TestSecretStoreReadsOnlyAfterApprovalAndSendsCiphertext(t *testing.T) {
	const value = "FAKE-STORE-ONLY-LOCAL-92494"
	path := filepath.Join(t.TempDir(), "field")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	old := call
	t.Cleanup(func() { call = old })
	var calls []string
	call = func(_ context.Context, name string, args any) (proto.ToolResult, error) {
		raw, _ := json.Marshal(args)
		calls = append(calls, string(raw))
		if strings.Contains(string(raw), value) || strings.Contains(string(raw), path) {
			t.Fatal("source value or path crossed tool boundary")
		}
		var in struct {
			Phase  string                 `json:"phase"`
			Target wire.SecretStoreTarget `json:"target"`
			KeyID  string                 `json:"key_id"`
			Sealed []byte                 `json:"sealed"`
		}
		_ = json.Unmarshal(raw, &in)
		if in.Phase == "prepare" {
			b, _ := json.Marshal(wire.SecretStoreResult{Target: in.Target, KeyID: "one", Key: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())})
			return proto.TextResult(string(b), false), nil
		}
		k, _ := hpke.NewDHKEMPrivateKey(priv)
		plain, err := hpke.Open(k, hpke.HKDFSHA256(), hpke.AES256GCM(), proto.SecretStoreInfo(in.KeyID), in.Sealed)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(plain)
		var vals []proto.SecretStoreValue
		if json.Unmarshal(plain, &vals) != nil || len(vals) != 1 || string(vals[0].Value) != value {
			t.Fatal("ciphertext did not preserve the value")
		}
		return proto.TextResult(`{"references":{"credential":"op://vault/item/field"}}`, false), nil
	}
	manifest, _ := json.Marshal(storeFileManifest{Operation: "create", Vault: "Dev", Title: "test", Category: "API_CREDENTIAL", Fields: []storeFileField{{Name: "credential", File: path}}})
	var out, errs bytes.Buffer
	if code := Main(context.Background(), []string{"secret_store", string(manifest)}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("exit %d, %s", code, errs.String())
	}
	if len(calls) != 2 || !strings.Contains(out.String(), "op://") || strings.Contains(out.String()+errs.String(), value) {
		t.Fatal("unexpected boundary output")
	}
	// A denied preparation must never touch a field file: even a nonexistent source is safe until approved.
	call = func(context.Context, string, any) (proto.ToolResult, error) {
		return proto.ErrorResult(proto.ToolErrorDeclined, "The user declined", 0), nil
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := Main(context.Background(), []string{"secret_store", string(manifest)}, strings.NewReader(""), &out, &errs); code != exitDeclined {
		t.Fatalf("denial became a file error: %s", errs.String())
	}
}
func TestSecretStoreRefusesPlaintextAndUnsafeSources(t *testing.T) {
	for _, raw := range []string{`{"operation":"create","vault":"Dev","title":"x","category":"PASSWORD","fields":[{"name":"password","value":"do-not-forward"}]}`, `{"operation":"create","vault":"Dev","title":"x","category":"PASSWORD","fields":[{"name":"password","file":"x","type":"OTP"}]}`} {
		if _, _, err := storeManifest([]byte(raw)); err == nil {
			t.Fatal("unsafe manifest accepted")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "source")
	_ = os.WriteFile(path, []byte("fake"), 0600)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if b, err := readPrivateFile(path, 100); err == nil || b != nil {
		t.Fatal("public file accepted")
	}
	_ = os.Chmod(path, 0600)
	link := filepath.Join(dir, "link")
	_ = os.Symlink(path, link)
	if b, err := readPrivateFile(link, 100); err == nil || b != nil {
		t.Fatal("symlink accepted")
	}
}
