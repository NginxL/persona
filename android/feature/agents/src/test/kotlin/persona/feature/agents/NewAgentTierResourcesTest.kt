package persona.feature.agents

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory

/** Every new New Agent or plan-preset string must ship in en, zh-Hans and ja together (#2225 AC5). */
class NewAgentTierResourcesTest {
    @Test fun agentAndPlanStringsHaveEnglishChineseAndJapaneseResources() {
        // Gradle runs this test with feature/agents as its working directory; chat is its sibling module.
        for (resources in listOf(File("src/main/res"), File("../chat/src/main/res"))) {
            assertTrue("Missing resource directory: ${resources.path}", resources.isDirectory)
            val english = keys(File(resources, "values"))
            assertTrue("No English resources in ${resources.path}", english.isNotEmpty())
            for (locale in listOf("values-zh", "values-ja")) {
                assertEquals("${resources.path}/$locale must have the same translatable keys as values", english, keys(File(resources, locale)))
            }
        }
    }

    private fun keys(directory: File): Set<String> {
        assertTrue("Missing resource directory: ${directory.path}", directory.isDirectory)
        val result = mutableSetOf<String>()
        val files = directory.listFiles { file -> file.extension == "xml" }.orEmpty()
        for (file in files) {
            val root = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(file).documentElement
            val nodes = root.childNodes
            for (index in 0 until nodes.length) {
                val node = nodes.item(index) as? org.w3c.dom.Element ?: continue
                if (node.tagName !in setOf("string", "plurals", "string-array") || node.getAttribute("translatable") == "false") continue
                result += "${node.tagName}:${node.getAttribute("name")}"
            }
        }
        return result
    }
}
