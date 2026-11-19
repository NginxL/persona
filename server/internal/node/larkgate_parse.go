package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"github.com/nwverse/persona/server/internal/textfmt"
	"mvdan.cc/sh/v3/syntax"
)

var (
	larkFilters = map[string]bool{"jq": true, "head": true, "tail": true, "grep": true, "egrep": true, "wc": true, "sort": true, "uniq": true, "cut": true, "tr": true, "column": true,
		// and commands that only print
		"echo": true, "printf": true, "true": true, "date": true, "which": true, "type": true, "cd": true}
	// curl flags a gated command passes on that cannot leak the token or reach another host, with whether they take a value.
	gateCurlFlags = map[string]bool{"--url-query": true, "-G": false, "--get": false, "--data-urlencode": true, "-d": true, "--data": true,
		"--data-raw": true, "-o": true, "--output": true, "-i": false, "--include": false, "-m": true, "--max-time": true, "-s": false,
		"-S": false, "-f": false, "--compressed": false, "-H": true, "--header": true, "--upload": true, "-X": true, "--request": true}
)

// classifyLark reads a Bash command against the command gates (curl / wget aimed at one's API is refused); cwd
// resolves @file bodies and query inputs; risk supplies CLI metadata when configured. Known read commands also have a
// static fallback.
func classifyLark(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk) larkVerdict {
	v := classifyLarkDepth(cmd, gates, cwd, risk, 0)
	// A relative @file body or query input is read from cwd at check time. Whatever else the command runs may run
	// the call elsewhere (cd in any spelling, env -C, sudo -D, a subshell) or change the file first (a redirect, an
	// earlier command), so its contents are known only for a lone call (larkLoneGateCall, #8404).
	if slices.ContainsFunc(v.calls, func(c larkCall) bool { return c.relFile }) && !larkLoneGateCall(cmd, gates) {
		v.other, v.unknown = true, true
	}
	return v
}

// larkLoneGateCall: the command is one gated call with literal words and no assignment or redirect, at most under
// `timeout <duration>`, piped only into output filters. Nothing in it runs before the call or changes its directory.
func larkLoneGateCall(cmd string, gates []proto.CommandGate) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil || len(tree.Stmts) != 1 {
		return false
	}
	var calls []*syntax.CallExpr
	var walk func(*syntax.Stmt) bool
	walk = func(st *syntax.Stmt) bool {
		if st.Background || st.Coprocess || st.Negated || len(st.Redirs) != 0 {
			return false
		}
		switch c := st.Cmd.(type) {
		case *syntax.CallExpr:
			calls = append(calls, c)
			return true
		case *syntax.BinaryCmd:
			return c.Op == syntax.Pipe && walk(c.X) && walk(c.Y)
		}
		return false
	}
	if !walk(tree.Stmts[0]) {
		return false
	}
	for i, c := range calls {
		if len(c.Assigns) != 0 || len(c.Args) == 0 {
			return false
		}
		words := make([]string, len(c.Args))
		for j, arg := range c.Args {
			var dynamic bool
			if words[j], dynamic = executionWord(arg); dynamic {
				return false
			}
		}
		if i > 0 {
			if !larkFilters[words[0]] || words[0] == "cd" {
				return false
			}
			continue
		}
		if words[0] == "timeout" && len(words) > 2 && timeoutArg.MatchString(words[1]) {
			words = words[2:]
		}
		if gateOf(gates, words[0]) < 0 {
			return false
		}
	}
	return true
}

func classifyLarkDepth(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk, depth int) larkVerdict {
	return classifyLarkAt(cmd, gates, cwd, risk, depth, false)
}

// classifyLarkAt is classifyLarkDepth for a command run here or, remote, on another machine through ssh: there PATH
// names that machine's directories, which say nothing about the programs here.
func classifyLarkAt(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk, depth int, remote bool) larkVerdict {
	var v larkVerdict
	if depth > 16 {
		v.other, v.unparsed, v.unknown = true, true, true
		return v
	}
	v.unknown = shellCommandUnknown(cmd, false, depth)
	merge := func(child larkVerdict) {
		v.calls = append(v.calls, child.calls...)
		if child.deny != "" {
			v.deny = child.deny
		}
		v.unknown = v.unknown || child.unknown || child.unparsed && larkCLIRe.MatchString(cmd)
		v.dynamic = v.dynamic || child.dynamic
		v.cliWrite = v.cliWrite || child.cliWrite
		v.unread = append(v.unread, child.unread...)
	}
	for _, g := range gates {
		for _, name := range g.Secret {
			if name != "" && strings.Contains(cmd, name) {
				v.deny = "不要读取、打印或直接使用 " + name + "：" + g.Title + "一律用 " + g.Command + "，它自己带凭据。"
				return v
			}
		}
	}
	if strings.Contains(cmd, "/proc/") && strings.Contains(cmd, "environ") {
		v.deny = "不要读取进程的环境变量（/proc/*/environ）：里面有凭据。"
		return v
	}
	if larkProfileEnv.MatchString(larkProfileRead.ReplaceAllString(cmd, "")) && larkCLIRe.MatchString(cmd) {
		v.deny = "lark-cli 的配置档由管理者定（manager.json 的 lark.cli.profile），不要自己设或清 LARKSUITE_CLI_PROFILE。"
		return v
	}
	if strings.Contains(cmd, ".lark-cli/") || strings.HasSuffix(cmd, ".lark-cli") {
		v.deny = "不要读或改 lark-cli 的配置和凭据（~/.lark-cli）：飞书用 lark-cli 命令，它自己带登录。"
		return v
	}
	if commands, ok := literalLarkLoopCommands(cmd, gates); ok {
		v.unknown, v.other = false, true
		for _, command := range commands {
			child := classifyLarkDepth(command, gates, cwd, risk, depth+1)
			merge(child)
			v.unsure = v.unsure || child.unsure
			v.unparsed = v.unparsed || child.unparsed
			// Loop expansion proves reads only. Writes (including GET deletion
			// routes) keep the whole-command approval and proxy restrictions.
			for _, call := range child.calls {
				v.unknown = v.unknown || !larkRead(call) || !larkBypassSafe(call)
			}
		}
		return v
	}
	cmd = normalizeLarkCommand(cmd, cwd, gates)
	v.unknown = shellCommandUnknown(cmd, false, depth)
	segs, ok := shellcmd.Split(cmd)
	if !ok {
		v.other, v.unparsed, v.unknown = true, true, v.unknown || gateNamed(gates, cmd) || larkCLIRe.MatchString(cmd) || shellCommandUnknown(cmd, true, depth)
		return v
	}
	vars := map[string]string{}
	varsStable := larkVariableScopeStable(cmd, gates)
	executableEnv := false
	// Text naming lark-cli is a call only where a program executes it (larkgate_text.go): flow notes each
	// segment for larkTextRuns.
	flow := make([]larkFlow, 0, len(segs))
	// sequential: a `;` / `&&` / `||` list of simple commands, run one after another in the order written.
	sequential := false
	if tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), ""); err == nil {
		_, sequential = larkFlatStmts(tree.Stmts)
	}
	for _, s := range segs {
		writes, notes := larkWritesFile(s.Redirect)
		flow = append(flow, larkFlow{text: s.Text, writes: writes, notes: notes})
		seg := &flow[len(flow)-1]
		nestedUnsure := false
		// fileReads: every substitution that is no filter only reads files (larkFileReadSubstitution).
		fileReads := true
		for _, nested := range s.Nested {
			if inner, ok := larkArithmetic(nested); ok {
				// $((…)) is arithmetic, not a subshell: it runs only its own $(…). It stays unsure (a variable's
				// value is evaluated as an expression, which may hold a substitution of its own).
				nestedUnsure, fileReads = true, false
				for _, c := range inner {
					child := classifyLarkDepth(c, gates, cwd, risk, depth+1)
					if len(child.calls) > 0 || child.unknown || child.unparsed {
						v.other = true
					}
					merge(child)
				}
				continue
			}
			child := classifyLarkDepth(nested, gates, cwd, risk, depth+1)
			if len(child.calls) > 0 || child.unknown || child.unparsed {
				v.other = true
			}
			nestedUnsure = nestedUnsure || !larkFilterSubstitution(nested) || child.unknown || child.unparsed || child.unsure
			fileReads = fileReads && (larkFilterSubstitution(nested) || larkFileReadSubstitution(nested)) &&
				len(child.calls) == 0 && !child.unknown && !child.unparsed && !child.unsure
			merge(child)
		}
		// Output filters in a substitution do not make the enclosing command
		// opaque. Gated calls and unresolved execution inside it still do. A lark-cli command's argument may also come
		// from a file (`$(cat ids.txt)`, `$(< ids.txt)`, #9101), as from $(head …): a write with it stays unknown by
		// the dynamic-argument rule below. In any other command's argument (curl's) a file read stays unsure: it could
		// carry the file elsewhere.
		cliFileRead := s.ArgSubst && nestedUnsure && fileReads && larkSegmentCLI(s.Words)
		if s.ArgSubst && nestedUnsure && !cliFileRead {
			v.unsure, v.other = true, true
		}
		if v.deny != "" {
			return v
		}
		words := s.Words
		// Loop bodies may prefix the invocation with both a shell keyword and environment assignments.
		for len(words) > 0 && (words[0] == "do" || words[0] == "then" || words[0] == "else") {
			words = words[1:]
			v.other = true
		}
		// Leading assignments: alone they set a variable for later words; before ol only the account / timeout.
		envWords := words
		if len(envWords) > 0 && envWords[0] == "export" {
			envWords = envWords[1:]
		}
		for _, w := range envWords {
			if !isAssignment(w) {
				break
			}
			executableEnv = executableEnv || larkExecutableAssignment(w, remote)
		}
		var env []string
		for len(words) > 0 && isAssignment(words[0]) {
			env = append(env, words[0])
			words = words[1:]
		}
		if len(words) == 0 {
			if varsStable {
				for _, a := range env {
					k, val, _ := strings.Cut(a, "=")
					if strings.ContainsAny(val, "$`") {
						delete(vars, k) // known only when it runs: stays a variable
						continue
					}
					vars[k] = val
				}
			}
			continue
		}
		for _, word := range words {
			if isAssignment(word) {
				executableEnv = executableEnv || larkExecutableAssignment(word, remote)
			}
		}
		var parsed bool
		words, parsed = unwrapCommand(words)
		if !parsed {
			v.other = true
			v.unknown = v.unknown || larkCLIRe.MatchString(s.Text)
			for _, word := range words {
				v.unknown = v.unknown || strings.ContainsAny(word, "$`*?[]{}")
			}
			continue
		}
		if len(words) == 0 {
			continue
		}
		name := words[0]
		gate := -1
		if (name == "bash" || name == "sh") && len(words) > 1 {
			if i := gateOf(gates, words[1]); i >= 0 {
				gate, words = i, words[1:]
			}
		}
		if gate < 0 {
			gate = gateOf(gates, name)
		}
		switch {
		case shellScriptIndex(words) >= 0:
			v.other = true
			merge(classifyLarkDepth(words[shellScriptIndex(words)], gates, cwd, risk, depth+1))
		case name == "eval":
			v.other = true
			merge(classifyLarkDepth(strings.Join(words[1:], " "), gates, cwd, risk, depth+1))
		case isLarkCLI(name):
			// As for gated calls (parseGateCall): literal assignments earlier in the command resolve; what stays a
			// variable may be a flag value of a read, never a command word, an api route or anything of a write.
			args := make([]string, len(words)-1)
			for i, a := range words[1:] {
				args[i] = shellVarRef.ReplaceAllStringFunc(a, func(m string) string {
					if val, ok := vars[strings.Trim(m, "${}")]; ok {
						return val
					}
					return m
				})
			}
			dynamic := strings.ContainsAny(strings.Join(args, " "), "$`")
			c, deny, ok := parseLarkCLI(args, risk)
			if deny != "" {
				v.deny = deny
				return v
			}
			if !ok {
				v.other, v.cliWrite, v.unknown = true, true, true
				continue
			}
			if !c.cliRead() {
				v.cliWrite = true
			}
			// An unquoted variable can add words; that matters only to a read that --dry-run or --help made one
			// (a later --dry-run=false sends): a command's own risk and an api method are set by its literal words.
			flagRead := flagOn(args, "--dry-run") || flagOn(args, "-h", "--help")
			if dynamic && (!c.cliRead() || strings.Contains(c.Path, "$") || strings.Contains(strings.Join(args, " "), "`") || flagRead && splitGateArguments(s.Text)) {
				v.other, v.unknown = true, true
			}
			v.calls = append(v.calls, c)
			seg.call = true // its words go to lark-cli alone, its output is data
			continue
		case gate >= 0:
			g := gates[gate]
			for _, a := range env {
				if k, _, _ := strings.Cut(a, "="); !slices.Contains(g.AllowedEnv, k) {
					v.other = true
				}
			}
			if s.ArgSubst || splitGateArguments(s.Text) {
				v.other, v.unknown = true, true
				v.unread = append(v.unread, gate)
				continue
			}
			c, deny, ok := parseGateCall(g, words[1:], vars, cwd)
			if deny != "" {
				v.deny = deny
				return v
			}
			if !ok {
				v.other, v.unknown = true, true
				v.unread = append(v.unread, gate)
				continue
			}
			c.Gate = gate
			v.calls = append(v.calls, c)
		case name == "curl" || name == "wget" || name == "http" || name == "xh":
			if g, ok := gateAimed(gates, s.Text); ok {
				v.deny = "不要直接 curl " + g.Title + " 的接口：用 " + g.Command + "，写操作要先在手机上确认。"
				return v
			}
			v.other = true
		case filepath.Base(name) == "ssh":
			v.other = true
			remote, ok := sshRemoteCommand(words[1:])
			if !ok || remote == "" {
				seg.opaque = true // a login shell there runs whatever comes in
				v.unknown = v.unknown || larkCLIRe.MatchString(s.Text)
				continue
			}
			// lark-cli there acts as the user too: only what the gate can read as reads stays out of the way.
			child := classifyLarkAt(remote, gates, "", risk, depth+1, true)
			for _, c := range child.calls {
				child.unknown = child.unknown || !c.CLI // a gated command there is not this machine's gate
			}
			merge(child)
			v.unsure = v.unsure || child.unsure
			v.unknown = v.unknown || child.unparsed && larkCLIRe.MatchString(remote)
			seg.call = true // the remote command, read above, is what its words do
		case filepath.Base(name) == "perl" && perlSubstitution(words[1:]):
			v.other = true // it edits text and runs none of it, whatever its pattern names
		case name == "for" || name == "done" || name == "fi":
			v.other = true // a loop's words are its variable's values: run only where a command uses $var (dynamicCommandName)
		case larkFilters[name] && len(env) == 0:
			seg.filter = true
		case name == "tee":
			v.other, seg.filter, seg.writes = true, true, true
		default:
			v.other, seg.program = true, true
			// Inline Python that can start nothing holds lark-cli's name as data (#8148); it may write it to a file.
			inert := inertPythonCode(words)
			dynamicName := dynamicCommandName(name)
			if larkCLIRe.MatchString(s.Text) && !inert || dynamicName {
				v.unknown = true // an interpreter, wrapper or script may invoke the CLI
			}
			v.dynamic = v.dynamic || dynamicName
			if inert && larkCLIRe.MatchString(s.Text) {
				if files, ok := pythonWrites(words); ok {
					seg.notes = append(seg.notes, files...)
				} else {
					seg.writes = true
				}
			}
			// A program whose code the command does not show may run lark-cli; inline code that runs nothing
			// else is read by its own words above.
			seg.opaque = indirectLarkCommand(name) && !selfContainedCode(words) && !inert
			sequential = sequential && name != "trap" // its code runs later
		}
	}
	if larkTextRuns(cmd, flow, sequential) {
		v.other, v.unknown = true, true
	}
	if executableEnv && (len(v.calls) > 0 || larkCLIRe.MatchString(cmd)) {
		v.other, v.unknown = true, true
	}
	if len(v.calls) == 0 {
		v.other = true
	}
	return v
}

// larkFilterSubstitution reports whether a command substitution only formats
// or prints data. Other programs keep the enclosing gated call conservative.
func larkFilterSubstitution(cmd string) bool {
	segs, ok := shellcmd.Split(cmd)
	if !ok || len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		if s.Subst || len(s.Words) == 0 {
			return false
		}
		words, ok := unwrapCommand(s.Words)
		if !ok || len(words) == 0 || len(words) != len(s.Words) || !larkFilters[words[0]] {
			return false
		}
	}
	return true
}

// larkFileReadSubstitution: a command substitution that only reads files, `cat <file>…` with plain file words or
// `< file` (#9101). Kept only in a lark-cli read's argument (larkSegmentCLI), as $(head …) is anywhere.
func larkFileReadSubstitution(cmd string) bool {
	segs, ok := shellcmd.Split(cmd)
	if !ok || len(segs) != 1 || segs[0].Subst {
		return false
	}
	s := segs[0]
	if len(s.Words) == 0 {
		return larkReadsFileOnly(s.Redirect)
	}
	if s.Redirect != "" || s.Words[0] != "cat" || len(s.Words) < 2 {
		return false
	}
	for _, w := range s.Words[1:] {
		if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "$`") {
			return false
		}
	}
	return true
}

// larkReadsFileOnly: redirect is one input redirection from a file ("<ids.txt"), nothing written, no descriptor moved.
func larkReadsFileOnly(redirect string) bool {
	file, ok := strings.CutPrefix(redirect, "<")
	return ok && file != "" && !strings.ContainsAny(file, "<>&| ")
}

// larkSegmentCLI: a segment's command, past loop keywords, assignments and wrappers (timeout N…), is lark-cli.
func larkSegmentCLI(words []string) bool {
	for len(words) > 0 && (words[0] == "do" || words[0] == "then" || words[0] == "else" || isAssignment(words[0])) {
		words = words[1:]
	}
	words, ok := unwrapCommand(words)
	return ok && len(words) > 0 && isLarkCLI(words[0])
}

func isAssignment(w string) bool {
	k, _, ok := strings.Cut(w, "=")
	if !ok || k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c == '_' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || i > 0 && '0' <= c && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// indirectLarkCommand identifies opaque execution in a command already naming the CLI.
// Without that context, interpreters and script files stay outside this gate.
func indirectLarkCommand(name string) bool {
	if name == "[" || name == "[[" {
		return false
	}
	if strings.ContainsAny(name, "$`*?[]{}~") {
		return true
	}
	base := filepath.Base(name)
	if larkInterpreter.MatchString(base) {
		return true
	}
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "python", "python2", "python3", "node", "nodejs", "perl", "ruby", "lua", "php", "eval", "xargs", "source", ".", "env", "exec", "command", "sudo", "timeout", "builtin", "busybox", "time", "!", "nohup", "nice", "setsid", "ssh", "awk", "gawk", "sed", "find":
		return true
	}
	if strings.Contains(name, "/") {
		if dir := filepath.Dir(name); dir == "/bin" || dir == "/usr/bin" {
			if larkFilters[base] {
				return false
			}
			switch base {
			case "rm", "git", "tee", "ls", "cat", "curl", "wget":
				return false
			}
		}
		return true
	}
	return strings.HasSuffix(name, ".sh") || strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".js")
}

// These variables can replace the executable or load code before a nominally read-only CLI call.
var larkExecutableEnv = map[string]bool{
	"PATH": true, "NODE_OPTIONS": true, "NODE_PATH": true, "BASH_ENV": true, "ENV": true, "ZDOTDIR": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "DYLD_INSERT_LIBRARIES": true,
	"PYTHONPATH": true, "PYTHONSTARTUP": true,
}

var larkInterpreter = lazyre.New(`^(?:python|node|perl|ruby|lua|php)(?:[0-9]+(?:\.[0-9]+)*)?$`)

var redirectFD = lazyre.New(`^[0-9]*[<>]&[0-9]+$`)

// safeRedirect is used only by readsOnlyMemory. It accepts /tmp and /dev/null;
// readsOnlyMemory rejects /tmp separately. Lark redirect targets are not checked here.
func safeRedirect(r string) bool {
	for _, t := range strings.Fields(r) {
		if strings.ContainsAny(t, "$`*?[]{}~\"'\\") {
			return false
		}
		if strings.HasSuffix(t, ">") || strings.HasSuffix(t, "<") {
			return false
		}
		if redirectFD.MatchString(t) {
			continue
		}
		t = strings.TrimLeft(t, "0123456789&>")
		t = filepath.Clean(t)
		if t == "/dev/null" || strings.HasPrefix(t, "/tmp/") {
			continue
		}
		return false
	}
	return true
}

// gateOf is the gate whose command or script name is, -1 for none.
func gateOf(gates []proto.CommandGate, name string) int {
	for i, g := range gates {
		if g.Command != "" && name == g.Command || g.Script != "" && (name == g.Script || strings.HasSuffix(name, "/"+g.Script)) {
			return i
		}
	}
	return -1
}

// gateAimed: text reaches a gate's API (its base's host, or its path prefix).
func gateAimed(gates []proto.CommandGate, text string) (proto.CommandGate, bool) {
	for _, g := range gates {
		if h := larkHost(g.Env[g.BaseEnv]); h != "" && strings.Contains(text, h) || g.PathPrefix != "" && strings.Contains(text, g.PathPrefix) {
			return g, true
		}
	}
	return proto.CommandGate{}, false
}

// gateNamed reports literal gate command words in a command the shell
// splitter could not read. The AST preserves names next to redirections;
// the fallback is deliberately conservative about quoted text.
func gateNamed(gates []proto.CommandGate, cmd string) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err == nil {
		found := false
		syntax.Walk(tree, func(node syntax.Node) bool {
			call, ok := node.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 || found {
				return !found
			}
			words := make([]string, len(call.Args))
			for i, word := range call.Args {
				words[i], _ = executionWord(word)
			}
			suffix, ok := unwrapCommand(words)
			if ok && len(suffix) > 0 {
				found = gateOf(gates, suffix[0]) >= 0
				if !found && (suffix[0] == "bash" || suffix[0] == "sh") && len(suffix) > 1 {
					found = gateOf(gates, suffix[1]) >= 0
				}
			}
			return !found
		})
		return found
	}
	cmd = strings.NewReplacer("<", " ", ">", " ").Replace(cmd)
	for _, word := range strings.Fields(cmd) {
		if gateOf(gates, strings.Trim(word, "'\"`;&|()")) >= 0 {
			return true
		}
	}
	return false
}

// larkVariableScopeStable reports whether straight-line assignments can be
// used to expand a later route. Only known read-only commands can appear
// between those assignments and the gated call.
func larkVariableScopeStable(cmd string, gates []proto.CommandGate) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	stable := true
	syntax.Walk(tree, func(node syntax.Node) bool {
		if !stable {
			return false
		}
		switch n := node.(type) {
		case *syntax.IfClause, *syntax.ForClause, *syntax.WhileClause, *syntax.CaseClause, *syntax.FuncDecl, *syntax.DeclClause:
			stable = false
			return false
		case *syntax.Stmt:
			if n.Background || n.Negated || n.Coprocess {
				stable = false
				return false
			}
		case *syntax.BinaryCmd:
			// A pipeline's parts and what follows && or || may run in a subshell or not at all: an assignment there
			// may never reach later commands. Commands there are checked as anywhere else.
			syntax.Walk(n, func(inner syntax.Node) bool {
				if call, ok := inner.(*syntax.CallExpr); ok && len(call.Assigns) > 0 {
					stable = false
				}
				return stable
			})
			return stable
		case *syntax.Assign:
			if n.Append || n.Index != nil || n.Array != nil || n.Value == nil {
				stable = false
				return false
			}
		case *syntax.ArithmExp:
			stable = false
			return false
		case *syntax.ParamExp:
			if n.Exp != nil {
				stable = false
				return false
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			words := make([]string, len(n.Args))
			for i, word := range n.Args {
				words[i], _ = executionWord(word)
			}
			suffix, ok := unwrapCommand(words)
			if !ok || len(suffix) == 0 {
				stable = false
				return false
			}
			name := suffix[0]
			allowed := gateOf(gates, name) >= 0 || isLarkCLI(name)
			if !allowed && (name == "bash" || name == "sh") && len(suffix) > 1 {
				allowed = gateOf(gates, suffix[1]) >= 0
			}
			if !allowed {
				allowed = map[string]bool{"cat": true, "tee": true, "ls": true, "git": true, "go": true, "pwd": true, "sleep": true, "false": true, ":": true}[name] ||
					larkFilters[name] && name != "printf" && name != "cd"
			}
			if !allowed {
				stable = false
				return false
			}
		}
		return true
	})
	return stable
}

// scalarParameter is a plain quoted scalar expansion, not "$@", an array
// expansion, or a parameter operation whose result needs further parsing.
func scalarParameter(p *syntax.ParamExp) bool {
	return p != nil && p.Param != nil && p.Param.Value != "@" && p.Param.Value != "*" &&
		!p.Excl && !p.Length && !p.Width && p.Index == nil && p.Slice == nil && p.Repl == nil && p.Exp == nil && p.Names == 0
}

// braceExpansion recognizes braces across the literal and quoted parts of one
// word. Quoted content is a placeholder because it does not itself expand.
func braceExpansion(word *syntax.Word) bool {
	var text strings.Builder
	for _, part := range word.Parts {
		if lit, ok := part.(*syntax.Lit); ok {
			text.WriteString(lit.Value)
		} else {
			text.WriteByte('_')
		}
	}
	value := text.String()
	return strings.Contains(value, "{") && strings.Contains(value, "}") && strings.Contains(value, ",")
}

// wordExpandsToMany reports shell syntax that can produce extra arguments.
// Double quotes protect scalar variables, but not "$@" or array expansions.
func wordExpandsToMany(word *syntax.Word) bool {
	if braceExpansion(word) {
		return true
	}
	found := false
	syntax.Walk(word, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.SglQuoted:
			return false
		case *syntax.DblQuoted:
			syntax.Walk(n, func(inner syntax.Node) bool {
				if p, ok := inner.(*syntax.ParamExp); ok && !scalarParameter(p) {
					found = true
					return false
				}
				return true
			})
			return false
		case *syntax.ParamExp, *syntax.BraceExp, *syntax.ArithmExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ExtGlob:
			found = true
			return false
		case *syntax.Lit:
			if strings.ContainsAny(n.Value, "*?[") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// splitGateArguments reports an expansion that can produce extra arguments.
// Redirect targets are excluded.
func splitGateArguments(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	for {
		stripped := false
		for _, keyword := range []string{"do", "then", "else"} {
			if len(cmd) > len(keyword) && cmd[:len(keyword)] == keyword && strings.ContainsRune(" \t\n", rune(cmd[len(keyword)])) {
				cmd = strings.TrimSpace(cmd[len(keyword):])
				stripped = true
			}
		}
		if !stripped {
			break
		}
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return true
	}
	found := false
	syntax.Walk(tree, func(node syntax.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			found = wordExpandsToMany(arg)
			if found {
				break
			}
		}
		return false
	})
	return found
}

// parseGateCall reads `METHOD PATH [BODY] [curl flags]` after a gated command.
func parseGateCall(g proto.CommandGate, args []string, vars map[string]string, cwd string) (larkCall, string, bool) {
	expand := func(s string) (string, bool) {
		if !strings.Contains(s, "$") {
			return s, true
		}
		out := shellVarRef.ReplaceAllStringFunc(s, func(m string) string {
			k := strings.Trim(m, "${}")
			if val, ok := vars[k]; ok {
				return val
			}
			return m
		})
		return out, !strings.Contains(out, "$")
	}
	// A literal method is part of the call's identity: even a previously
	// assigned variable could change before a loop invokes it. A literal GET
	// can keep variables in its query, but an unresolved route could hide a
	// deletion action or a configured deny rule.
	if len(args) < 2 || !httpMethod.MatchString(strings.ToUpper(args[0])) {
		return larkCall{}, "", false // a subcommand of its own (ol events …): not a call the rules know
	}
	method := strings.ToUpper(args[0])
	path, resolved := expand(args[1])
	path, _, _ = strings.Cut(path, "#") // curl never sends a URL fragment, including one after a raw path query.
	route, _, _ := strings.Cut(path, "?")
	if strings.Contains(route, "$") || strings.Contains(path, "$(") || strings.Contains(path, "`") || !resolved && method != "GET" {
		return larkCall{}, "", false
	}
	c := larkCall{Method: method}
	if g.PathPrefix != "" {
		path = strings.TrimPrefix(path, g.PathPrefix)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	c.Path, c.Query, _ = strings.Cut(path, "?")
	queryFlagSeen, queryFragment, get, hasData := false, false, false, false
	rest := args[2:]
	if len(rest) > 0 && (strings.HasPrefix(rest[0], "{") || strings.HasPrefix(rest[0], "[")) {
		c.Body, rest, hasData = rest[0], rest[1:], true
	} else if len(rest) > 0 && strings.HasPrefix(rest[0], "@") {
		c.Body, c.relFile, rest = readBodyFile(rest[0][1:], cwd), !filepath.IsAbs(rest[0][1:]), rest[1:]
		hasData = true
	}
	for i := 0; i < len(rest); i++ {
		f := rest[i]
		switch f {
		case "-v", "--verbose", "--trace", "--trace-ascii", "--trace-time", "-K", "--config", "--libcurl", "-w", "--write-out", "-D", "--dump-header":
			return larkCall{}, "不要给 " + g.Command + " 加会打印请求头（含凭据）或改写请求的 curl 参数（" + f + "）。", false
		}
		name, val, inline := strings.Cut(f, "=")
		if strings.HasPrefix(f, "-X") && len(f) > 2 { // -XPOST
			name, val, inline = "-X", f[2:], true
		}
		takes, known := gateCurlFlags[name]
		if !known {
			return larkCall{}, "", false // a positional word would be another URL curl sends the token to
		}
		if takes && !inline {
			if i+1 >= len(rest) {
				return larkCall{}, "", false
			}
			i++
			val = rest[i]
		}
		switch name {
		case "-X", "--request":
			c.Method = strings.ToUpper(val) // curl takes the last -X
		case "-G", "--get":
			get = true
		case "-H", "--header":
			if h, _, _ := strings.Cut(val, ":"); !slices.Contains(g.Headers, strings.ToLower(strings.TrimSpace(h))) {
				return larkCall{}, "", false
			}
		case "--upload":
			c.Body = "上传文件 " + val
		case "--url-query":
			if gateCurlQueryExpansionUnknown(val) {
				c.queryUnknown = true
			}
			query, relative := "", false
			if strings.HasPrefix(val, "+") {
				query = val[1:] // curl's leading plus says the rest is already encoded.
			} else if name, value, found := strings.Cut(val, "="); found {
				// A name is already literal: curl escapes only the value. A leading equals instead marks unnamed content,
				// which it encodes without emitting that equals sign.
				if name != "" {
					query = name + "="
				}
				query += gateCurlQueryEscape(value)
			} else if name, p, file := strings.Cut(val, "@"); file {
				// With no equals, name@file is curl's file form; @file is the same form without a name.
				if p == "-" {
					query = val
					c.queryUnknown = true
				} else {
					relative = !filepath.IsAbs(p)
					if relative {
						p = filepath.Join(cwd, p)
					}
					b, err := os.ReadFile(p)
					if err != nil {
						return larkCall{}, "读不到 --url-query 文件 " + p + "：" + err.Error(), false
					}
					if len(b) > 0 { // curl adds nothing for an empty file, even when it has a name.
						query = gateCurlQueryEscape(string(b))
						if name != "" {
							query = name + "=" + query
						}
					}
				}
			} else {
				query = gateCurlQueryEscape(val)
			}
			// A literal name or leading-plus content may introduce the final URL's fragment. Unlike a fragment already in
			// the path (which curl parses before applying query flags), everything appended after this marker belongs to
			// that fragment. Escaped values and file contents contain %23 instead and never set this state.
			query, _, hasFragment := strings.Cut(query, "#")
			c.relFile = c.relFile || relative
			if queryFragment {
				continue
			}
			if queryFlagSeen {
				// Every later query flag adds a separator, even when the preceding argument was empty or the URL query
				// already ended in one. Empty arguments and files therefore remain visible in the raw query identity.
				c.Query += "&"
			} else if c.Query != "" && !strings.HasSuffix(c.Query, "&") {
				// The first query flag appends to a nonempty URL query, but does not duplicate its trailing separator.
				c.Query += "&"
			}
			c.Query += query
			queryFlagSeen = true
			queryFragment = hasFragment
		case "-d", "--data", "--data-raw", "--data-urlencode":
			hasData = true
			if c.Body == "" {
				c.Body = val
			}
		}
	}
	if get && hasData {
		// Curl moves data into the URL query for -G, including GET/HEAD and even with -X POST, and its combination with
		// --url-query is not the order in which the flags appear. Mark every method unknown: rule-allowed calls can still
		// go, while larkCheck refuses to create a confirmation grant for a different request.
		c.queryUnknown = true
		c.dataInQuery = true
	}
	return c, "", true
}

// gateCurlQueryExpansionUnknown reports every shell expansion in a raw --url-query argument. Quote information is gone
// by this point, so even a previously assigned $NAME is unsafe to expand: single quotes would have kept it literal.
// Literal dollars that cannot start an expansion remain data; +a=%24value is the explicit preencoded literal alternative.
func gateCurlQueryExpansionUnknown(raw string) bool {
	if strings.Contains(raw, "`") || shellVarRef.MatchString(raw) {
		return true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '$' || i+1 == len(raw) {
			continue
		}
		next := raw[i+1]
		if next == '{' || next == '(' || next == '[' || next >= '0' && next <= '9' ||
			next >= 'A' && next <= 'Z' || next >= 'a' && next <= 'z' || next == '_' || strings.ContainsRune("*@#?$!-", rune(next)) {
			return true // an unsupported, positional, special, arithmetic or command substitution
		}
		// Every other dollar is literal data (for example the $' in an already quoted reserved-byte argument).
	}
	return false
}

// gateCurlQueryEscape is curl --url-query's form escaping without its leading-plus or @file handling: unreserved bytes
// stay as they are, a space becomes plus, and every other byte becomes a lowercase percent escape. Escaping bytes rather
// than URL-parsing the argument also keeps existing percent signs, ampersands and equals signs from acquiring structure.
func gateCurlQueryEscape(s string) string {
	const hex = "0123456789abcdef"
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~", rune(c)) {
			out.WriteByte(c)
		} else if c == ' ' {
			out.WriteByte('+')
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
	return out.String()
}

func readBodyFile(p, cwd string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "（读不到 " + p + "）"
	}
	return textfmt.Clip(string(b), 4000)
}

var httpMethod = lazyre.New(`^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$`)

var (
	// timeoutArg is the duration of a leading `timeout 20 …` (also read by readsOnlyMemory).
	timeoutArg = lazyre.New(`^[0-9.]+[smh]?$`)
	// shellVarRef is a $NAME or ${NAME} reference parseGateCall expands.
	shellVarRef = lazyre.New(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	// commandNameRe is what safeCommandName lets onto PATH.
	commandNameRe = lazyre.New(`^[a-z][a-z0-9_-]{0,31}$`)
)

func larkHost(base string) string {
	h := base
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h, _, _ = strings.Cut(h, "/")
	return h
}
