"""Messages (iMessage) in the mock hub (docs/plans/plan-connectors.md "5"), a domain of scripts/mock/mock_domains.py
that moved out of mock_hub.py (#11649): one Mac's Messages, read for the user's own eyes. What Settings › Connectors ›
Messages reads (GET /api/v1/imessage), the reader's chats and a chat's messages (GET …/chats, …/chats/{id}/messages),
"Give to Persona" (PUT …/settings) and deleting what the hub kept (DELETE …/data). Every name, number (+1 555 01xx) and
text is made up.

mock_hub.py keeps the connector: IMESSAGE_STATUS (MOCK_IMESSAGE=denied, IMESSAGE_DENIED: that Mac's node has no Full Disk
Access), IMESSAGE_CONNECTOR and _imessage_connector(), which GET /api/v1/connectors lists, and MOCK_IMESSAGE_INBOX's inbox
row. The routes here read and change them through the hub (HUB.IMESSAGE_STATUS), as the code did inside mock_hub.py.
"""
import json

HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register


def _imessage_status():
    if HUB.PLUGINS_MODE and not HUB._imessage_connector().get("enabled"):
        return None
    name = HUB._imessage_connector().get("account")
    return {**HUB.IMESSAGE_STATUS, "machine": name} if name else HUB.IMESSAGE_STATUS


def _im_person(handle, name=None):
    return {"handle": handle, "name": name} if name else {"handle": handle}


_ZHANG, _LI, _ALICE = _im_person("+15550100", "张三"), _im_person("+15550101", "李四"), _im_person("alice@example.com", "Alice Example")
IMESSAGE_CHATS = []  # load makes them, on the hub's clock
IMESSAGE_MESSAGES = {}


def load(hub):
    """The chats and their messages, as mock_hub.py made them when it loaded (hub.ago: its clock, NOW)."""
    global IMESSAGE_CHATS, IMESSAGE_MESSAGES
    ago = hub.ago
    IMESSAGE_CHATS = [
        {"id": "imessage-0a1b2c3d4e5f", "name": "张三", "members": [_ZHANG], "services": ["SMS", "iMessage"], "last": ago(minutes=8),
         "preview": "👍 好的，周五见 🎉", "unread": 1},
        {"id": "imessage-1b2c3d4e5f60", "name": "周末徒步", "group": True, "members": [_ZHANG, _LI, _ALICE], "services": ["iMessage"],
         "last": ago(minutes=40), "preview": "张三：我也去", "unread": 2},
        {"id": "imessage-2c3d4e5f6071", "name": "12345", "members": [_im_person("12345")], "services": ["SMS"], "last": ago(hours=3),
         "preview": "【示例】验证码 123456，5 分钟内有效", "unread": 1},
        {"id": "imessage-3d4e5f607182", "name": "Alice Example", "members": [_ALICE], "services": ["iMessage"], "last": ago(hours=20),
         "preview": "[语音]"},
        {"id": "imessage-4e5f60718293", "name": "李四、+15550102", "group": True, "members": [_LI, _im_person("+15550102")],
         "services": ["iMessage"], "last": ago(days=2), "preview": "收到", "from_me": True},
        {"id": "imessage-5f6071829304", "name": "王五", "members": [_im_person("+15550103", "王五")], "services": ["iMessage"],
         "last": ago(days=4), "preview": "示例：明天把文件带上", "from_me": True},
        {"id": "imessage-607182930415", "name": "示例快递", "members": [_im_person("95500")], "services": ["SMS"], "last": ago(days=9),
         "preview": "【示例快递】您的包裹已放到驿站，取件码 ••••"},
        {"id": "imessage-718293041526", "name": "赵六", "members": [_im_person("+15550104", "赵六")], "services": ["iMessage", "RCS"],
         "last": ago(days=40), "preview": "[图片]"},
    ]
    IMESSAGE_MESSAGES = {
        "imessage-0a1b2c3d4e5f": [
            {"id": "1", "at": ago(days=1, hours=3), "kind": "message", "sender": _ZHANG, "text": "示例：今天的快递到了吗", "service": "SMS"},
            {"id": "2", "at": ago(days=1, hours=2, minutes=55), "kind": "message", "from_me": True, "text": "到了，谢谢", "service": "SMS"},
            {"id": "3", "at": ago(minutes=50), "kind": "message", "sender": _ZHANG, "text": "示例消息：周五下午三点见，地方还是上次那家咖啡馆", "service": "iMessage"},
            {"id": "5", "at": ago(minutes=46), "kind": "message", "from_me": True, "text": "好的，我准时到", "service": "iMessage",
             "reply_to": "示例消息：周五下午三点见，地方还是上次那家咖啡馆"},
            {"id": "6", "at": ago(minutes=40), "kind": "message", "sender": _ZHANG, "service": "iMessage",
             "attachments": [{"kind": "image", "name": "IMG_0001.HEIC", "mime": "image/heic", "size": 1234567}]},
            {"id": "7", "at": ago(minutes=30), "kind": "message", "from_me": True, "text": "改一下：三点半", "service": "iMessage", "edited": True},
            {"id": "8", "at": ago(minutes=20), "kind": "message", "sender": _ZHANG, "service": "iMessage", "unsent": True},
            {"id": "9", "at": ago(minutes=12), "kind": "message", "sender": _ZHANG, "service": "iMessage", "undecoded": True},
            {"id": "10", "at": ago(minutes=8), "kind": "message", "sender": _ZHANG, "text": "👍 好的，周五见 🎉", "service": "iMessage", "unread": True},
        ],
        "imessage-1b2c3d4e5f60": [
            {"id": "12", "at": ago(hours=2), "kind": "event", "from_me": True, "text": "你把群名改成「周末徒步」"},
            {"id": "13", "at": ago(hours=1, minutes=58), "kind": "message", "sender": _LI, "text": "@张三 明天的示例行程改到十点出发"},
            {"id": "14", "at": ago(hours=1, minutes=50), "kind": "message", "sender": _ALICE, "text": "示例：带水和零食",
             "attachments": [{"kind": "file", "name": "路线.pdf", "mime": "application/pdf", "size": 345678}]},
            {"id": "15", "at": ago(hours=1, minutes=45), "kind": "message", "sender": _ALICE, "text": "还有防晒"},
            {"id": "16", "at": ago(hours=1, minutes=30), "kind": "event", "from_me": True, "text": "你把+15550102加进了群聊"},
            {"id": "17", "at": ago(minutes=42), "kind": "message", "from_me": True, "text": "我开车，能坐四个人"},
            {"id": "18", "at": ago(minutes=40), "kind": "message", "sender": _ZHANG, "text": "我也去", "unread": True},
        ],
    }


def imessage_get(h, req):
    """GET /api/v1/imessage, …/chats and a chat's …/messages, as mock_hub.py's GET chain answered them; any other GET
    under /api/v1/imessage goes on to it."""
    p = req.path
    if p == "/api/v1/imessage":  # Settings › Connectors › Messages; MOCK_IMESSAGE=denied: no Full Disk Access on the Mac
        status = _imessage_status()
        return h.send_json(status) if status else h.send_json({"error": "没有设置「信息」：在 设置 › 扩展 › 信息 选一台 Mac", "code": "not_configured"}, 404)
    if p == "/api/v1/imessage/chats" or p.startswith("/api/v1/imessage/chats/") and p.endswith("/messages"):
        if HUB.IMESSAGE_DENIED:
            return h.send_json({"error": HUB.IMESSAGE_STATUS["error"], "code": "no_permission"}, 403)
        if p == "/api/v1/imessage/chats":
            return h.send_json({"chats": IMESSAGE_CHATS})
        cid = p.split("/")[5]
        chat = next((c for c in IMESSAGE_CHATS if c["id"] == cid), None)
        if not chat:
            return h.send_json({"error": "没有这个会话", "code": "no_chat"}, 404)
        msgs = IMESSAGE_MESSAGES.get(cid) or [{"id": cid[-4:], "at": chat["last"], "kind": "message", "text": chat["preview"],
                                               "from_me": chat.get("from_me", False), "sender": None if chat.get("from_me") else chat["members"][0]}]
        return h.send_json({"chat": chat, "messages": msgs})
    return False


def imessage_write(h, req):
    """PUT /api/v1/imessage/settings and DELETE …/data, as mock_hub.py's POST chain answered them; PUT …/data and DELETE
    …/settings go on to it."""
    p = req.path
    if h.command == "PUT" and p == "/api/v1/imessage/settings":  # "Give to Persona" on / off
        body = json.loads(h.last_body or b"{}")
        if not isinstance(body.get("ai"), bool): return h.send_json({"error": "ai: true or false"}, 400)
        HUB.IMESSAGE_STATUS["ai"] = body["ai"]
        return h.send_json(HUB.IMESSAGE_STATUS)
    if h.command == "DELETE" and p == "/api/v1/imessage/data":  # what the hub kept, once "Give to Persona" is off
        if HUB.IMESSAGE_STATUS.get("ai"): return h.send_json({"error": "先关掉「交给 Persona」再删除", "code": "ai_on"}, 409)
        return h.send_json({"chats": 6, "people": 9, "inbox": 1})
    return False


def register(d):
    global HUB
    HUB = d.hub
    load(d.hub)
    d.route("GET", r"/api/v1/imessage(/.*)?", imessage_get)
    d.route(["PUT", "DELETE"], r"/api/v1/imessage/(settings|data)", imessage_write)
