package persona.feature.chat

import org.junit.Assert.*
import org.junit.Test

class ReplyImageDocumentTest {
    private fun image(n: Int) = "![Frame $n](https://pictures.example/frames/$n.png)"

    @Test fun groupsConsecutivePictureLinesAndIncludesInlineAndHiddenPicturesInGalleryOrder() {
        val original = "Found ${image(0)} in the archive.\n\n" +
            (1..7).joinToString("\n") { image(it) } + "\n\nAnother view\n${image(0)}"
        val doc = checkNotNull(ReplyImageDocument.parse(original))
        assertEquals(listOf(0, 1, 2, 3, 4, 5, 6, 7, 0).map { "Frame $it" }, doc.pictures.map { it.alt })
        assertTrue(doc.markdown.startsWith("Found ![Frame 0](persona-reply-images:0) in the archive."))
        assertTrue(doc.markdown.contains("~~~persona-reply-images\n0\n~~~"))
        assertEquals(1..7, doc.group("~~~persona-reply-images\n0\n~~~"))
        assertEquals(8..8, doc.group("~~~persona-reply-images\n1\n~~~"))
        assertEquals(0, doc.inline("persona-reply-images:0"))
        assertNull(doc.inline("persona-reply-images:99"))
        assertNull(doc.group("~~~some-example\n0\n~~~"))
    }

    @Test fun keepsCodeExamplesLinkedPicturesCredentialsAndLocalUrisOutOfTheGallery() {
        val example = listOf("```md", image(0), "```", "`${image(1)}`",
            "> ${image(2)}", "[${image(3)}](https://docs.example/)", "![file](file:///sample.png)",
            "![name](https://name@pictures.example/photo)", "\\${image(4)}").joinToString("\n\n")
        assertNull(ReplyImageDocument.parse(example))
        val text = "persona-reply-images:9\n\n$example\n\n![A](https://pictures.example/show?a=1&b=2)"
        val doc = checkNotNull(ReplyImageDocument.parse(text))
        assertEquals("https://pictures.example/show?a=1&b=2", doc.pictures.single().url)
        assertTrue(doc.markdown.contains(example))
        assertEquals(0..0, doc.group("~~~persona-reply-imagesx\n0\n~~~"))
    }

    @Test fun mapsPhysicalPixelSizesAndFullGridAndSingleImageGeometry() {
        assertEquals(listOf(256, 256, 512, 768, 1024, 1536, 2048), listOf(1f, 128f, 129f, 257f, 385f, 513f, 900f).map {
            ReplyImageLayout.request(it, 2f)
        })
        assertEquals(32f to 18f, ReplyImageLayout.single(32, 18, 360f))
        assertEquals(180f to 240f, ReplyImageLayout.single(100, 800, 360f))
        assertEquals(360f to 240f, ReplyImageLayout.single(1_200, 400, 360f))
        assertEquals(320f to 240f, ReplyImageLayout.single(null, null, 360f))
        assertEquals(178f, ReplyImageLayout.square(2, 360f), 0f)
        assertEquals(352f / 3f, ReplyImageLayout.square(4, 360f), 0f)
        assertEquals(472f / 3f, ReplyImageLayout.square(7, 600f), 0f)
        assertEquals(listOf(2, 3, 2, 3, 3), listOf(2, 3, 4, 6, 7).map(ReplyImageLayout::columns))
    }
}
