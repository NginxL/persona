package extension

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// fitNativeOracle is FitNative as it was before fitting a view it leaves
// alone skipped decoding the before and after encodings.
func fitNativeOracle(raw []byte, slot UISlot, version int, lang i18n.Lang) (json.RawMessage, error) {
	if err := wire.ValidateJSON("UIStreamView", raw); err != nil {
		return nil, err
	}
	if err := wire.CheckViewJSON(raw); err != nil {
		return nil, err
	}
	var view View
	if err := json.Unmarshal(raw, &view); err != nil {
		return nil, err
	}
	before, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	if _, err = fit(&view, slot, version, true, lang); err != nil {
		return nil, err
	}
	after, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	var source, old, next any
	_ = json.Unmarshal(raw, &source)
	_ = json.Unmarshal(before, &old)
	_ = json.Unmarshal(after, &next)
	fitted, err := json.Marshal(restorePresence(source, old, next))
	if err != nil {
		return nil, err
	}
	if err = wire.CheckFittedJSON(fitted); err != nil {
		return nil, err
	}
	return fitted, nil
}

// nativeFixtureViews are the example views and the views inside the example
// stream snapshots.
func nativeFixtureViews(t *testing.T) map[string][]byte {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "schema", "extension", "examples")
	out := map[string][]byte{"bench": nativeView(3, 4)}
	for _, pattern := range []string{"View.*.json", "UIStreamSnapshot.*.json", "traces/UIStreamSnapshot.*.json"} {
		names, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, name := range names {
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(filepath.Base(name), "UIStreamSnapshot") {
				var frame struct {
					View json.RawMessage `json:"view"`
				}
				if json.Unmarshal(raw, &frame) != nil || frame.View == nil {
					continue
				}
				raw = frame.View
			}
			out[name] = raw
		}
	}
	if len(out) < 10 {
		t.Fatalf("only %d fixture views", len(out))
	}
	return out
}

// FitNative answers exactly as before for every fixture view, slot and UI
// version, whether fitting changes the view (old versions, compact slots) or not.
func TestFitNativeMatchesOracle(t *testing.T) {
	fitted, unchanged, accepted := 0, 0, 0
	for name, raw := range nativeFixtureViews(t) {
		for _, slot := range UISlots {
			for version := 1; version <= UIVersion; version++ {
				for _, lang := range []i18n.Lang{i18n.En, i18n.Lang("zh-Hans")} {
					got, gotErr := FitNative(raw, slot, version, lang)
					want, wantErr := fitNativeOracle(raw, slot, version, lang)
					if (gotErr == nil) != (wantErr == nil) || gotErr != nil && gotErr.Error() != wantErr.Error() {
						t.Fatalf("%s %s v%d: error %v, oracle %v", name, slot.Name, version, gotErr, wantErr)
					}
					if !bytes.Equal(got, want) {
						t.Fatalf("%s %s v%d:\n got %s\nwant %s", name, slot.Name, version, got, want)
					}
					if gotErr != nil {
						continue
					}
					accepted++
					if canonicalOf(t, raw) == string(got) {
						unchanged++
					} else {
						fitted++
					}
				}
			}
		}
	}
	// Both paths must be exercised, or the comparison proves nothing.
	if fitted == 0 || unchanged == 0 {
		t.Fatalf("accepted %d: fitted %d, unchanged %d", accepted, fitted, unchanged)
	}
	t.Logf("accepted %d: fitted %d, unchanged %d", accepted, fitted, unchanged)
}

func canonicalOf(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}
