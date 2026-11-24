import SwiftUI

// Health (docs/assistant/health.md, #2691): the health space, apart from Persona's other conversations, like ChatGPT's Health. Its
// home says what it is, asks something to start, lists its past conversations and its own memory, where its data comes
// from and what stays private. Its conversations run in a directory of their own on the assistant's machine, so the
// assistant's other conversations never see them and its memory is its own (`?space=health`). The web's `/health`.

/// Health's state and what it asks the hub (MVVM, docs/apple/ios.md「Data flow」): the space's conversations
/// (`GET /api/v1/assistant?space=health`) and how many memories it keeps.
@Observable final class HealthModel {
    private(set) var fetched: [Session] = []
    private(set) var memoryCount: Int?
    private(set) var reportCount: Int?
    private(set) var loaded = false
    /// Why the list could not be read; shown only while there is nothing to show.
    private(set) var error: String?
    private(set) var intake: HealthIntake?
    private(set) var supportsIntake = false
    private(set) var intakeError: String?
    private(set) var intakeBusy = false
    private(set) var waterImportBusy = false
    private(set) var isDemo = false
    var showsWaterMigration: Bool { !isDemo && supportsIntake && intake?.waterExtension == "pending" }
    /// A broadcast arriving during a write is read once the write has settled, by the foreground task.
    private(set) var intakeRefresh = 0
    @ObservationIgnored private var intakeReadPending = false
    @ObservationIgnored private var intakeRead = 0
    @ObservationIgnored private var intakeScope: APIClient?
    @ObservationIgnored private var intakeOperation: Task<Void, Never>?
    @ObservationIgnored private var intakeOperationID = 0
    @ObservationIgnored private var cupAmount: Double = 250
    @ObservationIgnored private let deps: AssistantDeps

    init(deps: AssistantDeps = .live) { self.deps = deps }

    func load(machine: String, features: Set<String> = [], isDemo: Bool = false) async {
        self.isDemo = isDemo
        guard let api = deps.api() else { return }
        async let today: Void = loadIntake(api: api, supported: features.contains(HealthIntake.feature))
        async let memory = try? api.assistantMemory(space: Session.healthSpace)
        async let reports = try? api.healthReports(machine: machine)
        do {
            fetched = try await api.healthConversations()
            error = nil
        } catch {
            self.error = error.shown ?? self.error
        }
        if let files = await memory { memoryCount = files.filter { $0.name != AssistantMemoryEntry.index }.count }
        if let list = await reports { reportCount = list.count }
        await today
        loaded = true
    }

    private func loadIntake(api: APIClient, supported: Bool) async {
        let api = api.manager(nil)
        if !api.sameSettingsScope(as: intakeScope) {
            intake = nil
            intakeError = nil
            intakeBusy = false
            waterImportBusy = false
            intakeOperationID += 1
            intakeReadPending = false
            cupAmount = 250
            intakeScope = api
        }
        supportsIntake = supported
        guard supported else { intake = nil; intakeRead += 1; return }
        // Keep optimistic feedback until the write answers, then honor every invalidation seen meanwhile.
        guard !intakeBusy else { intakeReadPending = true; return }
        intakeRead += 1
        let read = intakeRead
        do {
            let value = try await api.healthIntake()
            guard !Task.isCancelled, read == intakeRead, isCurrent(api) else { return }
            intake = value
            intakeError = nil
        } catch {
            guard !Task.isCancelled, read == intakeRead, isCurrent(api) else { return }
            intakeError = error.shown
        }
    }

    private func isCurrent(_ api: APIClient) -> Bool {
        api.sameSettingsScope(as: deps.api()?.manager(nil)) && api.sameSettingsScope(as: intakeScope)
    }

    private func finishIntake(_ api: APIClient) {
        guard isCurrent(api) else { return }
        intakeBusy = false
        if intakeReadPending {
            intakeReadPending = false
            intakeRefresh += 1
        }
    }

    /// Keep every accepted intent, including a toast action tapped during another write. Each operation reads its
    /// rollback baseline only when it reaches the front; refreshes wait until the entire queue has settled.
    private func enqueueIntake(on api: APIClient, run: @escaping @MainActor () async -> Void) -> Task<Void, Never>? {
        guard isCurrent(api) else { return nil }
        intakeBusy = true
        intakeOperationID += 1
        let id = intakeOperationID
        let previous = intakeOperation
        let operation = Task { @MainActor in
            await previous?.value
            defer {
                if id == self.intakeOperationID {
                    self.intakeOperation = nil
                    self.finishIntake(api)
                }
            }
            guard self.isCurrent(api) else { return }
            await run()
        }
        intakeOperation = operation
        return operation
    }

    /// One request at a time, with optimistic feedback and authoritative totals afterwards.
    func record(_ kind: HealthIntake.Kind, amount: Double? = nil) async {
        await record(HealthIntake.Draft(kind: kind, amount: amount))?.value
    }

    private func record(_ draft: HealthIntake.Draft, on captured: APIClient? = nil) -> Task<Void, Never>? {
        guard supportsIntake, let api = captured ?? deps.api()?.manager(nil) else { return nil }
        return enqueueIntake(on: api) { await self.performRecord(draft, on: api) }
    }

    private func performRecord(_ draft: HealthIntake.Draft, on api: APIClient) async {
        guard supportsIntake, isCurrent(api),
              let previous = intake else { return }
        intakeRead += 1
        if draft.kind == .water { intake?.water.today += draft.amount ?? cupAmount }
        else { intake?.protein.today += draft.amount ?? 0 }
        do {
            let result = try await api.recordHealthIntake(draft)
            deps.intakeChanged()
            guard isCurrent(api) else { return }
            intake?.align(result)
            #if os(iOS)
            HomeWidgetData.shared.intakeChanged(on: api, confirmed: intake)
            #endif
            intakeError = nil
            if result.entry?.deletedAt != nil {
                deps.toast(Toast(String(localized: "Intake undone"), style: .success))
                return
            }
            if draft.kind == .water, draft.amount == nil, let entry = result.entry { cupAmount = entry.amount }
            deps.toast(Toast(String(localized: "Intake logged"), style: .success,
                             action: .init(title: String(localized: "Undo"), run: { [weak self] in
                                 _ = self?.enqueueUndo(draft.id, on: api)
                             })))
        } catch {
            guard isCurrent(api) else { return }
            intake = previous
            deps.toast(Toast(error.shown ?? String(localized: "Couldn't log intake"), style: .error,
                             action: .init(title: String(localized: "Try Again"), run: { [weak self] in
                                 _ = self?.record(draft, on: api)
                             })))
        }
    }

    func undo(_ id: String) async { await enqueueUndo(id, on: deps.api()?.manager(nil))?.value }

    private func enqueueUndo(_ id: String, on captured: APIClient?) -> Task<Void, Never>? {
        guard let api = captured else { return nil }
        return enqueueIntake(on: api) { await self.performUndo(id, on: api) }
    }

    private func performUndo(_ id: String, on api: APIClient) async {
        intakeRead += 1
        do {
            let result = try await api.undoHealthIntake(id)
            deps.intakeChanged()
            guard isCurrent(api) else { return }
            intake?.align(result)
            #if os(iOS)
            HomeWidgetData.shared.intakeChanged(on: api, confirmed: intake)
            #endif
            deps.toast(Toast(String(localized: "Intake undone"), style: .success))
        } catch {
            guard isCurrent(api) else { return }
            deps.toast(Toast(error.shown ?? String(localized: "Couldn't undo intake"), style: .error,
                             action: .init(title: String(localized: "Try Again"), run: { [weak self] in
                                 _ = self?.enqueueUndo(id, on: api)
                             })))
        }
    }

    /// Import is an explicit user action. Share the intake write lane so another drink or invalidation cannot
    /// replace the result; a second tap is ignored until both import and its authoritative read have settled.
    func importWaterExtension() async {
        guard showsWaterMigration, !intakeBusy, let api = deps.api()?.manager(nil) else { return }
        await enqueueIntake(on: api) {
            self.waterImportBusy = true
            defer { if self.isCurrent(api) { self.waterImportBusy = false } }
            self.intakeRead += 1
            do {
                let result = try await api.importWaterExtension()
                self.deps.intakeChanged()
                guard self.isCurrent(api) else { return }
                self.intake?.waterExtension = nil
                // A failed refresh must not turn an already completed import into a failed import.
                do {
                    let value = try await api.healthIntake()
                    guard self.isCurrent(api) else { return }
                    self.intake = value
                    self.intakeError = nil
                } catch {
                    guard self.isCurrent(api) else { return }
                    self.intakeError = error.shown
                    self.intakeReadPending = true
                }
                #if os(iOS)
                HomeWidgetData.shared.intakeChanged(on: api, confirmed: self.intake)
                #endif
                self.deps.toast(Toast(String(localized: "Moved \(result.imported) records; water extension disabled. Replace widgets, watch faces and shortcuts."), style: .success))
            } catch {
                guard self.isCurrent(api) else { return }
                self.deps.toast(Toast(error.shown ?? String(localized: "Couldn't move the water extension into Health"), style: .error))
            }
        }?.value
    }

    /// The space's open conversations, newest first: the hub's list, with what the pushes say since (a title, its
    /// last line, a new one) over it. An archived one leaves the list.
    nonisolated static func merge(_ fetched: [Session], live: [Session]) -> [Session] {
        var by: [SessionKey: Session] = [:]
        for s in fetched { by[s.key] = s }
        for s in live where s.isHealth { by[s.key] = s }
        return by.values.filter { $0.archived != true }
            .sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
    }

    /// New Chat from the open conversation: a health one's opens this page, a new health conversation with the space's
    /// past ones listed; any other the assistant's new conversation.
    nonisolated static func newOpensHealth(_ open: Session?) -> Bool { open?.isHealth == true }

    /// One-tap questions (the web's): each starts a health conversation with it.
    static let suggestions = [String(localized: "How did I sleep this week?"), String(localized: "How do my steps over the last 7 days compare with last week?"), String(localized: "Log my weight today: 62.5 kg [Health View 1a24cb]"), String(localized: "Log my weight today: 62.5 kg [Health View c5fdcd]")]
}

struct HealthView: View {
    private nonisolated struct IntakeRefresh: Hashable, Sendable {
        var group: String
        var hellos: Int
        var revision: Int
        var retry: Int
        var supported: Bool
    }
    @Environment(AppModel.self) private var model
    /// A conversation is being started (the Persona page shows it coming).
    var disabled: Bool
    @Binding var launcher: String?
    /// The model picked under `launcher` (#11472), in the same menu.
    @Binding var launcherModel: String?
    /// The permission mode and effort a new conversation starts with: Persona's new conversation's, which `start` sends
    /// for Health's too; picked here in the shell composer's model menu (#3825).
    @Binding var mode: String
    @Binding var effort: String
    /// Starts a health conversation with the message (`AssistantView.start(…, space:)`).
    var send: (String, [ImagePayload]) -> Void
    @State private var page = HealthModel()
    @State private var intakeDetail: HealthIntakeSheet.Route?
    #if os(iOS)
    #if DEBUG
    @State private var trends = HealthHomeSamples.trendsModel()
    #else
    @State private var trends = HealthTrendsModel()
    #endif
    #endif
    // Upload a Checkup Report (docs/assistant/health.md「Checkup reports」): the picked files go to reports/; once they are up the conversation starts.
    @State private var showReports = false
    /// Kept by the app, not the page (#2836): built again with the whole tree when iPhone Duo folds or opens, the page
    /// lost the uploads under way.
    private var reports: FileUploads { model.healthReportUploads() }
    #if DEBUG
    private static var reportUploadShown = false
    #endif

    /// The default manager's assistant machine (the hub's unless set otherwise), where the health space's directory is.
    private var machine: MachineRef { model.hubRef(model.healthMachineID) }

    private func uploadReports(_ urls: [URL]) {
        let send = send
        reports.startAll(urls, machine: machine, model: model, dest: Session.healthSpace) { paths in
            let names = paths.map(PathInsert.quoted).joined(separator: " ")
            send(String(localized: "This is my checkup report. Please go through it item by item: \(names)"), [])
        }
    }

    var body: some View {
        // The field under the list, not over it (a safe-area inset): the Mac's list ran on under the field.
        VStack(spacing: 0) {
            sections(HealthModel.merge(page.fetched, live: model.healthConversations))
                .themedPage()
            if !reports.items.isEmpty { UploadsBar(uploads: reports).padding(.horizontal, Theme.Space.page) }
            Group {
                if ChatComposerKind.padCard {
                    // iPhone and iPad, every style: Persona's new conversation's composer (#3825), with the attach panel and
                    // + menu (#3225, #3805) and the launcher, effort and permission mode in its model button.
                    StarterComposer(disabled: disabled, shell: StarterComposer.ShellModes(launcher: $launcher, launcherModel: $launcherModel, mode: $mode, effort: $effort, health: true),
                                    placeholder: String(localized: "Ask about your health…"), uploadDest: Session.healthSpace, send: send)
                } else if AppStyle.current.usesBeautifulUI {
                    BeautifulStarterComposer(disabled: disabled, launcher: $launcher, placeholder: String(localized: "Ask about your health…"), uploadDest: Session.healthSpace, send: send)
                } else {
                    StarterComposer(disabled: disabled, placeholder: String(localized: "Ask about your health…"), uploadDest: Session.healthSpace, send: send)
                }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.top, Theme.Space.s)
            .padding(.bottom, Theme.Space.s)
            .frame(maxWidth: ChatView.readableWidth)
            .frame(maxWidth: .infinity)
            .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
        }
        .background(Theme.groupedPage)
        .keepsHeightUnderUndockedKeyboard() // the composer stays at the bottom under a floating keyboard (#2565)
        .pageTitle(String(localized: "Health"))
        .fileUploadPicker(isPresented: $showReports) { uploadReports($0) }
        .sheet(item: $intakeDetail) { HealthIntakeSheet(kind: $0.kind) }
        #if DEBUG
        // `-showReportUpload <name>[,<name>…]`: made-up reports uploaded as if picked, to check the whole way without the
        // picker; `-showReportUploadMB <n>`: each that big, on its way a while (the mock hub reads it slowly). Once a launch:
        // the page built again (a fold, #2836) does not upload them twice.
        .task {
            if let list = UserDefaults.standard.string(forKey: "showReportUpload"), !Self.reportUploadShown {
                Self.reportUploadShown = true
                try? await Task.sleep(for: .seconds(3))
                let mb = UserDefaults.standard.integer(forKey: "showReportUploadMB")
                uploadReports(list.split(separator: ",").map { name in
                    let url = FileManager.default.temporaryDirectory.appending(path: String(name))
                    try? (mb > 0 ? Data(count: mb << 20) : Data("%PDF-1.7 demo".utf8)).write(to: url)
                    return url
                })
            }
        }
        #endif
        .tabActiveTask(id: IntakeRefresh(group: model.hubGroup, hellos: model.hubHellos,
                                        revision: model.healthIntakeRevision, retry: page.intakeRefresh,
                                        supported: model.hubFeatures.contains(HealthIntake.feature))) {
            await page.load(machine: machine.id, features: model.hubFeatures, isDemo: model.isDemo)
        }
        .refreshable { await page.load(machine: machine.id, features: model.hubFeatures, isDemo: model.isDemo) }
        #if os(iOS)
        // Load outside the conditional List row: a hidden empty section has no row to run its task.
        .tabActiveTask(id: trends.range) { await trends.load() }
        #endif
    }

    private func sections(_ list: [Session]) -> some View {
        ScrollViewReader { proxy in
            List {
                #if os(iOS)
                if !model.isDemo, page.showsWaterMigration {
                    Section { HealthWaterMigrationCard(page: page) }
                }
                #endif
                Section {
                    HealthTodaySection(page: page) { intakeDetail = .init(kind: $0) }
                    #if os(iOS)
                    HealthIntakeSyncLink()
                    #endif
                } header: { Text("Today") }
                #if os(iOS)
                if UIDevice.current.userInterfaceIdiom == .phone {
                    #if DEBUG
                    HealthActivitySection(model: HealthHomeSamples.activityModel())
                    #else
                    HealthActivitySection()
                    #endif
                }
                if trends.hasData {
                    HealthTrendsSection(model: trends, automaticallyLoads: false)
                }
                #endif
                // Where the data comes from, named first (App Review 2.5.1, #13118): HealthKit is Apple Health, with its
                // icon; on an iPhone or iPad it opens This iPhone, where the access is turned on.
                Section {
                    #if os(iOS)
                    NavigationLink { PhoneDataView() } label: { appleHealth }
                        .accessibilityIdentifier("health-apple-health")
                    #else
                    appleHealth
                    #endif
                } footer: {
                    Text("Persona reads Apple Health only after you allow it, and saves an entry only after you confirm it. You can change this anytime.")
                }
                Section {
                    Button { showReports = true } label: {
                        Label { Text("Upload a Checkup Report").foregroundStyle(Theme.Hue.pink).lineLimit(1) } icon: {
                            ThemedIcon(Theme.Icon.document).foregroundStyle(Theme.Hue.pink)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .disabled(disabled || !reports.items.isEmpty)
                    .accessibilityIdentifier("health-upload-report")
                    ForEach(HealthModel.suggestions, id: \.self) { q in
                        Button { send(q, []) } label: {
                            Label { Text(q).foregroundStyle(Color.primary).lineLimit(1) } icon: {
                                ThemedIcon(Theme.Icon.ai).foregroundStyle(Theme.Hue.pink)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .disabled(disabled)
                        .accessibilityIdentifier("health-suggestion")
                    }
                } header: {
                    Text("Try Asking")
                }
                Section {
                    NavigationLink { HealthReportsView(machine: machine) } label: {
                        HStack {
                            Label { Text("Checkup reports").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.document) }
                            Spacer(minLength: Theme.Space.s)
                            if let n = page.reportCount, n > 0 {
                                Text("\(n) [Health View cb8680]").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                    }
                    .accessibilityIdentifier("health-reports")
                }
                Section {
                    if !list.isEmpty {
                        ForEach(list) { s in row(s) }
                    } else if !page.loaded {
                        ProgressView().frame(maxWidth: .infinity)
                    } else if let error = page.error {
                        Text("Couldn't load health chats: \(error)").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    } else {
                        Text("No health chats yet. Ask a question below to start the first one.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                } header: {
                    Text("Health Chats")
                }
                Section {
                    NavigationLink { AssistantMemoryView(space: Session.healthSpace) } label: {
                        HStack {
                            Label { Text("Health Memory").lineLimit(1) } icon: { ThemedIcon(Theme.Icon.memory) }
                            Spacer(minLength: Theme.Space.s)
                            if let n = page.memoryCount, n > 0 {
                                Text("\(n) items [Assistant Profile View 29b8db]").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                    }
                    .accessibilityIdentifier("health-memory")
                } footer: {
                    Text("Goals, allergies, medications and other things noted in health chats are kept separately and never flow into other chats. You can view and delete them anytime.")
                }
                Section {
                    source(Theme.Brand.iosHealth, String(localized: "Apple Health"), String(localized: "Reads summaries of steps, sleep, heart rate, weight and more through your iPhone. Persona needs to be open on the phone with access granted."))
                    source(Theme.Icon.edit, String(localized: "Log Data"), String(localized: "Say “I drank 300 ml of water”, or take a photo of a meal in the field below, to log it in Health. When sync is on, it is also written to Apple Health."))
                    source(Theme.Icon.document, String(localized: "Checkup Reports and Lab Results"), String(localized: "Choose Upload a checkup report and pick a PDF or a photo: each item is read out and the ones outside the reference range are marked. The original and its record stay in Health, so years can be compared."))
                } header: {
                    Text("Data Sources")
                }
                Section {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        Label { Text("Privacy").font(Theme.Font.heading) } icon: { ThemedIcon(Theme.Icon.privacy) }
                        ForEach(Self.privacy, id: \.self) { line in
                            Text("· " + line).font(Theme.Font.secondary).foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, Theme.Space.xs)
                    .id("health-privacy")
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("health-privacy")
                }
            }
            #if DEBUG
            // `-showHealthBottom YES`: scrolled to the privacy note, for screenshots of the lower sections.
            .task(id: page.loaded) {
                guard page.loaded, UserDefaults.standard.bool(forKey: "showHealthBottom") else { return }
                try? await Task.sleep(for: .milliseconds(300))
                proxy.scrollTo("health-privacy", anchor: .bottom)
            }
            #endif
        }
    }

    private static let privacy: [String] = {
        var lines = [
            String(localized: "Health chats stay here: Persona's other chats can't see or read them."),
            String(localized: "Chats and memory stay on the hub machine and never go into Knowledge."),
        ]
        #if os(iOS)
        if UIDevice.current.userInterfaceIdiom == .phone {
            lines.append(String(localized: "When sync is on, water and protein records are written to Apple Health on this iPhone."))
        }
        #endif
        lines.append(String(localized: "This offers explanations and suggestions, not a substitute for a doctor's diagnosis or treatment."))
        return lines
    }()

    /// Apple Health, by its name and the Health app's icon (`PhoneDataSource.health`'s): what Health reads and saves.
    private var appleHealth: some View {
        HStack(spacing: Theme.Space.m) {
            BrandMark(Theme.Brand.iosHealth, side: Theme.Size.wellS)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text("Works with Apple Health").font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                Text("Reads steps, sleep, heart rate, weight and more from Apple Health, and saves the entries you confirm to it.")
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityElement(children: .combine)
    }

    private func row(_ s: Session) -> some View {
        let title = s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title
        return Button { model.openAssistantConversation(s.key) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.healthFilled)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.Hue.pink)
                    .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                    .background(Theme.Hue.pink.opacity(0.14), in: .circle)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    if let last = s.lastText, !last.isEmpty {
                        Text(last).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if let at = s.updatedAt {
                    Text(Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("health-conversation")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button { Task { await model.archiveConversation(s.key, true); await page.load(machine: machine.id) } } label: {
                Label("Archive", systemImage: Theme.Icon.archive)
            }
        }
        .contextMenu {
            Button { Task { await model.archiveConversation(s.key, true); await page.load(machine: machine.id) } } label: {
                Label("Archive", systemImage: Theme.Icon.archive)
            }
        }
    }

    private func source(_ symbol: String, _ title: String, _ body: String) -> some View {
        Label {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.body).lineLimit(1)
                Text(body).font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        } icon: {
            if Theme.Brand.isAppIcon(symbol) {
                BrandMark(symbol, side: Theme.Size.iconL)
            } else {
                ThemedIcon(symbol).foregroundStyle(.secondary)
            }
        }
    }
}
