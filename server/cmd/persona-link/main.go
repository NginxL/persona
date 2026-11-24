// Command persona-link (persona-link before #1159; cmd/persona-link is a link to this directory) keeps this computer
// reachable from the Persona hub when the hub cannot reach it — a laptop on
// whatever network it is on, a server or NAS behind NAT (docs/operations/link.md). It finds the hub, says which machine it is and
// hands each stream the hub opens on the link to this computer's resident node. Nothing else: everything a node does
// stays in personad. `persona-link install` makes it a systemd user service on Linux, a launchd agent on Mac or a
// scheduled task on Windows; the Electron desktop's optional setup window does not have to run alongside it.
package main

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
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/netaddr"
	"github.com/nwverse/persona/server/internal/update"
)

// What the publisher writes in (-X), as personad's and persona-node's: the connector's build must also be verifiable
// from the compressed release, including on another platform (update.VerifyRelease).
var (
	version = "dev"
	channel = "dev"
	build   = ""
)

func usage() string { return i18n.T(i18n.Terminal(), "link-cli.usage") }

func terminalText(key string, args ...i18n.Args) string {
	return i18n.T(i18n.Terminal(), key, args...)
}

func terminalJoin(items []string) string {
	return strings.Join(items, terminalText("link-cli.list_separator"))
}

// exitCode ends the program with a code and nothing printed: what it had to say is already out.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func main() {
	self := commandName(os.Args[0], os.Getenv)
	stdout, stderr := io.Writer(os.Stdout), io.Writer(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix(self + ": ")
	if len(os.Args) < 2 {
		fmt.Fprintln(stderr, usage())
		os.Exit(2)
	}
	if sub := os.Args[1]; helpTopics[sub] && wantsHelp(os.Args[2:]) {
		// `persona-link <sub> -h`: that subcommand's lines of the usage, and nothing failed
		fmt.Fprintln(stdout, subcommandHelp(sub))
		return
	}
	e := newEnv()
	e.stdout, e.stderr = stdout, stderr
	var err error
	switch args := os.Args[2:]; os.Args[1] {
	case "pair":
		err = pairCmd(e, args)
	case "mode":
		err = modeCmd(e, args)
	case "node":
		err = nodeCmd(e, args)
	case "discover":
		err = discover(e, args)
	case "install":
		err = install(e, args)
	case "uninstall":
		err = uninstall(e, args)
	case "status":
		err = status(e, args)
	case "reconnect":
		err = reconnectNow(e, args)
	case "logs", "log":
		err = logs(e, args)
	case "run":
		err = run(args)
	case "supervise": // what the Windows scheduled task runs (supervise_windows.go); not for people
		err = supervise(e, args)
	case "ssh":
		err = sshCmd(e, args)
	case "proxy":
		err = proxyCmd(e, args)
	case "address":
		err = address(args)
	case "name":
		err = nameCmd(e, args)
	case "plugin":
		err = pluginCmd(e, args)
	case "version", "--version", "-v":
		fmt.Println(version)
		if slices.Contains(args, "-v") {
			b := update.Make(version, channel, build, "")
			fmt.Printf("channel %s, build %d\n", b.Channel, b.Number)
		}
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, usage())
	default:
		fmt.Fprintln(stderr, usage())
		os.Exit(2)
	}
	var code exitCode
	switch {
	case errors.As(err, &code):
		os.Exit(int(code))
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, usage())
		os.Exit(2)
	case err != nil:
		language := i18n.Terminal()
		if os.Args[1] == "run" {
			// A foreground run's early error is for the terminal. A service child's stderr is a maintainer log.
			if fi, statErr := os.Stderr.Stat(); statErr == nil {
				language = runErrorLanguage(fi.Mode())
			}
		}
		fmt.Fprintln(stderr, explainLanguage(language, err))
		os.Exit(1)
	}
}

// runErrorLanguage keeps a redirected run's early stderr in the maintainer log's English.
func runErrorLanguage(mode os.FileMode) i18n.Lang {
	if mode&os.ModeCharDevice != 0 {
		return i18n.Terminal()
	}
	return i18n.En
}

var errUsage = errors.New("usage")

// helpTopics are the subcommands `<sub> -h` answers for (supervise is not for people: its -h shows the whole usage).
var helpTopics = map[string]bool{"pair": true, "mode": true, "node": true, "discover": true, "install": true,
	"uninstall": true, "status": true, "reconnect": true, "logs": true, "log": true, "run": true, "supervise": true, "ssh": true,
	"address": true, "name": true, "plugin": true, "proxy": true}

// wantsHelp says whether args ask for help (-h, -help or --help anywhere).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

// subcommandHelp is the usage's entries for sub with their continuation lines, or the whole usage when it has none.
func subcommandHelp(sub string) string {
	if sub == "log" {
		sub = "logs"
	}
	var out []string
	in := false
	for _, line := range strings.Split(usage(), "\n") {
		switch rest, isEntry := strings.CutPrefix(line, "  persona-link "); {
		case isEntry:
			name, _, _ := strings.Cut(rest, " ")
			in = name == sub
		case strings.TrimSpace(line) == "" || !strings.HasPrefix(line, " "):
			in = false
		}
		if in {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return usage()
	}
	return i18n.T(i18n.Terminal(), "link-cli.usage_header") + "\n" + strings.Join(out, "\n")
}

// flagValue reads `--name value` or `--name=value` at args[i]; ok says it was that flag, next is where to go on.
func flagValue(args []string, i int, name string) (value string, next int, ok bool, err error) {
	a := compatibleFlag(args[i], name)
	if v, found := strings.CutPrefix(a, name+"="); found {
		return v, i, true, nil
	}
	if a != name {
		return "", i, false, nil
	}
	if i+1 >= len(args) {
		return "", i, true, link.NewTextError("link-cli.flag_value_required", nil, i18n.Args{"flag": name})
	}
	return args[i+1], i + 1, true, nil
}

// optionalValue reads `--name`, `--name value` or `--name=value` at args[i], the value being optional: the next
// argument is taken unless it is another flag.
func optionalValue(args []string, i int, name string) (value string, next int, ok bool) {
	a := compatibleFlag(args[i], name)
	if v, found := strings.CutPrefix(a, name+"="); found {
		return v, i, true
	}
	if a != name {
		return "", i, false
	}
	if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
		return args[i+1], i + 1, true
	}
	return "", i, true
}

func pairCmd(e *env, args []string) error {
	var machine, raw, listen string
	var addrs []string
	var flags routeFlags
	seen, passive := false, false
	for i := 0; i < len(args); i++ {
		if v, next, ok := optionalValue(args, i, "--listen"); ok {
			passive, listen, i = true, v, next
			continue
		}
		if next, ok, err := flags.consume(args, i); ok || err != nil {
			if err != nil {
				return err
			}
			i = next
			continue
		}
		v, next, ok, err := flagValue(args, i, "--machine")
		if !ok && err == nil {
			if v, next, ok, err = flagValue(args, i, "--addr"); ok && err == nil {
				addrs, i = append(addrs, v), next
				continue
			}
		}
		switch {
		case err != nil:
			return err
		case ok:
			machine, i = v, next
		case !seen:
			raw, seen = args[i], true
		default:
			return errUsage
		}
	}
	if passive || len(addrs) > 0 {
		if flags.any() {
			return link.NewTextError("link-cli.pair_listen_ssh", nil)
		}
		if flags.proxy.any() {
			return link.NewTextError("link-cli.listen_proxy", nil)
		}
		if seen || machine != "" {
			return link.NewTextError("link-cli.pair_listen_machine", nil)
		}
		return pairListen(e, listen, addrs)
	}
	if err := flags.check(); err != nil {
		return err
	}
	c, err := pairWithSSH(e, raw, machine, flags)
	if err != nil {
		return err
	}
	pokeRunning(e.configPath())
	fmt.Fprintln(e.stdout, i18n.T(i18n.Terminal(), "link-cli.pair_success", i18n.Args{"machine": cmp.Or(c.MachineName, c.Machine), "path": e.configPath()}))
	switch {
	case link.Running(e.configPath()):
		fmt.Fprintln(e.stdout, i18n.T(i18n.Terminal(), "link-cli.pair_running"))
	case e.goos == "linux":
		fmt.Fprintln(e.stdout, i18n.T(i18n.Terminal(), "link-cli.pair_next_linux"))
	}
	return nil
}

// pair redeems a pairing link: raw, or read from stdin when raw is "" or "-" (with a prompt on a terminal).
func pair(e *env, raw, machine string) (*link.Config, error) {
	return pairWithSSH(e, raw, machine, routeFlags{})
}

func pairWithSSH(e *env, raw, machine string, flags routeFlags) (*link.Config, error) {
	if raw == "" || raw == "-" {
		in := bufio.NewReader(io.LimitReader(e.stdin, 16<<10))
		if e.stdinIsTerminal() {
			// One pasted line, then Enter: not waiting for the end of input.
			fmt.Fprint(e.stderr, i18n.T(i18n.Terminal(), "link-cli.pair_prompt"))
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return nil, err
			}
			raw = line
		} else {
			b, err := io.ReadAll(in)
			if err != nil {
				return nil, err
			}
			raw = string(b)
		}
		raw = strings.TrimSpace(raw)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, link.NewTextError("link-cli.pair_link_required", nil)
	}
	sshCfg, err := flags.build(raw)
	if err != nil {
		return nil, err
	}
	// Saved before pairing, so the pairing goes through the proxy too.
	if err := flags.saveProxy(e, sshCfg); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return link.PairWithSSH(ctx, raw, strings.TrimSpace(machine), "", e.configPath(), sshCfg, e.sshBin)
}

func address(args []string) error {
	if len(args) != 2 || (args[0] != "add" && args[0] != "remove") {
		return errUsage
	}
	if args[0] == "remove" {
		return link.RemoveAddress(link.DefaultPath(), args[1])
	}
	e, err := link.AddAddress(link.DefaultPath(), args[1])
	if err == nil {
		fmt.Println(i18n.T(i18n.Terminal(), "link-cli.address_added", i18n.Args{"address": e.URL}))
	}
	return err
}

func run(args []string) error {
	var parent bool
	var logPath string
	for i := 0; i < len(args); i++ {
		if args[i] == "--parent" {
			parent = true
			continue
		}
		v, next, ok, err := flagValue(args, i, "--log")
		switch {
		case err != nil:
			return err
		case !ok:
			return errUsage
		}
		logPath, i = v, next
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var app *fromApp
	if parent {
		app = &fromApp{presence: link.NewPresenceBox(), confirms: link.NewConfirms()}
		go func() {
			readParent(os.Stdin, app)
			stop() // the optional parent process went away, however it did
		}()
	}
	return keepLink(ctx, link.DefaultPath(), logPath, os.Stdout, app)
}

// fromApp is an optional parent process's presence and notification confirmations (internal/link presence.go).
// The independent Link service does not send either; the Electron desktop handles its own presence and notifications.
type fromApp struct {
	presence *link.PresenceBox
	confirms *link.Confirms
}

// readParent reads a compatible parent process's lines on stdin until it closes, into app; the rest is not for this build. A line
// too long to scan ends the scan, not the reading: only stdin's end says the app went away.
func readParent(r io.Reader, app *fromApp) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if p, ok := link.ParsePresenceLine(sc.Bytes()); ok {
			app.presence.Set(p, time.Now())
		} else if c, ok := link.ParseRangLine(sc.Bytes()); ok {
			app.confirms.Push(c)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// keepLink is `persona-link run`: the link for the link.json at path until ctx ends. Every status goes to stdout as a
// JSON line and to link.status.json for `persona-link status`; the log to logPath when there is one. app is what the
// parent process says (nil without it).
func keepLink(ctx context.Context, path, logPath string, stdout io.Writer, app *fromApp) error {
	release, err := link.LockRun(path)
	if err != nil {
		return err
	}
	defer release()
	if logPath != "" {
		lf, err := openRotating(logPath, logMax)
		if err != nil {
			return err
		}
		defer lf.Close()
		log.SetOutput(lf)
		defer log.SetOutput(os.Stderr)
	}
	// A node switch whose persona-link died half way (nodeswitch.go) is undone now and whenever one turns up: by the
	// one persona-link run that holds the lock, saying what it did in the log (stdout is reserved for JSON status lines).
	go watchSwitch(ctx)
	check, redial := make(chan struct{}, 1), make(chan struct{}, 1)
	poke := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	notifySignals(ctx, path, func() { poke(check) }, func() { poke(redial) })
	// Address changes: netlink on Linux; elsewhere the platform's address watcher or a slow poll.
	go func() {
		first := true
		for range netaddr.Watch(ctx) {
			if !first {
				poke(check)
			}
			first = false
		}
	}()
	var mu sync.Mutex
	out := json.NewEncoder(stdout)
	var last link.Status
	ended := false // a stream ending late must not write over "stopped"
	save := func(s link.Status) {
		if err := link.WriteStatus(path, link.SavedStatus{Status: s, PID: os.Getpid(), Version: version, Updated: time.Now()}); err != nil {
			log.Printf("status: %v", err)
		}
	}
	log.Printf("persona-link %s started", version)
	o := link.Options{Path: path, Target: link.AttachSocket(), Version: version,
		Status: func(s link.Status) {
			mu.Lock()
			defer mu.Unlock()
			if ended {
				return
			}
			_ = out.Encode(s)
			last = s
			save(s)
		},
		Check: check, Redial: redial, Logf: log.Printf}
	if app != nil {
		o.Presence, o.Confirms = app.presence, app.confirms
	}
	err = link.Run(ctx, o)
	mu.Lock()
	defer mu.Unlock()
	ended = true
	save(link.Status{State: "stopped", Mode: last.Mode, Machine: last.Machine, HubName: last.HubName, Endpoints: last.Endpoints,
		SSH: last.SSH, Listen: last.Listen, LastSeen: last.LastSeen, From: last.From})
	log.Printf("persona-link stopped")
	return err
}
