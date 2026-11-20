package persona.core.designsystem.icons

/** A keyboard with a lower chevron for the dismiss action. */
val PersonaIcons.dismissKeyboard: PhIcon
    get() = keyboardDismissGlyph

private val keyboardDismissGlyph by lazy {
    // Android outline glyph for the compound action. Phosphor supplies no keyboard-with-chevron glyph.
    val paths = arrayOf(
        // Rounded frame with a counter-clockwise cutout.
        "M40 36H216Q240 36 240 60V148Q240 172 216 172H40Q16 172 16 148V60Q16 36 40 36Z " +
            "M40 52Q32 52 32 60V148Q32 156 40 156H216Q224 156 224 148V60Q224 52 216 52Z",
        // Keys and space bar.
        "M50 68h20v16H50Z M84 68h20v16H84Z M118 68h20v16H118Z M152 68h20v16H152Z M186 68h20v16H186Z " +
            "M50 96h20v16H50Z M84 96h20v16H84Z M118 96h20v16H118Z M152 96h20v16H152Z M186 96h20v16H186Z " +
            "M50 124h20v16H50Z M84 124h88v16H84Z M186 124h20v16H186Z",
        "M83 185L128 202L173 185Q181 182 184 190Q187 197 179 200L131 218Q128 219 125 218" +
            "L77 200Q69 197 72 190Q75 182 83 185Z",
    )
    PhIcon("keyboard-dismiss", paths, paths, paths, paths, paths)
}
