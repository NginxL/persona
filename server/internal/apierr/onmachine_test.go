package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func onMachineWithoutPanic(t *testing.T, err error, name string) (out error) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("OnMachine panicked: %v", v)
		}
	}()
	return OnMachine(err, name)
}

func checkMachineRefusal(t *testing.T, err error, code, action string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("OnMachine returned %T, want *rpc.Error", err)
	}
	if re.Code != code || re.Message != "refusal" {
		t.Fatalf("OnMachine changed the refusal: %#v", re)
	}
	var data map[string]any
	if err := json.Unmarshal(re.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data["machine_name"] != "Studio" {
		t.Fatalf("data = %#v, want only machine_name Studio", data)
	}

	rec := httptest.NewRecorder()
	Call(rec, err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("HTTP status = %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != code || body["action"] != action || body["machine_name"] != "Studio" {
		t.Fatalf("HTTP body = %#v", body)
	}
}

func TestOnMachineNullAndAbsentData(t *testing.T) {
	tests := []struct {
		code   string
		action string
	}{
		{proto.CodeNoMachine, "add_computer"},
		{proto.CodeMachinePending, "confirm_computer"},
		{proto.CodeClaudeMissing, "install_claude"},
		{proto.CodeLauncherMissing, "connect_model"},
		{proto.TurnErrorNotLoggedIn, "connect_model"},
	}
	seen := make(map[string]bool, len(tests))
	for _, tc := range tests {
		seen[tc.code] = true
		for _, data := range []struct {
			name string
			raw  json.RawMessage
		}{
			{"null", json.RawMessage(`null`)},
			{"absent", nil},
		} {
			t.Run(tc.code+"/"+data.name, func(t *testing.T) {
				in := &rpc.Error{Code: tc.code, Message: "refusal", Data: data.raw}
				got := onMachineWithoutPanic(t, in, "Studio")
				if got == nil {
					return
				}
				checkMachineRefusal(t, got, tc.code, tc.action)
				if string(in.Data) != string(data.raw) {
					t.Fatalf("OnMachine mutated its input: %s", in.Data)
				}
			})
		}
	}
	for code := range codeActions {
		if !seen[code] {
			t.Errorf("codeActions entry %s has no null/absent data regression", code)
		}
	}
}

func TestOnMachinePreservesExistingObjectAndAction(t *testing.T) {
	const raw = `{"delivered":"none","launcher":"claude","action":"edit_provider","retryable":false}`
	in := &rpc.Error{Code: proto.CodeClaudeMissing, Message: "refusal", Data: json.RawMessage(raw)}
	got := OnMachine(in, "Studio")
	var re *rpc.Error
	if !errors.As(got, &re) {
		t.Fatalf("OnMachine returned %T", got)
	}
	if re.Code != in.Code || re.Message != in.Message {
		t.Fatalf("OnMachine changed the refusal: %#v", re)
	}
	var data map[string]any
	if err := json.Unmarshal(re.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 5 || data["machine_name"] != "Studio" || data["delivered"] != "none" ||
		data["launcher"] != "claude" || data["action"] != "edit_provider" || data["retryable"] != false {
		t.Fatalf("existing fields were not preserved: %#v", data)
	}
	if string(in.Data) != raw {
		t.Fatalf("OnMachine mutated its input: %s", in.Data)
	}
	rec := httptest.NewRecorder()
	body := call(t, rec, rec, got)
	if rec.Code != http.StatusConflict || body["code"] != in.Code || body["action"] != "edit_provider" ||
		body["machine_name"] != "Studio" || body["delivered"] != "none" || body["retryable"] != false {
		t.Fatalf("HTTP response changed existing fields or custom action: %d %#v", rec.Code, body)
	}
}

func TestOnMachineLeavesOtherDataAndCodesAlone(t *testing.T) {
	t.Run("existing machine name", func(t *testing.T) {
		in := &rpc.Error{Code: proto.CodeClaudeMissing, Message: "refusal", Data: json.RawMessage(`{"machine_name":"Existing"}`)}
		if got := OnMachine(in, "Studio"); got != in {
			t.Fatal("an existing machine_name was replaced")
		}
	})
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"invalid JSON", `{"machine_name":`},
		{"array", `[]`},
		{"string", `"not an object"`},
		{"number", `42`},
		{"boolean", `true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &rpc.Error{Code: proto.CodeClaudeMissing, Message: "refusal", Data: json.RawMessage(tc.raw)}
			if got := OnMachine(in, "Studio"); got != in {
				t.Fatalf("invalid or non-object data was changed: %#v", got)
			}
		})
	}
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"unrelated code with null data", json.RawMessage(`null`)},
		{"unrelated code with absent data", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &rpc.Error{Code: "input_draft", Message: "refusal", Data: tc.raw}
			if got := OnMachine(in, "Studio"); got != in {
				t.Fatalf("an unrelated coded refusal was changed: %#v", got)
			}
		})
	}
}
