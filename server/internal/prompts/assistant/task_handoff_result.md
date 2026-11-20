{{if eq .Result "received"}}Handoff result received{{else if eq .Result "failed"}}Handoff failed{{else if eq .Result "cancelled"}}Receiving to-do cancelled or removed{{else}}Handoff deadline passed without a matching result{{end}}. Receiving to-do: {{.Title}} ({{.Todo}}). Candidate: {{.Head}}. Requested check: {{.Check}}.{{with .Detail}}
Reported evidence: {{.}}{{end}}
