"""Mail in the mock hub (server/internal/modules/mail, docs/extensions/mail.md; fake data only, first for the Android mail lane),
a domain of scripts/mock/mock_domains.py that moved out of mock_hub.py's "area: mail" (#11543).

The accounts (GET / POST /api/v1/mail/accounts, POST …/accounts/{id}/test, DELETE …/accounts/{id}) and the mail's write
paths: flags, sending, Google's and Microsoft's sign-in start. Nothing is sent anywhere: what the app posted is in GET
/mock/mail. MOCK_MAIL=1 adds whole mails for the reader (an HTML one with a tracking pixel, a script and an attachment; a
two-mail thread) and a Gmail account whose authorization ran out and a Feishu one; MOCK_GOOGLE=1 makes google/config say
configured and google/start answer (with a made-up URL). MOCK_OUTLOOK=1: an Outlook account signed in with Microsoft
(the web's Outlook badge and line, #2968); MOCK_OUTLOOK_CLIENT=1: its client ID is saved already. With
MOCK_INBOX_SOURCES=none (mock_hub.py's NO_SOURCES) there is no account at all.

mock_hub.py keeps the inbox's side: the two-mail thread's inbox row (MOCK_MAIL=1), among the inbox's own rows where the
switches after it still change them, and the mails made from inbox rows (MAIL_MESSAGES, MAIL_BODIES), which GET
…/messages/{account}/{uid} falls back to for a uid with no whole mail here.
"""
import json
import os
import re
import uuid
from urllib.parse import unquote

import mock_domains

MAIL_ACCOUNTS = [{"id": "acc1", "name": "个人邮箱", "email": "me@example.com", "provider": "imap", "auth": "password",
                  "imap_host": "imap.example.com", "imap_port": 993, "smtp_host": "smtp.example.com", "smtp_port": 465}]
MAIL_LOG = {"sent": [], "flags": [], "accounts_added": [], "deleted": [], "tested": []}
MAIL_FULL = {}
MS_CONFIG = {"configured": False, "client_id": None, "source": None, "redirect_uri": "persona-mail-oauth://outlook"}


def load(hub):
    """The switches' accounts and whole mails, as mock_hub.py made them when it loaded (hub.ago: its clock, NOW)."""
    global MAIL_ACCOUNTS, MAIL_FULL
    ago = hub.ago
    if hub.NO_SOURCES:
        MAIL_ACCOUNTS.clear()
    if os.environ.get("MOCK_OUTLOOK"):
        MAIL_ACCOUNTS.append({"id": "acc4", "name": "me@outlook.example", "email": "me@outlook.example", "provider": "outlook", "auth": "oauth2",
                              "imap_host": "outlook.office365.com", "status": "ok"})
    if os.environ.get("MOCK_MAIL"):
        MAIL_ACCOUNTS += [
            {"id": "acc2", "name": "me@gmail.example", "email": "me@gmail.example", "provider": "gmail", "auth": "oauth2",
             "imap_host": "imap.gmail.com", "status": "error", "error": "Google authorization expired or was revoked"},
            {"id": "acc3", "name": "我的飞书邮箱", "email": "me@corp.example.com", "provider": "feishu", "auth": "password",
             "imap_host": "imap.larkoffice.com", "status": "ok"},
        ]
        MAIL_FULL = {
            501: {"subject": "下周的分享能不能提前到周二？", "from": {"name": "Lucy Wang", "email": "lucy@example.com"},
                  "to": [{"name": "我", "email": "me@example.com"}], "cc": [{"name": "Tom", "email": "tom@example.com"}],
                  "date": ago(hours=3), "message_id": "<501@example.com>", "references": ["<499@example.com>"],
                  "text": "Hi，\n\n周四我临时要出差，分享能不能挪到周二下午？三点以后都行。\n\nLucy", "html": "", "seen": False},
            502: {"subject": "你的订单已发货", "from": {"name": "示例商城", "email": "shop@example.com"},
                  "to": [{"name": "", "email": "me@example.com"}], "date": ago(hours=5), "message_id": "<502@example.com>",
                  "text": "", "seen": True,
                  "html": "<html><head><script>alert(1)</script><style>.box{padding:12px}</style></head>"
                          "<body><div class=box><h2>你的订单已发货</h2><p>订单 <b>2026102800123</b> 已由快递揽收，预计<b>周五</b>送达。</p>"
                          "<p><img src=\"https://images.example.com/track.png\" width=1 height=1>"
                          "<img src=\"https://images.example.com/parcel.jpg\" alt=\"包裹\"></p>"
                          "<p><a href=\"https://shop.example.com/orders/2026102800123\" onclick=\"steal()\">查看物流</a></p></div></body></html>",
                  "attachments": [{"filename": "发票.pdf", "mime": "application/pdf", "size": 48213}]},
            505: {"subject": "Re: 季度复盘的时间", "from": {"name": "Jack Li", "email": "jack@example.com"},
                  "to": [{"name": "我", "email": "me@example.com"}], "date": ago(hours=30), "message_id": "<505@example.com>",
                  "text": "周三上午可以吗？", "html": "", "seen": True},
            506: {"subject": "Re: 季度复盘的时间", "from": {"name": "Jack Li", "email": "jack@example.com"},
                  "to": [{"name": "我", "email": "me@example.com"}, {"name": "Tom", "email": "tom@example.com"}], "date": ago(hours=1),
                  "message_id": "<506@example.com>", "references": ["<505@example.com>"], "flagged": True, "seen": False,
                  "text": "改到周三下午两点吧，会议室我订好了。\n\n议程：\n1. 上季度目标回顾\n2. 下季度计划\n\nJack", "html": ""},
        }
    if os.environ.get("MOCK_OUTLOOK_CLIENT"):
        MS_CONFIG.update(configured=True, client_id="11111111-2222-3333-4444-555555555555", source="app")


def mail_area(h, method, p, q):
    """The mail paths above (True when it answered)."""
    if p == "/mock/mail" and method == "GET":
        h.send_json(MAIL_LOG); return True
    if not p.startswith("/api/v1/mail/"): return False
    body = json.loads(getattr(h, "last_body", b"") or b"{}") if method != "GET" else {}
    parts = p.split("/")  # ['', 'api', 'v1', 'mail', 'messages', aid, uid, (flags)]
    if p.startswith("/api/v1/mail/messages/") and len(parts) >= 7 and parts[6].isdigit():
        uid = int(parts[6])
        if len(parts) > 7 and parts[7] == "flags":
            MAIL_LOG["flags"].append(dict(body, account_id=unquote(parts[5]), uid=uid))
            if uid in MAIL_FULL: MAIL_FULL[uid].update({k: body[k] for k in ("seen", "flagged") if k in body})
            h.send_json({"ok": True}); return True
        if method == "GET" and uid in MAIL_FULL:
            h.send_json({"message": dict(MAIL_FULL[uid], account_id=unquote(parts[5]), mailbox=(q.get("mailbox") or ["INBOX"])[0], uid=uid)})
            return True
        return False
    if p.startswith("/api/v1/mail/accounts/") and p.endswith("/test"):
        aid = unquote(parts[5])
        a = next((x for x in MAIL_ACCOUNTS if x["id"] == aid), None)
        MAIL_LOG["tested"].append(aid)
        h.send_json({"ok": a is not None and a.get("status") != "error", "error": (a or {}).get("error") or (None if a else "no such account")})
        return True
    if p.startswith("/api/v1/mail/accounts/") and method == "DELETE":
        aid = unquote(parts[5])
        MAIL_ACCOUNTS[:] = [a for a in MAIL_ACCOUNTS if a["id"] != aid]
        MAIL_LOG["deleted"].append(aid)
        h.send_json({"ok": True}); return True
    if p == "/api/v1/mail/accounts" and method == "POST":
        email = (body.get("email") or "").strip()
        if "@" not in email or not body.get("password"):
            h.send_json({"error": "email and password are required"}, 400); return True
        if body.get("password") == "wrong":
            h.send_json({"error": "imap login: wrong authorization code"}, 400); return True
        prov = body.get("provider") or "imap"
        dom = email.split("@")[1]
        acc = {"id": "acc_" + uuid.uuid4().hex[:6], "name": body.get("name") or email, "email": email, "provider": prov,
               "auth": "password", "imap_host": body.get("imap_host") or ("imap.larkoffice.com" if prov == "feishu" else "imap." + dom),
               "imap_port": body.get("imap_port") or 993, "smtp_host": body.get("smtp_host") or ("smtp.larkoffice.com" if prov == "feishu" else "smtp." + dom),
               "smtp_port": body.get("smtp_port") or 465, "status": "ok"}
        MAIL_ACCOUNTS.append(acc)
        MAIL_LOG["accounts_added"].append({k: v for k, v in body.items() if k != "password"})
        h.send_json({"account": acc}); return True
    if p == "/api/v1/mail/send" and method == "POST":
        if not body.get("to"):
            h.send_json({"error": "at least one recipient is required"}, 400); return True
        MAIL_LOG["sent"].append(body)
        h.send_json({"ok": True}); return True
    if p == "/api/v1/mail/oauth/google/config":
        h.send_json({"configured": bool(os.environ.get("MOCK_GOOGLE"))}); return True
    if p == "/api/v1/mail/oauth/google/start" and method == "POST":
        if not os.environ.get("MOCK_GOOGLE"):
            h.send_json({"error": "Google sign-in is not configured on this hub: an admin adds google.client_id to the manager's manager.json and restarts the hub. Until then, add Gmail with an app password.",
                         "code": "google_not_configured"}, 400); return True
        h.send_json({"auth_url": "https://accounts.example.com/o/oauth2/auth?state=mock", "callback_scheme": "com.example.mock", "state": "mock"})
        return True
    # Outlook's "Sign in with Microsoft": no client ID until the app saves one (MOCK_OUTLOOK_CLIENT=1: already there).
    if p == "/api/v1/mail/oauth/microsoft/config":
        if method == "PUT":
            cid = (body.get("client_id") or "").strip()
            if cid and not re.fullmatch(r"[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}", cid):
                h.send_json({"error": "这不像 client ID：Entra 应用「概述」页的「应用程序(客户端) ID」，形如 xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"}, 400); return True
            MS_CONFIG.update(configured=bool(cid), client_id=cid or None, source="app" if cid else None)
            MAIL_LOG.setdefault("microsoft_client", []).append(cid)
        h.send_json({k: v for k, v in MS_CONFIG.items() if v is not None}); return True
    if p == "/api/v1/mail/oauth/microsoft/start" and method == "POST":
        if not MS_CONFIG["configured"]:
            h.send_json({"error": "还没有 Outlook 登录用的 client ID"}, 400); return True
        h.send_json({"auth_url": "https://login.example.com/common/oauth2/v2.0/authorize?state=mock", "callback_scheme": "persona-mail-oauth", "state": "mock"})
        return True
    return False


def route(h, req):
    """mock_hub.py's GET and POST chains asked mail_area, and then answered GET /api/v1/mail/accounts itself; a request
    neither answers goes on to them (the mails made from inbox rows, GET /api/v1/mail/messages)."""
    if mail_area(h, req.method, req.path, req.query):
        return True
    if req.method == "GET" and req.path == "/api/v1/mail/accounts":
        return h.send_json({"accounts": MAIL_ACCOUNTS})
    return False


def register(d):
    load(d.hub)
    d.route("GET", "/mock/mail", route)
    d.route(mock_domains.METHODS, r"/api/v1/mail/.*", route)
