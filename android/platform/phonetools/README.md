# Android 手机工具

本模块对应 `docs/archive/redesign/android.md` 5.12、`parity.md` 第 21 节和设置中的「手机数据」。提供通讯录搜索、日历查询与新建、当前位置、照片元数据与缩放取图、剪贴板、设备信息、本地通知、健康数据汇总（Health Connect）、运动记录列表与详情（Health Connect）及 Persona 当前通知列表，共 23 个工具。

Android 没有统一的系统提醒事项接口，故不注册 `phone_reminders_*`。健康数据通过 Health Connect 提供每天的汇总（`phone_health_summary`，安卓 14 起系统自带；写入健康数据 `phone_health_write_sample` 为 iOS 独有）。不申请通知监听权限，不读取其他 App 的通知。`phone_notifications_list` 是 Android 新增工具，只返回本应用当前显示的通知，不包含操作确认通知。

`phone_workouts_recent` 和 `phone_workout_detail` 读 Health Connect 的运动记录（`ExerciseSessionRecord`），距离、消耗、爬升和心率取自每段运动的时间窗口内的汇总；分段（每公里用时，扣除暂停）和每分钟心率只在详情里给出，算法与 iOS `WorkoutSplits` 一致，并有单测。需要 `READ_EXERCISE` 和 `READ_ELEVATION_GAINED`，它们与健康汇总的权限一起在「设置 › 手机数据 › 健康」里申请；未授权时工具报权限错误，不返回空列表。运动来源的名字用系统 App 名称，取不到时用包名。Dietary metrics use `NutritionRecord` totals per local day: `dietary_energy` in kcal and `protein`, `carbs`, and `fat` in g. `resting_energy` uses `BasalMetabolicRateRecord.BASAL_CALORIES_TOTAL` in kcal. Today's totals end at the query time. The new read permissions are `READ_NUTRITION` and `READ_BASAL_METABOLIC_RATE`; existing users can grant them under Settings › Phone data › Health.

`phone_health_types`, `phone_health_samples`, `phone_health_sources` and `phone_health_request_access` (#13740, `HealthRecords.kt`) read the 38 Health Connect record types of `HealthRecords.types` record by record, with the source package, device and metadata. Anchors carry Health Connect's page token while pages remain, then its changes token (taken before the first page), so later calls get only what changed. Access is `granted` / `not_granted`, or `unavailable` for a permission the manifest does not declare (`HealthRecordsTest` checks every type is declared). `phone_health_request_access` opens Persona's page in Health Connect, since a tool call has no activity result for the request contract. There is no `phone_health_watch`: the hub never wakes an Android phone, so a background snapshot would answer nobody.

## 和 iOS 对齐（2026-11-02）

hub 把各台手机同名的工具合并成一份定义（`server/internal/hub/hub.go` 的 `mcpTools` 取说明更长的那份），调用却可能发给任一台声明了它的手机（`phoneClient`）。所以除 `phone_notifications_list` 外，每个工具的名字、英文说明、参数（类型、说明、最小 / 最大值）和必填项都照 `apple/Persona/PhoneTools/*.swift` 逐字写（`PhoneToolCatalog`），连 iOS 说明里的「iOS version」「Allow Paste?」也不改。`PhoneToolCatalogTest` 拿 `src/test/resources/ios-phone-tools.json` 逐个比，夹具由 `python3 scripts/assets/export_ios_phone_tools.py` 从 iOS 源码导出（`--check` 只检查是否过期；iOS 独有的提醒事项和写健康数据不导出）。

- 参数照 iOS 读：数字超出范围夹到范围内，不是数字的当没传、用默认值；多余参数忽略；字符串去掉首尾空白，空的当没传（剪贴板写入除外，空字符串就是清空）。必填项由各工具自己检查，缺了回「参数错误 / Invalid argument: title is required」这样的句子。
- 返回结构和 iOS 一样：通讯录 `{query, count, results:[{id, name, nickname, company, job_title, phones[{label, number}], emails[{label, email}], birthday}]}`（没结果带英文 `note`，空查询按通讯录自己的顺序取前几个）；日程 `{start, end, count, truncated, events:[{id, title, start, end, all_day, calendar, location, notes 前 300 字, attendees[{name, role, status, is_current_user}]}]}`，`query` 同时查标题、地点和备注；照片 `{count, access: full|limited, assets:[{id, created, type, width, height, favorite, subtypes:["screenshot"], duration_seconds}]}`（没结果带 `note`，不传 `days` 不限时间）；取图是一段文字（`id, created, width, height, returned_width, returned_height, bytes`）加一个 `{"type":"image","data":…,"mime_type":"image/jpeg"}`；设备信息、剪贴板、通知、定位的键名也照 iOS（Android 用 `android_version` / `sdk` 代替 `ios_version`，定位多带 `provider` 和坐标系说明）。空字段不输出。时间一律是带偏移、到秒的 ISO 8601；只给日期或不带偏移的时间按手机时区读。
- 和 iOS 不同、有意保留的：剪贴板和定位只在 App 在前台时声明和执行（Android 后台读不到剪贴板）；视频取图返回预览帧（iOS 报错）；没有可写日历时建一个本地的 Persona 日历（iOS 报错）。
- Only Deny (or dismissing the dialog) returns the iOS sentence `The user declined on the phone.` (#1875). `PhoneConfirmations.confirm` says how a confirmation ended (`PhoneConfirmation`): a confirmation that couldn't be shown (in the background with notifications off), one withdrawn when the tools close and one whose notification was swiped away (ended at once, #3861) each get their own result, never the decline. Other failures are bilingual as on iOS, with the settings path "Persona › Settings › Connectors › This phone".
- `register_phone` 的 `device_name` 是「厂商 型号」；调用里 `args` 缺省或为 null 当成空对象。

## 当前 App 接线

App 已依赖 `:platform:phonetools`；`AppServices` 按配对及同一 device_id 创建 runtime／transport，将原始 tool_call 回调挂到唯一活动 socket，并转发应用前后台和常驻服务实际状态。`Root` 已注册手机工具页和全局确认浮层，配对结束撤下处理器并关闭 runtime／transport；旧配对缺 device_id 时不供给工具，需重新配对。

工具开关默认关闭并按配对保存，权限变化及执行条件失效会刷新 register_phone；真实授权、后台确认和与 iOS 同时在线的设备路由仍待验收。

## 行为与边界

工具执行前检查开关、权限、在线状态和前台限制；确认后再次检查。日历新建在前台展示完整参数摘要，在后台通过高优先级通知请求确认；允许按钮要求解锁，通知不可用、拒绝、断开连接都不会继续等待授权。通知中的令牌只在本次待确认期间有效，进程重启后失效。

**并发与确认排队（2026-11-12，对齐 iOS）**：`PhoneToolRegistry` 不再用一把全局 Mutex 串行所有调用。确认写进入 `PhoneConfirmations` 的 FIFO 队列（一次一张卡，按 token 回答），等待期间位置、照片、联系人、日历等读取可并发返回。同一个 `call_id` 重复到达时，`PhoneToolCallGate` 让它共享 in-flight leader 的结果或直接返回缓存的终态结果，绝不重复执行；follower 自己被取消不影响 leader。等待期限由 hub 的 `phoneConfirmWait` 决定，App 不再另设 90 秒倒计时；本地 `execute` 超时仍生效，但确认写的超时返回「结果不确定，可能已写入，不要自动重试」而不是诱导重试的普通 timeout 文案。确认写一旦开始本地 `execute`（写入 `writesInFlight`），连接断开、权限收回、前后台切换的 watcher 不再取消它，让它产生确定结果或失败；pairing runtime 关闭仍可取消，结果按 uncertain 处理。

**结果 outbox**：`transport.result` 返回 false 时，`PhoneToolResultOutbox` 只在 pairing runtime 内、内存中暂存结果（不是工具调用），连接恢复后按 FIFO 顺序补发；同一个 `call_id` 的新结果替换旧结果，容量有界。outbox 不落盘、不在进程重启后自动重放，也绝不调用 `registry` / `execute`；它补发的是结果，不是写入操作本身。`queue_if_offline` 由 hub 加 schema、接收并在转发前移除，Android 不实现第二队列。

日历优先使用指定的可写日历，其次默认可写日历；完全没有可写日历时创建 Persona 本地日历。指定名称找不到时直接报错，防止误写到其他日历。全天日程以 UTC 日期保存，结束日期不包含在内。本地日历在国产系统日历 App 中的展示尚待真机验证。

照片只读取系统允许的集合，支持 Android 14 部分授权；视频取图返回预览。没有申请媒体位置权限，因此照片元数据不含经纬度。联系人查询和日历返回量均有输入上限，照片缩放后以 MCP image 块返回。

定位使用平台 fused、GPS 和网络提供者，不依赖 Google 服务；GPS 标明 WGS-84，厂商定位标明坐标系待核对，不自动转换 GCJ-02。地址解析不可用时只返回坐标。旧版系统不调用阻塞的 Geocoder 接口。

延迟通知交给 WorkManager，受系统调度限制，不保证准点。已经创建的延迟通知属于用户已安排的本地操作；关闭工具或解除配对不会取消它们。通知投递时重新检查通知开关和渠道。

同一 runtime 内保留最近调用结果，重复 `call_id` 不重复写入；进程退出后不自动重放调用。插入操作发生后如果连接断开，服务端可能收不到结果，应先核对日历再重新调用。

权限行为参照 [Android 部分照片访问](https://developer.android.com/about/versions/14/changes/partial-photo-video-access)，通知确认参照 [Notification.Action.Builder](https://developer.android.com/reference/android/app/Notification.Action.Builder)。本模块运行性能、系统授权弹窗和国产系统后台行为均未实测。

## 验证

单元测试：`PhoneToolCatalogTest`（工具集合、每个工具的说明和 schema 与 iOS 夹具逐字一致、只有新建日程要确认、前台限定的工具、参数读取和夹取、时间解析和确认摘要、照片 id 和截图判断）、`PhoneToolRegistryTest`（开关、权限、前后台、离线、未知工具、超范围不拒绝、重复调用只执行一次、日历的拒绝原句 / 批准 / 批准后状态变化 / 参数错误不打扰用户）、`PhotoContentTest`（图片块用 `mime_type`），`core:hub` 的 `HubPhoneToolsTransportTest`（`args` 缺省或 null 当空对象）。2026-11-02 跑过 `:platform:phonetools:testDebugUnitTest`、`:core:hub:test` 和 `:app:assembleDebug`。

仍需在设备上验证：真实 WebSocket 往返（假 hub 的 `GET /mock/tool_call` 和 `GET /mock/phone`）、授权弹窗、前台确认对话框和后台通知的解锁确认、国产系统日历显示。

## Calling tool (English, 2026-11-21)

`phone_call_start` has the exact exported iOS description, parameters and required fields. Its Calls source is off by default, needs no Android permission and is offered only in the foreground. Every execution needs one existing confirmation; denial, a disabled source or a background phone starts no intent.

- Android accepts `kind: phone` (the default) with the same number validation and normalization as iOS. Blank, alphabetic, short and multiple-number input fails before confirmation. `facetime` and `facetime_audio` fail cleanly as unsupported, including email addresses.
- After Allow, the tool starts `ACTION_DIAL` with a normalized `tel:` URI and `FLAG_ACTIVITY_NEW_TASK`. It never uses `ACTION_CALL` or requests `CALL_PHONE`. The result is `handed_to_android`, not a claim that the call connected.
- A narrow `ACTION_DIAL` package-visibility declaration lets the tool detect a missing dialer. Missing handlers and launch failures return a definite "no call was started" error rather than the uncertain-write fallback.
- Robolectric `CallToolTest` covers schema use, confirmation, foreground rejection, validation, both handoff failures and permission absence. `PhoneToolsShotsTest` captures the Calls switch and confirmation in en / zh-Hans / ja, light / dark and large text. A physical dialer and Android device were not tested.

## Calendar and contact write tools (English, 2026-11-15)

Four confirmed write tools match iOS word for word: `phone_calendar_update_event`, `phone_calendar_delete_event`, `phone_contacts_create`, `phone_contacts_update`. Each needs one local Allow per call, goes through the same FIFO card queue, call-id de-duplication and result outbox as `phone_calendar_create_event`, and answers *uncertain* when the outcome of an approved write is unclear. Reads are never blocked by a card.

- **Permissions.** Contacts ask for `READ_CONTACTS` + `WRITE_CONTACTS`; the calendar for `READ_CALENDAR` + `WRITE_CALENDAR`. With read only, `phone_contacts_search` and `phone_calendar_events` stay available, the write tools are not registered and the source shows "Read only". `PhoneToolCatalog.confirmedWriteNames` is the one list of write tools.
- **Why the card is prepared.** Update and delete cards show before → after, so they need the current event or contact. The synchronous `ToolDefinition.confirmation` formatter only turns resources into text and must not read a provider. `confirmationNeedsPreparation` tools use the registry's suspend `prepareConfirmation` hook (run on `Dispatchers.IO` by `AndroidPhoneTools`), which reads the target, validates, plans the batch and returns the text plus an `execute` that carries the snapshot.
- **Card order.** A card queues in the order the calls *arrived*, not the order their preparation finished. `PhoneToolRegistry` hands each confirmed call an arrival ticket when it is received; the call queues its card in `PhoneConfirmations` only after every earlier ticket has queued its own card or given up (failed, timed out, was cancelled or was answered without a card), so a quick later call cannot jump ahead of a slower earlier one. Preparation that does not finish within the local timeout answers an ordinary timeout (nothing was written, so it is not *uncertain*) and frees the calls behind it. A later confirmed call therefore waits for an earlier one's preparation to finish or be given up, including a call that is cancelled while it waits for its turn; it does not wait for the earlier card to be answered. That wait is bounded by the local 30 second timeout constant in `PhoneToolRegistry` (a configured value, not a measurement); a provider read that ignores coroutine cancellation might hold the queue past it, which has not been tried on a device. Call-id de-duplication, cancellation and the result outbox are unchanged.
- **Stale snapshot rule.** `execute` re-reads the target and compares it field by field (calendar: series, occurrence, exception, attendees, reminders; contact: name, company, phone and email rows). A difference returns "nothing was written" and no batch runs. A failure inside the batch is uncertain and is never retried automatically.
- **Occurrences.** A repeating event needs `occurrence_start` that matches exactly one instance; `span` is `this` (default) or `future`. `this` is an exception event (or a cancelled one for delete), `future` ends the old series before the occurrence and, for an update, starts a new series in the same batch. Anything that cannot be done exactly is refused, never widened to the whole series.
- **Recurrence details.** An all-day event moved by `start` alone keeps its number of calendar days (a day is not always 24 hours). EXDATE / RDATE lists are read in the series' own zone (UTC for all-day), in either the provider's `ZONE;dates` form or the iCalendar `TZID=ZONE:dates` form, and written back with the same prefix. A date-only UNTIL on a timed series includes that whole day. The card shows the real length of a note it had to clip.
- **Contacts.** Without `replace=true` a different existing value is never overwritten and lists are appended without duplicates; with it scalars are overwritten and phones / emails are replaced wholesale (empty list clears). No contact deletion.
- **Tests (written, not run in the lane).** `CalendarWriteToolsTest` and `ContactsWriteToolsTest` use fake providers; `PhoneToolRegistryTest` covers the preparation and arrival order (slow first, fast later and the reverse), preparation timeout, cancellation while preparing and while queued for a turn, a card that fails to queue, concurrency, same call-id, failure and uncertain paths; `CalendarContactSummaryShotsTest` renders the cards in three languages, light / dark and the largest font. Fake providers prove generated operations only.
- **Real provider test.** `src/androidTest/.../CalendarContactsProviderTest.kt` runs on an emulator or a dedicated test phone, not a phone in daily use (`cd android && ./gradlew :platform:phonetools:connectedDebugAndroidTest`; it builds and runs; with a private adb server, install the `assembleDebugAndroidTest` APK and run `am instrument -w -e class …CalendarContactsProviderTest persona.platform.phonetools.test/androidx.test.runner.AndroidJUnitRunner`, as the Gradle test engine does not follow `ANDROID_ADB_SERVER_PORT`). It builds a throwaway local-account calendar (`CALLER_IS_SYNCADAPTER`) and placeholder contacts and removes only those afterwards; its projection check also reads, and ignores, real events in a small time window through the existing event query. First check: the Instances `RRULE` / `RDATE` projection of the existing `phone_calendar_events` query. Then one occurrence and this-and-future changes and deletes (COUNT, UNTIL, all-day, daylight saving), attendees and reminders copied once, a change made before a cut surviving a this-and-future change (with its attendee and reminder rows), a series in another zone than the phone's with EXDATE / RDATE seeded in the provider's `ZONE;dates`, the iCalendar `TZID=` and the UTC forms (the stored text is read back first and the tool's output is compared with that stored shape; a form the provider refuses is skipped, not passed), all-day length across clock changes, expected counts, a stale card, and contact create / add / replace / rename read-back. It ran on an API 35 Google APIs emulator only (22 tests pass; the `TZID=` date-list form is stored but not expanded by the provider, so those checks are skipped). Measured there: an exception of a series without a `_sync_id` is not applied to its instances, so the tools refuse it; the provider copies attendees and reminders onto an exception itself and rejects an exception DTEND; a shortened series is re-expanded only when DTSTART is written with the new rule. A phone maker's Calendar provider can differ and was not tried.
- **Needs a real device.** Whether the system Calendar accepts exception inserts through `CONTENT_EXCEPTION_URI`, the split series and its attendee / reminder rows; COUNT / UNTIL / EXDATE / RDATE, all-day and DST series; account sync after an edit; contacts with several raw contacts; the card on a real lock screen. OEM behaviour is unverified; see `scripts/app/android_adaptive_scenes/calendar-contacts-write.json` for the checklist.
