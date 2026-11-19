Only the current messages are new activity to triage or answer. Use background history only to understand them; do not treat an older request as newly received or reopen it merely because it appears in history.

Current messages (new activity):
{{ctx .Current}}{{with .History}}

Background history (context only, not new requests):
{{ctx .}}{{end}}
