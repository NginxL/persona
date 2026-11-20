# 会话文件与 Git

`:feature:files` 提供 `SessionFilesScreen`，接收已有的 `ChatRoute`、会话目录、`SessionFiles`、返回对话和插入路径回调，可选 `initialPane` 和 `initialPath`（按路径直接打开，参数表最后一个）。文件与 Git 是对话页内的两种视图，不增加返回栈路由。功能说明、假 hub 和截图方法见 `docs/android/android.md`「Files and Git in chat」。

- 浏览：按绝对路径走，面包屑 chip，⋯「上一级文件夹」可以走到 `/`；隐藏文件开关存本机；Git 标记、大小和相对时间；读取失败显示 node 的原因。
- 文件操作（同 iOS `FolderEdits`）：hub 报 `file_edit`（或没报 features）时，文件行的 ⋯ / 长按提供重命名、移动和移到废纸篓。移动时选择目标文件夹，同名时可保留两者、替换或取消。移到废纸篓先确认，之后提示条带「撤销」。目录上传和多选还没做。
- 预览：Markdown 渲染（README 的 HTML 先转换）和源文件切换、代码行号和轻量高亮、图片缩放、PDF 逐页；拷贝路径 / 内容、分享（`FilesShareProvider`，authority `${applicationId}.files.share`）、插入路径。
- Git：分支和上游、改动和 diff（未暂存 / 已暂存）、提交历史（refs chip、HEAD、分页），提交详情的文件列表和逐文件 diff、拷贝提交号、完整日期。
- `initialPath`：相对路径按会话目录补全，`~` 原样交给 hub；文件直接打开预览，返回回对话；以 `/` 结尾或 hub 回 `is_dir` 的在浏览器里打开，作为面包屑的顶。

逻辑在可单测的地方：`FilesController`（`FilesState.kt`）管浏览、页面栈、返回和提交缓存，路径 / 时间 / 大小在 `FilesPaths.kt`，Git 的解读在 `GitText.kt`，Markdown 文本处理在 `FileMarkdownText.kt`。

验证命令（在 `android/` 执行）：

```sh
./gradlew --max-workers=3 :feature:files:testDebugUnitTest
```

单测：`FilesControllerTest`、`FilesPathsTest`、`GitModelsTest`、`FilesCriticalPathTest`、`FileEditsTest`（截图 `FileEditsShotsTest`）。真实 hub 和真机未验证；PDF 渲染和长 Markdown 的性能未实测。
