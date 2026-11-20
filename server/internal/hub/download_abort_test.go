package hub

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A download that cannot be finished ends with an error every client sees, never as a complete response (#10855).
// The hub's routes are served whole (auth, gzip, the request log, panic recovery) over a real connection: HTTP/1.1,
// and HTTP/2 with TLS as on the hub's TLS port.

const downloadAbortSize = 3<<20 + 1234 // four chunks; reads fail at the third

// abortFixture serves the hub's routes, with machine m1 connected to f, over HTTP/1.1 and over HTTP/2 with TLS; a
// download waits for the node at most 5 s, looking for it every 5 ms.
func abortFixture(t *testing.T, f *downloadNode) (h1, h2 *httptest.Server, token string) {
	t.Helper()
	saved := [...]time.Duration{downloadRetryWindow, downloadRetryPoll, downloadRetryMax}
	downloadRetryWindow, downloadRetryPoll, downloadRetryMax = 5*time.Second, 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { downloadRetryWindow, downloadRetryPoll, downloadRetryMax = saved[0], saved[1], saved[2] })
	h, _ := downloadFixture(t, f)
	routes := h.routes()
	h1 = httptest.NewServer(routes)
	h2 = httptest.NewUnstartedServer(routes)
	h2.EnableHTTP2 = true
	h2.StartTLS()
	t.Cleanup(h1.Close)
	t.Cleanup(h2.Close)
	return h1, h2, h.cfg.Token
}

// h2Result is what an HTTP/2 client reads in the frames themselves, not what Go's client makes of them (it counts the
// bytes against Content-Length on its own): the status, the length declared, the body and how the stream ended.
type h2Result struct {
	status, length string
	body           []byte
	ended, reset   bool // END_STREAM: complete, as far as any client can tell; RST_STREAM: broken off
}

func h2Get(t *testing.T, srv *httptest.Server, path string, header ...string) h2Result {
	t.Helper()
	addr := srv.Listener.Addr().String()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	fr := http2.NewFramer(conn, conn)
	fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	fields := append([]string{":method", "GET", ":scheme", "https", ":authority", addr, ":path", path}, header...)
	for i := 0; i+1 < len(fields); i += 2 {
		_ = enc.WriteField(hpack.HeaderField{Name: fields[i], Value: fields[i+1]})
	}
	if err := fr.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndStream: true, EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	var res h2Result
	for !res.ended && !res.reset {
		frame, err := fr.ReadFrame()
		if err != nil {
			t.Fatalf("after %d bytes: %v", len(res.body), err)
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = fr.WriteSettingsAck()
			}
		case *http2.PingFrame:
			if !f.IsAck() {
				_ = fr.WritePing(true, f.Data)
			}
		case *http2.MetaHeadersFrame:
			res.status, res.ended = f.PseudoValue("status"), f.StreamEnded()
			for _, hf := range f.RegularFields() {
				if hf.Name == "content-length" {
					res.length = hf.Value
				}
			}
		case *http2.DataFrame:
			res.body = append(res.body, f.Data()...)
			if n := uint32(len(f.Data())); n > 0 && !f.StreamEnded() { // keep both windows open
				_ = fr.WriteWindowUpdate(0, n)
				_ = fr.WriteWindowUpdate(1, n)
			}
			res.ended = f.StreamEnded()
		case *http2.RSTStreamFrame:
			res.reset = true
		case *http2.GoAwayFrame:
			t.Fatalf("GOAWAY %v after %d bytes", f.ErrCode, len(res.body))
		}
	}
	return res
}

// h1Get reads path whole over HTTP/1.1 with Go's client: the status, the body and the error reading it.
func h1Get(t *testing.T, srv *httptest.Server, token, path string, header ...string) (int, []byte, error) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return res.StatusCode, body, err
}

// nodeFails has f's reads at or past offset fail: with err, or when err is nil by the node's connection dropping
// (as a lost relay drops it). once: only the first of them. then, when given, runs at that first failure (f.mu held).
func nodeFails(f *downloadNode, offset int64, err error, once bool, then func(p proto.FileChunkRequest)) {
	failed := false
	f.fail = func(p proto.FileChunkRequest) error {
		if p.Length == 0 || p.Offset < offset || once && failed {
			return nil
		}
		if !failed && then != nil {
			then(p)
		}
		failed = true
		if err != nil {
			return err
		}
		f.drop()
		return errors.New("not answered: the connection dropped")
	}
}

// downloadBytes is a file's content that gzip cannot shrink much, so a compressed answer streams as it is read.
func downloadBytes() []byte {
	b := make([]byte, downloadAbortSize)
	_, _ = rand.NewChaCha8([32]byte{1, 0, 8, 5, 5}).Read(b)
	return b
}

func TestDownloadThatBreaksOffIsAbortedNotEnded(t *testing.T) {
	data := downloadBytes()
	const path = "/api/v1/machines/m1/file/download?path=preview.mp4&cwd=/work"
	for _, test := range []struct {
		name   string
		mime   string
		header []string // request headers, name then value
		status string
		gone   bool // the node's connection drops and is not back in time
	}{
		{name: "whole file", mime: "video/mp4", status: "200"},
		{name: "range", mime: "video/mp4", header: []string{"range", "bytes=100-"}, status: "206"},
		{name: "gzipped", mime: "text/plain; charset=utf-8", header: []string{"accept-encoding", "gzip"}, status: "200"},
		{name: "node gone", mime: "video/mp4", status: "200", gone: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &downloadNode{data: data, mime: test.mime}
			h1, h2, token := abortFixture(t, f)
			var readErr error = &rpc.Error{Message: "read preview.mp4: input/output error"}
			if test.gone {
				downloadRetryWindow, readErr = 100*time.Millisecond, nil
			}
			nodeFails(f, 2<<20, readErr, false, nil)
			res := h2Get(t, h2, path, append([]string{"authorization", "Bearer " + token}, test.header...)...)
			if res.status != test.status || res.ended || !res.reset || len(res.body) >= len(data) {
				t.Fatalf("HTTP/2: status %s, %d bytes of %s, ended %t, reset %t", res.status, len(res.body), res.length, res.ended, res.reset)
			}
			if test.gone {
				f.connect() // back, to be lost again by the next request
			}
			code, body, err := h1Get(t, h1, token, path, test.header...)
			if err == nil || strconv.Itoa(code) != test.status || len(body) >= len(data) {
				t.Fatalf("HTTP/1.1: status %d, %d bytes, error %v", code, len(body), err)
			}
		})
	}
}

func TestDownloadCarriesOnWhenTheNodeComesBack(t *testing.T) {
	data := downloadBytes()
	for _, rangeHeader := range []string{"", "bytes=1048570-"} {
		t.Run(fmt.Sprintf("range %q", rangeHeader), func(t *testing.T) {
			f := &downloadNode{data: data}
			_, h2, token := abortFixture(t, f)
			// The connection drops at a read and is back a little later: the download waits, then reads on from there.
			var failed proto.FileChunkRequest
			back := make(chan struct{})
			nodeFails(f, 2<<20, nil, true, func(p proto.FileChunkRequest) {
				failed = p
				time.AfterFunc(50*time.Millisecond, func() { f.connect(); close(back) })
			})
			req, _ := http.NewRequest("GET", h2.URL+"/api/v1/machines/m1/file/download?path=preview.mp4&cwd=/work", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			want, code := data, http.StatusOK
			if rangeHeader != "" {
				req.Header.Set("Range", rangeHeader)
				want, code = data[1048570:], http.StatusPartialContent
			}
			res, err := h2.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != code || res.ProtoMajor != 2 || !bytes.Equal(body, want) {
				t.Fatalf("status %d over HTTP/%d: %d of %d bytes, %v", res.StatusCode, res.ProtoMajor, len(body), len(want), err)
			}
			<-back
			f.mu.Lock()
			defer f.mu.Unlock()
			var again []proto.FileChunkRequest
			for _, p := range f.requests {
				if p.Offset == failed.Offset && p.Length > 0 {
					again = append(again, p)
				}
			}
			if failed.Revision != "r1" || len(again) != 2 || again[1] != failed {
				t.Fatalf("the read that failed %+v, then %+v", failed, again)
			}
		})
	}
}

func TestDownloadAbortsWhenTheFileChangedWhileTheNodeWasAway(t *testing.T) {
	data := downloadBytes()
	f := &downloadNode{data: data}
	_, h2, token := abortFixture(t, f)
	nodeFails(f, 2<<20, nil, true, func(proto.FileChunkRequest) {
		f.revision = "r2" // saved again while the node was away
		time.AfterFunc(50*time.Millisecond, f.connect)
	})
	res := h2Get(t, h2, "/api/v1/machines/m1/file/download?path=preview.mp4", "authorization", "Bearer "+token)
	if res.status != "200" || res.ended || !res.reset || len(res.body) > 2<<20 || !bytes.HasPrefix(data, res.body) {
		t.Fatalf("status %s, %d bytes, ended %t, reset %t", res.status, len(res.body), res.ended, res.reset)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Asked again at the same offset and revision once it was back, and refused: nothing of the new file is sent.
	n := len(f.requests)
	if n < 2 || f.requests[n-1] != f.requests[n-2] || f.requests[n-1].Offset != 2<<20 || f.requests[n-1].Revision != "r1" {
		t.Fatalf("%+v", f.requests)
	}
}

// Only a node that is not there is waited for: a refusal, a changed file, a read that ran out of time or a request the
// client gave up end the download at once.
func TestDownloadWaitsOnlyForANodeThatIsAway(t *testing.T) {
	for _, test := range []struct {
		err  error
		away bool
	}{
		{rpc.ErrDropped, true},
		{rpc.NotSent(rpc.ErrClosed), true},
		{fmt.Errorf("box: %w", errOffline), true},
		{fmt.Errorf("m1: %w", errManagerOffline), true},
		{deviceError("m1", &rpc.Error{Code: devicewire.CodeNodeClosed, Message: "rpc: connection closed"}), true},
		{deviceError("m1", &rpc.Error{Code: devicewire.CodeMachineOffline, Message: "offline"}), true},
		{deviceError("m1", errBusDropped), true},
		{deviceError("m1", errBusOffline), true},
		{&rpc.Error{Code: proto.CodeFileChanged, Message: "file changed during download"}, false},
		{deviceError("m1", &rpc.Error{Code: devicewire.CodeDeadline, Message: "deadline"}), false},
		{context.DeadlineExceeded, false},
		{context.Canceled, false},
		{errors.New("read preview.mp4: input/output error"), false},
	} {
		if got := nodeAway(test.err); got != test.away {
			t.Errorf("%v: away %t", test.err, got)
		}
	}
	// The pauses grow up to downloadRetryMax, and the window is counted from the first failure.
	saved := [...]time.Duration{downloadRetryWindow, downloadRetryPoll, downloadRetryMax}
	downloadRetryWindow, downloadRetryPoll, downloadRetryMax = time.Minute, time.Second, 3*time.Second
	t.Cleanup(func() { downloadRetryWindow, downloadRetryPoll, downloadRetryMax = saved[0], saved[1], saved[2] })
	tries := 0
	var pauses []time.Duration
	var untils []time.Time
	reader := &remoteFileReader{size: 3, request: proto.FileChunkRequest{Revision: "r1"},
		get: func(q proto.FileChunkRequest) (*proto.FileChunk, error) {
			if tries++; tries < 5 {
				return nil, rpc.ErrDropped
			}
			return &proto.FileChunk{Size: 3, Revision: "r1", Data: []byte("abc")}, nil
		},
		back: func(pause time.Duration, until time.Time) bool {
			pauses, untils = append(pauses, pause), append(untils, until)
			return true
		}}
	if b, err := io.ReadAll(reader); err != nil || string(b) != "abc" || reader.failed.Load() != nil {
		t.Fatalf("%q %v", b, err)
	}
	if fmt.Sprint(pauses) != "[1s 2s 3s 3s]" || !untils[3].Equal(untils[0]) {
		t.Fatalf("pauses %v, until %v", pauses, untils)
	}
	// Not back in time: the read fails with the node's error, and the download knows where.
	reader = &remoteFileReader{size: 3, get: func(proto.FileChunkRequest) (*proto.FileChunk, error) { return nil, rpc.ErrDropped },
		back: func(time.Duration, time.Time) bool { return false }}
	if _, err := reader.Read(make([]byte, 3)); !errors.Is(err, rpc.ErrDropped) || reader.failed.Load() == nil {
		t.Fatalf("%v %+v", err, reader.failed.Load())
	}
}
