#!/bin/sh
# Install bubblewrap (bwrap, docs/extensions/extensions.md "Sandbox") on this computer if it has none: extension sandboxes
# on Linux need it, and an update must not leave them switched off. deploy.sh pipes it to each Linux hub it installs on.
# PERSONA_SKIP_BWRAP=1 skips it. Prints one line; a failed install only warns (exit 0), so the deploy goes on.
[ "${PERSONA_SKIP_BWRAP:-}" = 1 ] && exit 0
[ "$(uname -s 2>/dev/null)" = Linux ] || exit 0
has_bwrap() {
	command -v bwrap >/dev/null 2>&1 && return 0
	[ -x /usr/bin/bwrap ] || [ -x /usr/local/bin/bwrap ]
}
has_bwrap && exit 0

# The install needs root: directly when this is root, else through a passwordless sudo.
as_root() {
	if [ "$(id -u)" = 0 ]; then
		"$@"
	elif sudo -n true >/dev/null 2>&1; then
		sudo -n "$@"
	else
		return 1
	fi
}
installed() {
	for pm in apt-get dnf apk pacman; do
		command -v "$pm" >/dev/null 2>&1 || continue
		case "$pm" in
		apt-get) as_root apt-get install -y bubblewrap ;;
		dnf) as_root dnf install -y bubblewrap ;;
		apk) as_root apk add --no-cache bubblewrap ;;
		pacman) as_root pacman -S --noconfirm bubblewrap ;;
		esac >/dev/null 2>&1 && return 0
	done
	return 1
}
if installed && has_bwrap; then
	echo "bwrap: installed"
else
	echo "bwrap: could not install bubblewrap; extension sandboxes stay switched off until it is (install it with your package manager)" >&2
fi
exit 0
