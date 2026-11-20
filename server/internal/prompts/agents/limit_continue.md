{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}<persona-limit-continue />
⏰（Personaからの再開依頼です。）サブスクリプションの利用枠がリセットされ、再び利用できます。
利用枠がなくなったときの作業を続けてください。まず上の進捗を確認し、完了した作業を繰り返さず、止まったところから再開してください。{{else}}<persona-limit-continue />
⏰ (Persona is asking you to continue.) Your subscription's usage allowance has reset and is available again.
Continue the work you were doing when the allowance ran out. First check the progress above; do not repeat completed work. Resume where you stopped.{{end}}{{else}}⏰（Persona 叫你接着做）订阅的用量额度已经重置了，可以接着用了。
接着做额度用完时你正在做的事：先看一眼上面做到了哪一步，已经做完的不要重做，从停下的地方往下做。{{end}}
