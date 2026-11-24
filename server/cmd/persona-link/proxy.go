package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// proxyInvalid is status --json's proxy for a link.json setting that cannot be used.
const proxyInvalid = "invalid"

// proxyFlag is --proxy <url> and --proxy-always / --no-proxy-always on pair and install: saved as persona-link proxy
// saves them, before pairing, so the pairing goes through the proxy too.
type proxyFlag struct {
	value             string
	set               bool
	always, alwaysSet bool
}

func (f *proxyFlag) consume(args []string, i int) (next int, ok bool, err error) {
	switch args[i] {
	case "--proxy-always":
		f.always, f.alwaysSet = true, true
		return i, true, nil
	case "--no-proxy-always":
		f.always, f.alwaysSet = false, true
		return i, true, nil
	}
	v, next, ok, err := flagValue(args, i, "--proxy")
	if ok && err == nil {
		f.value, f.set = v, true
	}
	return next, ok, err
}

// any: a proxy flag is there.
func (f proxyFlag) any() bool { return f.set || f.alwaysSet }

// noProxyURL: v, given to --proxy, is no proxy URL: off, empty (the environment's again) or a flag such as --env.
func noProxyURL(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == link.ProxyOff || strings.HasPrefix(v, "-")
}

// save saves the flags as persona-link proxy saves them; on pair and install, through routeFlags.saveProxy.
func (f proxyFlag) save(e *env) error {
	if f.set {
		if err := link.SetProxy(e.configPath(), f.value); err != nil {
			return err
		}
	}
	if f.alwaysSet {
		return link.SetProxyAlways(e.configPath(), f.always)
	}
	return nil
}

// proxyCmd is `persona-link proxy [<url> | --off | --env] [--always | --no-always]`: show or set active mode's
// outbound proxy in link.json, and whether the link always uses it (docs/operations/link.md "Outbound proxy").
// Nothing it prints carries the proxy's user name or password.
func proxyCmd(e *env, args []string) error {
	var raw string
	var rawSet, always, alwaysSet bool
	for _, a := range args {
		switch {
		case (a == "--always" || a == "--no-always") && !alwaysSet:
			always, alwaysSet = a == "--always", true
		case rawSet:
			return errUsage
		case a == "--off":
			raw, rawSet = link.ProxyOff, true
		case a == "--env":
			raw, rawSet = "", true
		case strings.HasPrefix(a, "-"):
			return errUsage
		default:
			raw, rawSet = a, true
		}
	}
	if always && rawSet && (raw == link.ProxyOff || raw == "") {
		return errUsage // --off and --env turn proxy-only mode off
	}
	if always { // refused before the URL is saved, so a refusal changes nothing
		if c, err := link.Read(e.configPath()); err == nil && c.SSH != nil && c.SSH.Always {
			return link.ErrProxyAlwaysSSHAlways
		}
	}
	// No poke: a running link reads link.json itself. It moves when proxy-only mode changes or when it is on the proxy
	// (or a fallback route) and the proxy changes, so a working direct link stays.
	if rawSet {
		if err := link.SetProxy(e.configPath(), raw); err != nil {
			return err
		}
	}
	if alwaysSet {
		if err := link.SetProxyAlways(e.configPath(), always); err != nil {
			return err
		}
	}
	c, err := link.Read(e.configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	setting, key := "", "link-cli.proxy_current"
	if c != nil {
		setting = c.Proxy
		if c.ProxyAlways {
			key = "link-cli.proxy_current_always"
		}
	}
	fmt.Fprintln(e.stdout, terminalText(key, i18n.Args{"proxy": proxySettingText(setting)}))
	return nil
}

// proxySettingText is link.json's proxy setting for a person: the URL without its credentials, off, or not set.
func proxySettingText(raw string) string {
	switch raw {
	case "":
		return terminalText("link-cli.proxy_unset")
	case link.ProxyOff:
		return terminalText("link-cli.proxy_off")
	}
	if shown := link.RedactedProxy(raw); shown != "" {
		return shown
	}
	return terminalText("link-errors.proxy_invalid") // a hand-edited value: nothing of it is shown
}
