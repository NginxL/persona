package persona.feature.inbox

import org.jsoup.Jsoup
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.core.app.ApplicationProvider
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@org.robolectric.annotation.Config(sdk = [35])
class MailHtmlTest {
    private val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Test fun stripsActiveContentTrackingAndStylesButKeepsReadableBodyAndSafeLinks() {
        val html = safeMailHtml("""
            <script>alert(1)</script><iframe src="https://example.com"></iframe>
            <meta http-equiv="refresh" content="0;url=https://example.com">
            <img src="https://example.com/pixel"><img src="file:///private/file">
            <form action="https://example.com"><input value="secret"></form>
            <p style="color:white;background:white" onclick="alert(1)">正文<strong>重点</strong></p>
            <a href="javascript:alert(1)">危险链接</a><a href="https://example.com/help">查看</a>
            <a href="mailto:support@example.com?subject=Help">联系我们</a>
            <table><tr><td>附件说明</td></tr></table>
        """.trimIndent(), "#eeeeee", "#111111", "#aabbff")
        val doc = Jsoup.parse(html)
        assertTrue(doc.select("script,iframe,img,form,input,[onclick],[style]").isEmpty())
        assertFalse(html.contains("javascript:"))
        assertFalse(html.contains("file:///"))
        assertEquals("正文重点", doc.selectFirst("p")?.text())
        val links = doc.select("a[href]")
        assertEquals("https://example.com/help", links[0].attr("href"))
        assertEquals("mailto:support@example.com?subject=Help", links[1].attr("href"))
        assertEquals("附件说明", doc.selectFirst("td")?.text())
        assertTrue(html.contains("default-src 'none'"))
        assertTrue(html.contains("background:#111111;color:#eeeeee"))
    }

    @Test fun allowsImagesWhenEnabled() {
        val html = safeMailHtml("""
            <p>图片内容</p>
            <img src="https://example.com/photo.png" alt="照片" width="300">
            <img src="data:image/png;base64,abc">
            <img src="file:///etc/passwd">
            <script>alert(1)</script>
        """.trimIndent(), "#eeeeee", "#111111", "#aabbff", allowImages = true)
        val doc = Jsoup.parse(html)
        assertTrue(doc.select("script").isEmpty())
        assertFalse(html.contains("file:///"))
        val images = doc.select("img[src]")
        assertEquals(2, images.size)
        assertEquals("https://example.com/photo.png", images[0].attr("src"))
        assertEquals("照片", images[0].attr("alt"))
        assertEquals("data:image/png;base64,abc", images[1].attr("src"))
        val allImages = doc.select("img")
        assertEquals(3, allImages.size)
        assertFalse(allImages[2].hasAttr("src"))
        val (page, permitted) = mailImageDocument(html)
        val sources = Jsoup.parse(page).select("img[src]").map { it.attr("src") }
        assertEquals(listOf("https://mail-images.invalid/0", "data:image/png;base64,abc"), sources)
        assertEquals(mapOf(sources[0] to "https://example.com/photo.png"), permitted)
        assertTrue(page.contains("img-src https://mail-images.invalid data:"))
        assertFalse(page.contains("example.com"))
        assertTrue(mailImageDocument(safeMailHtml(html, "#111", "#fff", "#222")).second.isEmpty())
    }

    @Test fun mailDetailsShowReadableSizesAndLocalTimes() {
        assertEquals("812 字节", readableSize(res, 812))
        assertEquals("178 KB", readableSize(res, 182400))
        assertEquals("2.5 MB", readableSize(res, 2_621_440))
        val zone = java.time.ZoneId.of("Asia/Shanghai")
        val today = java.time.LocalDate.of(2026, 10, 31)
        assertEquals("今天 03:42", mailTime(res, "2026-10-30T19:42:55.439273Z", zone, today))
        assertEquals("昨天 09:00", mailTime(res, "Fri, 30 Oct 2026 09:00:00 +0800", zone, today))
        assertEquals("1月2日 08:00", mailTime(res, "2026-01-02T08:00:00+08:00", zone, today))
        assertEquals("not a date", mailTime(res, "not a date", zone, today))
    }
}
