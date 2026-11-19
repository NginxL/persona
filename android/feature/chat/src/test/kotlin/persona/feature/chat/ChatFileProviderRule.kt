package persona.feature.chat

import android.content.Context
import android.net.Uri
import androidx.core.content.FileProvider
import org.robolectric.Robolectric

/**
 * The chat's FileProvider for a test that hands out files from `cache/chat-captures/`. Each test owns its provider and
 * cache directory: a provider retained from another test cannot open or delete this test's captures.
 */
internal fun useChatFileProvider(context: Context) {
    val cache = FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }.get(null) as MutableMap<*, *>
    cache.clear()
    val provider = Robolectric.buildContentProvider(FileProvider::class.java).create().get()
    // There is nothing to delete: the call makes the provider take its folders under this test.
    provider.delete(Uri.parse("content://${context.packageName}.chat.files/chat-captures/none"), null, null)
}
