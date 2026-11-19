package persona.core.designsystem

import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/**
 * Extension graph canvas dimensions (UI version 10). Slot coordinates are shared; this pitch is Android's. Every size
 * derives from generated primitives, keeping the contract's exact dp values without another hard-coded page baseline.
 */
object GraphTokens {
    val nodeWidth = Tokens.Size.box280 - Tokens.Size.slimBar - Tokens.Space.Standard.block
    val nodeHeight = Tokens.Size.box64
    val horizontalGap = Tokens.MinTouch.android
    val verticalGap = Tokens.MinTouch.android
    val pageMinHeight = Tokens.Size.box280 + Tokens.Size.slimBar
    val pageMaxHeight = Tokens.Size.box300 * 3
    val compactHeight = Tokens.Size.box120 + Tokens.Size.box80
    val viewportPadding = Tokens.Space.Standard.block
    val framePadding = Tokens.Space.Standard.s
    val frameTitleHeight = Tokens.Size.icon
    val frameTitleInset = Tokens.Space.Standard.xxs
    val leadingWidth = Tokens.Size.bar
    val selectedWidth = Tokens.Space.Standard.xxs
    val edgeWidth = Tokens.Size.line
    val edgeBend = Tokens.Space.Standard.xxl
    val foldVisual = Tokens.Size.icon
    val foldIcon = Tokens.Size.markS
    val stateDot = Tokens.Space.Standard.s
    val progressHeight = Tokens.Size.bar
    val contentPadding = Tokens.Space.Standard.m
    val controlsPadding = Tokens.Space.Standard.m
    val iconSlot = Tokens.Size.iconXS
}

/** The graph controls' meanings, with their glyphs kept inside the design system. */
object GraphIcons {
    val zoomOut: PhIcon get() = PhIcons.Minus
    val zoomIn: PhIcon get() = PhIcons.Plus
    val fit: PhIcon get() = PhIcons.Scan
    val fullScreen: PhIcon get() = PhIcons.ArrowsOutSimple
    val foldRight: PhIcon get() = PhIcons.CaretRight
    val foldDown: PhIcon get() = PhIcons.CaretDown
}
