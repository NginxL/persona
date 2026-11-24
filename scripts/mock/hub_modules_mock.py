"""A hub that publishes module flags (#13849, the rule agreed on #13844): MOCK_HUB_MODULES.

The hello carries the marker `modules` and the listed module flags, as a hub with the module switch does; a client then
turns off every module with a feature whose flag is not listed. Unset: the hello has neither, as an older hub's, and
every module stays on.

    MOCK_HUB_MODULES=module.glasses,module.health   publishes those two module flags
    MOCK_HUB_MODULES=none                           publishes the marker alone: every gated module off
"""
import os

MODE = os.environ.get("MOCK_HUB_MODULES", "")


def flags(mode):
    """The features a hub with this switch adds: the marker, then the module flags listed."""
    if not mode:
        return []
    listed = [f.strip() for f in mode.split(",") if f.strip() and f.strip() != "none"]
    return ["modules"] + listed


def register(d):
    d.help("MOCK_HUB_MODULES=<module.id,…|none>  the hello publishes module flags: `modules` and those listed (#13849)")
    names = flags(MODE)
    if names:
        d.feature(*names)
