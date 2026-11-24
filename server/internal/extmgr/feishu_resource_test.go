package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

type feishuResourceTransport func(*http.Request) (*http.Response, error)

func (f feishuResourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type feishuResourceBody struct {
	io.Reader
	read       *int
	closed     *int
	afterClose func()
}

func (r feishuResourceBody) Read(b []byte) (int, error) {
	n, e := r.Reader.Read(b)
	*r.read += n
	return n, e
}
func (r feishuResourceBody) Close() error {
	*r.closed++
	if r.afterClose != nil {
		r.afterClose()
	}
	return nil
}

type feishuResourceZeros struct{}

func (feishuResourceZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type feishuResourceStats struct {
	requests, read, closed int
	uploads, uploaded      int
	name                   string
	uploadErr              error
	afterRead              func()
}

// Every HTTP request is handled in memory, including the synthetic tenant token. The node uses the real upload RPC pipe.
func feishuResourceManager(t *testing.T, size int, length int64, mime string) (*Manager, *feishuResourceStats) {
	t.Helper()
	h := newTestManager(t)
	st := &feishuResourceStats{}
	api := botport.NewWithHTTP("cli_test", "synthetic-secret", "https://feishu.example", &http.Client{Transport: feishuResourceTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`))}, nil
		}
		if r.URL.Path != "/open-apis/im/v1/messages/om_synthetic/resources/img_synthetic" || (r.URL.Query().Get("type") != "image" && r.URL.Query().Get("type") != "file") || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			return nil, fmt.Errorf("unexpected resource request %s", r.URL.Path)
		}
		st.requests++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, ContentLength: length,
			Body: feishuResourceBody{Reader: io.LimitReader(feishuResourceZeros{}, int64(size)), read: &st.read, closed: &st.closed, afterClose: func() {
				if st.afterRead != nil {
					st.afterRead()
				}
			}}}, nil
	})})
	h.bots.loaded = true
	h.bots.api = api
	h.bots.set = FeishuBotSettings{Enabled: true, Owner: &BotOwner{OpenID: "ou_owner", ChatID: "oc_p2p"}}
	imessageNode(t, h, func(method string, p json.RawMessage) (any, error) {
		switch method {
		case "file.upload.begin":
			var a struct {
				Name string
				Size int
			}
			if err := json.Unmarshal(p, &a); err != nil {
				return nil, err
			}
			st.uploads++
			st.name = a.Name
			if a.Size != size {
				return nil, fmt.Errorf("upload size %d, want %d", a.Size, size)
			}
			if st.uploadErr != nil {
				return nil, st.uploadErr
			}
			return map[string]any{"upload_id": "upload_synthetic"}, nil
		case "file.upload.chunk":
			var a struct {
				Offset int
				Data   []byte
			}
			if err := json.Unmarshal(p, &a); err != nil {
				return nil, err
			}
			if a.Offset != st.uploaded {
				return nil, fmt.Errorf("offset %d, want %d", a.Offset, st.uploaded)
			}
			for _, b := range a.Data {
				if b != 0 {
					return nil, errors.New("resource bytes changed")
				}
			}
			st.uploaded += len(a.Data)
			return map[string]any{}, nil
		case "file.upload.end":
			return map[string]any{"path": "/tmp/" + st.name}, nil
		}
		return nil, fmt.Errorf("unexpected node request %s", method)
	})
	return h, st
}

func feishuResourceCall(h *Manager, method, kind, machine string, asFile bool) (map[string]any, error) {
	p, _ := json.Marshal(map[string]any{"message_id": "om_synthetic", "key": "img_synthetic", "kind": kind, "name": "attachment.dat", "machine_id": machine, "as_file": asFile})
	raw, ok, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: method, Params: p})
	if !ok {
		return nil, errors.New("app call not handled")
	}
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}

func TestFeishuResourceSingleDownload(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name            string
		size            int
		unknown, asFile bool
		mime, suffix    string
		file, tooLarge  bool
	}{
		{name: "small PNG", size: 100, mime: "image/png"},
		{name: "inline boundary", size: botImageMax, mime: "image/jpeg"},
		{name: "above inline boundary", size: botImageMax + 1, mime: "image/jpeg", suffix: ".jpg", file: true},
		{name: "4 MiB JPEG", size: 4 << 20, mime: "image/jpeg", suffix: ".jpg", file: true},
		{name: "4 MiB chunked JPEG", size: 4 << 20, unknown: true, mime: "image/jpeg", suffix: ".jpg", file: true},
		{name: "small GIF past image count", size: 100, asFile: true, mime: "image/gif", suffix: ".gif", file: true},
		{name: "WebP", size: 4 << 20, mime: "image/webp", suffix: ".webp", file: true},
		{name: "unknown type keeps PNG fallback", size: 4 << 20, mime: "application/octet-stream", suffix: ".png", file: true},
		{name: "file boundary", size: botFileMax, mime: "image/jpeg", suffix: ".jpg", file: true},
		{name: "declared over file limit", size: botFileMax + 1, mime: "image/jpeg", tooLarge: true},
		{name: "chunked over file limit", size: botFileMax + 2, unknown: true, mime: "image/jpeg", tooLarge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			length := int64(tc.size)
			if tc.unknown {
				length = -1
			}
			h, st := feishuResourceManager(t, tc.size, length, tc.mime)
			out, err := feishuResourceCall(h, assistantwire.FeishuResource, "image", "mac", tc.asFile)
			if err != nil {
				t.Fatal(err)
			}
			wantRead := tc.size
			if tc.tooLarge {
				if tc.unknown {
					wantRead = botFileMax + 1
				} else {
					wantRead = 0
				}
			}
			if st.requests != 1 || st.read != wantRead || st.closed != 1 {
				t.Fatalf("requests=%d bytes=%d closed=%d; want 1/%d/1", st.requests, st.read, st.closed, wantRead)
			}
			if tc.tooLarge {
				if out["too_large"] != true || out["data"] != nil || out["path"] != nil || st.uploads != 0 || out["name"] != "img_synthetic" {
					t.Fatalf("oversize result %v, stats %+v", out, st)
				}
			} else if tc.file {
				if st.uploads != 1 || st.uploaded != tc.size || st.name != "img_synthetic"+tc.suffix || out["path"] != "/tmp/"+st.name || out["mime"] != tc.mime || out["data"] != nil || out["too_large"] != nil {
					t.Fatalf("file result %v, stats %+v", out, st)
				}
			} else {
				if st.uploads != 0 || out["data"] == nil || out["path"] != nil || out["mime"] != tc.mime {
					t.Fatalf("inline result keys=%v, stats %+v", keysOfResource(out), st)
				}
			}
			t.Logf("resource_requests=%d response_bytes=%d uploads=%d uploaded_bytes=%d", st.requests, st.read, st.uploads, st.uploaded)
		})
	}
}

func keysOfResource(out map[string]any) []string {
	var keys []string
	for k := range out {
		keys = append(keys, k)
	}
	return keys
}

func TestFeishuResourceSaveFailure(t *testing.T) {
	needsLarkModule(t)
	for _, machine := range []string{"missing", "mac"} {
		t.Run(machine, func(t *testing.T) {
			h, st := feishuResourceManager(t, 4<<20, 4<<20, "image/jpeg")
			st.uploadErr = errors.New("synthetic upload failure")
			_, err := feishuResourceCall(h, assistantwire.FeishuResource, "image", machine, false)
			var coded *rpc.Error
			if !errors.As(err, &coded) || coded.Code != assistantwire.CodeFeishuSave {
				t.Fatalf("error %v", err)
			}
			if st.requests != 1 {
				t.Fatalf("downloads %d", st.requests)
			}
		})
	}
}

// The ordinary save route retains its original limit; it cannot bypass the inline route's file ceiling.
func TestFeishuSaveKeepsFileLimit(t *testing.T) {
	needsLarkModule(t)
	for _, unknown := range []bool{false, true} {
		length := int64(botFileMax + 2)
		if unknown {
			length = -1
		}
		h, st := feishuResourceManager(t, botFileMax+2, length, "image/jpeg")
		out, err := feishuResourceCall(h, assistantwire.FeishuSave, "file", "mac", false)
		if err != nil || out["too_large"] != true || st.uploads != 0 {
			t.Fatalf("result %v, error %v, stats %+v", out, err, st)
		}
	}
}

func TestFeishuSaveNormalAttachment(t *testing.T) {
	needsLarkModule(t)
	h, st := feishuResourceManager(t, 1024, 1024, "application/pdf")
	out, err := feishuResourceCall(h, assistantwire.FeishuSave, "file", "mac", false)
	if err != nil || out["path"] != "/tmp/attachment.dat" || st.requests != 1 || st.uploads != 1 || st.uploaded != 1024 || st.name != "attachment.dat" {
		t.Fatalf("result %v, error %v, stats %+v", out, err, st)
	}
}

func TestFeishuResourceRequiresPairedBot(t *testing.T) {
	needsLarkModule(t)
	for _, unpaired := range []bool{false, true} {
		h, st := feishuResourceManager(t, 4<<20, 4<<20, "image/jpeg")
		if unpaired {
			h.bots.set.Owner = nil
		} else {
			h.bots.set.Enabled = false
		}
		for _, method := range []string{assistantwire.FeishuResource, assistantwire.FeishuSave} {
			if _, err := feishuResourceCall(h, method, "image", "mac", false); !errors.Is(err, errBotOff) {
				t.Fatalf("%s: %v", method, err)
			}
		}
		if st.requests != 0 || st.uploads != 0 {
			t.Fatalf("unavailable bot accessed a resource: %+v", st)
		}
	}
}

func TestFeishuResourceRechecksPairingBeforeSave(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name   string
		change func(*Manager)
	}{
		{"disabled", func(h *Manager) { h.bots.set.Enabled = false }},
		{"unpaired", func(h *Manager) { h.bots.set.Owner = nil }},
		{"owner changed", func(h *Manager) { h.bots.set.Owner.OpenID = "ou_other" }},
		{"chat changed", func(h *Manager) { h.bots.set.Owner.ChatID = "oc_other" }},
		{"paired anew", func(h *Manager) { h.bots.set.Owner.At = h.bots.set.Owner.At.Add(time.Second) }},
		{"client changed", func(h *Manager) {
			h.bots.api = botport.New("cli_other", "synthetic-secret", "https://feishu.example")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := feishuResourceManager(t, 4<<20, 4<<20, "image/jpeg")
			st.afterRead = func() { h.bots.mu.Lock(); defer h.bots.mu.Unlock(); tc.change(h) }
			_, err := feishuResourceCall(h, assistantwire.FeishuResource, "image", "mac", false)
			var coded *rpc.Error
			if !errors.As(err, &coded) || coded.Code != assistantwire.CodeFeishuSave || coded.Message != errBotOff.Error() {
				t.Fatalf("error %v", err)
			}
			if st.requests != 1 || st.uploads != 0 {
				t.Fatalf("a changed pairing saved the resource: %+v", st)
			}
		})
	}
}
