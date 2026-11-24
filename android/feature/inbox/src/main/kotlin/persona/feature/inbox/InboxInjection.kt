package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent

@EntryPoint
@InstallIn(SingletonComponent::class)
interface InboxEntryPoint {
    fun inboxRepository(): InboxRepository
}

@Composable
internal fun injectedInboxRepository(): InboxRepository {
    val context = LocalContext.current.applicationContext
    return remember(context) { EntryPointAccessors.fromApplication(context, InboxEntryPoint::class.java).inboxRepository() }
}
