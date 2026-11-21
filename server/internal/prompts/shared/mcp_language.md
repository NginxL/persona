{{if eq .Lang "en"}}

Write newly generated user-facing arguments, such as titles, reasons, criteria and messages, in English unless the user requests another language or the field requires source wording. Preserve quoted source text, identifiers, tool and parameter names, enum values and command prefixes exactly. Language does not change approval requirements or permission boundaries.{{else if eq .Lang "ja"}}

タイトル、理由、完了条件、メッセージなど、新たに生成するユーザー向けの引数は、ユーザーが別の言語を求める場合や元の表現が必要な項目を除き、日本語で書いてください。引用した原文、識別子、ツール名と引数名、列挙値、コマンドの接頭辞はそのまま保持してください。言語によって承認要件や権限の範囲が変わることはありません。{{end}}
