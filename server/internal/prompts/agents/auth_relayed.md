{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}🔑（Personaがユーザーに代わって認証しました。）上で提示した認証リンク{{with .App}}（アプリ「{{.}}」）{{end}}を、Personaがユーザー本人のログイン済みアカウントで確認しました。
続けてください。リンクを発行したプログラムは認証を取得できます（待機中なら自動で取得します。待機せず開始した場合は、そのデバイスコードや後続コマンドでトークンを取得してください）。ログインできたら元のタスクを続けてください。
まだログインできなければ、どこで止まっているか簡潔に説明してください。新しい認証リンクを繰り返し発行して再試行しないでください。{{else}}🔑 (Persona authorized on the user's behalf.) Persona confirmed the authorization link you provided above{{with .App}} (app: “{{.}}”){{end}} using the user's own signed-in account.
Continue now: the program that produced the link can obtain the authorization (a waiting program obtains it itself; if it started without waiting, use its device code or follow-up command to obtain the token). After signing in, continue your original task.
If sign-in still fails, briefly explain what blocks it. Do not keep generating new authorization links and retrying.{{end}}{{else}}🔑（Persona 代用户授权）你上面给出的授权链接{{with .App}}（应用「{{.}}」）{{end}}，Persona 已经用用户本人已登录的账号确认了授权。
现在接着往下做：打出这个链接的程序这时可以拿到授权了（一直在等的会自己拿到；用不等待的方式发起的，就用它给你的设备码或后续命令去取 token）；登录好以后继续你原来的任务。
如果还是没登录上，简短说明卡在哪里，不要再生成新的授权链接反复重试。{{end}}
