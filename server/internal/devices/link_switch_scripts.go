package devices

import (
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

// The remote commands of a switch to Persona Link (link_switch.go). Each is a fixed POSIX sh script run through
// posixCommand, so any login shell runs it alike; only shellQuote'd values go into it (the operation id, the sha-256 of
// the build sent, the directories a test moves). None of them ever carries the pairing link: install reads it on
// stdin. Every path is checked under its current name and its older one (compat), as persona-link looks for both.

// What each script answers with besides its output's key=value lines; 255 is ssh's own failure, as always.
const (
	lsExitStageWrite     = 10 // the bin directory or the staged file could not be written
	lsExitStageCopy      = 11 // the gzipped build on stdin did not arrive whole
	lsExitStageSum       = 12 // what arrived is not the build sent (sha-256), or nothing here can tell
	lsExitStageVersion   = 13 // the staged build does not run `version` here
	lsExitStageUnsafe    = 14 // ~/.persona or its bin may be written by other users (lsUnsafeDirs): nothing written
	lsExitInstallExists  = 20 // Persona Link appeared since the probe: nothing placed
	lsExitInstallStaged  = 21 // the staged build is gone: nothing placed
	lsExitInstallPlace   = 22 // the staged build could not be linked into place: nothing placed
	lsExitInstallUnsafe  = 23 // ~/.persona or its bin may be written by other users (lsUnsafeDirs): nothing placed
	lsExitInstallSum     = 24 // what was linked into place is not the build sent: taken out again, nothing run
	lsExitUninstallStays = 30 // something this switch should have removed is still there
)

// lsStagePrefix names the staged build in the data directory's bin (with the operation id): a hidden name nothing
// runs, which install links into place. Only a switch makes files of this name (the staged build, and its .node
// marker: a node service was there when the installer ran), so a stage and an uninstall remove every one of them.
const lsStagePrefix = ".persona-link-switch-"

// lsNodeBackup is where persona-link's node switch keeps a service file it replaced until the new node answers: its
// installer may put that back, so it counts as a node service the computer has.
const lsNodeBackup = "node-switch.backup"

// lsStagedAll is every file a switch staged or marked, as an sh glob.
var lsStagedAll = `"$B"/` + shellQuote(lsStagePrefix) + `*`

// The names under the data directory and the user's systemd units, current and older.
var (
	lsLinkBin        = "persona-link"
	lsLinkUnit       = "persona-link.service"
	lsNodeUnit       = nodeproto.ProgramPersonad + "-node.service"
	lsLegacyLinkBin  = compat.ConnectorBinary
	lsLegacyLinkUnit = compat.ConnectorBinary + ".service"
	lsLegacyNodeUnit = compat.DaemonBinary + "-node.service"
)

// lsPaths are where a switch looks on the computer: the systemd user manager's run directory and the logins that keep
// it running (loginctl enable-linger). Tests point them into a temporary directory.
type lsPaths struct {
	systemd, linger string
}

var defaultLSPaths = lsPaths{systemd: "/run/systemd/system", linger: "/var/lib/systemd/linger"}

// lsIn is name under the directory in shell variable dir, as one sh word.
func lsIn(dir, name string) string { return `"$` + dir + `"/` + shellQuote(name) }

// lsPrelude sets what every script reads: D the data directory, B its bin, O the older data directory, U the
// systemd user units, e() whether a path is there (a dangling link counts), and XDG_RUNTIME_DIR for systemctl --user
// when the ssh session has none.
func lsPrelude() []string {
	return []string{
		"set -u",
		`[ -n "${XDG_RUNTIME_DIR:-}" ] || { XDG_RUNTIME_DIR="/run/user/$(id -u)"; export XDG_RUNTIME_DIR; }`,
		`D="$HOME/.persona"`, `B="$D/bin"`, `O="$HOME"/` + shellQuote(compat.StateDir),
		`U="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"`,
		`e() { [ -e "$1" ] || [ -L "$1" ]; }`,
	}
}

// lsLinkPaths are what says Persona Link is on the computer already: its program, its pairing (link.json) and its
// service, in either data directory and under either name.
func lsLinkPaths() string {
	return strings.Join([]string{lsIn("B", lsLinkBin), lsIn("B", lsLegacyLinkBin), lsIn("D", "link.json"),
		lsIn("O", "bin/"+lsLinkBin), lsIn("O", "bin/"+lsLegacyLinkBin), lsIn("O", "link.json"),
		lsIn("U", lsLinkUnit), lsIn("U", lsLegacyLinkUnit)}, " ")
}

// lsNodeThere sets n=1 when the computer has a node service (either name), or one persona-link may put back.
var lsNodeThere = `n=0; for p in ` + lsIn("U", lsNodeUnit) + ` ` + lsIn("U", lsLegacyNodeUnit) + ` ` + lsIn("D", lsNodeBackup) +
	`; do if e "$p"; then n=1; fi; done`

// lsUnsafeDirs sets w=1 when the home folder, ~/.persona or its bin (followed through links) is not the user's own,
// or may be written by another user, who could swap the program a switch runs: by others, or by its group unless that
// is the user's own private group (the group named as the user, which a umask of 002 makes writable on distributions
// that give each user one). The probe says so; the stage and the install look again before they write or place
// anything.
var lsUnsafeDirs = `w=0; u=$(id -un); for p in "$HOME" "$HOME/.persona" "$HOME/.persona/bin"; do [ -e "$p" ] || continue; ` +
	`set -- $(ls -ldL "$p" 2>/dev/null); [ -n "$u" ] && [ "${3:-}" = "$u" ] || w=1; ` +
	`case "${1:-}" in ????????w*) w=1;; ?????w*) [ "${4:-}" = "$u" ] || w=1;; esac; done`

// lsRefuseUnsafe exits with code when lsUnsafeDirs finds a directory another user may write to.
func lsRefuseUnsafe(code int) string {
	return lsUnsafeDirs + `; if [ "$w" = 1 ]; then exit ` + strconv.Itoa(code) + `; fi`
}

// lsSum sets s to the sha-256 (hex) of the file in shell variable file, "" when nothing here computes one.
func lsSum(file string) string {
	return `if command -v sha256sum >/dev/null 2>&1; then s=$(sha256sum < "$` + file + `"); ` +
		`elif command -v shasum >/dev/null 2>&1; then s=$(shasum -a 256 < "$` + file + `"); else s=; fi; s="${s%% *}"`
}

func lsScript(lines ...string) string { return posixCommand(strings.Join(lines, "; ")) }

// lsStaged is the staged build's path for operation op, as an sh word.
func lsStaged(op string) string { return lsIn("B", lsStagePrefix+op) }

// lsProbeScript looks at the computer and changes nothing: it prints os, arch, systemd (a user manager answers),
// linger, link (Persona Link there already), custom (another data directory than ~/.persona would be used), unsafe
// (another user may write to ~/.persona or its bin),
// node_unit (a node service exists, or a backup of one persona-link may restore), node_bin (a node program to run as
// one) and end=1 last. Exit 0.
func lsProbeScript(p lsPaths) string {
	current, legacy := compat.EnvNames("PERSONA_HOME")
	lines := append(lsPrelude(),
		`echo "os=$(uname -s)"`, `echo "arch=$(uname -m)"`,
		`if command -v systemctl >/dev/null 2>&1 && [ -e `+shellQuote(p.systemd)+` ] && systemctl --user show-environment >/dev/null 2>&1; then echo systemd=1; else echo systemd=0; fi`,
		`if [ -e `+shellQuote(p.linger)+`/"$(id -un)" ]; then echo linger=1; else echo linger=0; fi`,
		`k=0; for p in `+lsLinkPaths()+`; do if e "$p"; then k=1; fi; done; echo "link=$k"`,
		`c=0; if [ -n "${`+current+`:-}" ] || [ -n "${`+legacy+`:-}" ]; then c=1; fi; if e "$O" && ! [ "$O" -ef "$D" ]; then c=1; fi; echo "custom=$c"`,
		lsUnsafeDirs+`; echo "unsafe=$w"`,
		lsNodeThere+`; echo "node_unit=$n"`,
		`x=0; for p in `+lsIn("B", nodeproto.ProgramPersonad)+` `+lsIn("B", nodeproto.ProgramPersonaNode)+` `+lsIn("B", compat.DaemonBinary)+
			`; do if [ -f "$p" ] && [ -x "$p" ]; then x=1; fi; done; echo "node_bin=$x"`,
		"echo end=1")
	return lsScript(lines...)
}

// lsStageScript writes the gzipped build on stdin to the staged name for op, checks it is the build of sha-256 sum and
// runs here (`version`), and prints staged=1; on any failure it removes what it wrote (lsExitStage*).
func lsStageScript(op, sum string) string {
	return lsScript("set -u", "umask 022", `B="$HOME/.persona/bin"`, "f="+lsStaged(op),
		lsRefuseUnsafe(lsExitStageUnsafe),
		`mkdir -p "$B" || exit `+strconv.Itoa(lsExitStageWrite),
		`rm -f `+lsStagedAll, // what an earlier switch left
		`if ! gzip -dc > "$f"; then rm -f "$f"; exit `+strconv.Itoa(lsExitStageCopy)+`; fi`,
		`chmod 755 "$f" || { rm -f "$f"; exit `+strconv.Itoa(lsExitStageWrite)+`; }`,
		lsSum("f"),
		`[ "$s" = `+shellQuote(sum)+` ] || { rm -f "$f"; exit `+strconv.Itoa(lsExitStageSum)+`; }`,
		`"$f" version </dev/null >/dev/null 2>&1 || { rm -f "$f"; exit `+strconv.Itoa(lsExitStageVersion)+`; }`,
		"echo staged=1")
}

// lsInstallScript refuses when Persona Link appeared since the probe, links the staged build of op into place as
// bin/persona-link (never over a file there), says placed=1, and runs its installer with the pairing link on stdin
// (`install --pair -` reads it to EOF), for at most limit where `timeout` exists, so it never outlives the ssh run and
// races a rollback: the script's exit is the installer's from there, and paired=1 says the installer got as far as
// pairing (link.json is there), so its link may still come. node_unit=1 (and the staged name's .node marker, which
// the uninstall reads) says a node service was there just before the installer ran: not this switch's to remove. It
// refuses a data directory others may write to, and checks what it linked into place is still the build of sha-256
// sum before it runs it.
func lsInstallScript(op, sum string, limit time.Duration) string {
	secs := strconv.Itoa(max(int((limit+time.Second-1)/time.Second), 1))
	lines := append(lsPrelude(), "f="+lsStaged(op), "t="+lsIn("B", lsLinkBin),
		`for p in `+lsLinkPaths()+`; do if e "$p"; then rm -f "$f"; exit `+strconv.Itoa(lsExitInstallExists)+`; fi; done`,
		`[ -f "$f" ] || exit `+strconv.Itoa(lsExitInstallStaged),
		lsRefuseUnsafe(lsExitInstallUnsafe),
		`ln "$f" "$t" 2>/dev/null || { rm -f "$f"; exit `+strconv.Itoa(lsExitInstallPlace)+`; }`,
		lsSum("t")+`; [ "$s" = `+shellQuote(sum)+` ] || { rm -f "$t" "$f"; exit `+strconv.Itoa(lsExitInstallSum)+`; }`,
		`rm -f "$f"`, "echo placed=1",
		lsNodeThere+`; if [ "$n" = 1 ]; then : > "$f.node"; echo node_unit=1; fi`,
		`if command -v timeout >/dev/null 2>&1; then timeout `+secs+` "$t" install --pair -; else "$t" install --pair -; fi`, "x=$?",
		`if [ -e `+lsIn("D", "link.json")+` ]; then echo paired=1; fi`, `exit "$x"`)
	return lsScript(lines...)
}

// lsUninstallScript undoes what the switch of op may have set up and checks it is gone: every staged file always;
// with node, the node service persona-link installed (there was none before: not when the install found one, its
// .node marker, which it then says as node_unit=1); with purge, Persona Link's service, pairing, logs and program.
// Either runs only through the program this switch sent (bin/persona-link of sha-256 sum), never another one found
// there. clean=1, or lsExitUninstallStays when anything of it is still there. The marker goes last, so an uninstall
// cut short and run again still sees it.
func lsUninstallScript(op, sum string, node, purge bool) string {
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	lines := append(lsPrelude(), "f="+lsStaged(op), "t="+lsIn("B", lsLinkBin),
		"node="+flag(node), "purge="+flag(purge), "want="+shellQuote(sum),
		`if e "$f.node"; then node=0; echo node_unit=1; fi`,
		`rm -f "$f"`,
		`ours=0; if [ -f "$t" ] && ! [ -L "$t" ]; then `+lsSum("t")+`; if [ -n "$want" ] && [ "$s" = "$want" ]; then ours=1; fi; fi`,
		`if [ "$node" = 1 ] && [ "$ours" = 1 ] && e `+lsIn("U", lsNodeUnit)+`; then "$t" node uninstall </dev/null >/dev/null 2>&1; echo "node_uninstall=$?"; fi`,
		`if [ "$purge" = 1 ] && [ "$ours" = 1 ]; then "$t" uninstall --purge </dev/null >/dev/null 2>&1; echo "purge=$?"; fi`,
		`rm -f `+lsStagedAll,
		`bad=0; for p in `+lsStagedAll+`; do if e "$p"; then bad=1; fi; done`,
		`if [ "$node" = 1 ] && e `+lsIn("U", lsNodeUnit)+`; then bad=1; fi`,
		`if [ "$purge" = 1 ]; then for p in "$t" `+lsIn("D", "link.json")+` `+lsIn("U", lsLinkUnit)+`; do if e "$p"; then bad=1; fi; done; fi`,
		`if [ "$bad" = 0 ]; then echo clean=1; else exit `+strconv.Itoa(lsExitUninstallStays)+`; fi`)
	return lsScript(lines...)
}
