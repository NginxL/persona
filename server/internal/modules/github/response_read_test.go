package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedResponseBody struct {
	*strings.Reader
	err    error
	closed bool
}

func (b *failedResponseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = b.err
	}
	return n, err
}

func (b *failedResponseBody) Close() error { b.closed = true; return nil }

func TestResponseReadFailures(t *testing.T) {
	for _, kind := range []string{"json", "raw", "write", "login"} {
		t.Run(kind, func(t *testing.T) {
			body := &failedResponseBody{
				Reader: strings.NewReader(`{"login":"example-user"}`),
				err:    errors.New("connection interrupted " + oldToken),
			}
			calls := 0
			c := New(Config{}, "", nil)
			c.token = oldToken
			c.client = &http.Client{Transport: responseTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body, Request: r}, nil
			})}
			jsonOut := struct{ Login string }{Login: "unchanged"}
			rawOut := []byte("unchanged")
			var err error
			switch kind {
			case "json":
				_, err = c.Get(context.Background(), "/user", &jsonOut)
			case "raw":
				_, err = c.Get(context.Background(), "/repos/example-org/example-repo/actions/jobs/1/logs", &rawOut)
			case "write":
				_, _, err = c.request(context.Background(), http.MethodPost, "/repos/example-org/example-repo/issues/1/comments", nil, []byte(`{"body":"example"}`), nil)
			case "login":
				err = c.postForm(context.Background(), "/login/device/code", url.Values{}, &jsonOut)
			}
			if err == nil || !strings.Contains(err.Error(), "connection interrupted [token]") || strings.Contains(err.Error(), oldToken) {
				t.Errorf("read failure = %v, want a scrubbed connection error", err)
			}
			if !body.closed || calls != 1 {
				t.Errorf("closed = %v, requests = %d; want closed body and no retry", body.closed, calls)
			}
			if jsonOut.Login != "unchanged" || string(rawOut) != "unchanged" {
				t.Errorf("failed response changed output: JSON = %+v, raw = %q", jsonOut, rawOut)
			}
		})
	}
}
