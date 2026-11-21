# Persona Runtime SDK

产品名为 Persona。私有 npm 包名为 `persona-runtime-sdk`（#753），按 `package.json.name` 使用；它与自动更新包 ID 不是同一身份。DSH / pi 的更新 ID `persona-dsh-runtime` / `persona-pi-runtime` 和线上子协议 `persona-runtime.v1` 保留，不能按 npm 名称机械替换。本文使用迁移版本的 `personad`（`personad` 兼容），见 [配置与兼容说明](../../docs/operations/config.md#命名与兼容) 和 [完整保留清单](../../docs/operations/rename.md)。

把一个没有终端、自己管会话的 Agent 运行时（runtime）接进 Persona 的 SDK：`runtime.sock` 协议的 runtime 一端。
连不上本机 socket 的常驻服务用监听模式当 daemon（`createRuntime(driver, {listen: {port, token}})` 或 `rt.accept(req, socket, head)`），
由 node 去连，见 [`docs/integrations/daemons.md`](../../docs/integrations/daemons.md)。
规范（要实现什么、内容和交互的约定、验收清单）见 [`docs/integrations/runtimes.md`](../../docs/integrations/runtimes.md)，线上协议见 `docs/protocol/protocol.md`「Runtime ↔ node」。

| 文件 | 内容 |
| --- | --- |
| `index.js` | 入口：`createRuntime`（driver + 选项；返回的 `rt` 还有 `recent()` / `touched()` 挑要列的会话）和 item 的工具（`page` 分页、`appToolName` 工具名、`light`、`clipInput`、`snippet`、`summaryOf`、`interactionId`） |
| `lib/runtime.js` | 连接 / 重连 / `runtime.hello`、列表推送、watch 补发和连续性、逐字节流、问手机、「谁先回答」、逐请求 token 用量与可选订阅额度窗口上报 |
| `lib/history.js` | 按时间段列会话（`listHistory`）：窗口（`active_at` → `updated_at` → `started_at`）、排序、分页和游标；`rt.history()` / `historyPage` 用它（`docs/integrations/runtimes.md`「History」） |
| `lib/items.js` | 页里的长度上限、截断说明、稳定的交互 id |
| `lib/wire.js` | 和 `server/internal/rpc` 同一种 JSON 行 RPC |
| `lib/loader.js` | 装在别的程序里的 runtime 的加载器（`createLoader`）：从带哈希的版本目录加载实现，node 带来新版本时取包、核对、等手机上的卡、换上，失败回旧的（`docs/integrations/runtimes.md`「Auto-update」） |
| `lib/package.js` | 包：文件清单（和 personad 的 `runtimepkg.Packages` 一样）、哈希、核对、原子写入存放目录；`installCurrent`（固定路径 `current` 的原子切换，给会自己重新加载的宿主，pi）、`selfPackage`（一个模块跑的是哪个包、在不在存放目录里） |
| `lib/listen.js` | daemon 的端点：握手（token、子协议 `persona-runtime.v1`）和服务端的 WebSocket 帧，`runtime.js` 的监听模式用它 |
| `testing/fake-node.js` | 单元测试用的假 node（临时 socket，记录 runtime 说了什么，像 node 一样调它） |
| `testing/node-harness.mjs` | 端到端用：起一个真的 `personad node --stdio`（隔离的 HOME），脚本当 hub |
| `testing/model.mjs` | 端到端用的可编排假模型（`scriptedModel(DSH_CHECKOUT)`：DSH 的 llm-mock-server，按阶段换剧本） |
| `testing/conformance.mjs` | 端到端的通用验收（`conformance({...})`：文字回合、批准、带理由的拒绝、提问、逐字、中断、推送连续） |
| `examples/echo-runtime.mjs` | 最小的完整 runtime，新 runtime 的骨架（`--listen <端口>` 当 daemon） |
| `e2e/echo.mjs` | 验收模板：`PERSONAD=<personad> node e2e/echo.mjs` |
| `e2e/daemon.mjs` | daemon 的验收模板：echo 例子当 daemon，node 从 `daemons.json` 去连：`PERSONAD=<personad> node e2e/daemon.mjs` |

`npm test` 跑单元测试。没有依赖、不用构建：Node 22.19+。

监听模式的 echo 示例在 #773 后优先读取 `PERSONA_DAEMON_TOKEN`，兼容 `PERSONA_DAEMON_TOKEN`；新名显式空值不会退回旧 token。线上子协议仍为 `persona-runtime.v1`，不因变量名或私有包名迁移而改变。
