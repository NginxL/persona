{{if eq .Language "日本語"}}{{if eq .Kind "review_requested"}}種類：GitHub PR のレビュー依頼（ユーザーがレビュアー）。通常は対応が必要
{{- else if eq .Kind "mention"}}種類：GitHub PR / issue でユーザーがメンションされた
{{- else if eq .Kind "team_mention"}}種類：GitHub でユーザーのチームがメンションされた（本人宛てではない）。多くは参考情報
{{- else if eq .Kind "author"}}種類：ユーザー自身の GitHub PR / issue に新しいコメントやレビュー。コード修正や質問は対応が必要。LGTM、承認、ボット通知は参考情報
{{- else if eq .Kind "ci_failed"}}種類：ユーザーが起動した GitHub Actions（CI）が失敗した
{{- else if eq .Kind "assign"}}種類：GitHub issue / PR がユーザーに割り当てられた
{{- else}}種類：GitHub 通知{{end}}{{else if .Language}}{{if eq .Kind "review_requested"}}Type: GitHub PR review requested from the user (listed as a reviewer), usually needs them
{{- else if eq .Kind "mention"}}Type: the user is mentioned in a GitHub PR / issue
{{- else if eq .Kind "team_mention"}}Type: the user's team, not the user personally, is mentioned on GitHub; usually FYI
{{- else if eq .Kind "author"}}Type: a new comment or review on the user's GitHub PR / issue; changes or questions need them, LGTM, approvals and bot messages are FYI
{{- else if eq .Kind "ci_failed"}}Type: GitHub Actions (CI) triggered by the user failed
{{- else if eq .Kind "assign"}}Type: a GitHub issue / PR is assigned to the user
{{- else}}Type: GitHub notification{{end}}{{else}}{{if eq .Kind "review_requested"}}类型：GitHub 上有人请用户 review 一个 PR（用户在 reviewers 里），一般需要用户
{{- else if eq .Kind "mention"}}类型：GitHub 的 PR / issue 里 @ 了用户
{{- else if eq .Kind "team_mention"}}类型：GitHub 上 @ 了用户所在的团队（不是用户本人），多数知悉即可
{{- else if eq .Kind "author"}}类型：用户自己的 PR / issue 有了别人的新评论或 review；要用户改代码、回答问题时需要用户，LGTM、批准、机器人消息知悉即可
{{- else if eq .Kind "ci_failed"}}类型：用户触发的 GitHub Actions（CI）失败了
{{- else if eq .Kind "assign"}}类型：GitHub 上有 issue / PR 分配给了用户
{{- else}}类型：GitHub 通知{{end}}{{end}}
