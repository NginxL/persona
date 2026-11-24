package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// On Linux persona-link runs as a systemd user service (docs/operations/link.md "Linux (headless)"): `install` writes the unit,
// enables and (re)starts it and waits for the link; `uninstall` takes it away. The service runs in the user's own
// systemd instance, so it needs no root, and runs with nobody logged in once linger is on.

const unitName = "persona-link.service"

// noUserInstance starts the reason when systemd is there but the user's own instance cannot be reached.
func noUserInstance() string { return i18n.T(i18n.Terminal(), "link-service.systemd_user_unavailable") }

// unitPath is where systemd looks for the user's own units.
func (e *env) unitPath() string {
	base := e.getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(e.home, ".config")
	}
	dir := filepath.Join(base, "systemd", "user")
	return existingPath(filepath.Join(dir, unitName), filepath.Join(dir, legacyLinkUnit))
}

// unitFile is the service. No network-online.target: a user instance has none to wait for, and persona-link retries
// by itself until the hub answers. Status is persisted in link.status.json, while the readable log goes to its own
// file, which `persona-link logs` reads whether or not the journal keeps user logs.
func unitFile(bin, logPath string) string {
	return `# Written by persona-link install; persona-link uninstall removes it (docs/operations/link.md).
[Unit]
Description=Persona link: keeps this computer reachable from the Persona hub

[Service]
ExecStart=` + systemdQuote(bin) + ` run --log ` + systemdQuote(logPath) + `
` + stateEnvironment(filepath.Dir(logPath)) + `Restart=always
RestartSec=5
StandardOutput=null

[Install]
WantedBy=default.target
`
}

// systemdQuote makes s one word of a unit's command line: quoted, with the specifier and variable characters escaped.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

// Environment= expands systemd specifiers, but never ExecStart's dollar variables.
func systemdEnvironmentQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

func stateEnvironment(root string) string {
	return "Environment=" + systemdEnvironmentQuote("PERSONA_HOME="+root) + "\n"
}

func (e *env) systemctl(args ...string) (string, error) {
	return e.command("systemctl", append([]string{"--user"}, args...)...)
}

// systemdMissing says why there is no systemd user instance to run persona-link in; "" when there is one.
func (e *env) systemdMissing() string {
	if e.goos != "linux" {
		return i18n.T(i18n.Default, "link-cli.systemd_not_linux")
	}
	if _, err := e.lookPath("systemctl"); err != nil {
		return i18n.T(i18n.Default, "link-cli.systemctl_missing")
	}
	if !e.exists("/run/systemd/system") {
		return i18n.T(i18n.Default, "link-cli.systemd_not_booted")
	}
	if out, err := e.systemctl("show-environment"); err != nil {
		why := firstLine(out)
		if why == "" {
			why = err.Error()
		}
		return terminalText("link-cli.systemd_unavailable_detail", i18n.Args{"error": why})
	}
	return ""
}

// systemdReason keeps the canonical reason in errors and JSON, translating only a person's view of it.
func systemdReason(reason string) link.StatusText { return link.StatusText(reason) }

// lingering: the user's systemd instance runs from boot, with nobody logged in (loginctl enable-linger).
func (e *env) lingering() bool {
	return e.user != "" && e.exists("/var/lib/systemd/linger/"+e.user)
}

func install(e *env, args []string) error {
	var pairLink, machine, listen, personad string
	var addrs []string
	var flags routeFlags
	nodeBinAny := false // --node-bin: either program, asked which (nodeInstall)
	pairing, passive, withNode := false, false, true
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--pair": // the link, or - or nothing for stdin
			pairing, pairLink = true, "-"
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				pairLink = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--pair="):
			pairing, pairLink = true, strings.TrimPrefix(a, "--pair=")
		case a == "--no-node":
			withNode = false
		default:
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
			var v string
			var next int
			var ok bool
			var err error
			for _, name := range []string{"--machine", "--addr", "--personad", "--node-bin"} {
				if v, next, ok, err = flagValue(args, i, name); ok || err != nil {
					switch {
					case err != nil:
					case name == "--machine":
						machine = v
					case name == "--addr":
						addrs = append(addrs, v)
					default:
						personad, nodeBinAny = v, name == "--node-bin"
					}
					break
				}
			}
			switch {
			case err != nil:
				return err
			case !ok:
				return errUsage
			}
			i = next
		}
	}
	if err := e.installationConflict(false); err != nil {
		return err
	}
	if withNode {
		if err := e.installationConflict(true); err != nil {
			return err
		}
	}
	if pairing && (passive || len(addrs) > 0) {
		return link.NewTextError("link-cli.install_pair_listen_conflict", nil)
	}
	if flags.any() && (passive || len(addrs) > 0) {
		return link.NewTextError("link-cli.install_listen_ssh", nil)
	}
	if flags.proxy.any() && (passive || len(addrs) > 0) {
		return link.NewTextError("link-cli.listen_proxy", nil)
	}
	if err := flags.check(); err != nil {
		return err
	}
	switch e.goos {
	case "linux", "darwin", "windows":
	default:
		return link.NewTextError("link-cli.install_unsupported", nil, i18n.Args{"goos": e.goos})
	}
	if e.goos == "windows" && nodeBinAny && personad != "" && e.programOf(personad) != programPersonad {
		return link.NewTextError("link-cli.windows_personad_required", nil, i18n.Args{"path": personad})
	}
	if pairing {
		c, err := pairWithSSH(e, pairLink, machine, flags)
		if err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, terminalText("link-cli.install_pair_success", i18n.Args{"machine": c.Machine, "path": e.configPath()}))
	} else {
		var ssh *link.SSH
		if flags.any() {
			if strings.TrimSpace(flags.server) == "" {
				return link.NewTextError("link-cli.ssh_flags_require_server", nil)
			}
			always := flags.always
			if !flags.alwaysSet {
				if c, err := link.Read(e.configPath()); err == nil {
					always = link.AllLoopback(c.Endpoints)
				}
			}
			ssh = &link.SSH{Server: flags.server, Target: flags.target, Always: always}
		}
		if err := flags.saveProxy(e, ssh); err != nil {
			return err
		}
		if ssh != nil {
			if _, err := link.SetSSH(e.configPath(), ssh); err != nil {
				return err
			}
		}
	}
	var invite *link.PairInvite
	if passive || len(addrs) > 0 {
		c, err := link.SetMode(e.configPath(), link.ModePassive, listen)
		if err != nil {
			return err
		}
		if c.Passive.Paired() && len(addrs) == 0 {
			fmt.Fprintln(e.stdout, terminalText("link-cli.install_passive_existing", i18n.Args{"hub": passiveHubText(c.Passive), "listen": c.Passive.ListenAddr()}))
		} else if invite, err = link.PairListen(e.configPath(), "", addrs, e.now()); errors.Is(err, link.ErrNoAddress) {
			// The service still goes in; the link waits for an address the hub can use.
			fmt.Fprintln(e.stdout, terminalText("link-cli.install_address_later", i18n.Args{"error": link.ErrorText(i18n.Terminal(), err)}))
		} else if err != nil {
			return err
		}
	}
	if e.goos == "windows" {
		return installTask(e, withNode, personad, invite) // winservice.go
	}
	if e.goos == "darwin" {
		return installMac(e, withNode, personad, nodeBinAny, invite) // service_mac.go
	}
	bin, copied, err := e.installBinary()
	if err != nil {
		return link.NewTextError("link-cli.binary_install_failed", err, i18n.Args{"path": e.binPath()})
	}
	if copied {
		fmt.Fprintln(e.stdout, terminalText("link-cli.binary_installed", i18n.Args{"path": bin}))
	}
	if why := e.systemdMissing(); why != "" {
		noSystemd(e, systemdReason(why).Text(i18n.Terminal()), bin)
		if withNode {
			fmt.Fprintln(e.stdout, terminalText("link-cli.node_nohup", i18n.Args{"binary": shellQuote(e.nodeBin()), "log": shellQuote(e.nodeLog())}))
			// nodeupdate.Options: what restarts the node on exit has to say so, or the hub cannot update it.
			fmt.Fprintln(e.stdout, terminalText("link-cli.node_supervisor_hint"))
		}
		printInvite(e, invite)
		return exitCode(1)
	}
	if withNode {
		if err := lockedNodeCommand(e, true, func() error { return nodeInstall(e, personad, nodeBinAny) }); err != nil {
			fmt.Fprintln(e.stdout, terminalText("link-cli.node_install_failed_output", i18n.Args{"error": link.ErrorText(i18n.Terminal(), err)}))
		}
	}
	var oldPID int
	if st, err := link.ReadStatus(e.configPath()); err == nil && link.Running(e.configPath()) {
		oldPID = st.PID
	}
	unit := e.unitPath()
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unit, []byte(unitFile(bin, e.logPath())), 0o644); err != nil {
		return err
	}
	started := e.now()
	for _, step := range [][]string{{"daemon-reload"}, {"enable", e.linkUnitName()}, {"restart", e.linkUnitName()}} {
		if out, err := e.systemctl(step...); err != nil {
			return link.NewTextError("link-cli.systemctl_step_failed", nil, i18n.Args{"step": strings.Join(step, " "), "error": orErr(out, err)})
		}
	}
	fmt.Fprintln(e.stdout, terminalText("link-cli.systemd_installed", i18n.Args{"unit": e.linkUnitName(), "path": unit}))
	if e.uid == 0 {
		fmt.Fprintln(e.stdout, terminalText("link-cli.root_install_warning"))
	}
	if !e.lingering() {
		fmt.Fprintln(e.stdout, terminalText("link-cli.linger_install_warning", i18n.Args{"user": e.userOr("$USER")}))
	}
	return waitStarted(e, started, oldPID, invite)
}

// waitStarted is install's end once the service is (re)started: wait for the link (passive mode: for it to listen,
// then print the pairing link for the hub), or say how to pair when it is not paired yet.
func waitStarted(e *env, started time.Time, oldPID int, invite *link.PairInvite) error {
	if c, _ := link.Read(e.configPath()); c.CurrentMode() == link.ModePassive {
		err := waitConnected(e, started, oldPID)
		printInvite(e, invite)
		fmt.Fprintln(e.stdout, firewallHint(e.goos, c.Passive.ListenAddr()))
		return err
	}
	if _, err := link.Load(e.configPath()); err != nil {
		fmt.Fprintln(e.stdout, terminalText("link-cli.install_unpaired"))
		return nil
	}
	return waitConnected(e, started, oldPID)
}

// printInvite shows the pairing link of passive mode, for the hub's admin page.
func printInvite(e *env, inv *link.PairInvite) {
	if inv == nil {
		return
	}
	fmt.Fprintln(e.stdout, terminalText("link-cli.install_invite", i18n.Args{"duration": duration(link.PairCodeLife), "addresses": terminalJoin(inv.Addrs), "link": inv.Link}))
	if inv.LegacyLink != "" {
		fmt.Fprintln(e.stdout, inv.LegacyLink)
	}
}

func (e *env) userOr(fallback string) string {
	if e.user != "" {
		return e.user
	}
	return fallback
}

func orErr(out string, err error) string {
	if s := firstLine(out); s != "" {
		return s
	}
	return err.Error()
}

// noSystemd tells what to do instead where there is no systemd user instance: a container, some NAS systems.
func noSystemd(e *env, why, bin string) {
	runLine := shellQuote(bin) + " run --log " + shellQuote(e.logPath())
	fmt.Fprintln(e.stdout, terminalText("link-cli.no_systemd", i18n.Args{"error": why, "run": runLine}))
	if strings.HasPrefix(why, noUserInstance()) {
		fmt.Fprintln(e.stdout, terminalText("link-cli.systemd_login_hint", i18n.Args{"user": e.userOr("$USER")}))
	}
}

// shellQuote quotes a path for sh when it needs it.
func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// waitConnected waits up to e.wait for the service just (re)started to report the link up and the node checked, and
// says how it went. The old process's last words (oldPID, "stopped") do not count.
func waitConnected(e *env, since time.Time, oldPID int) error {
	c, _ := link.Read(e.configPath())
	passive := c.CurrentMode() == link.ModePassive
	if passive {
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_listen"))
	} else {
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_connect"))
	}
	deadline := since.Add(e.wait)
	var st *link.SavedStatus
	for {
		if s, err := link.ReadStatus(e.configPath()); err == nil && !s.Updated.Before(since) && s.PID != oldPID && s.State != "stopped" &&
			(s.Mode == link.ModePassive) == passive {
			st = s
		}
		if st != nil && passive && st.State != "" {
			break // listening, waiting for a pairing, a stream already open, or unable to listen: each says enough
		}
		if st != nil && (st.State == "refused" || st.State == "unpaired" || (st.State == "connected" && st.Node != "unknown")) {
			break
		}
		if !e.now().Before(deadline) {
			break
		}
		e.sleep(200 * time.Millisecond)
	}
	switch {
	case st == nil:
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_no_report", i18n.Args{"duration": e.wait.String()}))
		return exitCode(1)
	case passive && st.State == "waiting":
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_not_listening", i18n.Args{"error": link.StatusErrorText(i18n.Terminal(), st.Error)}))
		return exitCode(1)
	case passive:
		line := terminalText("link-cli.wait_listening", i18n.Args{"listen": st.Listen})
		switch st.State {
		case "connected":
			line += terminalText("link-cli.wait_passive_connected", i18n.Args{"address": st.Address})
		case "listening":
			line += terminalText("link-cli.wait_passive_listening", i18n.Args{"hub": hubName(st.HubName)})
		default:
			line += terminalText("link-cli.wait_passive_pairing")
		}
		fmt.Fprintln(e.stdout, line)
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_status_logs"))
		return nil
	case st.State == "connected":
		line := terminalText("link-cli.wait_connected", i18n.Args{"hub": hubText(st.HubName, st.MeshHubs, "", true), "address": addressText(st.Kind, st.Address)})
		switch st.Node {
		case "online":
			line += terminalText("link-cli.wait_node_online")
		case "offline":
			line += terminalText("link-cli.wait_node_offline", i18n.Args{"error": link.StatusErrorText(i18n.Terminal(), st.NodeError), "hint": nodeHint(st.NodeError)})
		default:
			line += terminalText("link-cli.wait_node_connecting")
		}
		fmt.Fprintln(e.stdout, line)
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_status_logs"))
		return nil
	case st.State == "unpaired":
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_unpaired"))
		return nil
	default:
		fmt.Fprintln(e.stdout, terminalText("link-cli.wait_not_connected", i18n.Args{"state": stateText(&st.Status, e.now())}))
		for _, part := range errorParts(st.Error) {
			fmt.Fprintln(e.stdout, "  · "+link.StatusErrorText(i18n.Terminal(), part))
		}
		return exitCode(1)
	}
}

func uninstall(e *env, args []string) error {
	purge := false
	for _, a := range args {
		if a != "--purge" {
			return errUsage
		}
		purge = true
	}
	unit := e.unitPath()
	switch {
	case e.goos == "windows":
		uninstallTask(e) // winservice.go
	case e.goos == "darwin":
		if err := uninstallMac(e); err != nil { // service_mac.go
			return err
		}
	case e.goos != "linux":
	case fileExists(unit):
		if why := e.systemdMissing(); why == "" {
			if out, err := e.systemctl("disable", "--now", e.linkUnitName()); err != nil {
				fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_stop_error", i18n.Args{"error": orErr(out, err)}))
			}
		} else {
			fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_stop_unavailable", i18n.Args{"error": systemdReason(why).Text(i18n.Terminal())}))
		}
		if err := os.Remove(unit); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, _ = e.systemctl("daemon-reload")
		fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_systemd_removed", i18n.Args{"unit": e.linkUnitName()}))
	default:
		fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_systemd_missing", i18n.Args{"path": unit}))
	}
	if !purge {
		fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_credentials_kept", i18n.Args{"path": e.configPath()}))
		return nil
	}
	if link.Running(e.configPath()) {
		return link.NewTextError("link-cli.running_before_purge", nil)
	}
	cfg := e.configPath()
	var gone []string
	files := []string{cfg, cfg + ".lock", cfg + ".run", cfg + ".tmp", link.StatusPath(cfg), link.StatusPath(cfg) + ".tmp",
		link.RouteHintPath(cfg), link.RouteHintPath(cfg) + ".tmp", e.logPath(), e.logPath() + ".1", e.binPath()}
	if e.goos == "windows" { // the supervisor's (winservice.go); a running persona-link.exe cannot remove itself
		files = append(files, e.serviceLog(), e.serviceLog()+".1", filepath.Join(e.personaDir(), "link-service.run"))
	}
	for _, p := range files {
		if err := os.Remove(p); err == nil {
			gone = append(gone, p)
		}
	}
	if dir := link.TLSDir(cfg); fileExists(dir) && os.RemoveAll(dir) == nil {
		gone = append(gone, dir)
	}
	if len(gone) == 0 {
		fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_no_files"))
	} else {
		fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_files_removed", i18n.Args{"paths": terminalJoin(gone)}))
	}
	nodeKey := "link-cli.uninstall_node_kept"
	if e.goos == "windows" {
		nodeKey = "link-cli.uninstall_windows_node_stopped"
	}
	fmt.Fprintln(e.stdout, terminalText("link-cli.uninstall_revoke", i18n.Args{"node": terminalText(nodeKey, i18n.Args{"path": e.nodeBin()})}))
	return nil
}

// hubName is " name " in full-width corner brackets for a sentence, or nothing.
func hubName(name string) string {
	if name == "" {
		return ""
	}
	return terminalText("link-cli.hub_name_suffix", i18n.Args{"name": name})
}
