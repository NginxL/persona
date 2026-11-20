package persona.core.designsystem

import androidx.annotation.StringRes
import androidx.compose.ui.res.stringResource
import androidx.annotation.DrawableRes
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.TextUnitType
import androidx.compose.ui.unit.dp
import persona.core.contracts.api.HubChannel

/**
 * Other products' own marks (sources and terms in THIRD_PARTY_NOTICES.md, the same artwork as iOS's `Theme.Brand`).
 * They are drawn as their owners publish them: not recolored, not redrawn as icons. A product with no mark here is
 * drawn as a monogram of its name ([JSourceMark]).
 */
enum class Brand(@DrawableRes internal val drawable: Int, @StringRes val labelRes: Int) {
    /** Lark (Feishu)'s bird: the Feishu bot and /agent in Feishu. */
    Lark(R.drawable.brand_lark, R.string.designsystem_brand_lark);

    val label: String @Composable get() = stringResource(labelRes)
}

/** How large a source mark is drawn: in a line of text, at a list row's front, on top of a page. */
enum class MarkSize(internal val box: Dp) {
    /** Beside a line of caption text ("From WeChat", a conversation's title). */
    Inline(14.dp),
    /** A row's leading mark, the size of the rows' icons with their room. */
    Row(30.dp),
    /** The top of a page about the source. */
    Header(44.dp),
}

/** A product's own mark, fitted into a [size] square without padding of its own. */
@Composable
fun JBrandMark(brand: Brand, size: Dp, modifier: Modifier = Modifier, contentDescription: String? = brand.label) {
    Image(painterResource(brand.drawable), contentDescription, modifier.size(size), contentScale = ContentScale.Fit)
}

/**
 * Where something comes from, as a square mark: the product's own [brand] on a quiet tile, or the first character of
 * [name] when the design system has no mark for it. [MarkSize.Inline] draws the brand bare, sized like the text
 * beside it. Read out as [name].
 *
 * [picture]: the source's own icon (an extension's, #10358), drawn instead of either in the tile's box and corner, on
 * the light backing third-party logos keep in both appearances (`catalogLogoBackground`), at every size.
 */
@Composable
fun JSourceMark(name: String, modifier: Modifier = Modifier, brand: Brand? = null, size: MarkSize = MarkSize.Row,
                picture: ImageBitmap? = null) {
    val palette = PersonaTheme.palette
    val box = size.box
    val described = modifier.clearAndSetSemantics { contentDescription = name }
    val shape = RoundedCornerShape(box * if (PersonaTheme.style == AppStyle.Board) TileCornerBoard else TileCorner)
    if (picture != null) {
        Image(picture, null, described.size(box).clip(shape).background(palette.catalogLogoBackground), contentScale = ContentScale.Fit)
        return
    }
    if (brand != null && size == MarkSize.Inline) {
        JBrandMark(brand, box, described, contentDescription = null)
        return
    }
    Box(described.size(box).clip(shape).background(palette.surfaceStrong), contentAlignment = Alignment.Center) {
        if (brand != null) JBrandMark(brand, box * BrandInTile, contentDescription = null)
        else Text(
            name.trim().take(1).ifEmpty { "·" }, color = palette.ink2, maxLines = 1,
            style = PersonaTheme.type.label.copy(fontWeight = FontWeight.SemiBold,
                fontSize = TextUnit(box.value * MonogramScale, TextUnitType.Sp), lineHeight = TextUnit(box.value * MonogramScale, TextUnitType.Sp)),
        )
    }
}

private const val TileCorner = 0.28f
private const val TileCornerBoard = 0.2f
private const val BrandInTile = 0.66f
private const val MonogramScale = 0.5f

/** A channel's mark when the design system has one (Feishu's two channels); others get a monogram of their name. */
fun channelBrand(id: String): Brand? = when (id) {
    "feishu", "lark_agent" -> Brand.Lark
    else -> null
}

/**
 * The brand of a connector or a messaging source an extension brings, by its id (the web's `connectorBrand`): Lark's
 * for `lark` and `larkcli`; null for every other one.
 */
fun connectorBrand(id: String): Brand? = when (id) {
    "lark", "larkcli" -> Brand.Lark
    else -> null
}

/** A channel's mark (docs/extensions/channels.md "UI"): its product's own, else the first character of its name. */
@Composable
fun JChannelMark(channel: HubChannel, modifier: Modifier = Modifier, size: MarkSize = MarkSize.Row) =
    JSourceMark(channel.label, modifier, channelBrand(channel.id), size)

/** "From WeChat": the line under a message that came from a channel, its mark and its name in the secondary colour. */
@Composable
fun JChannelSource(channel: HubChannel, modifier: Modifier = Modifier) {
    val text = stringResource(R.string.designsystem_channel_source, channel.label)
    JSourceLine(text, modifier) { JChannelMark(channel, size = MarkSize.Inline) }
}

/** A message's source mark and words, read together once under its bubble. */
@Composable
fun JSourceLine(text: String, modifier: Modifier = Modifier, mark: @Composable () -> Unit) {
    Row(modifier.readAs(text), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
        mark()
        Text(text, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * The selected hubs' channels, for the marks on messages and conversations that came from one ("From WeChat"). The
 * App's shell provides it from its per-hub cache; empty without a hub that has channels.
 */
val LocalChannels = compositionLocalOf<List<HubChannel>> { emptyList() }

/** Reads [modifier]'s content as one element, [label] alone (a source line: its mark and its words, said once). */
private fun Modifier.readAs(label: String): Modifier = clearAndSetSemantics { contentDescription = label }
