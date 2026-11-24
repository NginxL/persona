package scriptcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

func createDirectionForTest(f *fake, args ...string) result {
	var out, errOut bytes.Buffer
	code := f.runner(false).CreateDirection(context.Background(), args, &out, &errOut)
	return result{code: code, out: out.String(), errOut: errOut.String()}
}

func writeProgressForTest(f *fake, args ...string) result {
	var out, errOut bytes.Buffer
	code := f.runner(false).WriteProgress(context.Background(), args, &out, &errOut)
	return result{code: code, out: out.String(), errOut: errOut.String()}
}

func TestCreateDirectionExactRequestAndSuccessOutput(t *testing.T) {
	setScriptTerminal(t, "en")
	f := &fake{reply: `{"status":"ok","leader": { "id": "direction_1", "name": "Release" }}`}
	got := createDirectionForTest(f, "Release", "--machine", "machine-example", "--project", "release", "--session", "session-example")
	wantRequest := map[string]any{
		"kind": nodeclient.DirectionCreateKind,
		"input": map[string]any{
			"name": "Release", "project": "release", "machine_id": "machine-example", "session_id": "session-example",
		},
	}
	if len(f.reqs) != 1 || !reflect.DeepEqual(f.reqs[0], wantRequest) {
		t.Fatalf("requests %#v, want exactly %#v", f.reqs, wantRequest)
	}
	want := result{code: ExitOK, out: "{\"leader\":{\"id\":\"direction_1\",\"name\":\"Release\"}}\n"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCreateDirectionOptionalPairAndDoubleDash(t *testing.T) {
	f := &fake{reply: `{"status":"ok","leader":{}}`}
	got := createDirectionForTest(f, "--project=release", "--", "-Release")
	wantInput := map[string]any{"name": "-Release", "project": "release"}
	if got.code != ExitOK || got.out != "{\"leader\":{}}\n" || got.errOut != "" || !reflect.DeepEqual(input(t, f), wantInput) {
		t.Fatalf("result %+v, input %#v; want input %#v", got, input(t, f), wantInput)
	}
	if _, ok := input(t, f)["machine_id"]; ok {
		t.Fatalf("an omitted machine was sent: %#v", input(t, f))
	}
	if _, ok := input(t, f)["session_id"]; ok {
		t.Fatalf("an omitted session was sent: %#v", input(t, f))
	}
}

func TestWriteProgressExactRequestRepeatedLinksAndEmptyFields(t *testing.T) {
	f := &fake{reply: `{"status":"ok","leader":{"id":"direction_1"}}`}
	got := writeProgressForTest(f,
		"direction_1", "--doing", "Working", "--link", "https://example.com/a,b",
		"--reached", "Reached", "--link=https://example.com/c", "--needs-you", "")
	wantRequest := map[string]any{
		"kind": nodeclient.DirectionProgressKind,
		"input": map[string]any{
			"id": "direction_1", "reached": "Reached", "doing": "Working", "needs_you": "",
			"links": []any{"https://example.com/a,b", "https://example.com/c"},
		},
	}
	if got.code != ExitOK || got.out != "{\"leader\":{\"id\":\"direction_1\"}}\n" || got.errOut != "" {
		t.Fatalf("result %+v", got)
	}
	if len(f.reqs) != 1 || !reflect.DeepEqual(f.reqs[0], wantRequest) {
		t.Fatalf("requests %#v, want exactly %#v", f.reqs, wantRequest)
	}
}

func TestWriteProgressDoubleDashAndLinksOmitted(t *testing.T) {
	f := &fake{reply: `{"status":"ok","leader":{}}`}
	got := writeProgressForTest(f, "--doing", "Working", "--", "-direction")
	wantInput := map[string]any{"id": "-direction", "reached": "", "doing": "Working", "needs_you": ""}
	if got.code != ExitOK || !reflect.DeepEqual(input(t, f), wantInput) {
		t.Fatalf("result %+v, input %#v; want %#v", got, input(t, f), wantInput)
	}
}

func TestDirectionUsageErrorsNeverDial(t *testing.T) {
	setScriptTerminal(t, "en")
	tests := []struct {
		name    string
		create  bool
		args    []string
		errText string
	}{
		{name: "create missing name", create: true, args: []string{"--project", "p"}, errText: "exactly one"},
		{name: "create empty name", create: true, args: []string{" ", "--project", "p"}, errText: "exactly one"},
		{name: "create extra name", create: true, args: []string{"a", "b", "--project", "p"}, errText: "exactly one"},
		{name: "create missing project", create: true, args: []string{"a"}, errText: "--project is required"},
		{name: "create empty project", create: true, args: []string{"a", "--project", " "}, errText: "--project is required"},
		{name: "machine without session", create: true, args: []string{"a", "--project", "p", "--machine", "m"}, errText: "supplied together"},
		{name: "session without machine", create: true, args: []string{"a", "--project", "p", "--session", "s"}, errText: "supplied together"},
		{name: "empty machine in pair", create: true, args: []string{"a", "--project", "p", "--machine=", "--session", "s"}, errText: "supplied together"},
		{name: "empty session in pair", create: true, args: []string{"a", "--project", "p", "--machine", "m", "--session="}, errText: "supplied together"},
		{name: "empty pair", create: true, args: []string{"a", "--project", "p", "--machine=", "--session="}, errText: "supplied together"},
		{name: "create unknown flag", create: true, args: []string{"a", "--project", "p", "--bogus"}, errText: "bogus"},
		{name: "progress missing id", args: []string{"--doing", "work"}, errText: "exactly one"},
		{name: "progress empty id", args: []string{" ", "--doing", "work"}, errText: "exactly one"},
		{name: "progress extra id", args: []string{"a", "b", "--doing", "work"}, errText: "exactly one"},
		{name: "empty progress", args: []string{"a"}, errText: "at least one"},
		{name: "blank progress", args: []string{"a", "--reached", " ", "--doing=", "--needs-you", ""}, errText: "at least one"},
		{name: "links only", args: []string{"a", "--link", "https://example.com/a,b"}, errText: "at least one"},
		{name: "blank text and links only", args: []string{"a", "--doing", " ", "--link", "https://example.com/a"}, errText: "at least one"},
		{name: "progress unknown flag", args: []string{"a", "--doing", "work", "--bogus"}, errText: "bogus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{reply: `{"status":"ok","leader":{}}`}
			var got result
			command := "write-progress"
			if tc.create {
				command = "create-direction"
				got = createDirectionForTest(f, tc.args...)
			} else {
				got = writeProgressForTest(f, tc.args...)
			}
			if got.code != ExitUsage || got.out != "" || len(f.reqs) != 0 {
				t.Fatalf("result %+v, requests %d; want usage error without dialing", got, len(f.reqs))
			}
			prefix := "personad " + command + ": "
			if !strings.HasPrefix(got.errOut, prefix) || !strings.Contains(got.errOut, tc.errText) || !strings.Contains(got.errOut, "usage: personad "+command) {
				t.Fatalf("stderr %q, want prefix %q, %q and usage", got.errOut, prefix, tc.errText)
			}
		})
	}
}

func TestDirectionHelpNeverDials(t *testing.T) {
	setScriptTerminal(t, "en")
	for _, tc := range []struct {
		name   string
		create bool
		usage  string
	}{
		{name: "create", create: true, usage: createDirectionUsage},
		{name: "progress", usage: writeProgressUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, help := range []string{"--help", "-h"} {
				f := &fake{}
				var got result
				if tc.create {
					got = createDirectionForTest(f, help)
				} else {
					got = writeProgressForTest(f, help)
				}
				if got.code != ExitOK || got.out != i18n.T(i18n.En, tc.usage)+"\n" || got.errOut != "" || len(f.reqs) != 0 {
					t.Fatalf("%s: result %+v, requests %d", help, got, len(f.reqs))
				}
			}
		})
	}
}

func TestDirectionRepliesAndTransportAreOneShot(t *testing.T) {
	setScriptTerminal(t, "en")
	tests := []struct {
		name    string
		reply   string
		dialErr error
		code    int
		errText string
	}{
		{name: "manager invalid", reply: `{"status":"invalid","error":"bad project"}`, code: ExitUsage, errText: "bad project"},
		{name: "manager failed", reply: `{"status":"failed","error":"database stopped"}`, code: ExitDenied, errText: "database stopped"},
		{name: "manager unavailable", reply: `{"status":"unavailable","error":"manager offline"}`, code: ExitUnreachable, errText: "manager offline"},
		{name: "old node", reply: `{"error":"unknown kind"}`, code: ExitUnreachable, errText: "unknown kind"},
		{name: "malformed reply", reply: `not json`, code: ExitUnreachable, errText: "bad answer from the Persona node"},
		{name: "ok without leader", reply: `{"status":"ok"}`, code: ExitUnreachable, errText: "no leader object"},
		{name: "null leader", reply: `{"status":"ok","leader":null}`, code: ExitUnreachable, errText: "no leader object"},
		{name: "array leader", reply: `{"status":"ok","leader":[]}`, code: ExitUnreachable, errText: "no leader object"},
		{name: "unknown status", reply: `{"status":"future"}`, code: ExitUnreachable, errText: `unknown status "future"`},
		{name: "retry is not followed", reply: `{"status":"unavailable","error":"node restarting","retry":true}`, code: ExitUnreachable, errText: "node restarting"},
		{name: "no node", dialErr: fmt.Errorf("%w: socket missing", nodeclient.ErrNoNode), code: ExitUnreachable, errText: "No Persona node is running"},
		{name: "transport failure", dialErr: errors.New("socket broke"), code: ExitUnreachable, errText: "socket broke"},
	}
	commands := []struct {
		name string
		run  func(*fake) result
	}{
		{name: "create", run: func(f *fake) result { return createDirectionForTest(f, "Release", "--project", "release") }},
		{name: "progress", run: func(f *fake) result { return writeProgressForTest(f, "direction_1", "--doing", "work") }},
	}
	for _, command := range commands {
		for _, tc := range tests {
			t.Run(command.name+"/"+tc.name, func(t *testing.T) {
				f := &fake{reply: tc.reply, err: tc.dialErr}
				got := command.run(f)
				wantErr := got.errOut
				if !strings.Contains(wantErr, tc.errText) {
					t.Fatalf("stderr %q, want it to contain %q", wantErr, tc.errText)
				}
				if got.code != tc.code || got.out != "" || len(f.reqs) != 1 {
					t.Fatalf("result %+v, requests %d; want exit %d, empty stdout and exactly one request", got, len(f.reqs), tc.code)
				}
				if strings.Count(got.errOut, "\n") != 1 {
					t.Fatalf("stderr %q, want exactly one diagnostic line", got.errOut)
				}
			})
		}
	}
}

func TestDirectionCallsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(Runner) int
	}{
		{name: "create", run: func(r Runner) int {
			var out, errOut bytes.Buffer
			return r.CreateDirection(context.Background(), []string{"Release", "--project", "release"}, &out, &errOut)
		}},
		{name: "progress", run: func(r Runner) int {
			var out, errOut bytes.Buffer
			return r.WriteProgress(context.Background(), []string{"direction_1", "--doing", "work"}, &out, &errOut)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{reply: `{"status":"ok","leader":{}}`}
			r := f.runner(false)
			calls := 0
			r.Dial = func(ctx context.Context, req any) ([]byte, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > directionCallTimeout || time.Until(deadline) <= 0 {
					t.Fatalf("context deadline %v (present %t), want a positive bound of at most %s", deadline, ok, directionCallTimeout)
				}
				return f.dial(ctx, req)
			}
			if code := tc.run(r); code != ExitOK || calls != 1 {
				t.Fatalf("exit %d, calls %d; want one bounded successful call", code, calls)
			}
		})
	}
}

func TestDirectionInterruption(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, Runner, *bytes.Buffer, *bytes.Buffer) int
	}{
		{name: "create", run: func(ctx context.Context, r Runner, out, errOut *bytes.Buffer) int {
			return r.CreateDirection(ctx, []string{"Release", "--project", "release"}, out, errOut)
		}},
		{name: "progress", run: func(ctx context.Context, r Runner, out, errOut *bytes.Buffer) int {
			return r.WriteProgress(ctx, []string{"direction_1", "--doing", "work"}, out, errOut)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			f := &fake{}
			r := f.runner(false)
			calls := 0
			r.Dial = func(ctx context.Context, req any) ([]byte, error) {
				calls++
				cancel()
				return nil, ctx.Err()
			}
			var out, errOut bytes.Buffer
			if code := tc.run(ctx, r, &out, &errOut); code != ExitInterrupted || out.Len() != 0 || errOut.Len() != 0 || calls != 1 {
				t.Fatalf("exit %d, stdout %q, stderr %q, calls %d; want 130, no output and one call", code, out.String(), errOut.String(), calls)
			}
		})
	}
}
