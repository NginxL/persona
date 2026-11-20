package node

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

// literalLarkLoopCommands binds each literal for-list value for classification
// only. It never executes shell code. Validate the whole tree before returning
// any commands: a later mutation or an opaque outer loop invalidates all bindings.
func literalLarkLoopCommands(cmd string, gates []proto.CommandGate) ([]string, bool) {
	if !strings.Contains(cmd, "for") {
		return nil, false
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, false
	}
	vars := map[string]string{}
	var commands []string
	resolved := false
	loops, steps, size := 0, 0, 0
	var visit func(*syntax.Stmt, bool) bool
	visit = func(stmt *syntax.Stmt, top bool) bool {
		steps++
		// Bound Cartesian products of nested lists and duplicated command text.
		if steps > 256 || stmt.Background || stmt.Coprocess || stmt.Negated {
			return false
		}
		if _, simple := stmt.Cmd.(*syntax.CallExpr); !simple && len(stmt.Redirs) > 0 {
			return false
		}
		switch n := stmt.Cmd.(type) {
		case *syntax.ForClause:
			list, ok := n.Loop.(*syntax.WordIter)
			if !ok || n.Select || !list.InPos.IsValid() || len(list.Items) == 0 || list.Name == nil {
				return false
			}
			name := list.Name.Value
			if _, shadow := vars[name]; shadow || name == "IFS" || larkExecutableEnv[name] {
				return false
			}
			switch name {
			case "RANDOM", "SRANDOM", "SECONDS":
				return false // the shell computes these values instead of retaining the assignment
			}
			values := make([]string, len(list.Items))
			for i, item := range list.Items {
				value, _, ok := literalLoopWord(item, nil)
				if !ok || strings.ContainsAny(value, "$`*?[]{}") || wordExpandsToMany(item) {
					return false
				}
				values[i] = value
			}
			loops++
			for _, value := range values {
				vars[name] = value
				for _, body := range n.Do {
					if !visit(body, false) {
						return false
					}
				}
			}
			delete(vars, name) // later uses need their own proven binding
			return true
		case *syntax.BinaryCmd:
			if n.Op == syntax.AndStmt || n.Op == syntax.OrStmt {
				// The right arm may be skipped. An assignment there must not replace a binding used by a later loop:
				// the old value (or no value) remains reachable, so the expansion stays conservative.
				return visit(n.X, top) && visit(n.Y, false)
			}
			return visit(n.X, false) && visit(n.Y, false) // a pipe's assignments do not reach the parent shell
		case *syntax.Block:
			for _, body := range n.Stmts {
				if !visit(body, false) {
					return false
				}
			}
			return true
		case *syntax.IfClause:
			for clause := n; clause != nil; clause = clause.Else {
				for _, body := range append(append([]*syntax.Stmt{}, clause.Cond...), clause.Then...) {
					if !visit(body, false) {
						return false
					}
				}
			}
			return true
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				if !top || len(stmt.Redirs) != 0 || !bindLiteralLoopAssignments(n.Assigns, vars, cmd) {
					return false
				}
				return true
			}
			if !literalLoopEnvAssignments(n.Assigns) {
				return false
			}
			words := make([]string, len(n.Args))
			bound := make([]bool, len(n.Args))
			for i, word := range n.Args {
				var ok bool
				words[i], bound[i], ok = literalLoopWord(word, vars)
				if !ok || isAssignment(words[i]) {
					return false
				}
			}
			// Locate the executable before resolving it: a variable must never
			// become a wrapper, shell, CLI or gate, even if its arguments look safe.
			original := make([]string, len(n.Args))
			for i, word := range n.Args {
				original[i], _ = executionWord(word)
			}
			rest, ok := unwrapCommand(original)
			if !ok || len(rest) == 0 {
				return false
			}
			index := len(original) - len(rest)
			name := words[index]
			base := filepath.Base(name)
			gated := gateOf(gates, name) >= 0 || isLarkCLI(name)
			if bound[index] {
				_, wrapper := commandWrappers[base]
				if !safeLiteralCommand(strings.TrimPrefix(name, "~/")) || gated || gateOf(gates, base) >= 0 || wrapper ||
					commandShells[strings.TrimRight(base, "0123456789.")] || indirectLarkCommand(base) {
					return false
				}
			}
			// A variable method remains unknown, matching the existing gate policy.
			if gated && index+1 < len(bound) && bound[index+1] {
				return false
			}
			if gateOf(gates, name) >= 0 && (index+1 >= len(words) || !strings.EqualFold(words[index+1], "GET") && !strings.EqualFold(words[index+1], "HEAD")) {
				return false // preserve the existing approval/denial path for explicit writes
			}
			resolved = resolved || bound[index]
			if gated {
				for _, changed := range bound[index+1:] {
					resolved = resolved || changed
				}
			}
			// These commands cannot mutate a binding in the parent shell. Unknown
			// builtins/functions and all assignment/declaration forms fall back.
			ordinary := map[string]bool{"[": true, ":": true, "false": true, "cd": true, "ls": true, "git": true, "go": true, "pwd": true, "sleep": true, "cat": true, "tee": true, "python": true, "python3": true, "sha256sum": true}[name] ||
				larkFilters[name] && name != "printf" && name != "cd"
			external := (filepath.IsAbs(name) || strings.HasPrefix(name, "~/")) && !commandShells[base] && !indirectLarkCommand(base)
			if !gated && !ordinary && !external {
				return false
			}
			type replacement struct {
				start, end int
				text       string
			}
			var replacements []replacement
			valid := true
			envValues := map[*syntax.Word]bool{}
			for _, a := range n.Assigns {
				envValues[a.Value] = true
			}
			syntax.Walk(stmt, func(node syntax.Node) bool {
				if r, ok := node.(*syntax.Redirect); ok && r.Hdoc != nil {
					// A literal heredoc is preserved byte for byte. Expansion in an unquoted body makes the whole
					// binding attempt opaque; a quoted body is data and needs no variable replacement.
					_, dynamic := executionWord(r.Hdoc)
					valid = valid && !dynamic
					return false
				}
				word, ok := node.(*syntax.Word)
				if !ok {
					return valid
				}
				if envValues[word] {
					return false // invocation-local environment values stay for the nested classifier
				}
				value, changed, ok := literalLoopWord(word, vars)
				if !ok {
					valid = false
				} else if changed {
					quoted, err := syntax.Quote(value, syntax.LangBash)
					valid = valid && err == nil
					replacements = append(replacements, replacement{int(word.Pos().Offset()), int(word.End().Offset()), quoted})
				}
				return false
			})
			if !valid {
				return false
			}
			sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })
			var out strings.Builder
			at := int(stmt.Pos().Offset())
			for _, r := range replacements {
				out.WriteString(cmd[at:r.start])
				out.WriteString(r.text)
				at = r.end
			}
			out.WriteString(cmd[at:stmt.End().Offset()])
			size += out.Len()
			if size > 256*1024 {
				return false
			}
			commands = append(commands, out.String())
			return true
		default:
			return false
		}
	}
	for _, stmt := range tree.Stmts {
		if !visit(stmt, true) {
			return nil, false
		}
	}
	return commands, loops > 0 && resolved
}

// bindLiteralLoopAssignments records only top-level scalar assignments whose value is one literal executable word.
// They have the same lifetime as a literal for-list binding. Anything dynamic or multiword stays for the normal gate.
func bindLiteralLoopAssignments(assigns []*syntax.Assign, vars map[string]string, cmd string) bool {
	if len(assigns) == 0 {
		return false
	}
	for _, a := range assigns {
		if a.Name == nil || a.Append || a.Index != nil || a.Array != nil || a.Value == nil || a.Name.Value == "IFS" || larkExecutableEnv[a.Name.Value] {
			return false
		}
		value, dynamic := executionWord(a.Value)
		if home, ok := larkHomeWord(a.Value, cmd); ok {
			value, dynamic = home, false
		}
		if dynamic || !safeLiteralCommand(value) {
			return false
		}
		vars[a.Name.Value] = value
	}
	return true
}

// literalLoopEnvAssignments permits environment prefixes on one invocation without treating them as persistent
// bindings. Their values are classified normally, including any nested command; executable-loading variables and
// IFS remain opaque because they can change what the expanded words mean.
func literalLoopEnvAssignments(assigns []*syntax.Assign) bool {
	for _, a := range assigns {
		if a.Name == nil || a.Append || a.Index != nil || a.Array != nil || a.Value == nil || a.Name.Value == "IFS" || larkExecutableEnv[a.Name.Value] {
			return false
		}
	}
	return true
}

// literalLoopWord removes shell quotes and substitutes only plain scalar loop
// bindings. An unquoted binding must remain one nonempty, non-globbing word.
func literalLoopWord(word *syntax.Word, vars map[string]string) (string, bool, bool) {
	var out strings.Builder
	changed := false
	var parts func([]syntax.WordPart, bool) bool
	parts = func(items []syntax.WordPart, quoted bool) bool {
		for _, item := range items {
			switch p := item.(type) {
			case *syntax.Lit:
				for i := 0; i < len(p.Value); i++ {
					c := p.Value[i]
					if c == '\\' && i+1 < len(p.Value) && (!quoted || strings.ContainsRune("\"\\$`\n", rune(p.Value[i+1]))) {
						i++
						c = p.Value[i]
						if c == '\n' {
							continue
						}
					}
					out.WriteByte(c)
				}
			case *syntax.SglQuoted:
				if p.Dollar {
					return false
				}
				out.WriteString(p.Value)
			case *syntax.DblQuoted:
				if p.Dollar || !parts(p.Parts, true) {
					return false
				}
			case *syntax.ParamExp:
				if !scalarParameter(p) {
					return false
				}
				if p.Param.Value == "?" {
					// `echo "$d exit=$?"` (#11193): the last exit status is always a number, set by the shell, never by
					// the command, so it cannot name a program or a lark-cli word. Kept as text: a word holding it is
					// not a literal the classifier can read as a command, a method or a path.
					out.WriteString("$?")
					continue
				}
				value, ok := vars[p.Param.Value]
				if !ok || !quoted && !safeLiteralCommand(strings.TrimPrefix(value, "~/")) {
					return false
				}
				out.WriteString(value)
				changed = true
			default:
				return false
			}
		}
		return true
	}
	ok := parts(word.Parts, false)
	// A neighboring glob or brace must not become quoted by replacement.
	if changed && braceExpansion(word) {
		ok = false
	}
	for _, part := range word.Parts {
		if lit, isLit := part.(*syntax.Lit); changed && isLit && strings.ContainsAny(lit.Value, "*?[") {
			ok = false
		}
	}
	return out.String(), changed, ok
}
