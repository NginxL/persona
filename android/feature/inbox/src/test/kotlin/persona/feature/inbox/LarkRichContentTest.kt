package persona.feature.inbox

import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.inbox.*

class LarkRichContentTest {
    private fun json(value: String) = Json.parseToJsonElement(value).jsonObject

    @Test fun larkCardKeepsFieldsReadOnlyButtonsImagesAndUnknownSummaries() {
        val card = parseLarkCard(json("""{
          "header":{"tag":"card_header","children":[{"tag":"plain_text","text":"状态更新"}]},
          "body":{"tag":"body","children":[
            {"tag":"div","fields":[
              {"short":true,"children":[{"tag":"plain_text","text":"阶段：准备中"}]},
              {"short":true,"children":[{"tag":"plain_text","text":"结果：待确认"}]},
              {"short":false,"children":[{"tag":"plain_text","text":"说明：内容可以换行"}]}
            ]},
            {"tag":"column_set","children":[
              {"tag":"column","style":{"weight":"Infinity"},"children":[{"tag":"plain_text","text":"正文"}]}
            ]},
            {"tag":"button","actionMethod":2,"url":"https://example.com/action","children":[{"tag":"plain_text","text":"确认"}]},
            {"tag":"img","image":{"url":"/api/v1/lark/media/cover"}},
            {"tag":"unsupported_widget","text":"可读摘要"}
          ]}
        }"""), "卡片摘要")
        assertEquals("状态更新", card.title)
        val fields = card.blocks[0] as LarkCardBlock.Columns
        assertEquals(2, fields.columns.size)
        assertTrue(card.blocks[1] is LarkCardBlock.Group)
        assertEquals(listOf(1f), (card.blocks[2] as LarkCardBlock.Columns).weights)
        assertEquals("确认", (card.blocks[3] as LarkCardBlock.Button).label)
        assertEquals("/api/v1/lark/media/cover", (card.blocks[4] as LarkCardBlock.Image).url)
        assertEquals("可读摘要", (card.blocks[5] as LarkCardBlock.Text).text)
        assertEquals("摘要", parseLarkCard(json("{}"), "摘要").summary)
    }

    @Test fun nativeDocumentKeepsBlocksPermissionsMediaAndRecognizesOnlyDocumentHosts() {
        val document = parseLarkDocument(json("""{
          "title":"阅读示例","partial":true,"authority":{"copy":false},
          "content":{"type":"doc","content":[
            {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"小标题"}]},
            {"type":"paragraph","content":[{"type":"text","text":"第一行"},{"type":"hardBreak"},{"type":"text","text":"第二行"}]},
            {"type":"orderedList","attrs":{"start":3},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"条目"}]}]}]},
            {"type":"codeBlock","attrs":{"language":"kotlin"},"content":[{"type":"text","text":"val ready = true"}]},
            {"type":"image","attrs":{"src":"https://example.com/api/documents/images/cover?size=full"}},
            {"type":"widget"}
          ]}
        }"""))
        assertEquals(listOf("heading", "paragraph", "paragraph", "codeBlock", "image", "unsupported"), document.blocks.map { it.kind })
        assertEquals("第一行\n第二行", document.blocks[1].text)
        assertEquals("3.", document.blocks[2].marker)
        assertEquals("kotlin", document.blocks[3].language)
        assertEquals("/api/v1/lark/raw/documents/images/cover?size=full", document.blocks[4].image)
        assertFalse(document.copyAllowed)
        assertTrue(document.partial)
        assertTrue(isLarkDocumentUrl("https://example.feishu.cn/docx/token"))
        assertTrue(isLarkDocumentUrl("https://example.larksuite.com/wiki/token"))
        assertFalse(isLarkDocumentUrl("https://feishu.cn.example.com/docx/token"))
        assertFalse(isLarkDocumentUrl("https://example.com/docx/token"))
        assertNull(larkWebUrl("javascript:alert(1)"))
        assertNull(larkWebUrl("https://account@example.com/docx/token"))
        assertTrue(runCatching { parseLarkDocument(json("{\"object\":{\"kind\":\"file\"}}")) }.isFailure)
    }
}
