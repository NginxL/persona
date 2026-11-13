# 设计 token

Design values shared across clients and documented platform-specific differences live here. iOS remains read-only in this phase: the existing `Theme.swift` and `StatusPalette.swift` stay in effect. The generator emits Kotlin, CSS and Markdown only; it does not emit `Tokens.swift` or modify client files automatically.

**当前严格检查会失败。** 保留 iOS 原值后，有些配色达不到文档要求；系统动态色也需要跨端回退。下文列出差异，检查器不会为它们降低阈值或默默放行。接入阻断式 pre-commit 前，需要协调者决定修色方案。

## 文件和来源

每个 token 都有 `$description`，写明来源或补充值。下表中的 Swift 路径均为只读参考。

| 文件 | 命名空间 | 来源 |
| --- | --- | --- |
| `color.semantic.json` | `semantic.<style>.<mode>` | `ios/Shared/Theme.swift` 的 `accent(_:)`、`userBubble(_:)`、`userBubbleText`、`pageBackground(_:)`、`card(_:)`、`cardStroke`；原生风格取 `prototypes/android/style.css` |
| 同上 | `common.<mode>` | `Theme.brand/danger/positive/caution/cautionText/ai/loop/countBadge`、代码和终端色、`Theme.Diff`、`Theme.Hue`、`Theme.Fill` |
| 同上 | `system.<mode>` | `Theme.systemPage/systemCard`、SwiftUI 的 `Color.primary/secondary`、UIKit 系统 fill 的显式回退值；见下文限制 |
| `color.status.json` | `status.<mode>.<state>` | `ios/Shared/StatusPalette.swift`；待唤醒和已屏蔽的映射取 `ios/Persona/Views/Chats/ChatsView.swift` 的分组表 |
| `dimension.json` | `radius`、`space`、`size`、`watch` | `Theme.Radius`、`Theme.Space`、`Theme.Size`、`Theme.Watch`；触控区取 `docs/archive/redesign/README.md` D7，胶囊按短边的一半。共享 `size.*` 必须与 iOS 同名同值 |
| `dimension-android.json` | `android.size` | Android 专用布局尺寸，不冒充 iOS 的 `Theme.Size`；由生成器输出 `Tokens.Android.Size`，与共享尺寸分开校验 |
| `type.json` | `type.<level>` | `Theme.Font` 的九个层级（display、title、heading、body、secondary、caption、sectionHeader、label、code）：文本样式、字重，以及 `Theme.Level` 的 iPhone 字号和行高倍数 |
| `type-glass.json` | `typeGlass.<level>` | Android Glass reading sizes only (#5780): body 16sp and secondary 14sp; an intentional platform difference that leaves the shared `type.*` values and iOS checks unchanged |
| `motion.json` | `motion` | `Theme.Motion` 的共享动效：state、layout、expand、drawer、springBack 是弹簧（时长和弹性，Compose 取阻尼比 `1 - bounce`、刚度 `(2π / 时长)²`），reveal、conceal 是时长和曲线，keyboard 是系统键盘的刚度和阻尼，needsYouBreath 是“需要你”呼吸环；减少动态效果时所有时长为零。它们是配置值，不是运行耗时的实测数据 |
| `surface.json` | `surface.<style>` | `ios/Persona/Views/Styling.swift` 的 `Surface`：材质、阴影颜色与偏移、描边；原生回退使用无阴影实心表面 |
| `rules.json` | `$extensions.persona` | `docs/apple/ios.md` 可读性底线、`docs/archive/redesign/ux.md` 第 7、8 节，以及 WCAG AA |

风格键为 `glass`（液态玻璃）、`claude`、`clay`（黏土）、`island`、`lark`（飞书）、`native`（Android 原生）；模式键为 `light`、`dark`。每个风格、模式必须有同样的语义色字段。

原生风格映射：`accent ← --primary`、`onAccent ← --on-primary`、`pageBackground ← --surface`、`card ← --sc`、`cardStroke ← --outline-var`、`text ← --on-surface`、`secondaryText ← --on-surface-var`、用户气泡取 `--bubble / --on-bubble`。这些是原型的静态回退色。Material You 壁纸动态取色由 Android 组件层接入，运行时配色仍须另做对比度检查，不能据此宣称动态取色已经验证。

## 状态色保持同一张表

| 键 | 含义 | iOS 来源 |
| --- | --- | --- |
| `needsYou` | 需要你 | `StatusPalette.needsYou` |
| `working` | 工作中 | `StatusPalette.working` |
| `backgroundWaiting` | Waiting in the background | `StatusPalette.backgroundWaiting`: working's dot at 0.6, `.secondary` words, working's dot at 0.08 as the tint |
| `unread` | 完成未看 | `StatusPalette.unread` |
| `idle` | 已看 | `StatusPalette.idle` |
| `offline` | 未运行 | `StatusPalette.offline` |
| `unknown` | 未知 | `AgentState.tone` 复用 `idle` |
| `waiting` | 待唤醒 | `ChatsView` 用 `StatusTone(dot: Theme.loop, text: Theme.loop)` |
| `muted` | 已屏蔽 | `ChatsView` 复用 `StatusPalette.idle` |

`dot` 用于点、图标和填充，`text` 用于文字，`tint` 用于浅底。需要你的点色来自 `Palette.systemBlue`，文字色为浅色 `#0064C8`、深色 `#409CFF`。需要你、工作中、完成未看与待唤醒的 `tint` 都是系统次要文字色乘以 `0.08` opacity，按本目录回退值计算后的 alpha 为 `0.048`。这里保存未合成的透明色，不能预先混白，否则放在深色背景上会变色。

已看与未运行引用系统次要文字色。点和浅底再乘上 iOS 的 opacity：已看为 `0.55 / 0.12`，未运行为 `0.3 / 0.08`。已屏蔽、未知继续通过引用复用已看，避免出现第二份灰色表。

## 格式与引用

采用 [Design Tokens Format 2025.10](https://www.designtokens.org/tr/2025.10/format/) 的常用字段和整值引用，以及 [Color Module](https://www.designtokens.org/tr/2025.10/color/) 的 sRGB 表达。生成器实现的是受限子集，不是完整标准解析器。

```json
{
  "example": {
    "red": {
      "$type": "color",
      "$value": {
        "colorSpace": "srgb",
        "components": [1, 0, 0],
        "alpha": 1
      },
      "$description": "示例颜色"
    },
    "alias": {
      "$type": "color",
      "$value": "{example.red}",
      "$description": "复用示例颜色"
    }
  }
}
```

- 文件内的对象层级组成 token 路径，文件名不参与路径。多个文件可以补充同一分组，不能重复定义 token。
- 叶子必须且只能包含 `$type`、`$value`、`$description`；分组可带 `$description`。名称使用小写开头的英文字母、数字和驼峰写法。
- 支持 `color`、`dimension`、`duration`、`fontWeight`、`number`，以及仓库扩展类型 `string`。颜色只接受 sRGB，RGB 与 alpha 都在 0…1 内。
- `dimension` uses `{ "value": 14, "unit": "px" }`. This is the design-space unit: Kotlin generation converts `type.*` and `typeGlass.*` to `sp` and other dimensions to `dp`; CSS divides font sizes by 16 to produce `rem` and keeps other values in `px`. This fixed mapping of logical units does not copy physical iOS pixels to Android.
- `duration` 接受 `ms` 或 `s`，必须能表示为整数毫秒；Kotlin 输出 `Int` 毫秒。`fontWeight` 输出 Compose `FontWeight`。
- 引用只能占据整个 `$value`，须与目标类型相同；不支持字符串插值、分量引用、隐式类型继承和组继承。缺失引用、循环引用、重复 JSON 键、非有限数值、生成名称冲突都会报错。
- 源文件按文件名和键排序后计算摘要。所有产物都带同一摘要，因此改了说明、规则或任何 token，也会要求重新生成。JSON 缩进和对象键顺序不影响产物。

## 系统色和字体的限制

iOS 的系统背景、文字与 fill 随系统环境变化，源码没有唯一 RGB。JSON 明确保存普通对比度下的跨端回退：主文字为黑白，次要文字为浅色 `#3C3C43 / 0.6`、深色 `#EBEBF5 / 0.6`；其余值在 `system` 分组中逐项说明。**这些值没有在 iOS 设备上采样，不能据此保证系统灰色与 iOS 渲染逐像素一致。** 固定写在 Swift 中的状态 RGB 和 opacity 则由测试直接对照源码。

Font weight and `iosTextStyle` come from `Theme.Font`; size and lineHeight come from the iPhone branch of `Theme.Level`, checked by `scripts/assets/test_gen_tokens.py`. lineHeight is a multiple of size, and iOS adds it only to content text. Android Glass leaves line height to the platform; components must allow wrapping and growth. `family` is a role hint: the platform selects a system or monospaced face, while Clay uses its rounded face. Fixed-height containers must not clip text after system font scaling. Glass long-form roles use the Android-specific 16sp / 14sp values in `typeGlass` (#5780); the other Glass roles keep their shared `type` mappings, and Brief / Board read neither group.

`Theme.Radius` 的五档圆角用于实际界面。`AppStyle.corner` 中的 20、24、26、10 只供设置预览，源码已说明它们不是实际卡片圆角，因此没有用它们覆盖 `radius.card`。胶囊输出 `shape.capsulePercent = 50`，Compose 用百分比圆角；圆形用平台 `CircleShape`。

## 生成与检查

只依赖 Python 标准库。以下命令从仓库根目录运行，产物放在临时目录，本车道不提交构建产物。

```sh
python3 scripts/assets/gen_tokens.py \
  --kotlin /tmp/persona-tokens/Tokens.kt \
  --css /tmp/persona-tokens/tokens.css \
  --md /tmp/persona-tokens/palette.md

python3 scripts/assets/gen_tokens.py --check \
  --kotlin /tmp/persona-tokens/Tokens.kt \
  --css /tmp/persona-tokens/tokens.css \
  --md /tmp/persona-tokens/palette.md

python3 -m unittest discover -s scripts/assets -p test_gen_tokens.py -v
```

三个输出参数可以单独使用，参数值是文件路径。包名默认 `persona.core.designsystem`，可用 `--package example.design` 或 `--kotlin-package example.design` 改。默认 token 目录相对脚本定位，任意工作目录均可调用；`--source <目录>` 可检查独立的测试用 token。

普通生成先校验结构与引用，打印对比度失败但仍输出文件，成功写入时返回 0。这样可以在修色前先审阅色板。`--check` 全程只读：结构、引用、底线对比度（不含已知例外）、已知例外是否已经达标、指定文件的字节一致性有任一失败就返回 1，成功返回 0；建议项只报告；参数用法错误返回 2。未传输出路径时只检查 JSON 与对比度，不会推测客户端中的产物位置。已有对比度失败不会跳过产物比较，文件不存在也算失败。

Kotlin 使用分组对象，例子如下；这是消费方式，业务页面应再通过设计系统提供的当前风格接口取值。

```kotlin
val accent = Tokens.Semantic.Native.Light.accent
val needsYou = Tokens.Status.Dark.NeedsYou.dot
val corner = Tokens.Radius.card
val body = TextStyle(
    fontSize = Tokens.Type.Body.size,
    fontWeight = Tokens.Type.Body.weight,
    lineHeight = Tokens.Type.Body.size * Tokens.Type.Body.lineHeight,
)
val duration = if (reduceMotion) Tokens.Motion.reduced else Tokens.Motion.State.duration
```

颜色输出浮点 RGBA，保留 `0.14` 等 alpha，避免先压成 8 位后与 CSS 不同。JSON 的双精度数到 Compose 浮点数存在常规数值精度差异。

CSS 在 `:root` 暴露全路径变量，例如 `--semantic-native-light-accent`、`--radius-card`；`data-style` 和 `data-theme` 选择当前风格，提供 `--accent`、`--user-bubble`、`--status-needs-you-dot` 等短别名。默认是原生浅色，深色须显式设 `data-theme="dark"`，没有自动跟随 `prefers-color-scheme`。切换属性需放在同一根容器上，子容器只覆盖主题属性时请同时写风格属性。

```html
<div data-style="native" data-theme="dark">
  <button style="background:var(--accent);color:var(--on-accent)">发送</button>
</div>
```

原型原有的 `--primary / --needs-dot` 等名称不会被自动改写，接入方需要映射或替换。CSS 的 `prefers-reduced-motion` 会把每个 `--motion-*-duration` 改为零；循环动效仍须由组件停止。触控区、读屏完整计数、装饰图标隐藏、字号放大后的布局调整，以及按钮不得换行，都需要组件层实现，token 检查不代替 UI 验收。

## 对比度规则与现有冲突

`rules.json` 的 `$extensions.persona.contrast` 保存规则。路径可用 `{style}` 和 `{mode}`，每条规则在六种风格、两种模式下展开。背景透明时必须提供不透明 `canvas`；检查器先在 sRGB 中按 alpha 合成背景，再合成前景，最后按 [WCAG 2.2 相对亮度公式](https://www.w3.org/TR/WCAG22/#dfn-relative-luminance) 计算。使用完整精度判断，显示时才取四位小数。

底线为按钮字与主色 3.5、主色链接与页面 3、用户气泡正文 4.5。其余正文、次要文字、状态字、计数、diff 符号与终端文字按 4.5；大字按 3，但达到大字尺寸和字重条件前仍按正文要求。状态标签还检查 `text / tint / pageBackground` 的透明度合成结果。点色与发丝描边不作为文字对比度规则；实际控件的非文字边界与焦点可见性仍需组件验收。

当前 339 个 token，29 条规则展开为 348 个组合，77 个不达标（底线 1 项，已列入已知例外；建议项 76 项）。按风格归类如下，同一公共色在不同风格中分别计数。

| 风格 | 浅色不达标 | 深色不达标 |
| --- | --- | --- |
| 液态玻璃 | 11 | 0 |
| Claude | 11 | 0 |
| 黏土 | 16 | 0 |
| Island | 15 | 0 |
| 飞书 | 13 | 1 |
| Android 原生 | 10 | 0 |

可以直接由固定源码色值确认的例子：飞书深色白字对主色为 3.3477，低于 3.5；黏土浅色工作中文字对页面为 4.4237；待唤醒字对黏土、Island 浅色页面分别为 4.1347、4.2302；`Theme.Diff.changed` 对代码背景为 4.4913，低于 4.5。部分状态浅底叠在非白页面上也不够对比度。色板输出列出每个组合。

系统灰色相关失败来自上述回退假设，需在 iOS 实际环境中另行核对；不能将这些结果写成 iOS 真机结论。

**两级规则**（协调者 2026-10-30 定）：
- `level: "error"`：`docs/apple/ios.md` 写明的三条底线（按钮字对主色 3.5、主色链接对页面 3、用户气泡正文 4.5）。不达标就让 `--check` 失败。
- `level: "warn"`：按 WCAG AA 补的建议项（正文、次要文字、状态字、计数、diff、终端）。只报告数量，不阻断：iOS 现在的取值有不少达不到，token 又必须和 iOS 一致，改色要两端一起改，这一轮 iOS 不改。
- `known-failures.json`：底线规则里 iOS 现在就达不到的组合（目前只有飞书深色的按钮白字 3.3477:1）。列在这里的不让 `--check` 失败；**以后达标了必须从列表删掉**（`--check` 会提示），新增的底线不达标一律失败，所以这张表只会变短。

这样 `--check` 可以作为提交闸门：它守住「底线不再变差、产物和 JSON 一致」，建议项的数量在检查结果里看得到。

## 增加 token 与接入采用率检查

在相应 JSON 分组加叶子，写清来源；尺寸与动效须带单位，颜色保留 alpha。涉及语义色时，补齐六种风格、深浅色的同名字段。需要复用就写整值引用，不复制另一份状态色。新增前景或背景用途时，在 `rules.json` 加实际使用组合；透明背景同时写 `canvas`。随后运行单测、生成文件，再跑 `--check`，逐条处理失败。标准库测试还会读取 iOS 源码核对固定状态色、透明度和主要语义色，防止以后两端悄悄漂移。

D7 的采用率检查需要知道客户端最终目录和生成文件位置。由接入车道登记白名单，在 token 产物之外统计色值、圆角和固定字号字面量，记录当时的真实清单与计数，只许减少。终端网格、灵动岛、分享卡片、飞书卡片和文档沿用明确的例外范围；不要把整个 feature 放进白名单。这里没有扫描或改写 `android/`、`ios/`，也没有安装全仓 pre-commit hook。

接入方需在 hook 中显式传入实际的 Kotlin、CSS、Markdown 路径，才能发现“改了 JSON 没重新生成”。只跑 `--check` 不会检查这些产物。动态取色、系统色采样和视觉快照也由客户端车道完成；本目录的通过范围只覆盖数据、生成结果和静态色值计算。
