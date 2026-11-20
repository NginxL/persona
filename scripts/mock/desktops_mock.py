"""Desktop client registration in the mock hub (hub/desktops.go), a domain of scripts/mock/mock_domains.py.

An app's socket sends `register_desktop`, and withdraws it with `unregister_desktop` (a hub with the feature
desktop_unregister) or by closing. GET /mock/desktops says who is registered now and what happened in order, for the
desktop shell's smoke (desktop/scripts/e2e-main.mjs) and scripts/mock/test_desktops_mock.py. MOCK_DESKTOPS=1 lists the
features; without it the registration works as on an older hub.
"""
import os
import threading

HELP = ("MOCK_DESKTOPS=1  features list desktops and desktop_unregister, so a desktop client withdraws its registration with "
        "unregister_desktop instead of closing its socket (an older hub). Either way GET /mock/desktops says who is registered "
        "and the register / unregister / close events in order")


class Desktops:
    """Who is registered (socket -> device_name) and the register / unregister / close events in order."""

    def __init__(self):
        self.registered = {}
        self.log = []
        self.lock = threading.Lock()

    def message(self, ws, msg):
        """A socket's register_desktop or unregister_desktop."""
        if ws.client.get("role") != "full":
            return  # hub/desktops.go: an answer-only watch never becomes desktop presence.
        with self.lock:
            if msg.get("type") == "register_desktop":
                name = msg.get("device_name") if isinstance(msg.get("device_name"), str) else ""
                self.registered[ws.connection] = name
                self.log.append({"event": "register", "device_name": name})
            elif ws.connection in self.registered:  # a withdrawal only of a socket that registered
                self.log.append({"event": "unregister", "device_name": self.registered.pop(ws.connection)})

    def closed(self, ws):
        with self.lock:
            if ws.connection in self.registered:
                self.log.append({"event": "close", "device_name": self.registered.pop(ws.connection)})

    def state(self, h, req):
        """GET /mock/desktops."""
        with self.lock:
            state = {"registered": sorted(self.registered.values()), "log": list(self.log)}
        return h.send_json(state)


def register(d):
    desktops = Desktops()
    d.help(HELP)
    d.feature("desktops", "desktop_unregister", when=lambda: os.environ.get("MOCK_DESKTOPS") == "1")
    d.route("GET", "/mock/desktops", desktops.state)
    d.ws("register_desktop", desktops.message)
    d.ws("unregister_desktop", desktops.message)
    d.on_ws_close(desktops.closed)
