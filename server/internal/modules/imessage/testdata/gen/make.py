#!/usr/bin/env python3
"""Builds testdata/home: a made-up Mac home with Messages' chat.db and an address book, every row synthetic.

The tables are Messages' and Contacts' own (chat-schema.sql, addressbook-schema.sql: CREATE TABLE / INDEX statements
of macOS 26, no triggers); the message texts that are only in attributedBody come from blobs.json (blobs.swift).
Numbers are in the reserved +1 555 01xx range, addresses at example.com. Run from this directory:
    python3 make.py
"""
import json
import os
import shutil
import sqlite3
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
HOME = os.path.join(HERE, "..", "home")
CHAT_DB = os.path.join(HOME, "Library", "Messages", "chat.db")
AB_DB = os.path.join(HOME, "Library", "Application Support", "AddressBook", "Sources", "S1", "AddressBook-v22.abcddb")
APPLE_EPOCH = 978307200  # 2001-01-01 in Unix seconds

BLOBS = {k: bytes.fromhex(v) for k, v in json.load(open(os.path.join(HERE, "blobs.json"))).items()}
# Starts like an archive and then makes no sense: the reader must count it, not crash.
BLOBS["broken"] = b"\x04\x0bstreamtyped\x81\xe8\x03\x84\x01\x40\x84\x84\x84\x12NSAttribu"


def fresh(path, schema):
    if os.path.exists(path):
        os.remove(path)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    db = sqlite3.connect(path)
    db.execute("PRAGMA page_size = 1024")  # small files: most of the schema's tables and indexes stay empty
    db.executescript(open(os.path.join(HERE, schema)).read())
    return db


def apple_ns(y, mo, d, h, mi):
    """Messages' date: nanoseconds since 2001-01-01 UTC."""
    t = datetime(y, mo, d, h, mi, tzinfo=timezone.utc).timestamp()
    return int(t - APPLE_EPOCH) * 1_000_000_000


def chat_db():
    db = fresh(CHAT_DB, "chat-schema.sql")
    handles = [  # ROWID, id, service
        (1, "+15550100", "iMessage"), (2, "+15550100", "SMS"), (3, "alice@example.com", "iMessage"),
        (4, "+15550101", "iMessage"), (5, "+15550102", "iMessage"), (6, "12345", "SMS"),
    ]
    for r, hid, svc in handles:
        db.execute("INSERT INTO handle (ROWID, id, service, country) VALUES (?, ?, ?, 'us')", (r, hid, svc))
    chats = [  # ROWID, guid, style (45 one-to-one, 43 group), chat_identifier, service, display_name, handles
        (1, "iMessage;-;+15550100", 45, "+15550100", "iMessage", None, [1]),
        (2, "SMS;-;+15550100", 45, "+15550100", "SMS", None, [2]),  # the same person over SMS: one conversation
        (3, "iMessage;-;alice@example.com", 45, "alice@example.com", "iMessage", None, [3]),
        (4, "iMessage;+;chat100000000000000001", 43, "chat100000000000000001", "iMessage", "周末徒步", [1, 4, 3]),
        (5, "iMessage;+;chat100000000000000002", 43, "chat100000000000000002", "iMessage", "", [4, 5]),
        (6, "SMS;-;12345", 45, "12345", "SMS", None, [6]),
    ]
    for r, guid, style, ident, svc, name, members in chats:
        db.execute("INSERT INTO chat (ROWID, guid, style, chat_identifier, service_name, display_name) VALUES (?, ?, ?, ?, ?, ?)",
                   (r, guid, style, ident, svc, name))
        for h in members:
            db.execute("INSERT INTO chat_handle_join (chat_id, handle_id) VALUES (?, ?)", (r, h))

    n = 0

    def msg(chat, handle, when, text=None, body=None, me=False, read=True, **extra):
        nonlocal n
        n += 1
        row = dict(ROWID=n, guid=f"00000000-0000-4000-8000-{n:012d}", text=text, attributedBody=BLOBS.get(body) if body else None,
                   handle_id=0 if me else handle, service=[s for (r, _, s) in handles if r == handle][0] if handle else "iMessage",
                   date=when, is_from_me=1 if me else 0, is_read=1 if (me or read) else 0, is_finished=1, is_sent=1 if me else 0)
        row.update(extra)
        cols = ", ".join(row)
        db.execute(f"INSERT INTO message ({cols}) VALUES ({', '.join('?' * len(row))})", list(row.values()))
        db.execute("INSERT INTO chat_message_join (chat_id, message_id, message_date) VALUES (?, ?, ?)", (chat, n, when))
        return row

    def attach(message, name, mime, size, uti):
        cur = db.execute("INSERT INTO attachment (guid, original_guid, filename, mime_type, transfer_name, total_bytes, uti) VALUES (?, ?, ?, ?, ?, ?, ?)",
                         (f"at_{message['ROWID']}_{name}", f"at_{message['ROWID']}_{name}", f"~/Library/Messages/Attachments/00/00/{name}", mime, name, size, uti))
        db.execute("INSERT INTO message_attachment_join (message_id, attachment_id) VALUES (?, ?)", (message["ROWID"], cur.lastrowid))

    # Zhang San (a Chinese-named contact), over SMS first and then iMessage.
    msg(2, 2, apple_ns(2026, 10, 27, 1, 0), text="示例：今天的快递到了吗")
    msg(1, 1, apple_ns(2026, 10, 27, 1, 5), text="到了，谢谢", me=True)
    plain = msg(1, 1, apple_ns(2026, 10, 28, 2, 0), body="plain")  # only attributedBody, like half of real messages
    msg(1, 1, apple_ns(2026, 10, 28, 2, 1), text="喜欢“示例消息：周五下午三点见”", me=True,
        associated_message_type=2000, associated_message_guid="p:0/" + plain["guid"])  # a tapback: not a message
    msg(1, 1, apple_ns(2026, 10, 28, 2, 3), text="好的，我准时到", thread_originator_guid=plain["guid"], thread_originator_part="0:0:12")
    photo = msg(1, 1, apple_ns(2026, 10, 28, 2, 10), text="￼", cache_has_attachments=1)
    attach(photo, "IMG_0001.HEIC", "image/heic", 1234567, "public.heic")
    msg(1, 1, apple_ns(2026, 10, 29, 3, 0), text="改一下：三点半", me=True, date_edited=apple_ns(2026, 10, 29, 3, 1))
    msg(1, 1, apple_ns(2026, 10, 29, 3, 2), date_retracted=apple_ns(2026, 10, 29, 3, 3))
    msg(1, 1, apple_ns(2026, 10, 30, 8, 0), body="emoji", read=False)

    # Alice, by address.
    msg(3, 3, apple_ns(2026, 10, 29, 9, 0), body="link")
    msg(3, 3, apple_ns(2026, 10, 29, 9, 30), body="long", me=True)
    audio = msg(3, 3, apple_ns(2026, 10, 29, 10, 0), text="￼", cache_has_attachments=1, is_audio_message=1)
    attach(audio, "Audio Message.caf", "audio/x-caf", 23456, "com.apple.coreaudio-format")

    # A named group, a rename and someone added.
    msg(4, 0, apple_ns(2026, 10, 30, 1, 0), me=True, item_type=2, group_title="周末徒步")
    msg(4, 4, apple_ns(2026, 10, 30, 1, 5), body="mention")
    doc = msg(4, 3, apple_ns(2026, 10, 30, 1, 10), text="示例：带水和零食￼", cache_has_attachments=1)
    attach(doc, "路线.pdf", "application/pdf", 345678, "com.adobe.pdf")
    msg(4, 1, apple_ns(2026, 10, 30, 1, 20), text="我也去", read=False)
    msg(4, 0, apple_ns(2026, 10, 30, 1, 30), me=True, item_type=1, group_action_type=0, other_handle=5)

    # A group without a name: named after its members.
    msg(5, 5, apple_ns(2026, 10, 30, 2, 0), body="mutable")
    msg(5, 0, apple_ns(2026, 10, 30, 2, 5), text="收到", me=True)

    # A short code nobody saved: a code, an archive that does not decode, and one dated in seconds (older macOS).
    msg(6, 6, int(datetime(2017, 1, 1, 0, 0, tzinfo=timezone.utc).timestamp() - APPLE_EPOCH), text="示例：旧系统的消息")
    msg(6, 6, apple_ns(2026, 10, 30, 3, 0), body="broken")
    msg(6, 6, apple_ns(2026, 10, 30, 3, 1), text="【示例】验证码 123456，5 分钟内有效", read=False)
    db.commit()
    db.execute("VACUUM")
    db.close()


def address_book():
    db = fresh(AB_DB, "addressbook-schema.sql")
    people = [  # Z_PK, first, last, organization, phones, emails
        (1, "三", "张", None, ["+1 (555) 010-0"], []),
        (2, "四", "李", None, ["555 0101"], []),  # saved without the country code
        (3, "Alice", "Example", None, [], ["Alice@Example.com"]),
        (4, None, None, "示例公司", ["+1 555 0199"], []),
    ]
    for pk, first, last, org, phones, emails in people:
        db.execute("INSERT INTO ZABCDRECORD (Z_PK, ZFIRSTNAME, ZLASTNAME, ZORGANIZATION) VALUES (?, ?, ?, ?)", (pk, first, last, org))
        for p in phones:
            db.execute("INSERT INTO ZABCDPHONENUMBER (ZOWNER, ZFULLNUMBER) VALUES (?, ?)", (pk, p))
        for e in emails:
            db.execute("INSERT INTO ZABCDEMAILADDRESS (ZOWNER, ZADDRESS) VALUES (?, ?)", (pk, e))
    db.commit()
    db.execute("VACUUM")
    db.close()


if __name__ == "__main__":
    shutil.rmtree(HOME, ignore_errors=True)
    chat_db()
    address_book()
    for p in (CHAT_DB, AB_DB):
        print(os.path.relpath(p, HERE), os.path.getsize(p), "bytes")
