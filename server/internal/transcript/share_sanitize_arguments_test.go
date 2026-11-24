package transcript

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSanitizeShareArgumentsPreservesValidJSONAndNormalizedKeys(t *testing.T) {
	raw := json.RawMessage(`{` +
		`"command":"export DB_PASSWORD=synthetic-db-secret\necho after-secret",` +
		`"header":"Authorization: Bearer synthetic-bearer-token",` +
		`"cookie_line":"Cookie: sid=synthetic-cookie-id; theme=dark",` +
		`"description":"run migration",` +
		`"timeout":30,` +
		`"\uff50\uff41\uff53\uff53\uff57\uff4f\uff52\uff44":["synthetic-item"],` +
		`"\uff54\uff4f\uff4b\uff45\uff4e":123456,` +
		`"\uff53\uff49\uff47":"",` +
		`"\uff4d\uff41\uff58\uff3f\uff54\uff4f\uff4b\uff45\uff4e\uff53":4096,` +
		`"\uff55\uff53\uff45\uff3f\uff54\uff4f\uff4b\uff45\uff4e":true` +
		`}`)
	clean, err := SanitizeShareArguments(raw)
	if err != nil {
		t.Fatalf("SanitizeShareArguments error: %v", err)
	}
	for _, secret := range []string{"synthetic-db-secret", "synthetic-bearer-token", "synthetic-cookie-id", "synthetic-item", "123456"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("secret %q leaked in %s", secret, clean)
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(clean), &got); err != nil {
		t.Fatalf("SanitizeShareArguments returned invalid JSON %q: %v", clean, err)
	}
	if got["command"] != "export DB_PASSWORD=[REDACTED]\necho after-secret" {
		t.Errorf("command = %q, want trailing lines preserved", got["command"])
	}
	if got["description"] != "run migration" || got["timeout"] != float64(30) {
		t.Errorf("sibling fields lost: %#v", got)
	}
	if got["password"] != "[REDACTED]" || got["token"] != "[REDACTED]" || got["sig"] != "[REDACTED]" {
		t.Errorf("NFKC-normalized sensitive keys not redacted: %#v", got)
	}
	if got["max_tokens"] != float64(4096) || got["use_token"] != true {
		t.Errorf("NFKC-normalized explicit count/flag settings lost: %#v", got)
	}

	// Redaction expansion past ShareMessageLimit is still rejected.
	repeated := strings.Repeat("token=1 ", ShareMessageLimit/8)
	oversized, err := json.Marshal(map[string]string{"cmd": repeated[:ShareMessageLimit-32]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SanitizeShareArguments(oversized); !errors.Is(err, ErrShareComponentLimit) {
		t.Fatalf("oversized marshaled output err = %v, want ErrShareComponentLimit", err)
	}
}
