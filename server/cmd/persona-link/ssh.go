package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// routeFlags holds the optional route flags on `pair` and `install --pair`: the SSH tunnel's and the outbound proxy's.
type routeFlags struct {
	server    string
	target    string
	always    bool
	alwaysSet bool
	proxy     proxyFlag
}

func (f *routeFlags) consume(args []string, i int) (next int, ok bool, err error) {
	if next, ok, err := f.proxy.consume(args, i); ok || err != nil {
		return next, ok, err
	}
	switch args[i] {
	case "--ssh-always":
		f.always, f.alwaysSet = true, true
		return i, true, nil
	case "--no-ssh-always":
		f.always, f.alwaysSet = false, true
		return i, true, nil
	}
	if v, n, ok, err := flagValue(args, i, "--ssh"); ok || err != nil {
		f.server = v
		return n, ok, err
	}
	if v, n, ok, err := flagValue(args, i, "--ssh-target"); ok || err != nil {
		f.target = v
		return n, ok, err
	}
	return i, false, nil
}

// any: an SSH flag is there.
func (f routeFlags) any() bool {
	return f.server != "" || f.target != "" || f.alwaysSet
}

// check refuses, before anything is saved, a --proxy-always the command line alone shows cannot be turned on: with
// a --proxy that is no URL (as persona-link proxy refuses --off and --env with --always), or with --ssh-always (only
// one route can be the only one). saveProxy checks the rest against link.json.
func (f routeFlags) check() error {
	if !f.proxy.always || !f.proxy.alwaysSet {
		return nil
	}
	if f.proxy.set && noProxyURL(f.proxy.value) {
		return errUsage
	}
	if f.always && f.alwaysSet {
		return link.ErrSSHAlwaysProxyAlways
	}
	return nil
}

// saveProxy saves the proxy flags once ssh, the tunnel this command line saves (nil: none), is known with its
// default Always. As persona-link proxy, a refusal changes nothing: proxy-only mode and Always use SSH are checked
// against link.json before anything is saved. --proxy-always is refused without a proxy URL given or saved, or while
// Always use SSH stays on (saved and left alone by this line, or this line's default for loopback addresses); a
// tunnel that is always used is refused while a saved proxy-only mode stays on. This line's tunnel replaces the saved
// one, so when it has no Always a saved Always use SSH goes off first, before proxy-only mode goes on.
func (f routeFlags) saveProxy(e *env, ssh *link.SSH) error {
	if !f.proxy.any() {
		return nil
	}
	saved, err := link.Read(e.configPath())
	if errors.Is(err, os.ErrNotExist) {
		saved, err = &link.Config{}, nil
	}
	if err != nil {
		return err
	}
	on := f.proxy.always && f.proxy.alwaysSet
	cleared := f.proxy.set && noProxyURL(f.proxy.value) // SetProxy turns proxy-only mode off with it
	sshAlways := saved.SSH != nil && saved.SSH.Always
	switch {
	case !f.proxy.alwaysSet && saved.ProxyAlways && !cleared && ssh != nil && ssh.Always:
		return link.ErrSSHAlwaysProxyAlways // what the pairing or SetSSH says once the proxy is saved
	case !on:
	case !f.proxy.set && (saved.Proxy == "" || saved.Proxy == link.ProxyOff):
		return link.ErrProxyAlwaysNeedsURL
	case ssh == nil && sshAlways:
		return link.ErrProxyAlwaysSSHAlways
	case ssh != nil && ssh.Always: // a default: check refuses --ssh-always
		return link.NewTextError("link-cli.proxy_always_ssh_default", nil)
	}
	if on && f.proxy.set {
		if _, err := link.ParseProxy(f.proxy.value); err != nil {
			return err
		}
	}
	if on && ssh != nil && sshAlways {
		if err := link.Update(e.configPath(), func(c *link.Config) error {
			if c.SSH != nil {
				c.SSH.Always = false
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return f.proxy.save(e)
}

func (f routeFlags) build(rawLink string) (*link.SSH, error) {
	if !f.any() {
		return nil, nil
	}
	if strings.TrimSpace(f.server) == "" {
		return nil, link.NewTextError("link-cli.ssh_flags_require_server", nil)
	}
	always := f.always
	if !f.alwaysSet {
		_, defAlways, ok := link.PairSSHDefaults(rawLink)
		if ok {
			always = defAlways
		}
	}
	return &link.SSH{Server: f.server, Target: f.target, Always: always}, nil
}

type sshStatusJSON struct {
	SSH           *link.SSH `json:"ssh"`
	DefaultTarget string    `json:"default_target,omitempty"`
}

type sshTestJSON struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Server  string `json:"server,omitempty"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message"`
}

func sshCmd(e *env, args []string) error {
	var server, target, rawLink string
	var off, testOnly, asJSON, always, alwaysSet, targetSet, serverSet bool
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--off":
			off = true
		case a == "--test":
			testOnly = true
		case a == "--json":
			asJSON = true
		case a == "--always":
			always, alwaysSet = true, true
		case a == "--no-always":
			always, alwaysSet = false, true
		default:
			if v, next, ok, err := flagValue(args, i, "--target"); ok || err != nil {
				if err != nil {
					return err
				}
				target, targetSet, i = v, true, next
				continue
			}
			if v, next, ok, err := flagValue(args, i, "--link"); ok || err != nil {
				if err != nil {
					return err
				}
				rawLink, i = v, next
				continue
			}
			if strings.HasPrefix(a, "-") || serverSet {
				return errUsage
			}
			server, serverSet = a, true
		}
	}

	if off && (serverSet || targetSet || alwaysSet || testOnly || rawLink != "") {
		return errUsage
	}

	cfgPath := e.configPath()
	c, readErr := link.Read(cfgPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}

	// `persona-link ssh [--json]`: show current tunnel.
	if !off && !testOnly && !serverSet && !targetSet && !alwaysSet {
		if rawLink != "" {
			return errUsage
		}
		var defTarget string
		if c != nil {
			defTarget = link.DefaultSSHTarget(c.Endpoints)
		}
		if asJSON {
			enc := json.NewEncoder(e.stdout)
			enc.SetIndent("", "  ")
			var cur *link.SSH
			if c != nil {
				cur = c.SSH
			}
			return enc.Encode(sshStatusJSON{SSH: cur, DefaultTarget: defTarget})
		}
		if c == nil || c.SSH == nil {
			fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_none"))
			return nil
		}
		fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_current", i18n.Args{"server": c.SSH.Server, "target": c.SSH.Target, "route": sshRouteText(c.SSH.Always)}))
		return nil
	}

	if off {
		if _, err := link.SetSSH(cfgPath, nil); err != nil {
			return err
		}
		pokeRunning(cfgPath)
		if asJSON {
			enc := json.NewEncoder(e.stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(sshStatusJSON{SSH: nil})
		}
		fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_removed"))
		if link.Running(cfgPath) {
			fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_switch_direct"))
		}
		return nil
	}

	// Fill missing fields from existing saved tunnel when editing or testing.
	if !serverSet && c != nil && c.SSH != nil {
		server = c.SSH.Server
	}
	if !targetSet && rawLink == "" && c != nil && c.SSH != nil && (!serverSet || server == c.SSH.Server) {
		target = c.SSH.Target
	}
	if !alwaysSet {
		switch {
		case c != nil && c.SSH != nil && (!serverSet || server == c.SSH.Server):
			always = c.SSH.Always
		case rawLink != "":
			if _, defAlways, ok := link.PairSSHDefaults(rawLink); ok {
				always = defAlways
			}
		case c != nil:
			always = link.AllLoopback(c.Endpoints)
		}
	}

	if testOnly {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		norm, err := link.TestSSH(ctx, link.SSH{Server: server, Target: target, Always: always}, rawLink, c, e.sshBin)
		if asJSON {
			rep := sshTestJSON{OK: err == nil}
			if norm != nil {
				rep.Server, rep.Target = norm.Server, norm.Target
			} else {
				rep.Server, rep.Target = strings.TrimSpace(server), strings.TrimSpace(target)
			}
			if err == nil {
				rep.Message = i18n.T(i18n.Default, "link-cli.ssh_test_success", i18n.Args{"target": rep.Target})
			} else {
				var se *link.SSHError
				if errors.As(err, &se) {
					rep.Code = se.Code
					if se.Server != "" {
						rep.Server = se.Server
					}
					if se.Target != "" {
						rep.Target = se.Target
					}
				}
				rep.Message = err.Error()
			}
			enc := json.NewEncoder(e.stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(rep)
			if err != nil {
				return exitCode(1)
			}
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_test_success", i18n.Args{"target": norm.Target}))
		return nil
	}

	if rawLink != "" {
		return errUsage
	}
	if strings.TrimSpace(server) == "" {
		return link.NewTextError("link-cli.ssh_server_required", nil)
	}
	saved, err := link.SetSSH(cfgPath, &link.SSH{Server: server, Target: target, Always: always})
	if err != nil {
		return err
	}
	pokeRunning(cfgPath)
	if asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sshStatusJSON{SSH: saved})
	}
	fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_set", i18n.Args{"server": saved.Server, "target": saved.Target, "route": sshRouteText(saved.Always)}))
	if link.Running(cfgPath) {
		fmt.Fprintln(e.stdout, terminalText("link-cli.ssh_reconnect"))
	}
	return nil
}

func sshRouteText(always bool) string {
	if always {
		return terminalText("link-cli.ssh_route_always")
	}
	return terminalText("link-cli.ssh_route_fallback")
}
