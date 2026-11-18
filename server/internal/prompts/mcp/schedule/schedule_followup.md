Wake yourself up later: Persona sends `prompt` into this conversation (or a new one) as if the user wrote it, you do the work (a new hop chain, at most every 10 minutes), and the user gets a push with your answer (with `push: "asked"`, only what you send with notify_user). `at` is RFC 3339 or relative ("30m", "2h", "1d"). For a recurring task pass `every` ("30m", "2h", at least 5m) once: Persona repeats it; the user edits or stops it in the app; never schedule the next run from inside a run. Otherwise pass `cron` instead of `at` / `every`: a standard 5-field `minute hour day-of-month month day-of-week` in the user's time zone (`*/30 9-18 * * 1-5`, `0 10 1 * *`, or @hourly / @daily / @weekly / @monthly / @yearly; day of week 0 and 7 are Sunday; restricted day fields match either; runs at least 5 minutes apart). A bad expression is refused with the next runs listed. An `every` task can take `until` (RFC 3339 or 30m / 2h / 1d, not a bare clock time): after it the task turns off and records a skipped run. A one-off ignores `until`; omit it to keep an existing end time, null clears it. The task runs on Main chat's launcher, model and effort unless overridden with `launcher`, `model` and `effort`; permission mode stays the user's choice. A follow-up continuing this conversation (`new_conversation` false) runs on that conversation's launcher, so overrides require `new_conversation: true`. Pass `inbox` (the user's Inbox setting) only when requested. With `fresh_each_run: true`, a recurring task starts a new chat on every run and carries its state note.

```json
{
  "type": "object",
  "required": ["prompt"],
  "properties": {
    "fresh_each_run": { "type": "boolean" },
    "at": {
      "type": "string",
      "description": "when (the first run, with every); default with every: one interval from now"
    },
    "every": {
      "type": "string",
      "description": "repeat this often, e.g. 30m"
    },
    "cron": {
      "type": "string",
      "description": "repeat on a cron expression instead of at / every: minute hour day-of-month month day-of-week in the user's time zone, e.g. */30 9-18 * * 1-5"
    },
    "prompt": {
      "type": "string",
      "description": "what to do then, self-contained"
    },
    "title": {
      "type": "string",
      "description": "short label shown in the app"
    },
    "new_conversation": {
      "type": "boolean",
      "description": "start a fresh conversation instead of continuing this one"
    },
    "push": {
      "type": "string",
      "enum": ["every", "asked"],
      "description": "every (default for a new task): each run's answer is pushed to the user; asked: only what a run sends with notify_user, for a task that watches something or a standing loop (a patrol, a daily round). Continuing this conversation, a run's answer is then not pushed or sent to channels; from Main chat it runs in its own conversation instead. Omit to keep an existing task's"
    },
    "inbox": {
      "type": "string",
      "enum": ["off", "worthy", "every"],
      "description": "what of a run goes into the user's Inbox, independent of push: off (default for a new task), worthy (a run puts a row there with inbox_post only when it finds something worth a look), every (every run ends with a row made by Persona). Set it only when the user asked for it. Omit to keep an existing task's"
    },
    "until": {
      "type": ["string", "null"],
      "description": "optional end time (RFC 3339 or 30m / 2h / 1d). After it, Persona turns the task off and records a skipped run instead of running. Omit to keep an existing task's end time; null clears it. Ignored for a one-off."
    },
    "launcher": {
      "type": "string",
      "description": "this task's own launcher, instead of Main chat's (a name on the computer where its runs start). Needs new_conversation: true: a follow-up that continues this conversation runs on that conversation's launcher. Empty clears the override and follows Main chat again. Omit to keep an existing task's"
    },
    "model": {
      "type": "string",
      "description": "model id for the launcher above; without a launcher it pins the launcher the task runs on now. Empty uses the launcher's default. Omit to keep an existing task's"
    },
    "effort": {
      "type": "string",
      "enum": ["low", "medium", "high", "xhigh", "max"],
      "description": "thinking effort for Claude launchers; without a launcher it pins the launcher the task runs on now. Omit to keep an existing task's; empty clears it"
    }
  }
}
```
