package node

import (
	"bytes"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

// normalizeLarkCommand removes inert here-documents, reads programs handed over in here-documents and one-command
// groups as written inline (larkgate_inline.go) and resolves immediately preceding literal command assignments
// solely for classification. It never executes the input.
func normalizeLarkCommand(cmd, cwd string, gates []proto.CommandGate) string {
	return normalizeLarkCommandTracked(cmd, cwd, gates, nil)
}

// resolved, when non-nil, records the normalized segment positions whose executable came from a proven assignment.
// The classifier uses that provenance only for the neighboring-opaque-program check; the command itself is still
// parsed and classified normally. Positions, rather than word content, keep an identical later literal segment distinct.
func normalizeLarkCommandTracked(cmd, cwd string, gates []proto.CommandGate, resolved map[int]bool) string {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return cmd
	}
	inlined := larkInlineSyntax(tree, gates)
	if inlined {
		var out bytes.Buffer
		if syntax.NewPrinter().Print(&out, tree) != nil {
			return cmd
		}
		cmd = out.String()
		if tree, err = syntax.NewParser().Parse(strings.NewReader(cmd), ""); err != nil {
			return cmd
		}
	}
	// Only semicolon-separated units take part in assignment propagation. larkPipeUnits returns the proven prefix
	// before an &&, ||, loop or other unsupported statement; flat && / || lists still get heredoc handling below.
	stmts, flat := larkFlatStmts(tree.Stmts)
	units, _ := larkPipeUnits(tree.Stmts)
	if !flat && len(units) == 0 {
		return cmd
	}
	for _, unit := range units {
		for _, stmt := range unit {
			call := stmt.Cmd.(*syntax.CallExpr)
			for _, a := range call.Assigns {
				if a.Name == nil || a.Append || a.Index != nil || a.Array != nil || a.Value == nil {
					return cmd
				}
				if a.Name.Value == "IFS" || larkExecutableEnv[a.Name.Value] {
					return cmd // executable lookup and field splitting invalidate earlier inferences
				}
				if _, home := larkHomeWord(a.Value, cmd); home {
					continue
				}
				if _, dynamic := executionWord(a.Value); dynamic {
					return cmd // expansions can assign shell state or alter executable lookup
				}
			}
		}
	}
	vars := map[string]string{}
	textVars := map[string]string{}
	// A pipe's commands run apart from the shell: its assignments set nothing for the commands after it. The units
	// here are the straight semicolon prefix only; an unsupported suffix stops later resolutions, not earlier ones.
	changed, unsafe := false, false
	// moved: a literal cd ran before. It changes nothing an absolute path names, but a relative one, or a bare name with
	// "." or an empty entry in PATH, is looked up from the new directory: only absolute values resolve after it (#8389).
	moved := false
	segmentPosition := 0
	for _, unit := range units {
		for _, stmt := range unit {
			position := segmentPosition
			segmentPosition++
			if unsafe {
				break
			}
			call := stmt.Cmd.(*syntax.CallExpr)
			if len(call.Args) == 0 {
				if len(unit) > 1 {
					unsafe = true // an assignment in a pipe sets nothing the next command sees
					break
				}
				for _, a := range call.Assigns {
					if a.Name == nil {
						continue
					}
					delete(vars, a.Name.Value)
					delete(textVars, a.Name.Value)
					if a.Append || a.Index != nil || a.Array != nil || a.Value == nil {
						continue
					}
					value, dynamic := executionWord(a.Value)
					if home, ok := larkHomeWord(a.Value, cmd); ok {
						value, dynamic = home, false
					}
					if !dynamic {
						textVars[a.Name.Value] = value
						if _, ok := literalCommandPrefix(value); ok {
							vars[a.Name.Value] = value
						}
					}
				}
				continue
			}
			words := make([]string, len(call.Args))
			for i, word := range call.Args {
				words[i], _ = executionWord(word)
			}
			suffix, ok := unwrapCommand(words)
			resolvedAbsolute := false
			if ok && len(suffix) > 0 {
				index := len(words) - len(suffix)
				base := filepath.Base(suffix[0])
				scriptArguments := commandShells[strings.TrimRight(base, "0123456789.")] || base == "ssh"
				if scriptArguments {
					for i, arg := range call.Args {
						if i == index {
							continue
						}
						value, replaced, literal := literalLoopWord(arg, vars)
						if !literal || !replaced {
							// Text bindings also cover literal script fragments. Unquoted field splitting rules still
							// reject a multiword value; a quoted expansion stays one word.
							value, replaced, literal = literalLoopWord(arg, textVars)
						}
						if !literal || !replaced {
							continue
						}
						// A single literal binding cannot add fields or globs. Quote the resulting word so a nested shell or
						// remote command receives exactly the bytes the outer shell would have expanded.
						arg.Parts = []syntax.WordPart{&syntax.SglQuoted{Value: value}}
						words[i], changed = value, true
					}
				}
				dynamicArgs := false
				for i, arg := range call.Args {
					if i == index {
						continue
					}
					_, dynamic := executionWord(arg)
					if dynamic || wordExpandsToMany(arg) {
						dynamicArgs = true
						break
					}
				}
				if dynamicArgs {
					unsafe = true
					break // do not resolve the executable after another argument can change shell state
				}
				word := call.Args[index]
				if len(word.Parts) == 1 {
					if p, ok := word.Parts[0].(*syntax.ParamExp); ok && p.Param != nil && !p.Excl && !p.Length && !p.Width && p.Index == nil && p.Slice == nil && p.Repl == nil && p.Exp == nil && p.Names == 0 {
						if value, ok := vars[p.Param.Value]; ok {
							prefix, literal := literalCommandPrefix(value)
							if literal && (!moved || filepath.IsAbs(prefix[0])) {
								expanded := make([]*syntax.Word, 0, len(call.Args)+len(prefix)-1)
								expanded = append(expanded, call.Args[:index]...)
								for _, field := range prefix {
									expanded = append(expanded, &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: field}}})
								}
								expanded = append(expanded, call.Args[index+1:]...)
								call.Args = expanded
								if resolved != nil {
									resolved[position] = true
								}
								resolvedAbsolute = filepath.IsAbs(prefix[0]) && (index == 0 || filepath.Base(words[0]) == "timeout")
								changed = true
							}
						}
					}
				}
			}
			// Only commands that cannot change the parent shell's state can take part. In a pipe that is its last
			// command (zsh runs it in the shell itself); the others run apart.
			name := words[0]
			unchanged := resolvedAbsolute || stmt != unit[len(unit)-1] || map[string]bool{"echo": true, ":": true, "true": true, "false": true, "ls": true, "git": true, "go": true, "pwd": true, "date": true, "sleep": true, "cat": true, "tee": true, "head": true, "tail": true, "cut": true, "sort": true, "uniq": true, "tr": true, "wc": true, "grep": true, "jq": true, "timeout": true, "env": true, "nice": true, "nohup": true, "setsid": true, "stdbuf": true, "xargs": true, "sudo": true, "ssh": true}[name]
			if name == "cd" && len(unit) == 1 && len(call.Args) <= 2 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 {
				literal := true
				for _, arg := range call.Args {
					_, dynamic := executionWord(arg)
					literal = literal && !dynamic && !wordExpandsToMany(arg)
				}
				unchanged, moved = literal, moved || literal
			}
			if !unchanged {
				unsafe = true
				break
			}
			if len(call.Assigns) != 0 || len(stmt.Redirs) != 0 {
				clear(vars)
			}
		}
	}
	// An opaque command stops later resolutions; expansions already made in earlier commands remain valid because
	// those commands run first. Keeping them also handles a literal prefix in the final command of a sequence.
	if !flat {
		stmts = nil // here-documents are judged only in lists of simple commands
	}

	// A heredoc fed only to a file writer is data, except when a later command
	// could execute it or its unquoted body contains an executable substitution.
	hasHeredoc, safe, goTest := false, true, false
	var bodies []*syntax.Word
	dir := cwd // a literal cd moves where later relative targets are
	for _, stmt := range stmts {
		call := stmt.Cmd.(*syntax.CallExpr)
		if len(call.Args) == 0 {
			if len(stmt.Redirs) != 0 {
				safe = false
			}
			continue
		}
		name, dynamic := executionWord(call.Args[0])
		base := filepath.Base(name)
		if name == "cd" && !dynamic && len(call.Args) == 2 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 {
			to, expanded := executionWord(call.Args[1])
			if to = larkHomePath(to); !filepath.IsAbs(to) && filepath.IsAbs(dir) {
				to = filepath.Join(dir, to)
			}
			if info, err := os.Stat(to); expanded || err != nil || !info.IsDir() || !filepath.IsAbs(to) {
				safe = false
			}
			dir = to
			continue
		}
		// Do not infer safety from an interpreter denylist: build tools and
		// arbitrary programs can execute the file as well. Only these ordinary
		// file/inspection commands and the specified test invocation are inert.
		allowed := map[string]bool{"cat": true, "tee": true, "echo": true, "pwd": true, "ls": true, "head": true, "wc": true}[base]
		if base == "go" && len(call.Args) > 1 && call.Args[1].Lit() == "test" {
			allowed = true
			goTest = true
		}
		if !allowed || name != base || dynamic || dynamicCommandName(name) || len(call.Assigns) != 0 {
			safe = false
		}
		for _, arg := range call.Args[1:] {
			value, expanded := executionWord(arg)
			option, _, _ := strings.Cut(strings.TrimLeft(value, "-"), "=")
			if expanded || strings.HasPrefix(value, "-") && (option == "exec" || option == "toolexec") {
				safe = false
			}
		}
		for _, redir := range stmt.Redirs {
			if redir.Hdoc == nil {
				if (redir.Op != syntax.RdrOut && redir.Op != syntax.AppOut) || redir.Word == nil || !safeHeredocTarget(redir.Word.Lit(), dir) {
					safe = false
				}
				continue
			}
			hasHeredoc = true
			bodies = append(bodies, redir.Hdoc)
			if base != "cat" && base != "tee" {
				safe = false
				continue
			}
			if base == "cat" {
				output := false
				for _, other := range stmt.Redirs {
					if (other.Op == syntax.RdrOut || other.Op == syntax.AppOut) && other.Word != nil && safeHeredocTarget(other.Word.Lit(), dir) {
						output = true
					}
				}
				if !output || len(call.Args) != 1 {
					safe = false
				}
			} else {
				files := 0
				for _, arg := range call.Args[1:] {
					value := arg.Lit()
					if value == "-a" || value == "--append" {
						continue
					}
					if value == "" || strings.HasPrefix(value, "-") || !safeHeredocTarget(value, dir) {
						safe = false
					}
					files++
				}
				if files == 0 {
					safe = false
				}
			}
			syntax.Walk(redir.Hdoc, func(n syntax.Node) bool {
				switch n.(type) {
				case *syntax.CmdSubst, *syntax.ProcSubst:
					safe = false
				}
				return true
			})
		}
	}
	if goTest {
		for _, body := range bodies {
			if !inertGoDocument(body) {
				safe = false
			}
		}
	}
	if hasHeredoc && safe {
		for _, stmt := range stmts {
			redirs := stmt.Redirs[:0]
			for _, r := range stmt.Redirs {
				if r.Hdoc == nil {
					redirs = append(redirs, r)
				}
			}
			stmt.Redirs = redirs
		}
		changed = true
	}
	if !changed {
		return cmd // with the inline rewrites, if any
	}
	var out bytes.Buffer
	if err := syntax.NewPrinter().Print(&out, tree); err != nil {
		return cmd
	}
	return out.String()
}

// Special files, symlinks and virtual filesystems can forward the heredoc to
// a running process. An unknown target is not proof of an ordinary file.
func safeHeredocTarget(name, cwd string) bool {
	if name = larkHomePath(name); !safeLiteralCommand(name) {
		return false
	}
	if !filepath.IsAbs(name) {
		if !filepath.IsAbs(cwd) {
			return false
		}
		name = filepath.Join(cwd, name)
	}
	name = filepath.Clean(name)
	parent, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return false
	}
	for _, path := range []string{name, filepath.Join(parent, filepath.Base(name))} {
		for _, prefix := range []string{"/dev", "/proc", "/sys"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return false
			}
		}
	}
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return true
	}
	return err == nil && info.Mode().IsRegular()
}

// go test can execute generated tests and package initializers. Only a document
// containing comments and an optional package clause is inert for this purpose.
func inertGoDocument(body *syntax.Word) bool {
	value, dynamic := executionWord(body)
	if dynamic {
		return false
	}
	content := []byte(value)
	file := token.NewFileSet().AddFile("", -1, len(content))
	var scan scanner.Scanner
	valid := true
	scan.Init(file, content, func(token.Position, string) { valid = false }, scanner.ScanComments)
	packageName := false
	seenPackage := false
	for {
		_, tok, _ := scan.Scan()
		switch tok {
		case token.EOF:
			return valid && !packageName
		case token.COMMENT, token.SEMICOLON:
		case token.PACKAGE:
			if seenPackage || packageName {
				return false
			}
			seenPackage, packageName = true, true
		case token.IDENT:
			if !packageName {
				return false
			}
			packageName = false
		default:
			return false
		}
	}
}

// literalCommandPrefix is the unquoted field splitting of a statically assigned command prefix. Quotes and shell
// metacharacters inside a variable are data at runtime, not syntax to reinterpret; rejecting them here keeps that
// distinction and prevents a value from smuggling in another command, glob or expansion.
func literalCommandPrefix(value string) ([]string, bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil, false
	}
	for _, field := range fields {
		if !safeLiteralCommand(field) {
			return nil, false
		}
	}
	return fields, true
}

func safeLiteralCommand(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._+-", c) {
			continue
		}
		return false
	}
	return true
}

// larkHomePath is a path with a leading ~ (the shell's own home) expanded.
func larkHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// larkPipeUnits lists the complete semicolon-separated prefix of simple commands, each as one command or a pipe's
// commands in order. It stops before &&, ||, loops, subshells and background jobs and returns the prefix with false.
func larkPipeUnits(in []*syntax.Stmt) ([][]*syntax.Stmt, bool) {
	var out [][]*syntax.Stmt
	for _, s := range in {
		var unit []*syntax.Stmt
		var add func(*syntax.Stmt) bool
		add = func(s *syntax.Stmt) bool {
			if s.Background || s.Negated || s.Coprocess {
				return false
			}
			switch c := s.Cmd.(type) {
			case *syntax.CallExpr:
				unit = append(unit, s)
				return true
			case *syntax.BinaryCmd:
				return (c.Op == syntax.Pipe || c.Op == syntax.PipeAll) && len(s.Redirs) == 0 && add(c.X) && add(c.Y)
			}
			return false
		}
		if !add(s) {
			return out, false
		}
		out = append(out, unit)
	}
	return out, true
}

// larkFlatStmts lists the simple commands of a `;`, `&&` and `||` list, in order; false for anything else (pipes,
// loops, subshells, background jobs).
func larkFlatStmts(in []*syntax.Stmt) ([]*syntax.Stmt, bool) {
	var out []*syntax.Stmt
	var add func(*syntax.Stmt) bool
	add = func(s *syntax.Stmt) bool {
		if s.Background || s.Negated || s.Coprocess {
			return false
		}
		switch c := s.Cmd.(type) {
		case *syntax.CallExpr:
			out = append(out, s)
			return true
		case *syntax.BinaryCmd:
			return (c.Op == syntax.AndStmt || c.Op == syntax.OrStmt) && len(s.Redirs) == 0 && add(c.X) && add(c.Y)
		}
		return false
	}
	for _, s := range in {
		if !add(s) {
			return nil, false
		}
	}
	return out, true
}
