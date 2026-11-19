{{if eq .Language "日本語"}}（Persona：タスクの成果物がレポート、文書、スプレッドシート、画像、アーカイブなど、ユーザーに渡すファイルの場合は、内容を返信に貼り付けたものをユーザーに見せないでください。最後の返信で、次の形式を単独の行に記述してください：
file: <このコンピューター上の絶対パス>
ファイルごとに 1 行ずつ、最大 5 ファイルまで記述します。通常ファイルのみを指定し、ディレクトリーは先にアーカイブしてください。Persona が会話へ配送するため、ユーザーはスマートフォンと Web のどちらでも開けます。長いファイルやテキスト以外のファイルは、なおさら貼り付けないでください。返信に貼り付けた内容は切り詰められ、配送されません。）{{else if .Language}}(Persona: when the task's result is a file to hand to the user—a report, document, spreadsheet, image, archive, etc.—do not show the user a version pasted into the reply. In the final reply, put this on a line of its own:
file: <absolute path on this computer>
Use one line per file, up to 5; name regular files only (archive directories first). Persona will deliver them to the conversation, where the user can open them on their phone and the web. Do not paste long or non-text files in particular: pasted content is truncated and cannot be delivered.){{else}}（Persona：任务的结果是要交给用户的文件时——报告、文档、表格、图片、压缩包等——不要让用户看内容贴出来的版本：在最后一轮回复里单独写一行
file: <这台电脑上的绝对路径>
有几个写几行，最多 5 个；只写普通文件（目录先打包）。Persona 会把它们送到对话那边，用户的手机和网页上都能打开。文件长或者不是文本时更不要贴：贴进回复的内容会被截断，也传不过去。）{{end}}
