# Urgent calls

When something can't wait, the assistant rings the owner's iPhone like a phone call (#8734). Answering starts a voice conversation with Persona about that matter: what happened, what it needs, and the owner answers or decides by voice. Audience: everyone using Assistant; the settings are plain words and the feature is off until the owner turns it on.

Status (#8734): built so far are the relay's `voip` push (`../operations/push-relay.md` "VoIP pushes"), the hub's `call.ring` and phone registration, the manager's side: the settings, the gates, the calls and their retries and reminders, and the iPhone app: PushKit and LiveCommunicationKit, the companion notification, the call page one turn at a time (the speech extension's voice or the phone's, the phone's own recognition) and Settings › Notifications › Urgent Calls. The web shows each call as a row in its conversation at the time it rang (`GET /api/v1/assistant/calls?conversation=`, read again on the `urgent_call` message): how it went, opening to why Persona called, what came of it and what was said. A realtime voice call (the voice extension's `mode: "call"`) follows.

An urgent call is the top step of the existing escalation (P0 push + Needs you, `../design/proposals/main-chat-availability.md`), never a parallel channel: whenever a call is not allowed or cannot reach the phone, the matter goes out exactly as a P0 push and stays in Needs you.

## When it rings

Off by default (`assistant.urgent_call.enabled`). When on, only two kinds of matter ring:

- **The owner asked for this one** (`why: "owner_asked"`): a specific request in the conversation ("call me if the deploy breaks", "call me at 7 tomorrow"). The tool carries `owner_quote`, which must appear word for word (spacing aside, at least 4 characters) in a message the owner wrote in that conversation within 7 days, among its last 50 turns: not the assistant's words, a message forwarded in, someone else's in a group or a scheduled run's prompt. A scheduled follow-up runs as a turn in the conversation that asked for it, so its call quotes the owner's request there.
  A general "call me if anything comes up" is not a request for any one matter.
- **It matches a rule in "what counts as urgent"** (`why: "rule"`, `rule: <id>`), and is happening now or about to, so that waiting makes it worse. The list starts with three rules the owner may edit or delete (and restore):
  - `money_loss`: money or resources being lost: runaway charges, deleted resources, fraudulent payments;
  - `harm`: personal harm or safety: smoke, gas or water alarms, a call for help, a fall alert;
  - `agent_disaster`: a catastrophic agent error: data deleted for good, a production incident, an irreversible outbound send (keys pushed to a public repository, a mass email to customers).

  The owner adds rules in free text, in Settings or by telling Persona ("from now on, call me about anything to do with picking up the kids"). Persona reads the rule back and adds it with `persona_settings` (`urgent_call.rules`: get, add, update, remove); like every setting the assistant changes, it takes effect only once the owner approves the card on the phone. Every rule goes into the assistant's instructions with its id. An unedited default uses the fuller definition in the prompt (`prompts/assistant/urgent_rule_<id>.md`), in the owner's language (Chinese, English or Japanese, as the assistant answers), as is the rule text a call keeps.

While urgent calls are off the assistant sees nothing of them: the `urgent_call` tool is listed and the instructions' "加急来电" section written only while they are on (the manager tells the hub with `assistant.tools_changed`, and the node starts the conversation again with the new instructions on its next idle message).

Before calling about an agent's error, the assistant stops or pauses that agent when it can.

### Hard gates (manager)

A call that fails any of these goes out as a P0 push instead and is counted as `call_skipped` with the reason:

| Gate | Reason |
| --- | --- |
| The feature is on | `off` |
| `owner_asked`: the quote is the owner's own words in this conversation within 7 days | `no_quote` |
| `rule`: the id is in the current list; `happening` is one factual line (8–120 characters) | `no_rule`, `no_happening` |
| There is something for the owner to do: an open card, or the notice's action | `no_action` |
| The matter has not rung before (same card or `related_key`), the retry aside | `already_called` |
| Not inside the urgent-call quiet hours, if the owner set any (none by default) | `quiet` |
| Fewer than `daily_cap` matters rang today (1–10, default 3); a matter's one retry is not counted again | `cap` |
| At least 10 minutes since a call about another matter | `spacing` |
| A phone can take calls (VoIP registered, its relay knows `voip`) | `no_phone` |

The call record keeps which rule matched (id and its text at the time).

## Flow

```
assistant / supervisor: urgent_call{title, why, owner_quote | rule + happening, card?}
  → manager gates → urgent_calls row (ringing) → bus call.ring → hub
hub → each phone with a VoIP token: voip push, sealed, ttl 30 s
phone: PushKit → open the payload → report the call (LiveCommunicationKit) + companion notification
  ├─ 45 s unanswered → missed → manager rings once more after 3 minutes → still missed: fallback
  ├─ declined → no retry; P0 push, the card stays in Needs you
  ├─ handled elsewhere (the card answered) → manager broadcasts the end → phone ends the call
  └─ answered → voice: start{protocol 2, target: the matter's conversation, mode: "call", call_id}
                 or one turn at a time (speech extension / the phone's voice; the phone's recognition)
     → Persona says what happened and why now → the question → the owner decides → Persona reads it back
       → the answer goes to the card → what happens next → hang up
  → ended: duration, outcome, transcript on the urgent_calls row; a call row in the conversation
```

### Mainland China iPhones

LiveCommunicationKit, not CallKit's `CXProvider`: apps on the China App Store may not use CallKit, while LiveCommunicationKit (iOS 17.4+) is what calling apps there use. No binary in the app may even link CallKit; CI and the release checks enforce that (`../operations/release.md` "No CallKit"). On some iPhones sold in mainland China the incoming call shows only as a top banner or in the Dynamic Island. So every call also posts a silent time-sensitive **companion notification** ("Persona is calling you: <title>") that stays after the call UI goes and opens the call page; it is withdrawn once the call is answered, ends or is handled elsewhere.

After two unanswered calls: a time-sensitive push "Missed urgent call: <title>", the Feishu reminder where set up, the card at the top of Needs you with **Call back**, and reminders at 10 and 30 minutes until it is handled anywhere.

Calls ring like ordinary calls: aloud in ring mode, vibration only in silent mode.

### On the phone: the shared call manager (#9290)

The urgent call is a system call of the app's one call manager (`PersonaCalls`, `docs/apple/calls.md`), shared with the owner's calls to Persona: one LiveCommunicationKit `ConversationManager` and one state machine per call. `UrgentCallCenter` keeps what is the ring's own (the PushKit report, the companion notification, the 45 s ring, missed, the manager's reports). The manager has two call groups, so an urgent call during a call to Persona offers **Hold & Accept**; once the urgent call is over the app takes the call to Persona off hold again.

An iPhone or iPad app running on a Mac does not offer urgent calls. The Mac's LiveCommunicationKit lacks delegate-requirement symbols the iPhone framework exports, which otherwise stops dyld before the app opens (#9948). The framework is weakly linked, and the `isiOSAppOnMac` platform cut makes no manager or delegate and no VoIP registration; Settings › Notifications omits Urgent Calls. iPhone and iPad behavior is unchanged.

- **The name.** The incoming call's handle is the assistant's name (`Persona.name` of the look on screen, "Persona" for the built-in Mochi and when it has none; `CallHandle.name`), as for a call to Persona: the system's call UI, the Lock Screen and Recents show the handle's value and ignore its display name (FB24933340). The call's name (below, "Privacy") goes only in the display name, in case the system ever shows it, so the title never lands in Recents. The call id is no longer in the handle.
- **Sound.** Answered, the call sounds like a phone call: from the earpiece, or the Bluetooth headset or car that is connected, never the loudspeaker by itself (owner decision, 2026-11-17; it was the loudspeaker before #9290). The manager sets the same audio category for both kinds of call (`.playAndRecord`, `.voiceChat`, Bluetooth hands-free and A2DP, no `.defaultToSpeaker`). The page's **Speaker** and the system's call UI move it to the loudspeaker and back; while Bluetooth, a car or headphones can take the call the page's **Audio** opens the system's route picker instead.
- **The page.** The assistant's small face and its name in its header over Urgent Call (who is calling, as on the call screen: the call's own name, taken as it rang, as its handle says it), and the call screen's round controls under what the call is about (`docs/design/DESIGN_SYSTEM.md` "Call screen"): Mute, Speaker or Audio, Resume when the call is on hold and its sound did not come back, and Hang Up; its line says On Hold or Muted when it is.
- **Nothing in the app ends it.** While it rings (once the system shows it) or is answered, dictation, recordings and voice chats are refused, as during a call to Persona (`docs/apple/calls.md`, "Nothing in the app ends a call").
- **Mute** (the page, the system's call UI, AirPods): Persona stops listening and keeps speaking. What it was hearing when the mute came is not taken as the owner's answer; it listens again once unmuted, and a muted wait does not count as silence.
- **Hold** (Hold & Accept, a long press on Mute in the system's call screen, an interruption such as a phone call): nothing is said or heard until it ends; a line the hold cut is said again after it, shown once in the transcript. An interruption never ends the call: the app tries to get the sound back and, failing that, offers Resume.
- **Voice chat.** Answering during an in-app or CarPlay voice chat ends the voice chat first, so its audio cannot end the call's (`VoiceEnd.urgentCall`, "Ended for Persona's urgent call", with Continue); the chat keeps its loudspeaker while the call only rings. While an urgent call rings or is on, a voice chat (any page's Voice Chat, its Continue, CarPlay) and a call to Persona are refused with "Finish the urgent call first". A call Focus or Do Not Disturb kept from ringing refuses nothing until it is answered from its companion notification; answered there during a call to Persona, that call goes on hold first, as with Hold & Accept.

### Voice approval

On the call the owner may approve the call's own card, plans and tool permissions included, by voice: Persona reads the decision back and it counts only after the owner says "confirm". Every other card still needs a tap.

## Settings

`assistant.urgent_call` (manager setting, Settings › Notifications › Urgent calls; `GET` / `PUT /api/v1/assistant/urgent-call`):

```json
{
  "enabled": false,
  "rules": [
    {"id": "money_loss", "builtin": true},
    {"id": "harm", "builtin": true},
    {"id": "agent_disaster", "builtin": true},
    {"id": "r_3f9a", "text": "Anything about picking up the kids"}
  ],
  "daily_cap": 3,
  "quiet": null,
  "retry_minutes": 3
}
```

`builtin` rules without `text` use the built-in wording in the owner's language; editing one stores its `text`. `quiet` is `{"start":"23:00","end":"07:00"}` in the owner's time zone, or null.

## Wire

| Where | What |
| --- | --- |
| Tool `urgent_call` | `{title (≤ 30 characters, shown on the lock screen), why: "owner_asked" \| "rule", owner_quote?, rule?, happening?, interaction_id?, body?}` → text for the model: ringing (and when it rings again), or why it did not ring and that the matter went as a P0 notification |
| `persona_settings` `urgent_call.rules` | get, and add `{text}` / update `{id, text}` / remove `{id}` through the owner's approval card |
| A scheduled follow-up | Its run is a turn in the conversation that asked for it, so "call me at 7" is that turn's `urgent_call` with the owner's words |
| Bus `call.ring` (manager → hub) | `{call: {id, title, conversation_id, interaction_id?, attempt, ttl}}` → `{devices, reason?}` |
| VoIP push (sealed) | `{"urgent_call": {"id", "title", "manager", "conversation_id", "attempt"}, "iat", "hub_id"}` |
| `POST /api/v1/devices` | adds `voip_token`, `voip_grant`, `voip_error` (relay registration with topic `<bundle id>.voip`) |
| `GET /api/v1/assistant/calls?conversation=<id>` | The conversation's calls of the last 30 days, for its call rows |
| `GET /api/v1/assistant/calls/{id}` | the matter: title, why, rule text, happening, the card, conversation |
| `POST /api/v1/assistant/calls/{id}/state` | `{state: "answered" \| "declined" \| "missed" \| "ended" \| "seen", seconds?, outcome?, transcript?}` |
| `GET` / `PUT /api/v1/assistant/urgent-call`, `POST …/test` | The settings and how many matters rang today; a test call |
| WebSocket `urgent_call` (ws.broadcast) | `{id, state, conversation}`: ends a ringing call handled elsewhere |
| Voice `start` | `mode: "call"`, `call_id`; feature `call` |

## Counting

Every call is a work event (`urgent_call`, `urgent_call_skipped`, `urgent_call_ended`). North star interruptions get the category `urgent_call`, by why it rang; a matter counts once however often it rang, and the pushes after a missed or declined call are not counted again. Calls that did not ring are excluded as `call_skipped` by reason, and their P0 notification counts as a `notify_user` when a device took it; a test call is a `probe` (`north-star.md`). The briefing's line names urgent calls only on a day one rang.

## Privacy

- The VoIP push is sealed like every other push; the relay and Apple see only that one went.
- The incoming call shows the assistant's name (its handle; the system ignores the display name). The call's name follows "Show Details on Lock Screen" (Settings › Notifications), as notifications do: on, "Urgent: <title>", a few words the assistant writes for the lock screen (at most 30 characters); off (and before the app has read the setting), only "Persona Urgent Call". It goes only in the call's display name, never in Recents. The companion notification shows the title only when details are on. What happened and the card show only on the call page, after answering.
- No audio is recorded. The transcript lives with the conversation in the manager's database and goes when the conversation is deleted. Logs keep counts and states, never content.
- Without realtime voice, the owner's words are recognized on the phone; the speech extension, when the owner set one up, receives only what Persona says.
