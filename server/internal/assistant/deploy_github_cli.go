package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// hasGitHubCLI positively identifies gh before trusting it with an authenticated request.
// The probe is deliberately uncached so replacing PATH or the executable cannot leave a stale identity behind.
func hasGitHubCLI(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "gh", "--version")
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_DEBUG=", "NO_COLOR=1")
	cmd.WaitDelay = time.Second
	stdout, stderr := githubCLIBuffer{limit: 4096}, githubCLIBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if runErr != nil || stdout.exceeded {
		return false, nil
	}
	// Official releases print the project URL. A matching executable name or version-looking text alone is insufficient.
	return strings.Contains(stdout.String(), "github.com/cli/cli"), nil
}

// gh owns its credential lookup and authenticated request. Never export its token or retry anonymously.
func githubCLIGet(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "--include", "--method", "GET", "--hostname", "github.com", path)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_DEBUG=", "NO_COLOR=1")
	cmd.WaitDelay = time.Second
	stdout, stderr := githubCLIBuffer{limit: 4 << 20}, githubCLIBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if runErr != nil && ctx.Err() != nil {
		// The context killed gh, possibly after it had written a 200's headers: what is left is not a response to trust.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("authenticated GitHub CLI timed out (retry later): %w", ctx.Err())
		}
		return fmt.Errorf("authenticated GitHub CLI interrupted: %w", ctx.Err())
	}
	if stdout.exceeded {
		return fmt.Errorf("authenticated GitHub CLI response exceeds size limit")
	}
	resp, err := githubCLIResponse(stdout.Bytes())
	if err != nil {
		if runErr != nil {
			if isGitHubCLIUnconfigured(runErr, stderr.String()) {
				return fmt.Errorf("authenticated GitHub CLI unavailable (no anonymous fallback) (%w): %s; %s",
					errGitHubUnconfigured, githubDiagnostic(runErr.Error()), githubDiagnostic(stderr.String()))
			}
			return fmt.Errorf("authenticated GitHub CLI unavailable (no anonymous fallback): %s; %s",
				githubDiagnostic(runErr.Error()), githubDiagnostic(stderr.String()))
		}
		return fmt.Errorf("authenticated GitHub CLI returned an invalid response")
	}
	defer resp.Body.Close()
	log.Printf("deploy verification: authenticated GitHub CLI GET %s: status=%d limit=%s remaining=%s request_id=%s",
		path, resp.StatusCode, githubDiagnostic(resp.Header.Get("X-RateLimit-Limit")),
		githubDiagnostic(resp.Header.Get("X-RateLimit-Remaining")), githubDiagnostic(resp.Header.Get("X-GitHub-Request-Id")))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", errForgeNotFound, githubResponseError(resp))
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return fmt.Errorf("%w: %w", errForgeUnprocessable, githubResponseError(resp))
	}
	if resp.StatusCode != http.StatusOK {
		return githubResponseError(resp)
	}
	if runErr != nil {
		return fmt.Errorf("authenticated GitHub CLI failed: %s", githubDiagnostic(stderr.String()))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// gh --include renders headers followed by an already decoded body, not a chunked HTTP wire stream.
func githubCLIResponse(raw []byte) (*http.Response, error) {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	line, err := reader.ReadLine()
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return nil, fmt.Errorf("invalid status line")
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 100 || status > 599 {
		return nil, fmt.Errorf("invalid status code")
	}
	header, err := reader.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header(header), Body: io.NopCloser(reader.R)}, nil
}

type githubCLIBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *githubCLIBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n > b.limit-b.Len() {
		p = p[:b.limit-b.Len()]
		b.exceeded = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// isGitHubCLIUnconfigured reports whether a gh execution failure without an HTTP response was positively caused by
// missing/unauthenticated GitHub CLI configuration rather than a transient network, DNS, TLS, or proxy error.
func isGitHubCLIUnconfigured(runErr error, stderr string) bool {
	if errors.Is(runErr, exec.ErrNotFound) {
		return true
	}
	lower := strings.ToLower(stderr)
	for _, networkErr := range []string{
		"proxyconnect",
		"proxy authentication",
		"407 proxy",
		"http 407",
		"dial tcp",
		"no such host",
		"tls:",
		"x509:",
		"connection refused",
		"i/o timeout",
	} {
		if strings.Contains(lower, networkErr) {
			return false
		}
	}
	for _, needle := range []string{
		"gh auth login",
		"not logged into",
		"no github authentication",
		"to authenticate, please run",
		"no oauth token",
		"failed to log in",
		"populate the gh_token",
		"gh_token environment variable",
		"github_token environment variable",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
