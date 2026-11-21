package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/proto"
)

// A new chat in the Codex App, started from the phone (docs/integrations/codex.md "New chats in the Codex App", #5726). The
// App takes no chat with a first message from outside: codex://new only fills its composer, and its codex_app MCP
// channel takes only the App's own processes. It does list any chat its Codex home has from an interactive source
// (vscode among them: what an app-server's threads are), opens one at codex://threads/<id>, and runs a turn in one it
// has loaded when a follower asks (thread-follower-start-turn, codex_desktop.go). So a start with codexAppLauncher:
//
//  1. starts the chat with the App's own codex (its bundled CLI, as `codex app-server` over stdio, client
//     codexAppClient): thread/start in the folder, then thread/name/set with the title, which writes the chat (Codex
//     writes one at its first turn otherwise); no model request; the app-server exits;
//  2. sends the first message as to any chat of the App (sendCodexDesktop): the App opens it (in front), and runs the
//     turn. Until then the App does not list it: a chat with no message is not in its sidebar.
//
// The chat is in the App's sidebar in the project its folder is in, or among the chats in no project, and the App runs
// it with its own settings. Persona lists it as one of the App's chats by its originator (codexOrigin.persona).

// codexAppLauncher is the launcher a computer whose Codex App runs offers (launchers.list), codexAppSource its Source;
// codexAppClient is the client name the chats are started with, which Codex writes as their originator.
const (
	codexAppLauncher = "codex-app"
	codexAppSource   = "codex_app"
	codexAppClient   = "persona_codex_app"
)

// codexAppModelLocked says why New Agent's model does not apply to the launcher; codexAppCheckMsg why Settings ›
// Models' Test has nothing to run for it.
const (
	codexAppModelLocked = "Codex App 用它自己选的模型运行这个对话。"
	codexAppCheckMsg    = "这个启动器在 Codex App 里新开对话，用的是 App 自己的设置，没有可以单独测试的命令。"
)

var (
	// codexAppSeedWait bounds the App's codex starting the chat.
	codexAppSeedWait = 30 * time.Second

	// codexAppCLI is the Codex CLI the App bundles (the same version as the App's own app-server), else one on PATH or
	// where Codex installs (a variable: tests use their own).
	codexAppCLI = func() (string, error) {
		if p := bundledCodexCLI(); p != "" {
			return p, nil
		}
		return agentExecutable("codex")
	}

	// bundledCodexCLI is the Codex CLI the ChatGPT app bundles on a Mac, "" when there is none (a variable: tests use
	// their own). Its base name is codex, so herdr, codex-start and codexEffortInherited take it for plain codex.
	bundledCodexCLI = func() string {
		if runtime.GOOS != "darwin" {
			return ""
		}
		p := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex"
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode().Perm()&0111 != 0 {
			return p
		}
		return ""
	}

	// seedCodexAppChat starts a chat named title in dir with codex in home, and returns its thread id (a variable:
	// tests start their own).
	seedCodexAppChat = startCodexAppChat
)

// isCodexAppLauncher: name is the Codex App's launcher, not a login shell's of that name.
func isCodexAppLauncher(name string) bool {
	return name == codexAppLauncher && !shellLauncherNames()[name]
}

// withCodexAppLauncher is list with the Codex App's launcher after it, on a computer whose App runs for this node's
// Codex home (its bus socket is there) and where a Codex CLI to start chats with is found; added reports whether it was.
// A launcher of the login shell's with its name is left as it is: that one runs.
func (n *Node) withCodexAppLauncher(list []proto.Launcher) (out []proto.Launcher, added bool) {
	if !codexDesktopAvailable(n.codexHome()) || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == codexAppLauncher }) {
		return list, false
	}
	if _, err := codexAppCLI(); err != nil {
		return list, false
	}
	return append(slices.Clone(list), proto.Launcher{Name: codexAppLauncher, Source: codexAppSource, Agent: "codex"}), true
}

// startInCodexApp starts a new chat in the Codex App for a start with its launcher; handled false for any other start
// (and a resume: a chat of the App is continued by a message, codex_desktop.go).
func (n *Node) startInCodexApp(ctx context.Context, p runtimeStartParams, nameHint string) (s *proto.Session, err error, handled bool) {
	if !isCodexAppLauncher(p.Launcher) || p.Resume != "" {
		return nil, nil, false
	}
	s, err = n.newCodexAppChat(ctx, p, nameHint)
	return s, err, true
}

func (n *Node) newCodexAppChat(ctx context.Context, p runtimeStartParams, nameHint string) (*proto.Session, error) {
	home := n.codexHome()
	if !codexDesktopAvailable(home) {
		return nil, refusal("codex_read_only", "电脑上的 Codex App 没在运行，不能在 App 里新开对话，这次没有发送。", nil)
	}
	if p.PermissionMode == "plan" {
		// The App runs the chat in its own mode, and a plan waiting for approval there could not be answered here.
		return nil, readonlyStartUnsupported()
	}
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		// The App lists a chat once it has a message: one with none would be nowhere to be seen.
		return nil, errors.New("在 Codex App 里新开对话需要第一条消息，这次没有发送。")
	}
	model, effort := strings.TrimSpace(p.Model), strings.TrimSpace(p.Effort)
	if model != "" && !proto.ValidModelID(model) {
		return nil, fmt.Errorf("invalid model %q", model)
	}
	cli, err := codexAppCLI()
	if err != nil {
		return nil, fmt.Errorf("没找到 Codex（codex 命令），不能在 Codex App 里新开对话：%v", err)
	}
	title := desktopTitle(cmp.Or(prompt, nameHint), "")
	dir, err := n.codexAppStartDir(p.Cwd, cmp.Or(prompt, nameHint))
	if err != nil {
		return nil, err
	}
	thread, err := seedCodexAppChat(ctx, cli, home, dir, title)
	if err != nil {
		return nil, fmt.Errorf("Codex 没能新开对话：%v", err)
	}
	log.Printf("new Codex App chat %s in %s", thread, dir)
	publicID := n.codexPublicID(home, thread)
	src := codexSource{home: home, thread: thread, path: rolloutPath(home, thread)}
	if model != "" || effort != "" {
		if err := n.setCodexDesktopSettings(ctx, publicID, src, model, effort); err != nil {
			log.Printf("new Codex App chat %s: initial settings (%q, %q) did not take: %v", thread, model, effort, err)
			return nil, err
		}
	}
	if err := n.sendCodexDesktop(ctx, src, p.Prompt, nil); err != nil {
		log.Printf("new Codex App chat %s: the first message did not go in: %v", thread, err)
		return nil, err
	}
	n.notifyCodex()
	return &proto.Session{SessionID: publicID, Cwd: dir, Title: title, Agent: "codex",
		Launcher: codexAppLauncher, Model: model, Effort: effort,
		Status: proto.StatusWorking, StartedByPersona: true, UpdatedAt: time.Now().UTC()}, nil
}

// codexAppStartDir is where a new chat of the App runs: dir as for the desktop app's (desktopStartDir: there, or a new
// folder in one that is); with none, a folder of its own where the App keeps a chat in no project,
// ~/Documents/Codex/<date>/<slug of its first message's first line>, made now.
func (n *Node) codexAppStartDir(dir, first string) (string, error) {
	if strings.TrimSpace(dir) != "" {
		return n.desktopStartDir(dir)
	}
	day := filepath.Join(n.home, "Documents", "Codex", time.Now().Format("2006-01-02"))
	line, _, _ := strings.Cut(strings.TrimSpace(first), "\n")
	base := codexAppSlug(line)
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		p := filepath.Join(day, name)
		if err := os.MkdirAll(day, 0o755); err != nil {
			return "", fmt.Errorf("建不了文件夹 %s：%v", day, err)
		}
		switch err := os.Mkdir(p, 0o755); {
		case err == nil:
			return p, nil
		case errors.Is(err, os.ErrExist) && i < 100:
			continue // another chat's: one of its own
		default:
			return "", fmt.Errorf("建不了文件夹 %s：%v", p, err)
		}
	}
}

// codexAppSlug is a folder name from a line of text, as the App names one: its ASCII words, lower case, joined by "-",
// whole words up to 40 characters; "chat" when it has none.
func codexAppSlug(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	slug := ""
	for _, w := range words {
		next := w
		if slug != "" {
			next = slug + "-" + w
		}
		if len(next) > 40 {
			break
		}
		slug = next
	}
	return cmp.Or(slug, "chat")
}

// startCodexAppChat runs `codex app-server` (stdio, JSON lines) in dir for home, starts a thread there and names it
// title, which writes it, then ends the app-server. It never starts a turn.
func startCodexAppChat(ctx context.Context, cli, home, dir, title string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, codexAppSeedWait)
	defer cancel()
	s, err := startCodexStdio(ctx, cli, home, dir)
	if err != nil {
		return "", err
	}
	defer s.close()
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := s.call(ctx, "thread/start", map[string]any{"cwd": dir}, &started); err != nil {
		return "", err
	}
	if started.Thread.ID == "" {
		return "", errors.New("thread/start: no thread")
	}
	if err := s.call(ctx, "thread/name/set", map[string]any{"threadId": started.Thread.ID, "name": title}, nil); err != nil {
		return "", err
	}
	return started.Thread.ID, nil
}

// codexStdio is a `codex app-server` over stdio (JSON lines) for a short exchange, as the App's client
// (codexAppClient): started in dir for home and initialized; close ends it. Its calls end when ctx does (the
// app-server is killed).
type codexStdio struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines *bufio.Reader
	next  int
}

func startCodexStdio(ctx context.Context, cli, home, dir string) (*codexStdio, error) {
	cmd := exec.CommandContext(ctx, cli, "app-server")
	cmd.Dir = dir
	cmd.Env = append(desktopSeedEnv(os.Environ()), "CODEX_HOME="+home)
	if bin := codexAppNodeBin(cli); bin != "" {
		path := withDirsAfter(envValue(cmd.Env, "PATH"), []string{bin})
		cmd.Env = append(withoutEnv(cmd.Env, []string{"PATH"}), "PATH="+path)
	}
	killGroupOnCancel(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stopGroup := cmd.Cancel
	cmd.Cancel = sync.OnceValue(func() error {
		// A CLI launcher can fork a child that keeps both pipes open. Close our ends even if the group is gone.
		_ = in.Close()
		_ = out.Close()
		if err := stopGroup(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	})
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &codexStdio{cmd: cmd, in: in, lines: bufio.NewReaderSize(out, 1<<20)}
	if err := s.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": codexAppClient, "title": "Persona", "version": "1"}}, nil); err != nil {
		s.close()
		return nil, err
	}
	b, _ := json.Marshal(map[string]any{"method": "initialized"})
	if _, err := in.Write(append(b, '\n')); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// codexAppNodeBin is the interpreter directory for the official npm launcher when the service has no node on PATH.
// npm links bin/codex to the script, which uses /usr/bin/env node rather than an absolute interpreter path.
func codexAppNodeBin(cli string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if _, err := exec.LookPath("node"); err == nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(cli)
	if err != nil {
		return ""
	}
	prefix, ok := strings.CutSuffix(real, "/lib/node_modules/@openai/codex/bin/codex.js")
	if !ok || !filepath.IsAbs(prefix) {
		return ""
	}
	bin := filepath.Join(prefix, "bin")
	if _, err := exec.LookPath(filepath.Join(bin, "node")); err != nil {
		return ""
	}
	return bin
}

func (s *codexStdio) close() {
	_ = s.cmd.Cancel()
	_ = s.cmd.Wait()
}

// call sends one request and reads up to its answer, into into (nil: none wanted); notifications and others' answers
// are skipped.
func (s *codexStdio) call(_ context.Context, method string, params, into any) error {
	s.next++
	id := s.next
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		return err
	}
	for {
		line, err := s.lines.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("%s: %v", method, err)
		}
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &m) != nil || m.ID == nil || *m.ID != id {
			continue // a notification, or another's
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		if into == nil {
			return nil
		}
		return json.Unmarshal(m.Result, into)
	}
}
