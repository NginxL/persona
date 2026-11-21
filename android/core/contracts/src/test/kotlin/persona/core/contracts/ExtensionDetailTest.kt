package persona.core.contracts

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.ExtensionDependent
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionPackMember
import persona.core.contracts.api.supportsExtensionSecretReferences

class ExtensionDetailTest {
    @Test fun integerStepDecodesFromTheFormAndIsOptional() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{
            "id":"example",
            "form":{"fields":[
                {"name":"goal","type":"integer","step":250},
                {"name":"count","type":"integer"},
                {"name":"large","type":"integer","step":9007199254740991}
            ]}
        }"""))
        assertEquals(250L, detail.form[0].step)
        assertNull(detail.form[1].step)
        assertEquals(9007199254740991L, detail.form[2].step)
    }

    @Test fun installedDetailsRetainDependencyStatesAndPackConstraints() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{
            "id":"example-pack",
            "depends":[
                {"id":"base","name":"Base","version":">=2.0.0","state":"old","installed":"1.0.0"},
                {"id":"missing","state":"missing"},
                {"id":"waiting","state":"not_ready"},
                {"id":"disabled","state":"disabled"},
                {"id":"ready","state":"ok"},
                {"id":"future","state":"future-state"}
            ],
            "extension_pack":[{"id":"member","market":"example","version":"^1.0.0"},{"id":"other"}]
        }"""))
        assertEquals(listOf("old", "missing", "not_ready", "disabled", "ok", "future-state"), detail.depends.map { it.state })
        assertEquals("Base", detail.depends.first().name)
        assertEquals(">=2.0.0", detail.depends.first().version)
        assertEquals("1.0.0", detail.depends.first().installed)
        assertEquals(listOf(ExtensionPackMember("member", "example", "^1.0.0"), ExtensionPackMember("other")), detail.extensionPack)
    }

    @Test fun packSwitchFieldsDecodeFromANewHub() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{
            "id":"example-pack",
            "pack_members_follow":true,
            "pack_on":[{"id":"base","name":"Base"},{"id":"addon"}],
            "pack_off":["base","addon"]
        }"""))
        assertTrue(detail.packMembersFollow)
        assertEquals(listOf(ExtensionDependent("base", "Base"), ExtensionDependent("addon")), detail.packOn)
        assertEquals("addon", detail.packOn.last().label)
        assertEquals(listOf("base", "addon"), detail.packOff)
    }

    @Test fun olderHubsHaveNoDependencyOrMemberSections() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{"id":"example"}"""))
        assertTrue(detail.depends.isEmpty())
        assertTrue(detail.extensionPack.isEmpty())
        assertFalse(detail.packMembersFollow)
        assertTrue(detail.packOn.isEmpty())
        assertTrue(detail.packOff.isEmpty())
    }

    @Test fun secretReferencesUsersAndRulesDecodeAndOldHubsDefaultToEmpty() {
        val detail = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{
            "id":"example-vault",
            "secrets":[{"name":"token","set":true,"reference":{"provider":"example-vault","provider_name":"Example Vault","reference":"op://Dev/Notes/token","title":"Notes token"}}],
            "secret_users":[
                {"kind":"extension","extension":"example-notes","extension_name":"Notes","secret":"api_key","secret_title":"API Key","reference":"op://Dev/Notes/token"},
                {"kind":"launcher","launcher":"work","launcher_name":"Work Gateway","machine":"m-1","reference":"op://Dev/Notes/token"}
            ],
            "handoff_rules":[{"id":"0123456789abcdef","reference":"op://Dev/Notes/token","launcher":"work","machine":"m-1","by":"owner","at":"2026-11-20T08:00:00Z"}],
            "unknown_future_field":{"value":"not read"}
        }"""))
        val reference = detail.secrets.single().reference!!
        assertEquals("Example Vault", reference.providerLabel)
        assertEquals("op://Dev/Notes/token", reference.reference)
        assertEquals("Notes token", reference.title)
        assertEquals(listOf("extension", "launcher"), detail.secretUsers.map { it.kind })
        assertEquals("Work Gateway", detail.secretUsers[1].launcherName)
        assertEquals("m-1", detail.secretUsers[1].machine)
        assertEquals("0123456789abcdef", detail.handoffRules.single().id)
        assertEquals("owner", detail.handoffRules.single().by)

        val old = ExtensionDetail.decode(JsonCodec.parseToJsonElement("""{"id":"old","secrets":[{"name":"token","set":true}]}"""))
        assertNull(old.secrets.single().reference)
        assertTrue(old.secretUsers.isEmpty())
        assertTrue(old.handoffRules.isEmpty())
    }

    @Test fun canonicalAndCompatibilitySecretReferenceFeaturesAreRecognized() {
        assertTrue(supportsExtensionSecretReferences(setOf("extension_secret_references")))
        assertTrue(supportsExtensionSecretReferences(setOf("plugin_secret_references")))
        assertFalse(supportsExtensionSecretReferences(setOf("launcher_secret_references")))
        assertFalse(supportsExtensionSecretReferences(emptySet()))
    }
}
