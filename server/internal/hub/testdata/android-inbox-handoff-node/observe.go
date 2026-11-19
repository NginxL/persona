package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type fixtureRequestDigest struct{}

// Forward the real connection (including the WebSocket) and record only the two
// fixture writes. The audit excludes credentials, message bodies and other calls.
func observe(origin, directory string) error {
	target, err := url.Parse(origin)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" {
		return errors.New("expected the isolated loopback hub")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(res *http.Response) error {
		r := res.Request
		digest, monitored := r.Context().Value(fixtureRequestDigest{}).(string)
		if !monitored {
			return nil
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			return errors.New("unreadable fixture response")
		}
		res.Body = io.NopCloser(bytes.NewReader(body))
		file, err := os.OpenFile(filepath.Join(directory, "requests.jsonl"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewEncoder(file).Encode(map[string]any{
			"method": r.Method, "path": r.URL.Path, "manager_header": r.Header.Get("X-Persona-Manager"),
			"manager_query": r.URL.Query().Get("manager"), "operation_id": r.Header.Get("X-Persona-Op-Id"),
			"status": res.StatusCode, "request_sha256": digest, "response_sha256": fmt.Sprintf("%x", sha256.Sum256(body)),
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.WriteFile(filepath.Join(directory, "observer-url"), []byte("http://"+listener.Addr().String()), 0600); err != nil {
		return err
	}
	return http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/messages") || strings.HasSuffix(r.URL.Path, "/handoff")) {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			r.Body.Close()
			if err != nil {
				http.Error(w, "unreadable fixture request", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r = r.WithContext(context.WithValue(r.Context(), fixtureRequestDigest{}, fmt.Sprintf("%x", sha256.Sum256(body))))
		}
		proxy.ServeHTTP(w, r)
	}))
}
