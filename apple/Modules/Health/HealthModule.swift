import PersonaModuleAPI
import SwiftUI

/// The `health` module (#14278, part of #13432 and #13430; docs/apple/ios.md "Modules"): the Health home, the health
/// space's page with Today, Activity, trends, its conversations and memory (docs/assistant/health.md). The core reaches it
/// only through `Modules.registry`: the shell page `health`, which the sidebar, the drawer and the iPad and Sidebar
/// layouts open. The health space's conversations, the phone's Health tools, the widgets and Health's settings stay in
/// the core until their slots are served.
struct HealthModule: PersonaModule {
    typealias Host = PersonaHostApp

    let descriptor = ModuleDescriptor(id: "health", hubGated: true, slots: [.shellPlaces])

    /// Built into every build that compiles the module (apple/modules.yml).
    var isAvailable: Bool { true }

    func register(into registrar: inout ModuleRegistrar<PersonaHostApp>) {
        registrar.shellPage(id: HealthModule.page) { c in
            HealthView(disabled: c.disabled, launcher: c.$launcher, launcherModel: c.$launcherModel, mode: c.$mode,
                       effort: c.$effort, send: c.send)
        }
    }

    /// The shell page's id, the core's route name (`SidebarRoute.health`).
    static let page = "health"
}
