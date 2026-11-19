#if os(iOS)
import SwiftUI

/// Settings › Meetings (#2981, docs/assistant/meeting-minutes.md「Attendance fields」): the role every meeting recording
/// starts with, kept on the hub (`meeting_attendance_default`, Off until changed), and what Persona may say in front of
/// others, both only where the hub offers attending. Each recording can still pick its own role and session (the
/// Recordings page). Every iPhone has the calendar meeting reminder (#8731 G2, `MeetingReminders`), this phone's own. A
/// hub with hub transcription (#3862 phase 5) adds how meetings are transcribed, its speech service, the glossary and
/// this iPhone's Wi-Fi rule (`MeetingTranscriptionSettingsSections`).
struct MeetingSettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var capturedAPI: APIClient?
    @State private var role: MeetingAttendance?
    @State private var saving = false
    /// Hub transcription's settings, made once the page shows (its model reads this phone's Wi-Fi rule: P3).
    @State private var transcription: MeetingTranscriptionSettings?
    /// This phone's experiment (#8731 B2): echo cancellation turned on with the speaker mid-meeting.
    @StoredValue(MeetingRecorder.echoWithSpeakerKey) private var echoWithSpeaker = false
    /// This phone's calendar meeting reminder (#8731 G2), on until turned off.
    @StoredValue(MeetingReminders.enabledKey) private var reminders = true
    /// Whether the calendar can be read, or may still be asked for: read as the page shows and after the prompt.
    @State private var calendarReadable = true
    @State private var calendarAskable = false

    var body: some View {
        let offered = MeetingAttendees.offered(model)
        SettingsForm {
            // Persona's part and its limits are the hub's: only where it offers attending (#2981).
            if offered { personaSection }
            reminderSection
            if let transcription, transcription.offered, let values = transcription.values {
                MeetingTranscriptionSettingsSections(settings: transcription, values: values)
            }
            if offered { limitsSection }
            if MeetingAttendees.conversation(model) {
                // Off until a phone has measured how long the microphone stops and how far-off voices sound with it.
                Section {
                    Toggle(isOn: $echoWithSpeaker) { Text("Echo Cancellation with the Speaker") }
                        .accessibilityIdentifier("settings-meeting-echo-speaker")
                } header: {
                    Text("Experimental [Meeting Settings]")
                } footer: {
                    Text("Turning on the speaker during a meeting stops the microphone for a moment.")
                }
            }
        }
        .themedPage()
        .navigationTitle(String(localized: "Meetings"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            readCalendarAccess()
            if offered { await load() }
            if transcription == nil { transcription = MeetingTranscriptionSettings(deps: .live) }
            await transcription?.load()
        }
    }

    /// The role every meeting starts with: with the meeting conversation, one switch; without, the role list.
    @ViewBuilder private var personaSection: some View {
        if MeetingAttendees.conversation(model) {
            // Persona is someone a meeting invites (#8731, D3): one switch, off by default, the same hub field
            // (off, or listener; a participant default stays one and reads as on).
            Section {
                Toggle(isOn: Binding(get: { role?.answers ?? false }, set: { on in Task { await save(on ? .listener : .off) } })) {
                    Text("Invite Persona Automatically")
                }
                .accessibilityIdentifier("settings-meeting-invite")
                .disabled(role == nil || saving)
            } header: {
                Text("Persona in Meetings")
            } footer: {
                Text(role?.answers == true
                     ? String(localized: "Every meeting starts with Persona in it, answering on screen. Remove it from a meeting on the Meetings page.")
                     : String(localized: "A meeting starts with only you. Invite Persona on the Meetings page when you need it; the minutes and highlights come either way."))
            }
        } else {
            Section {
                Picker(String(localized: "Persona in Meetings"), selection: Binding(
                    get: { role ?? .off },
                    set: { role in Task { await save(role) } })) {
                    ForEach(MeetingAttendance.choices, id: \.self) { Text($0.title).tag($0) }
                }
                .pickerStyle(.inline)
                .labelsHidden()
                .accessibilityIdentifier("settings-meeting-attendance")
                .disabled(role == nil || saving)
            } header: {
                Text("Persona in Meetings")
            } footer: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let role { Text(role.detail) }
                    Text("Each meeting recording starts with this. To change it for one meeting, pick another on the Recordings page before you start.")
                }
            }
        }
    }

    /// What Persona may say in front of others.
    private var limitsSection: some View {
        Section {
            Label("Answers only from meeting material: earlier minutes, project progress and who owns a to-do.", systemImage: Theme.Icon.meeting)
            Label("Never brings up private chats, mail, health or other personal data.", systemImage: Theme.Icon.privacy)
            Label("Speaks only when you tap Ask Persona, and only when it takes part.", systemImage: Theme.Icon.question)
        } header: {
            Text("In Front of Others")
        } footer: {
            Text("The hub holds Persona to these limits in a meeting, whatever it is asked.")
        }
        .font(Theme.Font.secondary)
    }

    /// The calendar meeting reminder (#8731 G2): the switch, and the way to calendar access when the phone has none (asked
    /// here when never asked, else the Settings app).
    @ViewBuilder private var reminderSection: some View {
        Section {
            Toggle(isOn: $reminders) { Text("Remind Me Before Meetings") }
                .accessibilityIdentifier("settings-meeting-reminders")
            if reminders, !calendarReadable {
                Button {
                    if calendarAskable {
                        Task {
                            _ = await MeetingCalendar.requestAccess()
                            readCalendarAccess()
                            MeetingReminders.shared.refresh()
                        }
                    } else if let url = URL(string: UIApplication.openSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                } label: {
                    Text(calendarAskable ? String(localized: "Allow Calendar Access") : String(localized: "Open Settings")).lineLimit(1)
                }
                .accessibilityIdentifier("settings-meeting-calendar")
            }
        } header: {
            Text("Calendar [Meeting Settings]")
        } footer: {
            Text(reminders && !calendarReadable
                 ? String(localized: "Reminders need access to your calendar.")
                 : String(localized: "A minute before a meeting on your calendar starts, a notification asks whether to record it."))
        }
        .onChange(of: reminders) { MeetingReminders.shared.refresh() }
    }

    private func readCalendarAccess() {
        calendarReadable = MeetingCalendar.canRead
        calendarAskable = MeetingCalendar.canAsk
    }

    private func load() async {
        if capturedAPI == nil { capturedAPI = model.api?.currentAssistantSettings() }
        guard let api = capturedAPI else { return }
        do {
            let settings = try await api.assistant()
            role = settings.meetingAttendanceDefault.flatMap(MeetingAttendance.init(rawValue:)) ?? .off
        } catch { model.show(error) }
    }

    private func save(_ next: MeetingAttendance) async {
        guard !saving, let api = capturedAPI else { return }
        saving = true
        defer { saving = false }
        do {
            _ = try await api.setAssistant(meetingAttendanceDefault: next.rawValue)
            role = next
            if api.sameSettingsScope(as: model.api) { await model.refreshAssistant() }
        } catch { model.show(error) }
    }
}
#endif
