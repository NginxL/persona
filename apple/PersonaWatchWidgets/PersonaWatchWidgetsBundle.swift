import SwiftUI
import WidgetKit

@main
struct PersonaWatchWidgetsBundle: WidgetBundle {
    init() { LanguageStore.shared.follow(LanguageStore.shared.groupResolved) }
    var body: some Widget {
        ExtensionSystemWidget()
        WatchWaterComplication()
        WatchProteinComplication()
        PetComplication() // the Milk Frog on the watch face (docs/apple/persona.md)
        MeetingRecordingComplication() // one tap from the face / Smart Stack to a meeting recording (docs/apple/recording.md)
        AllDayRecordingComplication() // all-day recording's state, one tap to continue it (docs/assistant/day-recap.md)
    }
}
