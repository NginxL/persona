package persona.android.di

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files

/**
 * Verifies that the InboxRepository facade in InboxModule explicitly forwards
 * every method defined on InboxRepository to avoid silent fallback to default interface implementations.
 * (Fixes #1619)
 */
class InboxFacadeTest {
    private val modulePath = "android/app/src/main/java/persona/android/di/InboxModule.kt"

    private fun facadeSource(start: File): File? =
        generateSequence(start.canonicalFile) { it.parentFile }
            .map { File(it, modulePath) }
            .firstOrNull { it.isFile }

    @Test
    fun facadeOverridesAllInboxRepositoryMethods() {
        val moduleFile = facadeSource(File("."))
        assertNotNull("InboxModule.kt must exist in the current checkout", moduleFile)

        val content = requireNotNull(moduleFile).readText()
        val facadeBlock = content.substringAfter("val inbox = object : persona.feature.inbox.InboxRepository {")
            .substringBefore("val assistant = object : AssistantConversations {")

        val requiredMethods = listOf(
            "momentsPost",
            "momentsComments",
            "urlPreview",
            "react",
            "cardAction",
            "userProfile",
            "quickReactions",
            "emojis",
            "refresh",
            "refreshNow",
            "loadOlder",
            "refreshConversation",
            "loadNewer",
            "latestConversation",
            "markVisible",
            "setDone",
            "readDraft",
            "saveDraft",
            "reply",
            "downloadFile",
            "replyStatus",
            "verifyReply",
            "askPersona",
            "personaContext",
            "draftReply",
            "handOff"
        )

        for (method in requiredMethods) {
            assertTrue(
                "InboxRepository facade in InboxModule.kt must explicitly override $method to forward to active inbox",
                facadeBlock.contains("override suspend fun $method") ||
                facadeBlock.contains("override fun $method") ||
                facadeBlock.contains("override val $method")
            )
        }
    }

    @Test
    fun assistantFacadeOverridesMainConversationResolution() {
        val moduleFile = facadeSource(File("."))
        assertNotNull("InboxModule.kt must exist in the current checkout", moduleFile)

        val facadeBlock = requireNotNull(moduleFile).readText()
            .substringAfter("val assistant = object : AssistantConversations {")
        assertTrue(
            "AssistantConversations facade must resolve the main conversation through the active scoped repository",
            facadeBlock.contains(
                "override suspend fun mainConversationOf(manager: String) = use { it.assistant.mainConversationOf(manager) }"
            )
        )
    }

    @Test
    fun assistantFacadesForwardRecentTaskChats() {
        val moduleFile = facadeSource(File("."))
        assertNotNull("InboxModule.kt must exist in the current checkout", moduleFile)

        val facadeBlock = requireNotNull(moduleFile).readText()
            .substringAfter("val assistant = object : AssistantConversations {")
        assertTrue(
            "The app facade must update Recent through the active scoped repository",
            facadeBlock.contains("override suspend fun updateRecentTaskChats(value: Boolean?) = use { it.assistant.updateRecentTaskChats(value) }")
        )
        assertTrue(
            "The app facade must capture one write's original scoped repository",
            facadeBlock.contains("override suspend fun recentTaskChatsUpdater() = use { it.assistant.recentTaskChatsUpdater() }")
        )
        val groupBlock = File(moduleFile.parentFile, "GroupTranslation.kt").readText()
            .substringAfter("internal fun AssistantConversations.inGroup")
        assertTrue(
            "The group facade must not fall back to the interface's no-op",
            groupBlock.contains("override suspend fun updateRecentTaskChats(value: Boolean?) = source.updateRecentTaskChats(value)")
        )
        assertTrue(
            "The group facade must preserve the captured write scope",
            groupBlock.contains("override suspend fun recentTaskChatsUpdater() = source.recentTaskChatsUpdater()")
        )
    }

    @Test
    fun facadeSourceSupportsRepositoryAndroidAndAppWorkingDirectories() {
        val root = Files.createTempDirectory("inbox-facade-source-").toFile().canonicalFile
        try {
            val module = File(root, modulePath)
            check(module.parentFile.mkdirs())
            module.writeText("package fixture\n")
            for (start in listOf(root, File(root, "android"), File(root, "android/app"))) {
                assertEquals(module.canonicalFile, facadeSource(start)?.canonicalFile)
            }
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun missingFacadeSourceAndDirectoriesAreRejected() {
        val root = Files.createTempDirectory("inbox-facade-missing-").toFile().canonicalFile
        try {
            assertNull(facadeSource(root))
            check(File(root, modulePath).mkdirs())
            assertNull(facadeSource(File(root, "android/app")))
        } finally {
            root.deleteRecursively()
        }
    }
}
