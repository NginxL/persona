// Package tmux drives a pane of a tmux server through the tmux command: what its screen shows and the keys pressed in
// it. A pane id is tmux's own ("%11"), unique within one server; the server is the default one unless Socket names
// another (a server started with -L or -S), even for a node started inside a pane of another server: tmux runs without
// the node's own TMUX. Nothing here reads tmux's layout or streams its output: a pane Persona did not start is reached
// by the id Claude Code itself wrote down (sessions/<pid>.json "tmux").
package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Client runs tmux for one server.
type Client struct {
	Bin    string // the tmux to run, "" for the one on PATH
	Socket string // the server's socket, "" for the default server
}

// ErrNoServer marks a tmux that is not running (no server for the socket): the pane is gone.
var ErrNoServer = fmt.Errorf("tmux: no server")

func (c Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "tmux"
}

func (c Client) run(ctx context.Context, stdin string, args ...string) (string, error) {
	if c.Socket != "" {
		args = append([]string{"-S", c.Socket}, args...)
	}
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	if c.Socket == "" {
		// tmux without -S runs against the server $TMUX names: a node started inside a pane of another server would
		// read and type into that server's pane of the same id.
		cmd.Env = envWithout(os.Environ(), "TMUX", "TMUX_PANE")
	}
	// A tmux wrapper whose child keeps the output pipe open would otherwise hold Run past ctx (or past its own exit).
	cmd.WaitDelay = time.Second
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // tmux itself exited 0; only a child it left behind held the pipe
	}
	if err != nil {
		if strings.Contains(errb.String(), "no server running") || strings.Contains(errb.String(), "error connecting to") {
			return out.String(), fmt.Errorf("%w: %s", ErrNoServer, strings.TrimSpace(errb.String()))
		}
		return out.String(), fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// envWithout is env without the variables named.
func envWithout(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !slices.Contains(names, name) {
			out = append(out, kv)
		}
	}
	return out
}

// Version is the tmux client's version ("tmux 3.6a"), "" when tmux is not installed or not running.
func (c Client) Version(ctx context.Context) string {
	out, err := c.run(ctx, "", "-V")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// PaneInfo describes a tmux pane's current root process, tty, and modal/dead state.
type PaneInfo struct {
	PID    int
	TTY    string
	Dead   bool
	InMode bool
}

// PaneInfo reads the pane's pid, tty, dead flag, and mode flag in a single display-message call.
func (c Client) PaneInfo(ctx context.Context, pane string) (PaneInfo, error) {
	out, err := c.run(ctx, "", "display-message", "-p", "-t", pane, "#{pane_pid}\t#{pane_tty}\t#{pane_dead}\t#{pane_in_mode}")
	if err != nil {
		return PaneInfo{}, err
	}
	parts := strings.Split(strings.TrimRight(out, "\r\n"), "\t")
	if len(parts) != 4 {
		return PaneInfo{}, fmt.Errorf("tmux: unexpected pane info %q", strings.TrimSpace(out))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || pid <= 0 {
		return PaneInfo{}, fmt.Errorf("tmux: bad pane pid %q", parts[0])
	}
	return PaneInfo{
		PID:    pid,
		TTY:    strings.TrimSpace(parts[1]),
		Dead:   strings.TrimSpace(parts[2]) != "0",
		InMode: strings.TrimSpace(parts[3]) != "0",
	}, nil
}

// Capture is what the pane shows, read as host.ReadOpts says: source "visible" (or "") is the screen alone; "recent"
// reaches lines rows back into the scrollback, and "recent-unwrapped" also joins the lines tmux wrapped. lines > 0
// keeps at most the last lines rows (tmux itself returns the history it is asked for plus the whole screen); with
// lines <= 0 every source is the screen. ansi keeps the colors tmux re-emits, and rows then end in \r\n.
func (c Client) Capture(ctx context.Context, pane, source string, lines int, ansi bool) (string, error) {
	args := []string{"capture-pane", "-p", "-t", pane}
	if ansi {
		args = append(args, "-e")
	}
	switch source {
	case "", "visible":
	case "recent", "recent-unwrapped":
		if lines > 0 {
			args = append(args, "-S", fmt.Sprintf("-%d", lines))
		}
		if source == "recent-unwrapped" {
			args = append(args, "-J")
		}
	default:
		return "", fmt.Errorf("tmux: no screen source %q", source)
	}
	out, err := c.run(ctx, "", args...)
	if err != nil {
		return "", err
	}
	return lastRows(out, lines, ansi), nil
}

// lastRows cuts a capture to its last n rows (all of them with n <= 0), past the blank rows that end it, and ends each
// row in \r\n for ansi or \n otherwise. A row is blank by what it shows: tmux -e keeps a colored row of spaces that
// the plain read drops, and the two reads of one pane must end at the same row so that row i of one is row i of the
// other (session.screen.before pages the plain read and takes the colors from the same rows of the ANSI one).
func lastRows(out string, n int, ansi bool) string {
	rows := strings.Split(out, "\n")
	for len(rows) > 0 && blankRow(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return ""
	}
	if n > 0 && len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	end := "\n"
	if ansi {
		end = "\r\n"
	}
	return strings.Join(rows, end) + end
}

// blankRow reports whether a captured row shows nothing: only spaces once the CSI (colors) and OSC (hyperlinks)
// sequences tmux -e emits are skipped.
func blankRow(row string) bool {
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == ' ' || row[i] == '\r':
		case row[i] == 0x1b && i+1 < len(row) && row[i+1] == '[':
			i += 2
			for i < len(row) && (row[i] < 0x40 || row[i] > 0x7e) {
				i++
			}
		case row[i] == 0x1b && i+1 < len(row) && row[i+1] == ']':
			i += 2
			for i < len(row) && row[i] != 0x07 && !(row[i] == 0x1b && i+1 < len(row) && row[i+1] == '\\') {
				i++
			}
			if i < len(row) && row[i] == 0x1b {
				i++ // the ST's backslash
			}
		default:
			return false
		}
	}
	return true
}

// Persona's key names (host.Host.SendKeys: "enter", "shift+tab", "ctrl+u", "alt+left", "f5"...) become tmux's key
// names, so that tmux sends the bytes the pane's terminal would. tmux's send-keys types a string it does not know as a
// key as literal characters, so every name is translated here and an unknown one is refused, never passed through. The
// vocabulary is the Orca host's (node/hostorca_keys.go): the same name presses the same key on either host.

// tmuxNamedKeys take any of shift, alt and ctrl as tmux's S-, M- and C- prefixes ("shift+right" is "S-Right").
var tmuxNamedKeys = map[string]string{
	"up": "Up", "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End",
	"pageup": "PageUp", "pagedown": "PageDown", "delete": "DC", "insert": "IC",
	"f1": "F1", "f2": "F2", "f3": "F3", "f4": "F4", "f5": "F5", "f6": "F6",
	"f7": "F7", "f8": "F8", "f9": "F9", "f10": "F10", "f11": "F11", "f12": "F12",
}

// tmuxPlainKeys are keys that take only alt as a modifier ("alt+enter" is "M-Enter"), by their base name.
var tmuxPlainKeys = map[string]string{
	"enter": "Enter", "return": "Enter", "esc": "Escape", "escape": "Escape", "tab": "Tab",
	"backspace": "BSpace", "space": "Space",
}

// tmuxFullKeys are keys whose tmux name does not follow from their parts, by their full name. shift+enter is ESC CR,
// what Orca sends; tmux types C-BSpace as text, so ctrl+backspace is C-h (BS).
var tmuxFullKeys = map[string]string{
	"shift+tab": "BTab", "backtab": "BTab", "shift+enter": "M-Enter", "ctrl+backspace": "C-h",
	"ctrl+space": "C-Space",
}

// tmuxHexKeys are keys tmux has no name for, as the bytes send-keys -H sends. ctrl+enter has no legacy bytes: it is
// the CSI u form ("\x1b[13;5u", as on Orca), which Claude Code reads as its send-now (#2467).
var tmuxHexKeys = map[string][]string{
	"ctrl+enter": {"1b", "5b", "31", "33", "3b", "35", "75"},
}

// tmuxKey is a Persona key name as send-keys arguments: one tmux key name, or with raw the bytes for send-keys -H.
// false for a name Persona does not know.
func tmuxKey(k string) (args []string, raw bool, ok bool) {
	name := strings.ToLower(strings.TrimSpace(k))
	if b, ok := tmuxHexKeys[name]; ok {
		return b, true, true
	}
	if t, ok := tmuxFullKeys[name]; ok {
		return []string{t}, false, true
	}
	if t, ok := tmuxPlainKeys[name]; ok {
		return []string{t}, false, true
	}
	if r := []rune(k); len(r) == 1 && r[0] >= ' ' && r[0] != 0x7f {
		return []string{tmuxArg(k)}, false, true // a character as itself (case kept)
	}
	parts := strings.Split(name, "+")
	base := parts[len(parts)-1]
	shift, alt, ctrl := false, false, false
	for _, m := range parts[:len(parts)-1] {
		switch m {
		case "shift":
			shift = true
		case "alt", "option", "meta":
			alt = true
		case "ctrl", "control":
			ctrl = true
		default:
			return nil, false, false
		}
	}
	if base == "" {
		return nil, false, false
	}
	prefix := ""
	if ctrl {
		prefix += "C-"
	}
	if alt {
		prefix += "M-"
	}
	if t, ok := tmuxNamedKeys[base]; ok {
		if shift {
			prefix += "S-"
		}
		return []string{prefix + t}, false, true
	}
	if t, ok := tmuxPlainKeys[base]; ok && alt && !shift && !ctrl {
		return []string{"M-" + t}, false, true
	}
	r := []rune(base)
	if len(r) != 1 || r[0] < ' ' || r[0] == 0x7f {
		return nil, false, false
	}
	c := base
	if shift {
		c = strings.ToUpper(c)
	}
	if ctrl {
		switch {
		case r[0] >= 'a' && r[0] <= 'z', r[0] >= '[' && r[0] <= '_', r[0] == '@':
			c = strings.ToLower(c)
		case r[0] == '2':
			c = "@"
		default:
			return nil, false, false // no control byte, as on Orca
		}
	}
	return []string{tmuxArg(prefix + c)}, false, true
}

// tmuxArg keeps a key that ends in ';' a key: tmux splits its argv into commands at an argument ending in ';', and
// "\;" is the escaped form.
func tmuxArg(key string) string {
	if strings.HasSuffix(key, ";") {
		return strings.TrimSuffix(key, ";") + `\;`
	}
	return key
}

// Keys presses keys in the pane; every name is checked first, and an unknown one is refused before any key is
// pressed. Consecutive named keys go in one send-keys, a key tmux has no name for (ctrl+enter) in its own send-keys -H.
func (c Client) Keys(ctx context.Context, pane string, keys ...string) error {
	var calls [][]string
	lastRaw := false
	for _, k := range keys {
		args, raw, ok := tmuxKey(k)
		if !ok {
			return fmt.Errorf("tmux: unknown key %q", k)
		}
		if len(calls) == 0 || raw || lastRaw {
			call := []string{"send-keys", "-t", pane}
			if raw {
				call = []string{"send-keys", "-H", "-t", pane}
			}
			calls = append(calls, call)
		}
		calls[len(calls)-1] = append(calls[len(calls)-1], args...)
		lastRaw = raw
	}
	for _, args := range calls {
		if _, err := c.run(ctx, "", args...); err != nil {
			return err
		}
	}
	return nil
}

// bufferPrefix names this process's tmux buffers: another Persona on the same tmux server (a dev node next to the
// installed one) has a prefix of its own, so neither loads over a buffer the other has yet to paste.
var bufferPrefix = fmt.Sprintf("persona-%d-%s", os.Getpid(), randomHex(4))

var bufferSeq atomic.Uint64

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail
	return hex.EncodeToString(b)
}

// dropBufferTimeout bounds the cleanup of a buffer whose load or paste failed.
const dropBufferTimeout = 2 * time.Second

// SendText types text into the pane without submitting it, through a tmux buffer so that a line break is text and not
// Enter (bracketed paste when the program takes it). Empty text types nothing. The buffer never outlives the call: the
// paste deletes it, and a load or paste that fails (the pane closed, ctx ended) is followed by an explicit delete. A
// failed load needs it too: the server may have taken the text before the client gave up.
func (c Client) SendText(ctx context.Context, pane, text string) error {
	if text == "" {
		return nil
	}
	buf := fmt.Sprintf("%s-%d", bufferPrefix, bufferSeq.Add(1))
	if _, err := c.run(ctx, text, "load-buffer", "-b", buf, "-"); err != nil {
		c.dropBuffer(ctx, buf)
		return err
	}
	if _, err := c.run(ctx, "", "paste-buffer", "-p", "-d", "-b", buf, "-t", pane); err != nil {
		c.dropBuffer(ctx, buf)
		return err
	}
	return nil
}

// dropBuffer deletes a buffer best-effort, on a short context of its own so that it runs after ctx has ended: the text
// it holds would otherwise stay in the user's tmux server (list-buffers, choose-buffer).
func (c Client) dropBuffer(ctx context.Context, buf string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dropBufferTimeout)
	defer cancel()
	_, _ = c.run(ctx, "", "delete-buffer", "-b", buf) // the load or paste error is the one that matters
}

// Prompt types text and presses Enter.
func (c Client) Prompt(ctx context.Context, pane, text string) error {
	if err := c.SendText(ctx, pane, text); err != nil {
		return err
	}
	return c.Keys(ctx, pane, "enter")
}
