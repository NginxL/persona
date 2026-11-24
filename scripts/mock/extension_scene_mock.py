"""UI version 11's scene on a made-up extension page, a domain of scripts/mock/mock_domains.py (#12544).

MOCK_EXTENSION_SCENE=1 (with MOCK_EXTENSIONS=1): a made-up "Home" page: a two-room flat with wood and tile floors,
cutaway walls with a doorway and a glass window, a sofa, a round table, a TV and a bed, room labels and five device
pins in plain words. Tapping a pin pushes its device page; each draw toggles the bedroom lamp so a redraw changes pin
and light state while keeping the same key and shapes. An overview card shows the scene as a still preview. All names
are invented. MOCK_EXTENSION_SCENE_DOWNGRADE=1 answers as for an app before UI version 11 (the scene's fallback items).
apple/PersonaUITests/ExtensionSceneUITests.swift drives it.
"""
import json
import os

HELP = ("MOCK_EXTENSION_SCENE=1  (with MOCK_EXTENSIONS=1) A made-up Home page, UI version 11's scene (#12544): a two-room flat with floors, walls, windows, furniture, room labels and device pins in plain words; tapping a pin pushes its device page; each draw flips one lamp so a redraw changes state without moving the camera; an overview card shows the scene as a still preview",
        "MOCK_EXTENSION_SCENE_DOWNGRADE=1  (with MOCK_EXTENSION_SCENE=1) the Home page answers as for an app before UI "
        "version 11: the scene's fallback items instead of the scene")
MODE = os.environ.get("MOCK_EXTENSION_SCENE") == "1"
DOWNGRADE = os.environ.get("MOCK_EXTENSION_SCENE_DOWNGRADE") == "1"
PAGE = "/api/v1/extensions/example-home/-/ui/home-scene"
_HOME_DRAWS = [0]


def _home_scene_component(draw):
    bedroom_on = (draw % 2) == 1
    shapes = [
        {"kind": "prism", "points": [[-4.0, -2.6], [0.6, -2.6], [0.6, 2.6], [-4.0, 2.6]],
         "bottom": 0.0, "top": 0.06, "color": "#C9AD8C", "material": "wood"},
        {"kind": "prism", "points": [[0.6, -2.6], [4.0, -2.6], [4.0, 2.6], [0.6, 2.6]],
         "bottom": 0.0, "top": 0.06, "color": "#D8D3CA", "material": "tile"},
        {"kind": "box", "center": [0.0, 2.54], "size": [8.0, 0.12], "bottom": 0.06, "top": 2.5, "color": "#EAE6DF", "wall": True},
        {"kind": "box", "center": [-3.94, 0.0], "size": [0.12, 5.2], "bottom": 0.06, "top": 2.5, "color": "#EAE6DF", "wall": True},
        {"kind": "box", "center": [3.94, 0.0], "size": [0.12, 5.2], "bottom": 0.06, "top": 2.5, "color": "#EAE6DF", "wall": True},
        {"kind": "box", "center": [-2.7, -2.54], "size": [2.6, 0.12], "bottom": 0.06, "top": 2.5, "color": "#EAE6DF", "wall": True},
        {"kind": "box", "center": [-0.3, -2.54], "size": [2.2, 0.12], "bottom": 0.06, "top": 0.55, "color": "#EAE6DF", "wall": True},
        {"kind": "box", "center": [-0.3, -2.54], "size": [2.2, 0.06], "bottom": 0.55, "top": 2.2,
         "color": "#9EC4DC", "material": "glass"},
        {"kind": "box", "center": [2.4, -2.54], "size": [3.2, 0.12], "bottom": 0.06, "top": 2.5, "color": "#EAE6DF", "wall": True},
        # a wardrobe against the west wall: see-through while that wall is cut away (#13036)
        {"kind": "box", "center": [-3.56, -0.9], "size": [0.6, 1.4], "bottom": 0.06, "top": 2.1, "color": "#EFEBE4", "material": "wood"},
        {"kind": "box", "center": [0.6, 1.55], "size": [0.1, 2.1], "bottom": 0.06, "top": 2.5, "color": "#E2DDD4", "wall": True},
        {"kind": "box", "center": [0.6, -1.55], "size": [0.1, 2.1], "bottom": 0.06, "top": 2.5, "color": "#E2DDD4", "wall": True},
        {"kind": "box", "center": [-2.2, 1.4], "size": [2.1, 0.9], "round": 0.14, "bottom": 0.06, "top": 0.48, "color": "#788796"},
        {"kind": "box", "center": [-2.2, 1.76], "size": [2.1, 0.2], "round": 0.08, "bottom": 0.48, "top": 0.84, "color": "#6E7D8C"},
        {"kind": "cylinder", "center": [-2.1, 0.15], "radius": 0.46, "bottom": 0.06, "top": 0.44,
         "color": "#B58E65", "material": "wood"},
        {"kind": "box", "center": [-2.1, -1.75], "size": [1.7, 0.42], "round": 0.06, "bottom": 0.06, "top": 0.46,
         "color": "#8E7358", "material": "wood"},
        {"kind": "box", "center": [-2.1, -1.78], "size": [1.35, 0.06], "bottom": 0.5, "top": 1.24,
         "color": "#20252D", "material": "screen"},
        {"kind": "cylinder", "center": [-3.45, 1.95], "radius": 0.16, "bottom": 0.06, "top": 1.52,
         "color": "#9BA2AA", "material": "metal"},
        {"kind": "box", "center": [2.35, 1.05], "size": [2.0, 2.2], "round": 0.1, "bottom": 0.06, "top": 0.56, "color": "#DFD9CF"},
        {"kind": "box", "center": [2.35, 1.95], "size": [1.85, 0.35], "round": 0.08, "bottom": 0.56, "top": 0.82, "color": "#C8C1B6"},
    ]
    pins = [
        {"id": "living-lamp", "at": [-1.9, 0.3], "height": 2.35, "label": "Ceiling lamp", "state": "On",
         "icon": "light", "tone": "positive", "light": {"on": True, "warmth": "warm"},
         "action": {"id": "open", "values": {"id": "living-lamp"}, "push": {"ui": "home-scene", "state": "living-lamp"}}},
        {"id": "living-ac", "at": [-3.75, 0.1], "height": 2.05, "label": "Air conditioner", "state": "26 °C",
         "icon": "fan", "color": "blue",
         "action": {"id": "open", "values": {"id": "living-ac"}, "push": {"ui": "home-scene", "state": "living-ac"}}},
        {"id": "living-tv", "at": [-2.1, -1.78], "height": 1.0, "label": "TV", "state": "Not sure if on",
         "icon": "video", "tone": "caution",
         "action": {"id": "open", "values": {"id": "living-tv"}, "push": {"ui": "home-scene", "state": "living-tv"}}},
        {"id": "bedroom-lamp", "at": [2.35, 0.35], "height": 2.35, "label": "Bedroom lamp",
         "state": "On" if bedroom_on else "Off",
         "icon": "light", **({"tone": "positive"} if bedroom_on else {}),
         "light": {"on": bedroom_on, "warmth": "cool"},
         "action": {"id": "open", "values": {"id": "bedroom-lamp"}, "push": {"ui": "home-scene", "state": "bedroom-lamp"}}},
    ]
    return {
        "type": "scene",
        "key": "home",
        "alt": "Two-room flat with a living room on the west and a bedroom on the east.",
        "caption": "Drag to turn, pinch to zoom, tap a device for its controls.",
        "shapes": shapes,
        "labels": [
            {"text": "Living room", "at": [-0.9, -0.35], "height": 0.08},
            {"text": "Bedroom", "at": [2.3, -1.2], "height": 0.08},
        ],
        "pins": pins,
    }


def _home_scene_downgrade(scene):
    items = [{"type": "text", "text": scene["caption"]}]
    for pin in scene["pins"]:
        row = {"type": "row", "key": f"{scene['key']}:{pin['id']}", "title": pin["label"],
               "icon": pin.get("icon"), "action": pin.get("action")}
        if pin.get("tone"):
            row["badge"] = {"text": pin.get("state", ""), "tone": pin["tone"]}
        elif pin.get("state"):
            row["value"] = pin["state"]
        if pin.get("color"):
            row["icon_color"] = pin["color"]
        items.append(row)
    return items


def _home_scene_view(draw, device=None, ui_version=11):
    if device:
        return {"title": device, "sections": [{"key": "device", "items": [
            {"type": "header", "title": device, "subtitle": "A made-up device", "badges": [{"text": "On", "tone": "positive"}]},
            {"type": "text", "text": "This page stands for a device's own page: its switches, its temperature and its history."}]}]}
    scene = _home_scene_component(draw)
    body_items = [scene] if ui_version >= 11 else _home_scene_downgrade(scene)
    lamp_count = 2 if (draw % 2) == 1 else 1
    return {"title": "Home", "refresh_after": 30, "sections": [{"key": "home", "items": [
        {"type": "header", "title": "Two-room flat", "subtitle": f"{lamp_count} of 2 lamps on · 26 °C",
         "badges": [{"text": "Home", "tone": "positive"}]},
        *body_items]}]}


def mock_home_scene(handler, body, card=False):
    request = json.loads(body) if body else {}
    ctx = request.get("context") or {}
    ui_version = ctx.get("ui_version", 11)
    if DOWNGRADE:
        ui_version = 10
    state = request.get("state")
    device = state if isinstance(state, str) and state else None
    if request.get("action") == "open":
        device = (request.get("values") or {}).get("id") or "living-lamp"
        handler.send_json({"view": _home_scene_view(0, device, ui_version=ui_version)})
        return True
    if not card and not device:
        _HOME_DRAWS[0] += 1
    view = _home_scene_view(_HOME_DRAWS[0], device, ui_version=ui_version)
    if card:
        scene = _home_scene_component(_HOME_DRAWS[0])
        items = [scene] if ui_version >= 11 else _home_scene_downgrade(scene)[:4]
        view = {"title": "Home", "sections": [{"key": "home", "items": items}]}
    handler.send_json({"view": view})
    return True


def register(d):
    d.help(*HELP)
    if not (MODE and d.hub.PLUGINS_MODE):
        return  # off: the catalog lists no Home page; the help lines stay
    d.hub.EXTENSION_UI_EXTRA.extend([
        {"plugin": "example-home", "plugin_name": "Example Home", "id": "home-scene", "title": "Home",
         "placement": "nav_page", "size": "full", "icon": "home"},
        {"plugin": "example-home", "plugin_name": "Example Home", "id": "home-scene-card", "title": "Home",
         "placement": "overview_card", "size": "compact", "opens": "home-scene"},
    ])
    # mock_hub.py's plugins_route answered both from its GET and POST chains, with the request's body (none on a GET).
    d.route(["GET", "POST"], PAGE + "(?P<card>-card)?",
            lambda h, req: mock_home_scene(h, req.body, card=bool(req.match["card"])))
