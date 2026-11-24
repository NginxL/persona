package persona.core.designsystem

import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35])
class ReadingSizeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun interfaceStopsAtTheCapAndReadingKeepsTheChosenScale() {
        var scale by mutableStateOf(2f)
        var chosen: Density? = null
        var interfaceDensity: Density? = null
        var readingDensity: Density? = null
        var embeddedControlDensity: Density? = null
        rule.setContent {
            val parent = LocalDensity.current
            val density = Density(parent.density, scale)
            chosen = density
            CompositionLocalProvider(LocalDensity provides density) {
                PersonaTheme(reduceMotion = true) {
                    interfaceDensity = LocalDensity.current
                    ReadingContent {
                        readingDensity = LocalDensity.current
                        InterfaceContent { embeddedControlDensity = LocalDensity.current }
                    }
                }
            }
        }
        for (chosenScale in listOf(0.85f, 1f, InterfaceFontScaleCap, 1.6f, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            val expected = minOf(chosenScale, InterfaceFontScaleCap)
            assertEquals("interface at $chosenScale", expected, interfaceDensity!!.fontScale, 0f)
            assertEquals("reading at $chosenScale", chosenScale, readingDensity!!.fontScale, 0f)
            assertEquals("embedded control at $chosenScale", expected, embeddedControlDensity!!.fontScale, 0f)
            assertEquals("density pixels at $chosenScale", chosen!!.density, interfaceDensity!!.density, 0f)
            if (chosenScale <= InterfaceFontScaleCap) assertSame(chosen, interfaceDensity)
            else assertNotSame(chosen, interfaceDensity)
            assertSame(chosen, readingDensity)
        }
    }

    @Test fun nestedThemesRetainTheOriginalReadingScale() {
        var interfaceScale = 0f
        var readingScale = 0f
        var embeddedControlScale = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(2f, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    ReadingContent {
                        PersonaTheme(reduceMotion = true, darkTheme = true) {
                            interfaceScale = LocalDensity.current.fontScale
                            ReadingContent { readingScale = LocalDensity.current.fontScale }
                            InterfaceContent { embeddedControlScale = LocalDensity.current.fontScale }
                        }
                    }
                }
            }
        }
        assertEquals(InterfaceFontScaleCap, interfaceScale, 0f)
        assertEquals(2f, readingScale, 0f)
        assertEquals(InterfaceFontScaleCap, embeddedControlScale, 0f)
    }

    @Test fun fullScreenRetainsTheCallingInterfaceAndReadingScales() {
        var interfaceScale = 0f
        var readingScale = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    JFullScreen(onDismiss = {}) {
                        interfaceScale = LocalDensity.current.fontScale
                        ReadingContent { readingScale = LocalDensity.current.fontScale }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals(InterfaceFontScaleCap, interfaceScale, 0f)
        assertEquals(2f, readingScale, 0f)
    }

    @Test fun fullScreenAtAndBelowTheCapKeepsTheLegacyWindowScale() {
        var scale by mutableStateOf(1f)
        var interfaceScale = 0f
        var readingScale = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, scale)) {
                PersonaTheme(reduceMotion = true) {
                    JFullScreen(onDismiss = {}) {
                        interfaceScale = LocalDensity.current.fontScale
                        ReadingContent { readingScale = LocalDensity.current.fontScale }
                    }
                }
            }
        }
        for (chosenScale in listOf(0.85f, 1f, InterfaceFontScaleCap)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            assertEquals("legacy interface at $chosenScale", 1f, interfaceScale, 0f)
            assertEquals("legacy reading at $chosenScale", 1f, readingScale, 0f)
        }
    }

    @Test fun sheetOpenedFromReadingContentKeepsItsInterfaceAtTheCap() {
        var interfaceScale = 0f
        var readingScale = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    ReadingContent {
                        JSheet(onDismiss = {}) {
                            interfaceScale = LocalDensity.current.fontScale
                            ReadingContent { readingScale = LocalDensity.current.fontScale }
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals(InterfaceFontScaleCap, interfaceScale, 0f)
        assertEquals(2f, readingScale, 0f)
    }

    @Test fun menuOpenedFromReadingContentKeepsItsInterfaceAtTheCap() {
        var interfaceScale = 0f
        var readingScale = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    ReadingContent {
                        JMenu(expanded = true, onDismiss = {}, actions = emptyList()) {
                            interfaceScale = LocalDensity.current.fontScale
                            ReadingContent { readingScale = LocalDensity.current.fontScale }
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals(InterfaceFontScaleCap, interfaceScale, 0f)
        assertEquals(2f, readingScale, 0f)
    }

    @Test fun dialogRetainsTheCallingInterfaceAndReadingScales() {
        var interfaceScale = 0f
        var readingScale = 0f
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    JDialog(onDismiss = {}, title = "Title") {
                        interfaceScale = LocalDensity.current.fontScale
                        ReadingContent { readingScale = LocalDensity.current.fontScale }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals(InterfaceFontScaleCap, interfaceScale, 0f)
        assertEquals(2f, readingScale, 0f)
    }

    @Test fun renderedReadingTextKeepsGrowingAboveTheCap() {
        var interfaceHeight = 0
        var readingHeight = 0
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(2f, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    Text("A reading line", style = PersonaTheme.type.body, onTextLayout = { interfaceHeight = it.size.height })
                    ReadingContent {
                        Text("A reading line", style = PersonaTheme.type.body, onTextLayout = { readingHeight = it.size.height })
                    }
                }
            }
        }
        assertTrue("reading height $readingHeight must exceed capped interface height $interfaceHeight",
            readingHeight > interfaceHeight)
    }
}
