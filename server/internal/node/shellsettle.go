package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A start types its line into a tab it has just made (typedstart.go). A fixed 300 ms after the tab was made, the
// shell was often still reading its startup files, and what those files do with the terminal decides what becomes of
// the typed line: oh-my-zsh's "Would you like to update? [Y/n]" reads one key, so it ate the line's first character
// and the shell printed "command not found" for the rest; the start then waited the full claudeStartWait for a claude
// that never came (#2570). settleShell waits for the shell to be at rest first: only shells in front of the pane (no
// git, nvm or brew an rc file runs) and the screen the same for a moment. A startup question is not answered: the
// start ends with shell_waiting and the tab is kept for someone to answer it (keepStartTab).

// shellSettleWait is how long a start waits at most for the new tab's shell to settle before typing anyway, as it did
// before. shellQuietFor is how long its screen must stay the same. shellQuestionWait is how long a startup question may
// stay on screen (it may time out by itself) before the start ends with shell_waiting. shellSettlePoll is how often the
// screen is read meanwhile. A node's shellSettle, shellQuiet and shellAsk replace the first three when set (tests).
const (
	shellSettleWait   = 15 * time.Second
	shellQuietFor     = 600 * time.Millisecond
	shellQuestionWait = 20 * time.Second
	shellSettlePoll   = 200 * time.Millisecond
)

// keptQuestionWait is how long a question may stay on screen in a failed start's kept tab that a start runs in again
// (session.start's in_pane: a Retry) before it ends with shell_waiting once more: that question was asked already, so
// the start says so with it and its answers at once rather than wait shellQuestionWait for nobody, and no new tab
// stops on it too. A start start.continue runs after typing an answer waits as in a new tab (answeredOf), and so does
// one in a kept tab typed into since its question was kept (start.answer, start.keys: keptStart.typed).
const keptQuestionWait = time.Second

// keptStartWait is how long the tab of a start that ended waiting on its shell (shell_waiting) or on the agent's own
// screen (agent_start_stalled) stays open for someone to look at or answer; then it is closed unless an agent was
// bound to it meanwhile.
const keptStartWait = 10 * time.Minute

// shellWaits is how long this node's starts wait for a new tab's shell: at most, its screen unchanged, a question.
func (n *Node) shellWaits() (settle, quiet, ask time.Duration) {
	settle, quiet, ask = shellSettleWait, shellQuietFor, shellQuestionWait
	if n.shellSettle > 0 {
		settle = n.shellSettle
	}
	if n.shellQuiet > 0 {
		quiet = n.shellQuiet
	}
	if n.shellAsk > 0 {
		ask = n.shellAsk
	}
	return settle, quiet, ask
}

// shellQuestions are the last lines of a screen whose shell waits for an answer before its prompt: a yes/no question
// ([Y/n], [y/N], (y/n), (yes/no)), a key to press, and oh-my-zsh's update question by its words; and, since #10365,
// the ones a typed line would otherwise be read into (a key each, or the whole line as an answer or a password):
// compinit's insecure directories, ssh's unknown host key, zsh-newuser-install's menu, powerlevel10k's wizard (where
// the tab's environment could not turn it off), RVM's project file trust, and a password or passphrase.
var shellQuestions = []*lazyre.Regexp{
	lazyre.New(`\[[Yy]/[Nn]\]\s*[:?]?\s*$`),
	lazyre.New(`\((?:[Yy]|[Yy]es)/(?:[Nn]|[Nn]o)\)\s*[:?]?\s*$`),
	askPressKey, askUpdate, askCompinit, askSSHHostKey, askNewUser, askP10k, askRVM, askSecret,
}

// The startup questions questionChoices tells apart by their words.
var (
	askCompinit   = lazyre.New(`\[[yY]\] or abort compinit \[[nN]\]\?\s*$`)
	askSSHHostKey = lazyre.New(`(?i)continue connecting \(yes/no(?:/\[fingerprint\])?\)\?\s*$`)
	askNewUser    = lazyre.New(`Type one of the keys in parentheses`)
	askP10k       = lazyre.New(`(?i)^choice \[[a-z0-9]+\]:\s*$`)
	askRVM        = lazyre.New(`c\[ancel\]>\s*$`)
	askSecret     = lazyre.New(`(?i)(?:pass(?:word|phrase)[^:]*|\[sudo\] password for \S+):\s*$`)
	askUpdate     = lazyre.New(`(?i)would you like to (?:update|check for updates)\b`)
	askPressKey   = lazyre.New(`(?i)\bpress (?:enter|return|any key)\b`)
	// askYesNo is a [Y/n] or (y/n) question, its capital the answer Enter gives (yes, no); askYesNoWords one that wants
	// the words.
	askYesNo      = lazyre.New(`[\[(]([Yy])(?:es)?/([Nn])(?:o)?[\])]\s*[:?]?\s*$`)
	askYesNoWords = lazyre.New(`\((?:[Yy]es)/(?:[Nn]o)\)\s*[:?]?\s*$`)
)

// shellChoice is one answer to a startup question that the app may give (start.continue): ID its stable name (what
// the apps send and word), Label the keys as the terminal names them, Keys what is pressed (Persona's key names).
type shellChoice struct {
	ID    string
	Label string
	Keys  []string
}

// shellAsk is how a startup question is answered from the app: its kind, the answers offered (only these are ever
// typed), whether it reads a whole line (LineMode: an answer it echoes waits for Enter) or one key (SingleKey: one it
// echoes has been read, and an Enter after it would go to whatever reads next), whether it reads a secret (Secret:
// nothing is typed into it but Ctrl-C), and the answer Enter alone gives ("" when it does not say). Neither LineMode
// nor SingleKey: the reader is not known (a bare [Y/n]), and the screen after the key tells (enterAfterEcho).
type shellAsk struct {
	Kind      string
	Choices   []shellChoice
	LineMode  bool
	SingleKey bool
	Secret    bool
	Default   string
}

// The answers a startup question is offered.
var (
	choiceEnter  = shellChoice{ID: "enter", Label: "Enter", Keys: []string{"enter"}}
	choiceCancel = shellChoice{ID: "cancel", Label: "Ctrl-C", Keys: []string{"ctrl+c"}}
	choiceNoWord = shellChoice{ID: "no", Label: "no", Keys: []string{"n", "o", "enter"}}
)

// keyChoice is the answer that is one key, k (a letter or a digit), labelled as the question shows it.
func keyChoice(k, label string) shellChoice {
	return shellChoice{ID: strings.ToLower(k), Label: label, Keys: []string{strings.ToLower(k)}}
}

// questionChoices is how line, a startup question (shellQuestion), is answered from the app. A secret prompt is
// offered Ctrl-C alone, and ssh's unknown host key only "no" or Ctrl-C: trusting a key is decided at the computer. A
// question with nothing to choose from (one it does not know) is offered nothing.
func questionChoices(line string) shellAsk {
	lineMode := strings.HasSuffix(strings.TrimSpace(line), ":") // read -p "… [Y/n]: " reads a line (oh-my-bash)
	switch {
	case askSecret.MatchString(line):
		return shellAsk{Kind: "secret", Choices: []shellChoice{choiceCancel}, LineMode: true, Secret: true}
	case askSSHHostKey.MatchString(line):
		return shellAsk{Kind: "ssh_host_key", Choices: []shellChoice{choiceNoWord, choiceCancel}, LineMode: true}
	case askCompinit.MatchString(line): // read -q: one key, y or n, echoed after the question
		return shellAsk{Kind: "compinit", Choices: []shellChoice{keyChoice("y", "y"), keyChoice("n", "n")}, SingleKey: true}
	case askNewUser.MatchString(line): // read -k; q leaves it for next time, 0 writes a ~/.zshrc with only a comment
		return shellAsk{Kind: "zsh_newuser", Choices: []shellChoice{keyChoice("q", "q"), keyChoice("0", "0")}, SingleKey: true}
	case askP10k.MatchString(line): // read -k; its other answers are about how glyphs look: those are for the computer
		return shellAsk{Kind: "p10k_wizard", Choices: []shellChoice{keyChoice("q", "q")}, SingleKey: true}
	case askRVM.MatchString(line): // read: a line
		return shellAsk{Kind: "rvm_trust", Choices: []shellChoice{keyChoice("n", "n"), keyChoice("c", "c")}, LineMode: true}
	case askYesNoWords.MatchString(line):
		return shellAsk{Kind: "yes_no", Choices: []shellChoice{{ID: "yes", Label: "yes", Keys: []string{"y", "e", "s", "enter"}}, choiceNoWord},
			LineMode: true}
	}
	kind := "yes_no"
	if askUpdate.MatchString(line) {
		kind = "update"
	}
	if m := askYesNo.FindStringSubmatch(line); m != nil {
		// oh-my-zsh's update question reads one key (read -k 1); oh-my-bash's ends in a colon and reads a line
		ask := shellAsk{Kind: kind, Choices: []shellChoice{keyChoice(m[1], m[1]), keyChoice(m[2], m[2]), choiceEnter}, LineMode: lineMode,
			SingleKey: kind == "update" && !lineMode}
		switch {
		case m[1] == "Y" && m[2] == "n":
			ask.Default = "y"
		case m[1] == "y" && m[2] == "N":
			ask.Default = "n"
		}
		return ask
	}
	if askPressKey.MatchString(line) {
		return shellAsk{Kind: "press_key", Choices: []shellChoice{choiceEnter}}
	}
	return shellAsk{Kind: kind}
}

// choice is the offered answer named id.
func (a shellAsk) choice(id string) (shellChoice, bool) {
	for _, c := range a.Choices {
		if c.ID == id {
			return c, true
		}
	}
	return shellChoice{}, false
}

// data is how a refusal names the question's answers (shellWaiting): its kind, the answers' ids joined by commas (a
// start's start_error_args are text), and line_mode, secret and default when they say something.
func (a shellAsk) data(m map[string]any) {
	ids := make([]string, len(a.Choices))
	for i, c := range a.Choices {
		ids[i] = c.ID
	}
	m[proto.ShellAskKind] = a.Kind
	m[proto.ShellAskChoices] = strings.Join(ids, ",")
	if a.LineMode {
		m[proto.ShellAskLineMode] = "true"
	}
	if a.Secret {
		m[proto.ShellAskSecret] = "true"
	}
	if a.Default != "" {
		m[proto.ShellAskDefault] = a.Default
	}
}

// shellQuestion is the last line of screen when it asks something (shellQuestions), else "".
func shellQuestion(screen string) string {
	last := lastLine(screen)
	for _, q := range shellQuestions {
		if q.MatchString(last) {
			return last
		}
	}
	return ""
}

// lastLine is screen's last line that is not blank, trimmed.
func lastLine(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// settleShell waits until the shell of the tab a start made, pane, is at rest before the start types into it: only
// shells in front of the pane (or a host that cannot say) and its screen unchanged for shellQuietFor. After
// shellSettleWait it returns nil anyway, as the start went on before. A screen that cannot be read returns at once,
// but one the host has not drawn yet is waited for: a new Orca tab has none for its first ~100 ms, and typing then
// reaches the tty before the shell does (#5450). A startup question on the last line is waited out for
// shellQuestionWait (keptQuestionWait in a failed start's kept tab); one still there ends the start: shellWaiting.
func (n *Node) settleShell(ctx context.Context, pane string) error {
	if n.shellSettled {
		return nil
	}
	settle, quiet, ask := n.shellWaits()
	if c := keptClaimOf(ctx); c != nil && c.pane == pane && !answeredOf(ctx) && !c.k.typed && ask > keptQuestionWait {
		ask = keptQuestionWait
	}
	began := time.Now()
	last, since := "", began
	var asked time.Time // since when a question has been on screen
	for {
		screen, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil && !errors.Is(err, orcart.ErrNoScreen) {
			return nil
		}
		now := time.Now()
		if screen != last {
			last, since = screen, now
		}
		if q := shellQuestion(screen); q != "" {
			if asked.IsZero() {
				asked = now
			}
			if now.Sub(asked) >= ask {
				full, rerr := n.readPane(ctx, pane, "recent-unwrapped", 30)
				if rerr != nil || strings.TrimSpace(full) == "" {
					full = screen
				}
				return shellWaiting(q, full, ask)
			}
		} else {
			asked = time.Time{}
			if strings.TrimSpace(screen) != "" && now.Sub(since) >= quiet && n.shellBackOrUnknown(ctx, pane) {
				return nil
			}
			if now.Sub(began) >= settle {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(shellSettlePoll):
		}
	}
}

// shellWaiting is the refusal of a start whose new tab's shell stayed on question for wait: nothing was typed. The
// tab's last lines go with it (screen) for whoever looks at it, and the answers the app may give (questionChoices,
// start.continue); the start adds where the tab is kept (keepStartTab).
func shellWaiting(question, screen string, wait time.Duration) error {
	data := map[string]any{"line": question, "screen": lastLines(screen, 12)}
	questionChoices(question).data(data)
	return refusal(proto.CodeShellWaiting, "the new tab's shell is waiting for an answer: "+question+
		" (on screen for "+wait.String()+"; nothing was typed, and the tab is kept open)\n"+lastLines(screen, 12), data)
}

// keptStart is the tab of a failed start left open (keepStartTab).
type keptStart struct {
	id      string // the session id the start would have had ("" for an agent whose id it learns later)
	launch  host.Launcher
	created *host.Created
	ws      string
	until   time.Time
	taken   bool // a start holds it (claimKeptTab): only that start settles or releases it
	// typed: something was typed into it since its start failed: an answer to its question (start.answer) or the
	// assistant's keys (start.keys). Its question then stays the last line written until the shell prints more, so the
	// start that claims it next waits that out as in a new tab (answeredOf) rather than end at once (keptQuestionWait).
	typed bool
}

// keepStartTab keeps the tab of a start that failed waiting on its shell or on the agent's screen, created, open for
// keptStartWait instead of closing it, and returns until when: someone may answer the question or look at what
// stopped. Then it is closed (closeNewTab), unless an agent was bound to its pane meanwhile.
func (n *Node) keepStartTab(launch host.Launcher, created *host.Created, ws, id string) time.Time {
	pane := created.Pane.String()
	until := time.Now().Add(keptStartWait)
	n.mu.Lock()
	if n.keptStarts == nil {
		n.keptStarts = map[string]keptStart{}
	}
	n.keptStarts[pane] = keptStart{id: id, launch: launch, created: created, ws: ws, until: until}
	n.mu.Unlock()
	time.AfterFunc(keptStartWait, func() { n.expireKeptStart(pane, until) })
	return until
}

// expireKeptStart closes the kept tab on pane kept until until, once that has passed, when no agent is bound to it.
func (n *Node) expireKeptStart(pane string, until time.Time) {
	n.mu.Lock()
	k, ok := n.keptStarts[pane]
	if !ok || k.taken || !k.until.Equal(until) { // a start holding it sets its timer again if it lets it go
		n.mu.Unlock()
		return
	}
	delete(n.keptStarts, pane)
	_, bound := n.panes[pane]
	for _, a := range n.agents { // an agent that came up there after all (a stalled claude that went on)
		bound = bound || a.PaneID == pane
	}
	n.mu.Unlock()
	if bound {
		return
	}
	n.closeNewTab(context.Background(), k.launch, k.created, k.ws)
}

// withKeptTab is err, a start's refusal, saying where its tab is kept and until when (pane, kept_until), for whoever
// looks at or answers it. Any other error is returned as it is.
func withKeptTab(err error, pane string, until time.Time) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return err
	}
	data := map[string]any{}
	if len(re.Data) > 0 {
		_ = json.Unmarshal(re.Data, &data)
	}
	data["pane"] = pane
	data["kept_until"] = until.UTC().Format(time.RFC3339)
	b, _ := json.Marshal(data)
	return &rpc.Error{Code: re.Code, Message: re.Message, Data: b}
}
