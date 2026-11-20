package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Unlike a fake pane, this builds a real receiver and runs the production helper
// through the OS process boundary. On WSL it also executes a Windows PE receiver:
// CreateProcessW/UTF-16 decoding, not a Linux imitation of Windows quoting.
func TestCodexStartNativeArgvRoundTrip(t *testing.T) {
	fixture, err := os.ReadFile("testdata/codexargv/first-message.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(fixture)); got != "ea995681eff64521b454b9fac9dbb74f9141875739e4d89e3801345843cfb1e0" {
		t.Fatal("the native-acceptance input fixture changed")
	}
	prompt := strings.Repeat("Synthetic context: 中文 🧪\n", 100) + string(fixture)
	targets := []string{runtime.GOOS}
	if release, _ := os.ReadFile("/proc/sys/kernel/osrelease"); runtime.GOOS == "linux" && strings.Contains(strings.ToLower(string(release)), "microsoft") {
		targets = append(targets, "windows")
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "argv receiver")
			if target == "windows" {
				bin += ".exe"
			}
			build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./testdata/codexargv")
			build.Env = append(withoutEnv(os.Environ(), []string{"GOOS", "GOARCH", "CGO_ENABLED"}), "GOOS="+target, "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build native boundary fixture: %v\n%s", err, out)
			}
			nativeDir := dir
			if target == "windows" && runtime.GOOS != "windows" {
				out, err := exec.CommandContext(ctx, "wslpath", "-w", dir).Output()
				if err != nil {
					t.Fatalf("Windows path for WSL native test: %v", err)
				}
				nativeDir = strings.TrimSpace(string(out))
			}
			type inputCase struct {
				name, text                    string
				refused, npmShim, unsafeBatch bool
			}
			cases := []inputCase{{name: "multiline-CJK-nonBMP-quotes", text: prompt}}
			if target == "windows" {
				cases = append(cases,
					inputCase{name: "ASCII-under-limit", text: strings.Repeat("a", 17000)},
					inputCase{name: "nonBMP-over-UTF16-limit", text: strings.Repeat("🧪", 17000), refused: true},
					inputCase{name: "quoting-over-limit", text: strings.Repeat(`\" `, 8500), refused: true},
					inputCase{name: "npm-shim-resolves-native-exe", text: prompt, npmShim: true},
					inputCase{name: "unknown-batch-refused", text: prompt, unsafeBatch: true},
				)
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					data, _ := json.Marshal(map[string]any{"dir": nativeDir, "prompt": tc.text, "npmShim": tc.npmShim, "unsafeBatch": tc.unsafeBatch})
					cmd := exec.CommandContext(ctx, bin)
					cmd.Stdin = bytes.NewReader(data)
					var stderr bytes.Buffer
					cmd.Stderr = &stderr
					out, err := cmd.Output()
					if err != nil {
						t.Fatalf("real %s helper process: %v: %s", target, err, stderr.String())
					}
					var got struct {
						Args          []string `json:"args"`
						SourceRemoved bool     `json:"source_removed"`
						LimitRefused  bool     `json:"limit_refused"`
						BatchRefused  bool     `json:"batch_refused"`
					}
					if err := json.Unmarshal(out, &got); err != nil {
						t.Fatalf("native receiver did not return JSON: %v", err)
					}
					if !got.SourceRemoved || got.LimitRefused != tc.refused || got.BatchRefused != tc.unsafeBatch {
						t.Fatalf("source removed=%v, limit refused=%v; expected refusal=%v", got.SourceRemoved, got.LimitRefused, tc.refused)
					}
					if !tc.refused && !tc.unsafeBatch && !slices.Equal(got.Args, []string{"--", tc.text}) {
						t.Fatalf("native %s argument changed: expected %d UTF-8 bytes, got argument lengths %v", target, len(tc.text), argvLengths(got.Args))
					}
					if (tc.refused || tc.unsafeBatch) && len(got.Args) != 0 {
						t.Fatal("oversized input reached the receiver")
					}
					t.Logf("native %s: UTF-8 bytes=%d, Unicode chars=%d, limit_refused=%v", target, len(tc.text), len([]rune(tc.text)), tc.refused)
				})
			}
		})
	}
}

func argvLengths(args []string) []int {
	var lengths []int
	for _, arg := range args {
		lengths = append(lengths, len(arg))
	}
	return lengths
}
