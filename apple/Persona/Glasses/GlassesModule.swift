import PersonaModuleAPI
import SwiftUI

/// The `glasses` module (#13432's pilot; docs/apple/meta-glasses.md): Glasses Photo in the composer's +, the Meta AI
/// app's callback URL, and the SDK's set-up at launch. The core reaches it only through `Modules.registry`.
struct GlassesModule: PersonaModule {
    typealias Host = PersonaHostApp

    let descriptor = ModuleDescriptor(id: "glasses", hubGated: true, slots: [.launchHooks, .deepLinks, .composerActions])

    /// Only a build with the Meta Wearables SDK (PERSONA_META_WEARABLES=YES, apple/meta-wearables.yml) has the glasses.
    var isAvailable: Bool { MetaGlasses.isBuiltIn }

    func register(into registrar: inout ModuleRegistrar<PersonaHostApp>) {
        registrar.onLaunch { MetaGlasses.shared.configure() }
        registrar.handleURLs { MetaGlasses.shared.handle($0) } // the Meta AI app's callback
        #if os(iOS)
        registrar.composerAction(id: "glasses", title: "Glasses Photo", detail: "Take one with the Meta glasses",
                                 symbol: Theme.Icon.glasses) { deliver in
            GlassesCaptureSheet { deliver($0) }
        }
        #endif
    }
}
