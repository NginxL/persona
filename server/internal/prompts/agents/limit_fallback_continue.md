{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}<persona-limit-fallback />
⏰（Personaからの再開依頼です。）この会話が使っていたランチャーは、作業が終わる前に利用上限に達しました。上限がリセットされるまで、Personaがこの会話を別のランチャーに移しました。
止まったときに進めていた作業を続けてください。まず上の進捗を確認し、完了した作業を繰り返さず、止まったところから再開してください。{{else}}<persona-limit-fallback />
⏰ (Persona is asking you to continue.) The launcher this conversation ran on reached its usage limit before the work was done, so Persona moved the conversation to another launcher until that limit resets.
Continue the work that was under way when it stopped. First check the progress above; do not repeat completed work. Resume where it stopped.{{end}}{{else}}<persona-limit-fallback />
⏰（Persona 叫你接着做）这个对话原来用的启动器额度用完了，事情还没做完；额度恢复前，Persona 先把对话换到了另一个启动器上。
接着做停下时正在做的事：先看一眼上面做到了哪一步，已经做完的不要重做，从停下的地方往下做。{{end}}
