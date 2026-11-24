import Foundation
import Testing
import UIKit
import PersonaWire
@testable import Persona

/// Export as Image of a long reply with a table, a list and code (#2216; made-up text), light and dark. With
/// PERSONA_SHOTS set the images are written there (`share-<light|dark>.png`) to look at. Its width: `ShareCardWidthTests`.
@MainActor struct ShareCardExportTests {
    static let reply = """
    ## 三个方案对比

    | 方案 | 首屏时间 | 包体积 | 改动量 | 风险 |
    | --- | --- | --- | --- | --- |
    | 服务端渲染 | 0.8 s | +120 KB | 大：路由和数据层都要改 | 中：缓存失效要单独处理 |
    | 静态预渲染 | 0.5 s | +40 KB | 中：只改构建脚本 | 低 |
    | 懒加载拆包 | 1.4 s | −300 KB | 小：改 12 个页面的导入 | 低 |

    推荐**静态预渲染**，理由：

    1. 首屏最快，用户最在意这一点
    2. 只动构建脚本，回滚简单
    3. 和现在的缓存策略不冲突

    构建脚本这样改：

    ```js
    export default defineConfig({
      build: { target: "es2020", sourcemap: true },
      plugins: [prerender({ routes: ["/", "/pricing", "/docs"], renderAfterDocumentEvent: "render-ready" })],
    })
    ```

    改完跑一遍 `npm run build && npm run preview`，首页和价格页打开时看一下网络面板里有没有多余的请求。
    """

    @Test(arguments: [false, true]) func aReplyWithATableAndCode(dark: Bool) throws {
        let image = try #require(MessageShareCard.render(Self.message, dark: dark))
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"], let png = image.pngData() {
            try png.write(to: URL(fileURLWithPath: dir).appending(path: "share-\(dark ? "dark" : "light").png"))
        }
        #expect(image.size.height > 0)
    }

    @Test func aGraphCardDrawsItsStillSceneImmediately() throws {
        let card = """
        {
          "fallback": "Work",
          "title": "Work",
          "sections": [
            {
              "items": [
                {
                  "type": "graph",
                  "key": "work",
                  "selected": "a",
                  "nodes": [
                    {"id": "a", "title": "Design"},
                    {"id": "b", "title": "Build"}
                  ],
                  "edges": [
                    {"from": "a", "to": "b"}
                  ]
                }
              ]
            }
          ]
        }
        """
        let raw = try JSONDecoder().decode(ExtensionView.self, from: Data(card.utf8))
        #expect(AssistantRichMarkdown.supports(raw))
        let decoded = try #require(AssistantRichMarkdown.decode(card))
        #expect(ShareCardParts.drawable(decoded))
        let message = ShareMessage(text: "```persona-view\n\(card)\n```", isUser: false, source: "MacBook",
                                   date: Date(timeIntervalSince1970: 1_793_000_000), agent: "Claude")
        let image = try #require(MessageShareCard.render(message, dark: false))
        #expect(image.size.height >= 200)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"], let png = image.pngData() {
            try png.write(to: URL(fileURLWithPath: dir).appending(path: "share-graph.png"))
        }
    }

    @Test func aSceneCardDrawsItsStillSceneImmediately() throws {
        let card = """
        {
          "fallback": "Home: ceiling lamp on, 26 °C",
          "title": "Home",
          "sections": [
            {
              "items": [
                {
                  "type": "scene",
                  "key": "home",
                  "alt": "A two-room home with a sofa, a table and two lamps.",
                  "shapes": [
                    {"kind": "prism", "points": [[-3, -2], [3, -2], [3, 2], [-3, 2]], "bottom": 0, "top": 0.08, "color": "#C9AD8C", "material": "wood"},
                    {"kind": "box", "center": [-1.2, 0.4], "size": [1.8, 0.85], "round": 0.1, "bottom": 0.08, "top": 0.78, "color": "#7C8A98"},
                    {"kind": "cylinder", "center": [0.4, 0.2], "radius": 0.45, "bottom": 0.08, "top": 0.5, "color": "#B8946E", "material": "wood"}
                  ],
                  "labels": [
                    {"text": "Living room", "at": [-0.5, -0.8], "height": 0.08}
                  ],
                  "pins": [
                    {"id": "lamp", "at": [-0.5, 0.2], "height": 2.3, "label": "Ceiling lamp", "state": "On", "tone": "positive", "light": {"on": true, "warmth": "warm"}}
                  ]
                }
              ]
            }
          ]
        }
        """
        let raw = try JSONDecoder().decode(ExtensionView.self, from: Data(card.utf8))
        #expect(AssistantRichMarkdown.supports(raw))
        let decoded = try #require(AssistantRichMarkdown.decode(card))
        #expect(ShareCardParts.drawable(decoded))
        let message = ShareMessage(text: "```persona-view\n\(card)\n```", isUser: false, source: "MacBook",
                                   date: Date(timeIntervalSince1970: 1_793_000_000), agent: "Claude")
        let image = try #require(MessageShareCard.render(message, dark: false))
        #expect(image.size.height >= 200)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"], let png = image.pngData() {
            try png.write(to: URL(fileURLWithPath: dir).appending(path: "share-scene.png"))
        }
    }

    static let message = ShareMessage(text: reply, isUser: false, source: "MacBook · shop-web",
                                      date: Date(timeIntervalSince1970: 1_793_000_000), agent: "Claude")
}
