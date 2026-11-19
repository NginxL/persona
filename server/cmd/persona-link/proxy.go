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

// proxyFlag is --proxy <url> on pair and install: saved as persona-link proxy saves it, before pairing, so the
// pairing goes through it too.
type proxyFlag struct {
	value string
	set   bool
}

func (f *proxyFlag) consume(args []string, i int) (next int, ok bool, err error) {
	v, next, ok, err := flagValue(args, i, "--proxy")
	if ok && err == nil {
		f.value, f.set = v, true
	}
	return next, ok, err
}

func (f proxyFlag) save(e *env) error {
	if !f.set {
		return nil
	}
	return link.SetProxy(e.configPath(), f.value)
}

// proxyCmd is `persona-link proxy [<url> | --off | --env]`: show or set active mode's outbound proxy in link.json
// (docs/operations/link.md "Outbound proxy"). Nothing it prints carries the proxy's user name or password.
func proxyCmd(e *env, args []string) error {
	if len(args) > 1 {
		return errUsage
	}
	if len(args) == 1 {
		raw := args[0]
		switch {
		case raw == "--off":
			raw = link.ProxyOff
		case raw == "--env":
			raw = ""
		case strings.HasPrefix(raw, "-"):
			return errUsage
		}
		// No poke: a running link reads link.json itself and moves only when it is on a fallback route, so a working
		// direct link stays.
		if err := link.SetProxy(e.configPath(), raw); err != nil {
			return err
		}
	}
	c, err := link.Read(e.configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	setting := ""
	if c != nil {
		setting = c.Proxy
	}
	fmt.Fprintln(e.stdout, terminalText("link-cli.proxy_current", i18n.Args{"proxy": proxySettingText(setting)}))
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
