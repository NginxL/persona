package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// report is `persona-link status --json`: what is known here about the link, without asking the running persona-link
// (it keeps its last status on disk, link.status.json) and without touching the resident node (a connection to its
// socket would take the node from the hub).
type report struct {
	// Running: a persona-link holds the run lock (whoever started it: the service, a compatible parent or a person).
	Running bool `json:"running"`
	// Mode: active (this computer dials the hub) | passive (the hub dials it).
	Mode string `json:"mode"`
	// Paired, Machine, HubID, HubName, Endpoints: active mode's pairing (also there in passive mode, for switching back).
	Paired  bool   `json:"paired"`
	Machine string `json:"machine,omitempty"`
	// MachineName: what the hub calls this computer, as it last said (older hubs say nothing: Machine, its id).
	MachineName string `json:"machine_name,omitempty"`
	HubID       string `json:"hub_id,omitempty"`
	HubName     string `json:"hub_name,omitempty"`
	// Endpoints: the candidates in link.json, in the order they are tried.
	Endpoints []link.Endpoint `json:"endpoints,omitempty"`
	// SSH: the optional SSH tunnel in link.json.
	SSH *link.SSH `json:"ssh,omitempty"`
	// Proxy: link.json's outbound proxy without its user name and password, "off", or "invalid" when it cannot be
	// used; missing when none is set (HTTPS_PROXY and ALL_PROXY apply). link.proxy says the one a link is connected
	// through.
	Proxy string `json:"proxy,omitempty"`
	// ProxyAlways: proxy-only mode is set in link.json (persona-link proxy --always).
	ProxyAlways bool `json:"proxy_always,omitempty"`
	// Trust: how the hub is checked, "pin" (its key, from the pairing) | "system" (behind an HTTPS reverse proxy:
	// the system's CAs and the host name).
	Trust string `json:"trust,omitempty"`
	// Link is the last status the running persona-link reported; with running false, the last one before it stopped.
	Link *link.SavedStatus `json:"link,omitempty"`
	// Passive is passive mode's pairing, whichever mode is on.
	Passive *passiveReport `json:"passive,omitempty"`
	Service *serviceState  `json:"service,omitempty"`
	// ClaudePlugin is the Persona plugin for Claude Code as the node last checked it (persona-link plugin status checks
	// now).
	ClaudePlugin *proto.ClaudePlugin `json:"claude_plugin,omitempty"`
	Config       string              `json:"config"`
	Log          string              `json:"log"`
	Error        string              `json:"error,omitempty"` // link.json there but unreadable
	loadErr      error               // the same error with its catalog identity; never serialized
}

// passiveReport is passive mode's part of link.json, without the credential's hash.
type passiveReport struct {
	Listen      string     `json:"listen"`
	Paired      bool       `json:"paired"`
	HubID       string     `json:"hub_id,omitempty"`
	HubName     string     `json:"hub_name,omitempty"`
	Machine     string     `json:"machine,omitempty"`
	MachineName string     `json:"machine_name,omitempty"`
	PairedAt    *time.Time `json:"paired_at,omitempty"`
	CodeExpires *time.Time `json:"code_expires,omitempty"` // a pairing code from pair --listen is waiting until then
	FP          string     `json:"fp,omitempty"`           // the connector's pin (public: anyone reaching the port sees the key)
}

// serviceState is the systemd user service (Linux), launchd user agent (Mac) or scheduled task (Windows).
type serviceState struct {
	Manager   string `json:"manager"` // systemd, launchd, schtasks
	Unit      string `json:"unit"`    // the unit, the task's name
	File      string `json:"file"`
	Installed bool   `json:"installed"`
	Active    string `json:"active,omitempty"`  // systemctl --user is-active; the task's status as schtasks words it
	Enabled   string `json:"enabled,omitempty"` // systemctl --user is-enabled
	Linger    bool   `json:"linger"`
	Missing   string `json:"missing,omitempty"` // why there is no systemd user instance here
}

func gather(e *env) report {
	cfg := e.configPath()
	r := report{Running: link.Running(cfg), Mode: link.ModeActive, Config: cfg, Log: e.logPath()}
	if c, err := link.Load(cfg); err == nil {
		r.Paired, r.Machine, r.HubID, r.HubName, r.Endpoints = true, c.Machine, c.Hub, c.HubName, link.Ranked(c.Endpoints)
		r.MachineName = c.MachineName
		r.SSH = c.SSH
		r.Trust = "pin"
		if c.SystemTrust() {
			r.Trust = link.TrustSystem
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Error, r.loadErr = err.Error(), err
	}
	if c, err := link.Read(cfg); err == nil {
		r.Mode = c.CurrentMode()
		if r.Proxy = link.RedactedProxy(c.Proxy); r.Proxy == "" && c.Proxy != "" {
			r.Proxy = proxyInvalid // nothing of an unusable setting is shown, but that there is one
		}
		r.ProxyAlways = c.ProxyAlways
		if r.SSH == nil {
			r.SSH = c.SSH
		}
		if p := c.Passive; p != nil || r.Mode == link.ModePassive {
			pr := &passiveReport{Listen: p.ListenAddr(), Paired: p.Paired()}
			if p != nil {
				pr.HubID, pr.HubName, pr.Machine, pr.MachineName, pr.PairedAt = p.HubID, p.HubName, p.Machine, p.MachineName, p.PairedAt
				if until := p.CodeUntil(e.now()); !until.IsZero() {
					pr.CodeExpires = &until
				}
			}
			pr.FP = link.ReadPin(link.TLSDir(cfg)) // read only: status makes nothing
			r.Passive = pr
		}
	}
	if st, err := link.ReadStatus(cfg); err == nil {
		r.Link = st
		if r.HubName == "" {
			r.HubName = st.HubName
		}
	}
	if e.goos == "linux" {
		s := &serviceState{Manager: "systemd", Unit: e.linkUnitName(), File: e.unitPath(), Linger: e.lingering()}
		s.Installed = fileExists(s.File)
		if s.Missing = e.systemdMissing(); s.Missing == "" {
			// Both exit non-zero for inactive and disabled; the word printed is the answer.
			s.Active, _ = e.systemctl("is-active", s.Unit)
			s.Enabled, _ = e.systemctl("is-enabled", s.Unit)
			s.Active, s.Enabled = firstLine(s.Active), firstLine(s.Enabled)
		}
		r.Service = s
	}
	if e.goos == "darwin" {
		r.Service = e.macServiceState() // service_mac.go
	}
	if e.goos == "windows" {
		r.Service = e.taskState() // winservice.go
	}
	r.ClaudePlugin = e.savedPluginStatus() // plugin.go
	return r
}

func status(e *env, args []string) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return errUsage
		}
		asJSON = true
	}
	r := gather(e)
	if asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		fmt.Fprint(e.stdout, r.text(e.now(), e.userOr("$USER")))
	}
	if r.healthy() {
		return nil
	}
	return exitCode(1)
}

// healthy: running and connected; in passive mode also listening for a hub that paired (it comes when it wants to).
func (r report) healthy() bool {
	if !r.Running || r.Link == nil || r.Link.Mode != r.Mode && r.Link.Mode != "" {
		return false
	}
	return r.Link.State == "connected" || (r.Mode == link.ModePassive && r.Link.State == "listening")
}

// text is the report for a person.
func (r report) text(now time.Time, user string) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	st := r.Link
	live := r.Running && st != nil
	if r.Mode == link.ModePassive {
		r.passiveText(line, now, live)
		r.serviceText(line, user)
		r.pluginText(line)
		if live {
			line("%s", terminalText("link-cli.process", i18n.Args{"pid": st.PID, "version": st.Version}))
		}
		line("%s", terminalText("link-cli.log_path", i18n.Args{"path": r.Log}))
		return b.String()
	}
	line("%s", terminalText("link-cli.mode_current", i18n.Args{"mode": modeName(r.Mode)}))
	switch {
	case r.Error != "":
		line("%s", terminalText("link-cli.credentials_error", i18n.Args{"error": r.credentialsErrorText()}))
	case !r.Paired:
		line("%s", terminalText("link-cli.link_unpaired"))
	case !r.Running && st != nil && st.State != "stopped":
		// How long ago, not a clock time: this computer's zone may well be UTC (a server) and not the user's.
		line("%s", terminalText("link-cli.link_not_running_last", i18n.Args{"ago": duration(now.Sub(st.Updated)), "state": stateText(&st.Status, now)}))
	case !r.Running:
		line("%s", terminalText("link-cli.link_not_running"))
	case st == nil:
		line("%s", terminalText("link-cli.link_running_no_status"))
	default:
		line("%s", terminalText("link-cli.link_state", i18n.Args{"state": stateText(&st.Status, now)}))
		if st.State == "waiting" || st.State == "refused" {
			for _, part := range errorParts(st.Error) {
				line("%s", terminalText("link-cli.error_part", i18n.Args{"error": link.StatusErrorText(i18n.Terminal(), part)}))
			}
		}
	}
	if r.Paired {
		mesh := 0
		if live && st.State == "connected" {
			mesh = st.MeshHubs
		}
		line("%s", terminalText("link-cli.hub_status", i18n.Args{"hub": hubText(r.HubName, mesh, r.HubID, live && st.State == "connected")}))
		if live && st.State == "connected" {
			switch st.Node {
			case "online":
				line("%s", terminalText("link-cli.node_online"))
			case "offline":
				line("%s", terminalText("link-cli.node_offline", i18n.Args{
					"error": link.StatusErrorText(i18n.Terminal(), st.NodeError), "hint": nodeHint(st.NodeError),
				}))
			default:
				line("%s", terminalText("link-cli.node_checking"))
			}
		}
		line("%s", terminalText("link-cli.machine_status", i18n.Args{"machine": machineText(r.MachineName, r.Machine)}))
		if r.Trust == link.TrustSystem {
			line("%s", terminalText("link-cli.trust_system"))
		}
		if len(r.Endpoints) > 0 || r.SSH != nil {
			line("%s", terminalText("link-cli.candidates_header"))
			inUse := "" // the kind of route connected now
			if live && st.State == "connected" {
				inUse = st.Kind
			}
			notesText := func(notes []string) string {
				if len(notes) == 0 {
					return ""
				}
				return terminalText("link-cli.candidate_notes", i18n.Args{"notes": strings.Join(notes, terminalText("link-cli.candidate_note_separator"))})
			}
			// Proxy-only mode, unless Always use SSH (which wins) is set too.
			proxyOnly := r.ProxyAlways && (r.SSH == nil || !r.SSH.Always)
			sshLine := func() {
				if r.SSH == nil {
					return
				}
				var notes []string
				if inUse == link.KindSSH {
					notes = append(notes, terminalText("link-cli.note_in_use"))
				}
				if r.SSH.Always {
					notes = append(notes, terminalText("link-cli.note_always_ssh"))
				}
				if proxyOnly {
					notes = append(notes, terminalText("link-cli.note_skip_proxy_only"))
				}
				line("%s", terminalText("link-cli.candidate_ssh", i18n.Args{
					"kind": kindName(link.KindSSH), "server": r.SSH.Server, "target": r.SSH.Target, "notes": notesText(notes),
				}))
			}
			// The proxy: the one in use, else link.json's.
			proxyLine := func() {
				proxy := r.Proxy
				if inUse != link.KindProxy && (proxy == "" || proxy == link.ProxyOff) {
					return
				}
				var notes []string
				if inUse == link.KindProxy {
					proxy = st.Proxy
					notes = append(notes, terminalText("link-cli.note_in_use"))
				} else if proxy == proxyInvalid {
					proxy = terminalText("link-errors.proxy_invalid")
				}
				if r.SSH != nil && r.SSH.Always {
					notes = append(notes, terminalText("link-cli.note_skip_direct"))
				}
				if proxyOnly {
					notes = append(notes, terminalText("link-cli.note_always_proxy"))
				}
				line("%s", terminalText("link-cli.candidate_endpoint", i18n.Args{"kind": kindName(link.KindProxy), "address": proxy, "notes": notesText(notes)}))
			}
			// Routes are listed as they are tried: the only one first.
			switch {
			case r.SSH != nil && r.SSH.Always:
				sshLine()
			case proxyOnly:
				proxyLine()
			}
			for _, ep := range r.Endpoints {
				var notes []string
				if inUse != "" && inUse != link.KindSSH && inUse != link.KindProxy && st.Address == ep.URL {
					notes = append(notes, terminalText("link-cli.note_in_use"))
				}
				if ep.Manual {
					notes = append(notes, terminalText("link-cli.note_manual"))
				}
				if r.SSH != nil && r.SSH.Always {
					notes = append(notes, terminalText("link-cli.note_skip_direct"))
				}
				if proxyOnly {
					notes = append(notes, terminalText("link-cli.note_skip_proxy_only"))
				}
				line("%s", terminalText("link-cli.candidate_endpoint", i18n.Args{"kind": kindName(ep.Kind), "address": ep.URL, "notes": notesText(notes)}))
			}
			if !proxyOnly {
				proxyLine() // after the direct addresses, as it is tried
			}
			if r.SSH != nil && !r.SSH.Always {
				sshLine()
			}
		}
	}
	r.serviceText(line, user)
	r.pluginText(line)
	if live {
		line("%s", terminalText("link-cli.process", i18n.Args{"pid": st.PID, "version": st.Version}))
	}
	line("%s", terminalText("link-cli.log_path", i18n.Args{"path": r.Log}))
	return b.String()
}

// pluginText is the report's line about the Claude Code plugin, when the node has checked it.
func (r report) pluginText(line func(string, ...any)) {
	if r.ClaudePlugin == nil {
		return
	}
	for _, l := range strings.Split(strings.TrimRight(pluginText(r.ClaudePlugin, false), "\n"), "\n") {
		line("%s", l)
	}
}

func (r report) credentialsErrorText() string {
	if r.loadErr != nil {
		return link.ErrorText(i18n.Terminal(), r.loadErr)
	}
	return link.StatusErrorText(i18n.Terminal(), r.Error)
}

// passiveText is the report's part about passive mode.
func (r report) passiveText(line func(string, ...any), now time.Time, live bool) {
	p, st := r.Passive, r.Link
	listen := link.DefaultListen
	if p != nil {
		listen = p.Listen
	}
	if live && st.Listen != "" {
		listen = st.Listen
	}
	line("%s", terminalText("link-cli.mode_passive_listen", i18n.Args{"mode": modeName(link.ModePassive), "listen": listen}))
	paired := p != nil && p.Paired
	switch {
	case r.Error != "":
		line("%s", terminalText("link-cli.credentials_error", i18n.Args{"error": r.credentialsErrorText()}))
	case !r.Running && st != nil && st.State != "stopped":
		line("%s", terminalText("link-cli.passive_not_running_last", i18n.Args{"ago": ago(now, st.Updated), "state": stateText(&st.Status, now)}))
	case !r.Running:
		line("%s", terminalText("link-cli.link_not_running"))
	case st == nil:
		line("%s", terminalText("link-cli.link_running_no_status"))
	default:
		line("%s", terminalText("link-cli.link_state", i18n.Args{"state": stateText(&st.Status, now)}))
		if st.State == "waiting" {
			for _, part := range errorParts(st.Error) {
				line("%s", terminalText("link-cli.error_part", i18n.Args{"error": link.StatusErrorText(i18n.Terminal(), part)}))
			}
		}
	}
	if !paired {
		line("%s", terminalText("link-cli.pairing_none"))
	}
	if p != nil && p.CodeExpires != nil {
		line("%s", terminalText("link-cli.pair_code_left", i18n.Args{"duration": duration(p.CodeExpires.Sub(now))}))
	}
	if paired {
		hub := "hub" + passiveHubText(&link.Passive{HubID: p.HubID, HubName: p.HubName})
		if p.PairedAt != nil {
			hub += terminalText("link-cli.paired_ago", i18n.Args{"ago": ago(now, *p.PairedAt)})
		}
		line("%s", hub)
	}
	if live && st.LastSeen != nil {
		line("%s", terminalText("link-cli.last_hub_connection", i18n.Args{"ago": ago(now, *st.LastSeen), "from": st.From}))
	}
	if live && st.Node != "" {
		switch {
		case st.State == "connected" && st.Node == "online":
			line("%s", terminalText("link-cli.node_online"))
		case st.Node == "offline":
			line("%s", terminalText("link-cli.node_offline", i18n.Args{
				"error": link.StatusErrorText(i18n.Terminal(), st.NodeError), "hint": nodeHint(st.NodeError),
			}))
		}
	}
	if paired && p.Machine != "" {
		line("%s", terminalText("link-cli.machine_status", i18n.Args{"machine": machineText(p.MachineName, p.Machine)}))
	}
	if p != nil && p.FP != "" {
		line("%s", terminalText("link-cli.connector_fingerprint", i18n.Args{"fingerprint": p.FP}))
	}
	if live && len(st.Addrs) > 0 {
		line("%s", terminalText("link-cli.passive_addresses_header"))
		for _, a := range st.Addrs {
			line("%s", terminalText("link-cli.error_part", i18n.Args{"error": a}))
		}
	}
	if r.Paired {
		line("%s", terminalText("link-cli.active_also_paired", i18n.Args{"machine": r.Machine}))
	}
}

// serviceText is the report's line about the connector's platform service.
func (r report) serviceText(line func(string, ...any), user string) {
	if s := r.Service; s != nil && s.Manager == "schtasks" {
		taskText(s, line)
	} else if s != nil && s.Manager == "launchd" {
		text := func(key string, args ...i18n.Args) string {
			return i18n.T(i18n.Terminal(), key, args...)
		}
		switch {
		case !s.Installed:
			line("%s", text("link-service.status_missing"))
		case s.Active != "":
			line("%s", text("link-service.status_loaded", i18n.Args{"label": s.Unit, "state": s.Active}))
		default:
			line("%s", text("link-service.status_not_loaded", i18n.Args{"label": s.Unit, "path": s.File}))
		}
	} else if s != nil {
		switch {
		case strings.HasPrefix(s.Missing, noUserInstance()) && s.Installed && !s.Linger:
			line("%s", terminalText("link-cli.service_linger_unavailable", i18n.Args{
				// i18n-ignore: trims the canonical full-width colon in a persisted systemd reason
				"unit": s.Unit, "missing": noUserInstance(), "detail": strings.TrimPrefix(s.Missing, noUserInstance()+"："), "user": user,
			}))
		case s.Missing != "":
			line("%s", terminalText("link-cli.service_missing", i18n.Args{"missing": systemdReason(s.Missing).Text(i18n.Terminal())}))
		case !s.Installed:
			line("%s", terminalText("link-cli.service_not_installed"))
		default:
			linger := terminalText("link-cli.linger_enabled")
			if !s.Linger {
				linger = terminalText("link-cli.linger_disabled", i18n.Args{"user": user})
			}
			line("%s", terminalText("link-cli.service_systemd", i18n.Args{
				"unit": s.Unit, "active": s.Active, "enabled": s.Enabled, "linger": linger,
			}))
		}
	}
}

var kindNames = map[string]string{
	"lan": "link-errors.kind_lan", "ipv6": "link-errors.kind_ipv6", "name": "link-errors.kind_name", "ipv4": "link-errors.kind_ipv4", link.KindProxy: "link-errors.kind_proxy",
}

func kindName(kind string) string {
	if kind == link.KindSSH {
		return "SSH"
	}
	if key, ok := kindNames[kind]; ok {
		return terminalText(key)
	}
	return terminalText("link-errors.kind_address")
}

// addressText is a hub URL as a person sees it: its kind and host:port.
func addressText(kind, address string) string {
	host := address
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		host = u.Host + u.Path // a reverse proxy's path says which hub
	}
	return kindName(kind) + " " + host
}

// machineText names this computer as the hub calls it, with its id when that is another.
func machineText(name, id string) string {
	switch {
	case name == "" || name == id:
		return terminalText("link-cli.machine_name", i18n.Args{"name": id})
	case id == "":
		return terminalText("link-cli.machine_name", i18n.Args{"name": name})
	}
	return terminalText("link-cli.machine_name_id", i18n.Args{"name": name, "id": id})
}

// hubText names the hub: by the name it gave, with its mesh's size when known (mesh 0); else by id, and a hub that
// is connected without having said its name is an older one.
func hubText(name string, mesh int, id string, connected bool) string {
	var s string
	switch {
	case name != "":
		s = terminalText("link-cli.hub_name", i18n.Args{"name": name})
	case id != "" && connected:
		s = terminalText("link-cli.hub_old_unnamed", i18n.Args{"id": id})
	case id != "":
		s = id
	default:
		s = "hub"
	}
	switch {
	case mesh > 1:
		s += terminalText("link-cli.hub_mesh_many", i18n.Args{"count": mesh})
	case mesh == 1:
		s += terminalText("link-cli.hub_mesh_one")
	}
	return s
}

// stateText is a status in a few words.
func stateText(s *link.Status, now time.Time) string {
	if s.Mode == link.ModePassive {
		switch s.State {
		case "connected":
			t := terminalText("link-cli.state_passive_connected", i18n.Args{"address": s.Address})
			if s.Since != nil {
				t += terminalText("link-cli.state_connected_for", i18n.Args{"duration": duration(now.Sub(*s.Since))})
			}
			return t
		case "listening":
			return terminalText("link-cli.state_listening")
		case "unpaired":
			return terminalText("link-cli.state_passive_unpaired")
		case "waiting":
			t := terminalText("link-cli.state_listen_failed")
			if s.Retry != nil {
				if d := s.Retry.Sub(now).Round(time.Second); d > 0 {
					t += terminalText("link-cli.state_retry_after", i18n.Args{"duration": duration(d)})
				} else {
					t += terminalText("link-cli.state_retry_now")
				}
			}
			return t
		}
	}
	switch s.State {
	case "connected":
		t := terminalText("link-cli.state_connected", i18n.Args{"address": addressText(s.Kind, s.Address)})
		if s.Since != nil {
			t += terminalText("link-cli.state_connected_for", i18n.Args{"duration": duration(now.Sub(*s.Since))})
		}
		return t
	case "connecting":
		return terminalText("link-cli.state_connecting")
	case "unpaired":
		return terminalText("link-cli.state_unpaired")
	case "refused":
		return terminalText("link-cli.state_refused")
	case "stopped":
		return terminalText("link-cli.state_stopped")
	}
	t := terminalText("link-cli.state_disconnected")
	if s.Retry != nil {
		if d := s.Retry.Sub(now).Round(time.Second); d > 0 {
			t += terminalText("link-cli.state_retry_after", i18n.Args{"duration": duration(d)})
		} else {
			t += terminalText("link-cli.state_retry_now")
		}
	}
	return t
}

// errorParts: persona-link joins why each candidate failed with a full-width semicolon.
func errorParts(s string) []string {
	var out []string
	for _, p := range strings.Split(s, i18n.T(i18n.Default, "link-errors.error_separator")) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func duration(d time.Duration) string {
	s := int(max(d, 0) / time.Second)
	switch {
	case s < 60:
		return terminalText("link-cli.duration_seconds", i18n.Args{"count": s})
	case s < 3600:
		return terminalText("link-cli.duration_minutes", i18n.Args{"count": s / 60})
	case s < 86400:
		return terminalText("link-cli.duration_hours_minutes", i18n.Args{"hours": s / 3600, "minutes": s % 3600 / 60})
	}
	return terminalText("link-cli.duration_days_hours", i18n.Args{"days": s / 86400, "hours": s % 86400 / 3600})
}
