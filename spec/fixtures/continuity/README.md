# 对话同步的连续性

本领域记录当前 iOS 的同步行为，供纯 Kotlin 核心逐步回放。依据是 [协议](../../../docs/protocol/protocol.md)「items 的连续性」「折叠的工作」、[重设计 D1、D4](../../../docs/archive/redesign/README.md) 和 [流程图 11](../../../docs/archive/redesign/flows.md)。夹具共 29 个用例、70 个事件步骤，数据全部编造。

## 来源与规则

下表缩写均指仓库内的只读来源：

- **Store**：[ChatStore.swift](../../../apple/Shared/ChatStore.swift)，含 `ChatStore`、`Item.folds` 和 `Item.isWork`。
- **Model**：[AppModel.swift](../../../apple/Persona/App/AppModel.swift)。
- **API**：[APIClient.swift](../../../apple/Shared/APIClient.swift)。
- **Tests**：[ChatStoreTests.swift](../../../apple/PersonaTests/ChatStoreTests.swift) 的 `TurnTests`。
- **FoldTests**：[FoldedWorkTests.swift](../../../apple/PersonaTests/FoldedWorkTests.swift)。
- **Corpus**：[continuity_corpus_test.go](../../../server/internal/transcript/continuity_corpus_test.go)。

| 规则 | 来源 | 覆盖用例名 |
| --- | --- | --- |
| 连续批次按 id 合并，`from_seq <= covered` 时取 `max(covered, to_seq)`。 | Store `apply`；Tests `aBatchThatDoesNotContinueRevealsAHole`；Corpus `simClient.apply` | `append_then_fill_lost_batch`、`overlapping_batch_never_regresses_covered` |
| 没有 item 的行仍计入 `covered`。 | Store `apply`；Tests `emptyBatchesMoveCoveredForward` | `empty_batch_advances_covered` |
| 已有基线时，`from_seq > covered` 保留新消息但不推进位置，并安排稍后补洞。 | Store `apply`；Model `settle`；协议「items 的连续性」 | `append_then_fill_lost_batch` |
| 从零接续的首批推送可以直接建立基线。 | Tests `aBatchFromTheFirstLineIsABaseline`；Store `apply` | `first_line_push_is_baseline` |
| 无基线且首页未到时先等首页，首页已到仍无基线时改取最新页。 | Tests `noFillBeforeABaseline`、`aLostFirstPushOfANewSessionIsNoticed`；Store `apply` | `wait_for_first_backlog`、`lost_first_push_after_empty_backlog` |
| 订阅补发带 `more_after: true` 时继续补洞。 | Tests `aCappedCatchUpAsksForTheRest`；Store `apply` | `capped_subscription_requests_rest` |
| 补洞定时器触发时使用当时的 `covered`，即使等待期间缺口已被补发填上也会请求一次。 | Model `settle`、`fill` | `timer_uses_current_covered` |
| 第八页仍返回补洞需求时立即改取最新页，不发第九次 `after_seq` 请求。 | Model `fill`；Corpus `simRun.fill`；流程图 11 | `eight_fill_pages_fall_back_to_latest` |
| `history` 改变时清空旧状态，完整首页或从零开始的批次可直接接收，其他接续批次要求重取。 | Store `apply`、`reset`；Tests `anotherHistoryStartsOver` | `history_changes_reset_local_items`、`new_history_from_zero_is_self_contained` |
| 最新页首条位置大于 `covered + 1` 时替换缓存，相邻或重叠的页合并。 | Store `apply`；Tests `aLatestPageThatDoesNotReachTheCacheReplacesIt` | `latest_page_replaces_distant_cache`、`adjacent_latest_page_merges` |
| 重取页保留同一历史中超过页尾的推送，历史不同时不保留。 | Store `replace`；Tests `theLatestPageKeepsPushesThatOvertookIt` | `reload_keeps_overtaking_pushes`、`reload_discards_other_history_pushes`、`eight_fill_pages_fall_back_to_latest` |
| 重连订阅只在 `covered > 0` 时带 `since_seq=covered`，同时请求 `fold_work`。 | Model `subscribeMessage`、`didConnect`；协议「items 的连续性」 | `reconnect_subscribes_from_covered`、`reconnect_without_baseline_omits_since_seq` |
| 缺失 `has_more` 表示该响应未提供信息，并保留本地已有判断。 | Store `merge`；Tests `catchUpNeverEndsTheHistory`；CLAUDE.md 分页经验 | `missing_has_more_stays_unknown`、`catch_up_cannot_end_history` |
| 一批消息没有覆盖本地最早消息时不能把 `has_more` 改成 false，首页前的补发也如此。 | Store `merge` 的 `reachesStart`；Tests `catchUpNeverEndsTheHistory` | `first_catch_up_does_not_override_cached_history`、`catch_up_cannot_end_history` |
| `merge` 仅在 backlog 且首次加载或合并后条数不超过本批条数时接受分页标志，接受 false 还须覆盖最早消息。 | Store `merge` | `page_reaching_oldest_can_end_history`、`smaller_page_cannot_overwrite_history_flag` |
| 历史分页用 `prepend` 合并并直接设置 `has_more`，不改变 `covered` 或 `history`。 | Store `prepend`；Model `loadOlder` | `older_page_sets_has_more_without_moving_covered` |
| 缺少 `to_seq` 时按老 hub 规则合并并以末条位置推进，`more_after` 只能触发重取。 | Store `apply`；Tests `olderHubsWithoutSpansWorkAsBefore` | `legacy_batches_merge_without_gap_detection` |
| 普通 item 的重复 id 保留第一次收到的版本。 | Store `merge`；CLAUDE.md 缓存版本经验 | `same_id_plain_item_keeps_original` |
| 新 fold 替换范围内的工作，同 id 且范围变大的 fold 也替换旧 fold。 | Store `dropFolded`；FoldTests `aFoldReplacesTheStepsItCoversAndOpeningItPutsThemBack` | `fold_replaces_work_and_expands_in_pages` |
| fold 的闭区间按 seq 和数字块序号判断，范围内的 narration 被替换，范围外的工作和最终正文保留。 | Store `Item.folds`、`Item.isWork`；FoldTests `narrationTextFollowsItsFoldInBothDirections` | `fold_range_uses_numeric_block_bounds`、`fold_preserves_steps_outside_range` |
| 展开按 `from_seq…through_seq` 分页取回，`more` 时从末条 seq 加一继续，完成后一次替换 fold。 | API `foldedWork`；Model `unfold`；Store `unfold` | `fold_replaces_work_and_expands_in_pages` |
| 展开响应中的 fold 不再合入，展开本身不改变连续位置或分页判断。 | Store `unfold` | `unfold_discards_fold_items_in_response` |

## JSON 结构

每个主题文件为 `{"version": 1, "cases": [...]}`。用例包含公共字段 `name`、`description`、`source`、`input`、`expected`，以及 `from_test` 或 `derived`。

- `append_and_gaps.json`：正常追加、缺口、首批基线、补洞上限。
- `history_and_cache.json`：历史重写、缓存替换、重连。
- `pagination_and_legacy.json`：分页信息、老 hub、重复 id。
- `folded_work.json`：fold 合并及展开。

`input.initial` 是进入事件序列前的状态，字段均必填：

| 字段 | 含义 |
| --- | --- |
| `items` | 按 `(seq, id 最后一个 # 后的数字)` 排序的线上 Item 数组；保留正文、工具关联和 fold 信息供比较 |
| `covered` | 从本地已加载范围起连续覆盖到的行；零表示尚无基线，不能用最大 item.seq 代替 |
| `history` | transcript 身份，字符串或 `null`；示例 `h`、`old`、`new` 均为编造 |
| `has_more` | `true` 表示还有更早消息，`false` 表示已知没有，`null` 表示尚未接受过有效分页判断 |
| `loaded_once` | 当前 Store 是否已收到 backlog；磁盘缓存本身不把它变为 true |

`has_more: null` 是夹具记录的信息状态。当前 iOS 的 `ChatStore.hasMore` 是 Bool，初始与 reset 后均为 false，尚无独立的 unknown 存储；对照 iOS 时将 `null` 投影为 false。响应字段缺失或 `null` 都映射成 Swift 的 `nil`，不会把已有 true/false 清成未知；history reset 才清掉这份判断。

`input.events` 与 `expected` 等长，按下标一一对应。每步的 expected 包含完整状态字段、按顺序的 `item_ids`、`gap`（`none` / `fill` / `reload`）和 `actions`。`items` 必须完整比较，避免只核对 id 而漏掉正文或 fold 范围变化。`gap` 是本步 `apply` 的返回值；无 `apply` 的事件为 `none`，不表示前一步发出的异步任务已取消。

### 事件

| `kind` | 处理方式及字段 |
| --- | --- |
| `http_latest` | HTTP 首页响应：`items`、可选 `history/from_seq/to_seq/has_more`；调用 `apply` 时 `backlog=true` |
| `ws_items` | WS 推送或订阅补发：`items`、可选 `history/from_seq/to_seq/backlog/has_more/more_after`；缺失 Bool 参数默认为 false，`has_more` 仍为未知 |
| `fill_timer` | 之前安排的补洞等待结束；开始一次最多八页的补洞，正在补洞则不另起一次 |
| `http_after` | 当前补洞请求的响应：`items`、可选范围字段和 `more_after`；沿用 `AppModel.fill`，不把 `has_more` 传给 Store |
| `http_reload` | 重取最新页的响应：`items`、必填 `has_more`、可选范围字段；调用 `replace`，不等同于首次 `apply` |
| `load_older` | 用户请求历史；本地 `has_more=true` 且 items 非空时发出 `before_seq` 请求 |
| `http_older` | 历史页响应：`items`、必填 `has_more`；调用 `prepend`，范围字段不参与连续性判断 |
| `reconnect` | 重连完成，按当前 `covered` 生成订阅动作 |
| `open_fold` | 传入现有 `fold_id`，发起范围请求；本格式同一时间只展开一个 fold |
| `http_range` | 当前展开的范围响应：`items`、可选 `more`；多页期间只积累工作，结束时调用 `unfold` |

这些是回放事件，不是新的线上 envelope。每个用例的虚拟任务计数从零开始；响应紧跟对应请求或作为已在途请求的结果输入。HTTP 失败、任务取消、并行展开、跨会话调度不在本格式覆盖范围内。

### 动作

`actions` 只列本步新产生的动作，空数组表示没有新增动作；它不是全部待执行任务的快照。

| `kind` | 参数与含义 |
| --- | --- |
| `schedule_fill` | `delay_ms: 1500`，等定时器触发后再读取 covered |
| `fetch_after` | `after_seq`，补洞的接续位置 |
| `fetch_latest` | `fold_work: true`，改取最新一页 |
| `subscribe` | `fold_work: true`，已有基线时带 `since_seq`，否则省略该键 |
| `fetch_older` | `before_seq`、`fold_work: true` |
| `fetch_range` | `from_seq`、`through_seq`，两端都包含；下一页按末条 seq 加一 |

请求中的机器、会话、鉴权、`lazy_images` 等不影响本领域判定的公共参数省略。`delay_ms` 是 `AppModel.settle` 的调度常量；实际等待延迟未实测，不作性能结论。测试不睡眠，由 `fill_timer` 表达等待结束。

## 对照验证

在仓库根目录运行：

```bash
cd server
go test -count=1 -p 1 ./internal/transcript/...
```

The shared Rust core (#6871) replays every case too: directly against `persona-core`, then through its runtime with
encoded actions and the list rebuilt from drained patches. Android's `:core:rustcore` does the same through the real
JNI binding:

```bash
cd native/app-core && cargo test --locked
cd android && ./gradlew :core:rustcore:test
```

[continuity_fixtures_test.go](../../../server/internal/transcript/continuity_fixtures_test.go) 读取本目录全部 JSON，拒绝未知字段、未知事件、重复名称及缺少逐步期望的用例，逐步比较完整 items、位置、分页信息、加载标志和动作。

Go 测试保持原有 corpus 测试不变，额外直接调用原有 `simClient.apply` 对照以下七个场景：`append_then_fill_lost_batch`、`empty_batch_advances_covered`、`overlapping_batch_never_regresses_covered`、`first_line_push_is_baseline`、`wait_for_first_backlog`、`capped_subscription_requests_rest`、`latest_page_replaces_distant_cache`。其余语义使用按当前 Store 和 Model 写的测试模型，避免改变 corpus 既有行为。

本次另在 `/tmp/spec-cont-swift/` 编译了临时回放程序，直接引用未修改的 iOS 源文件；29 个用例、70 步的完整 Item、item id 顺序、`covered`、`history`、`hasMore`、`loadedOnce` 和 `Gap` 全部通过。程序同步运行，不等待异步行渲染和延迟存盘；每个用例用独立的 `fixture` 缓存键初始化，结束后删除该键，不读取真实会话缓存。没有仅靠现有单测断言、未做 Store 对照的用例。

临时 Swift 程序按任务要求不提交。重新制作程序时按下面步骤执行，比较逻辑直接读取本目录 JSON，不把 Go 模型翻译成 Swift：

1. 用 `JSONSerialization` 读取每个文件的 `cases`；用仓库的 `JSON.decoder` 将 `initial.items` 和每步 `items` 解为 `[Item]`。
2. 创建唯一的 `SessionKey(machineID: "fixture", sessionID: "continuity-<UUID>")`，调用 `ChatCache.save` 写入初始 items、`has_more ?? false`、history、covered，再构造 `ChatStore(key:)`；`loaded_once=true` 时调用 `store.merge([], backlog: true)`。
3. 按上方事件表直接调用 `apply`、`replace`、`prepend`；`open_fold` 保存原 fold，`http_range` 累积完整数组，最后调用 `store.unfold(fold, into: work)`；定时器、重连和翻历史请求不直接改 Store。
4. 每步断言 `store.items == expected.items`、id 顺序、covered、history、hasMore（`null` 投影成 false）、loadedOnce、apply 返回的 Gap；任何不一致都报用例名、步骤和字段并退出非零状态。
5. 每个用例用 `defer { ChatCache.remove(key) }` 删除临时缓存；保持入口同步，不让异步任务在退出后写回测试数据。

入口放到 `/tmp/spec-cont-swift/main.swift` 后，在仓库根目录编译并运行：

```bash
mkdir -p /tmp/spec-cont-swift/modules
swiftc -module-cache-path /tmp/spec-cont-swift/modules \
  -swift-version 6 -default-isolation MainActor -enable-bare-slash-regex \
  apple/Shared/Models.swift apple/Shared/JSONValue.swift \
  apple/Shared/MachineMetrics.swift apple/Shared/FleetState.swift \
  apple/Shared/TextMemo.swift apple/Shared/ChatStore.swift \
  /tmp/spec-cont-swift/main.swift -o /tmp/spec-cont-swift/replay
/tmp/spec-cont-swift/replay spec/fixtures/continuity/*.json
```

本次编译器为 Swift 6.4。工具环境禁止启动编译器内部的嵌套 sandbox，因此本次编译额外使用 `-Xfrontend -disable-sandbox`；源码和观察宏均保持原样，外层工具权限不变。普通本机编译无需添加这个参数。

验证边界：Swift 对照调用真实 Store，但没有启动完整 `AppModel`、`APIClient` 或网络；请求动作由 Go 模型逐步断言，并逐项核对 `settle/fill/reload/subscribeMessage/foldedWork` 的控制流。`null` 所表达的知识状态也是规格层附加记录，Swift 只能核对其 Bool 投影。`SyncEventTests.swift` 已作为只读参考核查，但其中 revs、lark、会话模式和懒图片断言不属于连续性状态机，留给 wire 领域。

## 与简写规则的差异

- 任务中的「同 id 的 item 被新版本覆盖」仅对范围变化的 fold 成立；普通 item 在当前 iOS 中保留旧值，CLAUDE.md 也记录了这个缓存行为，因此夹具分别覆盖这两种情况。
- 流程图 11 把 history 变化统一画成重新取页；Store 对已经自包含的新首页或从零接续的批次会清空后直接接收。
- 「补洞超过八页」在实现中是第八页仍需继续便重取；不会先发第九页请求。
- `has_more` 的未知值在响应中保留为可选值，Store 内部仍为 Bool；缺失字段不能抹掉已有分页判断，空批次的 false 也不能证明已经到头。
- `replace` 不主动把 `loadedOnce` 置为 true；history reset 后紧接 reload 时该值仍为 false，夹具保留这一实现细节。

这些差异均以当前 iOS 行为为准，本车道未修改客户端或服务端生产代码。

## 新增用例

先定位 Store 或 Model 的对应分支及现有测试，再选择主题文件。新增英文 snake_case 名称和中文说明，填写可定位的来源；移植已有单测用 `from_test`，补充分支用 `derived: true`。沿用现有单测但增加定时器、请求或范围分页步骤时，`source` 同时列出调度实现。

编写独立的 `input.initial`，逐事件手写完整 expected；用不存在于任何真实会话中的示例内容。运行 Go 回放和临时 Swift 对照，更新上方规则表；新增只属于调度层的动作时，明确写出无法由 Store 单独验证的部分。最后按顶层约定检查 JSON 排版、隐私和提交信息。
