package persona.feature.inbox

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.LarkFeatures
import persona.core.inbox.*

/** Feishu links open in native views only while the hub has the Lark extension's features (#1530). */
class LarkNativeLinkTest {
    private val doc = "https://example.feishu.cn/docx/token"
    private val post = "https://applink.feishu.cn/client/moments/detail?postId=post-1"
    private val page = "https://example.com/docx/token"

    @Test fun extensionHubOpensDocumentsAndPostsNatively() {
        val lark = LarkFeatures.of(setOf("client_ops", "lark", "lark_moments"))
        assertEquals(LarkNativeLink.Document(doc), larkNativeLink(doc, lark))
        assertEquals(LarkNativeLink.MomentsPost("post-1"), larkNativeLink(post, lark))
        assertNull(larkNativeLink(page, lark))
    }

    @Test fun withoutTheFeedPostsAreWebPages() {
        val lark = LarkFeatures.of(setOf("client_ops", "lark"))
        assertEquals(LarkNativeLink.Document(doc), larkNativeLink(doc, lark))
        assertNull(larkNativeLink(post, lark))
    }

    @Test fun hubWithoutTheExtensionOpensEveryFeishuLinkAsAWebPage() {
        val lark = LarkFeatures.of(setOf("client_ops", "plugin_manager"))
        assertNull(larkNativeLink(doc, lark))
        assertNull(larkNativeLink(post, lark))
    }

    @Test fun unknownOrOlderHubsKeepTheNativeViews() {
        for (features in listOf(emptySet(), setOf("client_ops", "lark_attachments"))) {
            val lark = LarkFeatures.of(features)
            assertEquals(LarkNativeLink.Document(doc), larkNativeLink(doc, lark))
            assertEquals(LarkNativeLink.MomentsPost("post-1"), larkNativeLink(post, lark))
        }
    }
}
