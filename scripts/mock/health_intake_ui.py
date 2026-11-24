"""Synthetic manager responses shared by the HTTP mock and bundled demo generator."""
from copy import deepcopy
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path

FIXTURES = Path(__file__).with_name("fixtures") / "health-intake-ui"


def pages():
    """Keep the hub's legacy and gauge-capable responses, in each exported language."""
    result = {}
    for path in sorted(FIXTURES.glob("*.response.json")):
        _, locale, version, page, _, _ = path.name.split(".")
        result[f"{locale}.{version}.{page}"] = json.loads(path.read_text())
    return result


def answer(body):
    context = body.get("context") or {}
    locale = "zh-Hans" if context.get("locale", "").startswith("zh") else "en"
    version = "v10" if context.get("ui_version", 4) >= 9 else "v4"
    state = json.loads(body.get("state") or "{}")
    kind = "protein" if state.get("kind") == "protein" else "water"
    edit = state.get("edit", False) or body.get("action") == "settings"
    page = "water-settings" if edit else kind
    result = deepcopy(pages()[f"{locale}.{version}.{page}"])
    view = result["view"]
    if edit and kind == "protein":
        view["title"] = "蛋白质" if locale == "zh-Hans" else "Protein"
        field = view["sections"][0]["items"][0]
        field.update(name="goal_g", value="60", label="每天蛋白质目标（克）" if locale == "zh-Hans" else "Daily protein goal (g)")
        view["sections"][0]["items"] = [field] + view["sections"][0]["items"][-2:]
        view["state"] = json.dumps({"kind": kind, "days": 7, "edit": True}, separators=(",", ":"))
    # Chart labels track UTC today even after the export day. Record clock labels stay synthetic.
    for section in view["sections"]:
        for item in section["items"]:
            for series in item.get("series", []):
                points = series.get("points", [])
                for index, point in enumerate(points):
                    point["label"] = (datetime.now(timezone.utc) - timedelta(days=len(points) - index - 1)).strftime("%m-%d")
    if body.get("action") and body["action"] != "settings":
        result["toast"] = "Sample action: " + body["action"]
    return result
