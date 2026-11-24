"""More of the main conversation's tasks waiting for the owner, a domain of scripts/mock/mock_domains.py (#12831).

MOCK_DOCK=1 (with MOCK_TASKS): the To Decide pager over the composer gets several kinds: the widget fix asks to run a
command (a tool approval) and the blog migration asks a question read off its terminal (a screen card with no tool use
id). GET /mock/reset puts both back unanswered, and the network tests' question.
apple/PersonaUITests/QuestionDockTests.swift drives it.
"""
import copy
import os

HELP = ("MOCK_DOCK=1  (with MOCK_TASKS) two more of the main conversation's tasks wait for the owner, a tool approval "
        "and a terminal question, so the To Decide pager has several kinds; /mock/reset puts them back unanswered (#12831)")
MODE = os.environ.get("MOCK_DOCK") == "1"


def cards(hub):
    return [
        {"id": "ix_dock_widget_bash", "machine_id": "macbook", "session_id": hub.WIDGET, "session_title": "修小组件横屏崩溃",
         "cwd": "/Users/me/code/weather", "kind": "permission", "created_at": hub.ago(minutes=20),
         "expires_at": hub.later(minutes=40), "tool_name": "Bash", "tool_use_id": "toolu_dock_bash",
         "tool_input": {"command": "xcodebuild test -scheme WeatherWidget -destination 'platform=iOS Simulator,name=iPhone 17'",
                        "description": "跑小组件的测试"}},
        {"id": "scr_studio_blog_6a1d0e9c", "machine_id": "studio", "session_id": hub.BLOG, "session_title": "博客迁移到 Astro",
         "cwd": "/Users/me/code/blog", "kind": "screen", "created_at": hub.ago(minutes=10), "expires_at": hub.later(hours=24),
         "screen": " 旧文章的固定链接怎么处理？\n ❯ 1. 保留旧链接\n   2. 换成新格式并加跳转\n",
         "dialog": {"question": "旧文章的固定链接怎么处理？", "detail": "", "rev": "6a1d0e9c", "options": [
             {"key": "1", "label": "保留旧链接", "keys": ["1"]},
             {"key": "2", "label": "换成新格式并加跳转", "keys": ["2"]}]}},
    ]


def register(d):
    d.help(HELP)
    if not MODE:
        return
    hub = d.hub
    own = cards(hub)
    hub.INTERACTIONS.extend(copy.deepcopy(own))
    # The network tests' question too: a test answers it in the pager, the next starts with it again.
    again = own + [copy.deepcopy(x) for x in hub.INTERACTIONS if x["id"] == "ix_tests"]

    def reset():
        """After mock_hub.py's reset: the cards back, unanswered (as it does for its own card fixtures)."""
        ids = {x["id"] for x in again}
        hub.INTERACTIONS[:] = [x for x in hub.INTERACTIONS if x["id"] not in ids] + copy.deepcopy(again)
        hub.RESOLVED[:] = [r for r in hub.RESOLVED if r["id"] not in ids]
        hub.INTERACTION_HISTORY[:] = [x for x in hub.INTERACTION_HISTORY if x["id"] not in ids]
    d.on_reset(reset)
