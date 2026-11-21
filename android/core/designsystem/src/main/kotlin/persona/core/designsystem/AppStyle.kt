package persona.core.designsystem

import android.content.Context
import android.os.Build
import android.os.PowerManager
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.core.contracts.api.PersonaManifest

/**
 * The three interface styles (Settings › Interface style). Pages are written once against the components in this module;
 * each style supplies its own tokens, component skins and app chrome. Light and dark follow the system unless
 * Light/dark ([ColorSchemeChoice]) pins one; the accent can be another theme color ([ThemeColor]).
 */
enum class AppStyle(val key: String, @StringRes val labelRes: Int, @StringRes val summaryRes: Int) {
    Glass("glass", R.string.designsystem_style_glass, R.string.designsystem_style_glass_summary),
    Brief("brief", R.string.designsystem_style_brief, R.string.designsystem_style_brief_summary),
    Board("board", R.string.designsystem_style_board, R.string.designsystem_style_board_summary);

    val label: String @Composable get() = stringResource(labelRes)
    val summary: String @Composable get() = stringResource(summaryRes)

    companion object {
        val Default = Glass
        fun fromKey(key: String?): AppStyle = entries.firstOrNull { it.key == key } ?: Default
    }
}

/** What the user asked for in the glass style; the device and the frame monitor can still lower it. */
enum class GlassPreference(val key: String, @StringRes val labelRes: Int) {
    Auto("auto", R.string.designsystem_glass_auto), Reduced("reduced", R.string.designsystem_glass_reduced);

    val label: String @Composable get() = stringResource(labelRes)

    companion object { fun fromKey(key: String?) = entries.firstOrNull { it.key == key } ?: Auto }
}

/** How glass is drawn: refraction (API 33+), plain blur, or opaque tinted panels. */
enum class GlassLevel { Liquid, Frosted, Solid }

/** Retired choices cannot return; unknown package ids keep their intent and draw the default until available. */
internal fun restoredPersonaId(id: String?): String =
    id?.takeUnless { it.isBlank() || it in PersonaManifest.RETIRED_IDS } ?: PersonaDesign.Default.id

enum class SidebarLayout(val key: String) {
    Separate("separate"),
    Merged("merged");

    companion object {
        fun fromKey(key: String?): SidebarLayout = entries.firstOrNull { it.key == key } ?: Separate
    }
}

/** Device-wide appearance choices; not tied to a hub, so they survive re-pairing. */
class AppStyleStore(context: Context) {
    private val app = context.applicationContext
    private val preferences = app.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE)
    private val currentStyle = MutableStateFlow(AppStyle.fromKey(preferences.getString(KEY_STYLE, null)))
    private val currentGlass = MutableStateFlow(GlassPreference.fromKey(preferences.getString(KEY_GLASS, null)))
    val style: StateFlow<AppStyle> = currentStyle.asStateFlow()
    val glass: StateFlow<GlassPreference> = currentGlass.asStateFlow()
    private val currentNativeInboxChats = MutableStateFlow(preferences.getBoolean(KEY_NATIVE_INBOX_CHATS, true))
    val nativeInboxChats: StateFlow<Boolean> = currentNativeInboxChats.asStateFlow()
    fun setNativeInboxChats(enabled: Boolean) {
        preferences.edit().putBoolean(KEY_NATIVE_INBOX_CHATS, enabled).apply()
        currentNativeInboxChats.value = enabled
    }
    /** Settings › Connectors › Lark: the optional Messages place on this device, independent of the current hub. */
    private val currentLarkHomeMessages = MutableStateFlow(preferences.getBoolean(KEY_LARK_HOME_MESSAGES, false))
    val larkHomeMessages: StateFlow<Boolean> = currentLarkHomeMessages.asStateFlow()
    fun setLarkHomeMessages(enabled: Boolean) {
        preferences.edit().putBoolean(KEY_LARK_HOME_MESSAGES, enabled).apply()
        currentLarkHomeMessages.value = enabled
    }

    private val currentSidebar = MutableStateFlow(SidebarLayout.fromKey(preferences.getString(KEY_SIDEBAR, null)))
    val sidebar: StateFlow<SidebarLayout> = currentSidebar.asStateFlow()

    fun setSidebar(layout: SidebarLayout) {
        preferences.edit().putString(KEY_SIDEBAR, layout.key).apply()
        currentSidebar.value = layout
    }

    private val currentTheme = MutableStateFlow(preferences.getString(KEY_THEME, null))
    val themeKey: StateFlow<String?> = currentTheme.asStateFlow()
    fun setTheme(key: String?) {
        preferences.edit().putString(KEY_THEME, key).apply()
        currentTheme.value = key
    }
    private val currentPersona = MutableStateFlow(restoredPersonaId(preferences.getString(KEY_PERSONA, null)))
    /** Settings › Appearance › Persona look: a built-in's id or a hub package's ([PersonaStore]), kept while the package is away. */
    val personaId: StateFlow<String> = currentPersona.asStateFlow()
    private val currentFace = MutableStateFlow(PersonaDesign.fromId(currentPersona.value))
    /** The built-in face to draw: the chosen one, or the default while a package is chosen (it shows over this). */
    val face: StateFlow<PersonaDesign> = currentFace.asStateFlow()

    private val currentScheme = MutableStateFlow(ColorSchemeChoice.fromKey(preferences.getString(KEY_SCHEME, null)))
    /** Settings › Appearance › Light/Dark: the system's (the default), or always light or dark. */
    val scheme: StateFlow<ColorSchemeChoice> = currentScheme.asStateFlow()
    private val currentColor = MutableStateFlow(ThemeColor.fromKey(preferences.getString(KEY_COLOR, null)))
    /** Settings › Appearance › Theme color: the style's own (the default), the persona's, or a fixed accent. */
    val color: StateFlow<ThemeColor> = currentColor.asStateFlow()
    private val currentDensity = MutableStateFlow(LayoutDensity.fromKey(preferences.getString(KEY_DENSITY, null)))
    /** Settings › Appearance › Layout density: Standard (the default) or Compact, on this phone. */
    val density: StateFlow<LayoutDensity> = currentDensity.asStateFlow()

    init {
        ActiveStyle.value = currentStyle.value
        AppAppearance.scheme = currentScheme.value
        LayoutDensity.apply(currentDensity.value)
    }

    fun setDensity(choice: LayoutDensity) {
        preferences.edit().putString(KEY_DENSITY, choice.key).apply()
        currentDensity.value = choice
        LayoutDensity.apply(choice)
    }

    fun setScheme(choice: ColorSchemeChoice) {
        preferences.edit().putString(KEY_SCHEME, choice.key).apply()
        currentScheme.value = choice
        AppAppearance.scheme = choice
    }

    fun setColor(choice: ThemeColor) {
        preferences.edit().putString(KEY_COLOR, choice.key).apply()
        currentColor.value = choice
    }

    fun setStyle(style: AppStyle) {
        preferences.edit().putString(KEY_STYLE, style.key).apply()
        currentStyle.value = style
        ActiveStyle.value = style
    }

    fun setGlass(preference: GlassPreference) {
        preferences.edit().putString(KEY_GLASS, preference.key).apply()
        currentGlass.value = preference
    }

    fun setFace(design: PersonaDesign) = setPersonaId(design.id)

    /** Whether this phone chose a look itself (the key is written only by a choice): what moves up to a hub that keeps the choice. */
    val personaChosen: Boolean get() = preferences.contains(KEY_PERSONA)

    fun setPersonaId(id: String) {
        val restored = restoredPersonaId(id)
        preferences.edit().putString(KEY_PERSONA, restored).apply()
        currentPersona.value = restored
        currentFace.value = PersonaDesign.fromId(restored)
    }

    /** The level glass actually renders at, before any runtime downgrade. */
    fun deviceLevel(preference: GlassPreference): GlassLevel {
        val power = app.getSystemService(PowerManager::class.java)
        return when {
            preference == GlassPreference.Reduced || power?.isPowerSaveMode == true -> GlassLevel.Solid
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU -> GlassLevel.Liquid
            else -> GlassLevel.Frosted
        }
    }

    private companion object {
        const val KEY_SIDEBAR = "sidebar-layout"
        const val KEY_STYLE = "style"
        const val KEY_GLASS = "glass"
        const val KEY_PERSONA = "persona"
        const val KEY_THEME = "extension-theme"
        const val KEY_SCHEME = "scheme"
        const val KEY_COLOR = "color"
        const val KEY_DENSITY = "layout-density"
        const val KEY_NATIVE_INBOX_CHATS = "native-inbox-chats"
        const val KEY_LARK_HOME_MESSAGES = "integrations.lark.enabled"
    }
}

val LocalAppStyleStore = staticCompositionLocalOf<AppStyleStore?> { null }

/**
 * Lowered at runtime when glass makes scrolling miss frames (see the app's frame monitor). Session-only: a new
 * launch tries the device level again.
 */
object GlassThrottle {
    val ceiling = mutableStateOf(GlassLevel.Liquid)

    fun lower(deviceLevel: GlassLevel = GlassLevel.Liquid) {
        ceiling.value = when (maxOf(deviceLevel, ceiling.value)) {
            GlassLevel.Liquid -> GlassLevel.Frosted
            else -> GlassLevel.Solid
        }
    }
}

/**
 * The style of the root theme, for the legacy helpers ([PersonaTypography], [statusTone]) that are not composable.
 * Reading it inside composition still subscribes, so a style switch recomposes their callers.
 */
internal val ActiveStyle = mutableStateOf(AppStyle.Default)
