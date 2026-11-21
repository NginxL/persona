import OSLog
import SwiftUI

/// A list and the pages it opens (Agents, Inbox, Settings), laid out for the width there is (docs/apple/ios.md「iPhone Duo」).
///
/// Compact width — every iPhone, and iPhone Duo closed — is one stack with the list at its root, exactly as before.
/// Regular width — iPhone Duo open — keeps the list as a sidebar column and opens its pages in the detail column,
/// the way Mail shows a list and a message side by side; the split view also keeps its columns clear of the fold
/// when the device is partly folded. Both share `path`, so folding or unfolding keeps the page that was open.
///
/// In the split, the list's own links (`NavigationLink(value:)`) replace what the detail column shows; code in the list
/// should do the same (`path = NavigationPath([value])`, which in the single stack is the same as appending).
struct SplitNavigation<Sidebar: View, Empty: View, Destinations: ViewModifier>: View {
    @Environment(\.horizontalSizeClass) private var widthClass
    /// Whether the list column shows (its button in the toolbar closes it).
    @State private var columns = SplitColumns.shown
    /// What the split hands the pages under it, the same objects for its life (#10303): `SplitShowList`.
    @State private var showList = SplitShowList()
    @State private var pusher = SplitPush()
    @Binding var path: NavigationPath
    /// iOS: whether the list column is shown, remembered under this key (`SplitListMemory`), and slid by a finger
    /// (`SplitColumnControl`); nil: nothing kept, no slide.
    var listKey: String? = nil
    /// The pages the list opens (`navigationDestination`s), registered in both layouts.
    var destinations: Destinations
    /// The list column's own color, for the strip over it on the iPad (`SidebarTopFill`) and behind the phone's drawer;
    /// nil, the system sidebar's.
    var columnColor: Color? = nil
    /// The Sidebar layout's page (#8359): `empty` draws the root the sidebar picked, and the path holds only what is
    /// opened inside it. Its first page is the root itself (`firstPage`), and a pushed page always has Back.
    var rootsSwap = false
    /// The root the sidebar picked (`rootsSwap`): a new one, as a new path, slides a list laid over the page away.
    var selection: AnyHashable? = nil
    #if os(iOS)
    /// The Sidebar layout on a phone's width (#6332): the list in this drawer, the pages one stack whose root is `empty`
    /// (`drawerLayout`); nil, the list at the stack's root as before. Set around this split only (`splitDrawer`): it is
    /// cleared for what is inside, so a split in a page or a sheet keeps its own layout.
    @Environment(\.splitDrawer) private var drawer
    #endif
    @ViewBuilder var sidebar: Sidebar
    /// The detail column before anything is open.
    @ViewBuilder var empty: Empty
    #if os(iOS)
    /// The list column lies over the page instead of beside it: where the page beside it would be narrower than
    /// `Theme.Size.splitPageMin` (the open iPhone Duo and an iPad mini, upright). Set from the split's width
    /// (`SplitListMemory`); nil before it is laid out.
    @State private var overlays: Bool?
    /// The list column as UIKit has it (`SplitColumnControl`): a finger moving it, the split's size as a turn lays it out.
    @State private var columnState = SplitColumnState()
    /// The system's bars down the side (the open iPhone Duo on its side) beside the Sidebar layout's column: nothing is
    /// above the columns there (`topPad`, #4853).
    @Environment(\.splitSideBars) private var sideBars
    /// Each column's own top safe area, outside the margin it is given (the page's has the system's bar room under side
    /// bars, the list's does not). Read under side bars only, where `topPad` uses them (`readsSafeTop`).
    @State private var listSafeTop: CGFloat = 0
    @State private var pageSafeTop: CGFloat = 0
    #else
    /// No system bars down the side off iOS (#9310): visionOS's split passes `SplitDetail` none, as the Mac's does.
    private var sideBars: Bool { false }
    #endif

    init(path: Binding<NavigationPath>, listKey: String? = nil, destinations: Destinations, columnColor: Color? = nil,
         rootsSwap: Bool = false, selection: AnyHashable? = nil,
         @ViewBuilder sidebar: () -> Sidebar, @ViewBuilder empty: () -> Empty) {
        _path = path
        self.listKey = listKey
        self.destinations = destinations
        self.columnColor = columnColor
        self.rootsSwap = rootsSwap
        self.selection = selection
        self.sidebar = sidebar()
        self.empty = empty()
        #if os(iOS)
        // A list closed last time starts closed (#2624). Closed only once it appeared, the split put it back out as it
        // first laid itself out, and the list came back on every launch.
        _columns = State(initialValue: SplitListMemory.startsClosed(listKey) ? .detailOnly : SplitColumns.shown)
        #endif
    }

    /// The page in front is the first one: nothing under it to go back to. With roots (#8359) that is the root alone;
    /// otherwise the first page the list opened, beside it.
    private var firstPage: Bool { rootsSwap ? path.isEmpty : path.count <= 1 }

    var body: some View {
        layout.environment(\.splitPush, pusher.set { [path = $path] value in path.wrappedValue.append(value) })
        #if os(iOS)
            .environment(\.splitDrawer, nil)
            .environment(\.splitSideBars, false)
        #endif
    }

    @ViewBuilder private var layout: some View {
        if widthClass == .regular {
            #if os(macOS)
            // The list in the content column (flush; the sidebar column floats as a glass card on macOS 26 and stays
            // empty). A value link in the content column replaces the detail column's stack, as one in the sidebar did.
            NavigationSplitView(columnVisibility: $columns) {
                SplitColumns.noSidebar
            } content: {
                // The look the list had in the sidebar column (no row lines, footers as before) on the window's white,
                // as the Persona and Agents lists beside the sections are; lists that pick a style keep it. Inside the
                // destinations, so the style stays in this column whichever environment the pushed pages are built in.
                sidebar
                    .listStyle(.sidebar)
                    .scrollContentBackground(.hidden)
                    .modifier(destinations)
                    .modifier(SidebarWidth())
            } detail: {
                NavigationStack(path: $path) {
                    empty.modifier(destinations)
                }
                // The page keeps a width it can be read at; narrowing the window narrows the list first (#1200).
                .navigationSplitViewColumnWidth(min: Theme.Size.macDetailMin, ideal: Theme.Size.macWindowWidth)
                .onGeometryChange(for: Bool.self) { $0.size.width < Theme.Size.macSplitSquished } action: { squished in
                    SplitColumns.log.notice("detail squished: \(squished) (columns \(String(describing: columns), privacy: .public), depth \(path.count))")
                }
            }
            .navigationSplitViewStyle(.balanced)
            .environment(\.splitDetail, SplitDetail(isFirstPage: firstPage))
            // The detail column once came out a few points wide, the chat squeezed into it (2026-11-02, not reproduced):
            // what the columns and the stack did, for the next time (`log show --predicate 'category == "split"'`).
            .onChange(of: columns) { old, new in
                SplitColumns.log.notice("columns \(String(describing: old), privacy: .public) → \(String(describing: new), privacy: .public)")
            }
            .onChange(of: path.count) { old, new in SplitColumns.log.notice("depth \(old) → \(new)") }
            #else
            NavigationSplitView(columnVisibility: $columns) {
                // No stack of its own: a value link in the sidebar column replaces the detail column's stack. A sidebar
                // stack that refused its pushes (a path binding that always read empty) first slid the list away, then
                // back: the sidebar folded and unfolded on every tap.
                sidebar.modifier(destinations)
                    #if os(iOS)
                    // Roots (#8603): the column has its own fold button and each root its own sidebar button
                    // (`ListButtonItem`; an Agent's chat its header's, or under side bars its side column's first item);
                    // the system's toggle was not drawn again after a root's drill-down was popped. Under side bars (the
                    // Duo on its side) too (#11636): there it was missing after a pop as well, and on an Agent's chat,
                    // which removes the bar's title row the toggle is drawn in, always.
                    .toolbar(removing: rootsSwap ? .sidebarToggle : nil)
                    .safeAreaPadding(.top, topPad(listSafeTop))
                    .background { readsSafeTop { listSafeTop = $0 } }
                    #endif
                    // Last, on the column's root: under the top margin's padding the column's width no longer reached
                    // the split (on the Duo on its side).
                    .modifier(SidebarWidth())
            } detail: {
                NavigationStack(path: $path) {
                    empty.modifier(destinations)
                }
                #if os(iOS)
                .safeAreaPadding(.top, topPad(pageSafeTop))
                .background { readsSafeTop { pageSafeTop = $0 } }
                .background { columnControl }
                .modifier(SplitPageMin())
                #endif
            }
            .navigationSplitViewStyle(.balanced)
            #if DEBUG
            // The perf UI tests' cue (`FrameProbe.toggleSidebar`): the Sidebar layout's column folded or brought back as
            // its buttons do it.
            .onAppear {
                guard listKey == "sidebar.list" else { return }
                FrameProbe.sidebarToggle = { [columns = $columns] in
                    withAnimation(Theme.Motion.layout) {
                        columns.wrappedValue = columns.wrappedValue == .detailOnly ? SplitColumns.shown : .detailOnly
                    }
                }
            }
            #endif
            .modifier(SidebarTopFill(shown: columns != .detailOnly, color: columnColor))
            #if os(iOS)
            .modifier(SplitListMemory(columns: $columns, key: listKey, overlays: $overlays, state: columnState))
            #endif
            // On the whole split, not the detail stack: its pages are built from the destinations the sidebar
            // registered, in the sidebar's environment.
            .environment(\.splitDetail, SplitDetail(isFirstPage: firstPage, listHidden: columns == .detailOnly,
                                                     sideBars: sideBars, roots: rootsSwap))
            .environment(\.splitShowList, showList.set(show: { [columns = $columns] in
                withAnimation(Theme.Motion.layout) { columns.wrappedValue = SplitColumns.shown }
            }, hide: { [columns = $columns] in
                withAnimation(Theme.Motion.layout) { columns.wrappedValue = .detailOnly }
            }, hideOverlay: hideOverlay))
            #if os(iOS)
            // A pick that changes the page (an Agent, a place): a list laid over the page goes (#4852).
            .onChange(of: path) { hideOverlay() }
            // A new root the sidebar picked, with no path to change (#8359): the content is already replaced, the list's
            // sliding away is the only motion.
            .onChange(of: selection) { hideOverlay() }
            #endif
            #endif
        } else {
            #if os(iOS)
            if let drawer {
                drawerLayout(drawer)
            } else {
                NavigationStack(path: $path) {
                    sidebar.modifier(destinations)
                }
            }
            #else
            NavigationStack(path: $path) {
                sidebar.modifier(destinations)
            }
            #endif
        }
    }

    /// A pick in a list laid over the page (the open Duo upright, an iPad mini upright) changed what the page shows: the list
    /// slides away, as the system's sidebar does on an iPad in portrait (#4852). Beside the page it stays; a filter or a
    /// grouping, which changes no page, leaves it too.
    private func hideOverlay() {
        #if os(iOS)
        guard overlays == true, columns != .detailOnly else { return }
        withAnimation(Theme.Motion.layout) { columns = .detailOnly }
        #endif
    }

    #if os(iOS)
    /// Under side bars both columns start lower (#4853): at least `Theme.Space.l`, as ChatGPT's open Duo, so the column's
    /// head and the conversation's top bar stay on one line. Never from the page's own top (#8359): that is the root's
    /// navigation bar, 0 on the conversation (its bar floats over it), 82 pt on a place, 24 on an Agent's chat, and the
    /// column's head followed it down on every page but the conversation. The column's head stays where the conversation
    /// has it; a page with a bar of its own takes no more. Set inside each column: the split's columns do not take a safe
    /// area given around it.
    private func topPad(_ own: CGFloat) -> CGFloat {
        guard sideBars else { return 0 }
        return max(0, max(Theme.Space.l, listSafeTop) - own)
    }

    /// A column's own top safe area, for `topPad`: read under side bars only, by a clear view behind the column, which comes
    /// and goes with them while the column's content stays as it is. Read on the column itself everywhere, the column was
    /// laid out once more as it went and as it came, its list's rows too: about 40 ms of main thread in an extra frame at
    /// each fold's end and show's start on the iPad (#4206).
    @ViewBuilder private func readsSafeTop(_ read: @escaping (CGFloat) -> Void) -> some View {
        if sideBars {
            Color.clear.onGeometryChange(for: CGFloat.self, of: { $0.safeAreaInsets.top }, action: read)
        }
    }

    /// The list column's control from UIKit (`SplitColumnControl`): slid by a finger where the list's shown state is kept
    /// (`listKey`). In the detail column, which is there while the list is away.
    private var columnControl: some View {
        SplitColumnControl(drags: listKey != nil, state: columnState) { shown in
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) { columns = shown ? SplitColumns.shown : .detailOnly }
            SplitListMemory.keep(listKey, shown: shown, state: columnState)
        }
    }

    /// The Sidebar layout on a phone (#6332): the list in the drawer that slides in from the left, the pages one stack
    /// whose root is `empty` (the Persona conversation). What the list opens is that stack's first page, as beside an
    /// iPad's column, with the drawer's button where its back would be (`SplitDetail.drawer`), and a drag to the right on
    /// it opens the drawer as on the conversation (`AssistantDrawerLayout.firstPages`). Behind the drawer, the column's
    /// color (the Agents list's own page color without one): its head and foot and the list are one surface, in dark mode too.
    private func drawerLayout(_ drawer: AssistantDrawer) -> some View {
        // With roots (#8359) the drawer's drag works at the root, and a pushed page's back swipe has the first page too.
        AssistantDrawerLayout(drawer: drawer, enabled: firstPage, backdrop: columnColor ?? Theme.groupedPage, firstPages: !rootsSwap, sidebar: { sidebar }, page:
            NavigationStack(path: $path) { empty.modifier(destinations) })
            .environment(\.splitDetail, SplitDetail(isFirstPage: firstPage, listHidden: true, drawer: true))
            .environment(\.splitShowList, showList.set(show: { drawer.open() }))
    }
    #endif
}

/// What a split shows when its list is out: the list beside the page. On the Mac the list is the content column of a
/// three-column split whose sidebar column stays empty (`noSidebar`): macOS 26 draws the sidebar column as a glass card
/// floating off the window's edges, the window's backdrop showing round it (2026-11-02 user: like floating on a backdrop, with a gap).
/// The shell's sections column is the window's sidebar there.
enum SplitColumns {
    #if os(macOS)
    static let shown = NavigationSplitViewVisibility.doubleColumn

    /// The empty sidebar column, and no system button to bring it out.
    static var noSidebar: some View { Color.clear.toolbar(removing: .sidebarToggle) }

    static let log = Logger(subsystem: "persona", category: "split")
    #else
    static let shown = NavigationSplitViewVisibility.all
    static let log = Logger(subsystem: "persona", category: "split")

    #if os(iOS)
    /// The list column's one width here (`SidebarWidth`).
    @MainActor static var listWidth: CGFloat {
        UIDevice.current.userInterfaceIdiom == .pad ? Theme.Size.sidebar : Theme.Size.sidebarPhone
    }

    /// In a split this wide the page beside the list column keeps its least width (`SplitPageMin`): the column lies beside
    /// it; else over it.
    @MainActor static func fitsBeside(width: CGFloat) -> Bool { width - listWidth >= Theme.Size.splitPageMin }
    #endif
    #endif
}

/// The list column's width: one width that never changes while the column shows or slides (#4206, the owner). Hidden and
/// shown, the column only slides in and out, as Feishu's and ChatGPT's do; while it followed a dragged handle (#2624)
/// every row was laid out again on every frame. iPad: `Theme.Size.sidebar`, wider than the system's sidebar, which wrapped
/// the agents' card titles. iPhone Duo open: `Theme.Size.sidebarPhone` (ChatGPT's width there; the system's was about
/// 199 pt upright), which leaves the page most of the 951 pt on its side. A Mac window keeps its range.
struct SidebarWidth: ViewModifier {
    func body(content: Content) -> some View {
        switch UIDevice.current.userInterfaceIdiom {
        case .mac:
            content.navigationSplitViewColumnWidth(min: Theme.Size.sidebarMin, ideal: Theme.Size.sidebar, max: Theme.Size.sidebarMax)
                .background(edge)
        case .pad: content.navigationSplitViewColumnWidth(Theme.Size.sidebar).background(edge)
        case .phone: content.navigationSplitViewColumnWidth(Theme.Size.sidebarPhone).background(edge)
        default: content
        }
    }

    /// Where the column ends (`SidebarEdge`).
    private var edge: some View {
        GeometryReader { g in Color.clear.preference(key: SidebarEdge.self, value: g.frame(in: .global).maxX) }
    }
}

/// Where the list column ends, for `SidebarTopFill`.
struct SidebarEdge: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

/// iPad: the list column starts below the status bar (its background cannot reach above it), so the strip over it was
/// the window's white and the list looked cut off at the top (2026-10-31). The split fills that strip with the
/// column's color, as wide as the column is.
struct SidebarTopFill: ViewModifier {
    /// The column is open (closed, its last frame stays reported: the fill must go by this).
    var shown: Bool
    /// The column's own color where it has one (the Sidebar layout's, `UnifiedSidebar.columnColor`); nil, the system's.
    var color: Color? = nil
    @State private var edge: CGFloat = 0

    func body(content: Content) -> some View {
        if UIDevice.current.userInterfaceIdiom == .pad {
            content
                .onPreferenceChange(SidebarEdge.self) { edge = $0 }
                .background(alignment: .topLeading) {
                    (color ?? (AppStyle.current.usesGlass ? Theme.systemSidebar : Theme.pageBackground))
                        .frame(width: shown ? max(edge, 0) : 0)
                        .animation(Theme.Motion.layout, value: shown)
                        .ignoresSafeArea()
                }
        } else {
            content
        }
    }
}

/// Set on the detail column of a `SplitNavigation` in regular width; nil in a single stack.
struct SplitDetail: Equatable {
    /// The page is the first one the list opened: there is nothing to go back to (the list is beside it).
    var isFirstPage: Bool
    /// The list column is closed (iOS: its button in the list's toolbar closed it). A first page with its own header
    /// (an Agents chat) has no system button to bring it back: its back does (`splitShowList`).
    var listHidden = false
    /// The page is a window of its own (the Mac's chat window), not a split's detail column: the window's title bar has
    /// its title.
    var ownWindow = false
    /// The Sidebar layout on a phone (#6332): the list is the drawer's, and a first page has the drawer's button where a
    /// back would be.
    var drawer = false
    /// The system's bars run down the side (the open Duo on its side, #4853): their Show Sidebar brings the list back.
    var sideBars = false
    /// The list picks the page's root (#8359, `SplitNavigation(rootsSwap:)`): a root draws its own sidebar button.
    var roots = false
}

extension EnvironmentValues {
    @Entry var splitDetail: SplitDetail?
    /// Pushes a page by value onto the stack the view is in (an extension's `push`, docs/extensions/extensions.md「Page navigation」): its
    /// destination is registered with the list's.
    @Entry var splitPush: SplitPush?
    /// Opens the list column of the split the page is in again (iOS); nil in a single stack.
    @Entry var splitShowList: SplitShowList?
    #if os(iOS)
    /// The drawer the next `SplitNavigation` puts its list in at a phone's width (the Sidebar layout, #6332).
    @Entry var splitDrawer: AssistantDrawer?
    /// The system puts its bars down the side beside the next `SplitNavigation` (the Sidebar layout on the open iPhone Duo
    /// on its side): its columns take a top margin (#4853).
    @Entry var splitSideBars = false
    #endif
}

/// Brings a `SplitNavigation`'s list column back, or (the Sidebar layout's Persona page, its sidebar button, #6332)
/// folds it away.
///
/// One for the split's life, its actions set again each time the split's body runs (`SplitNavigation`): made in the body,
/// a value of closures never compared equal to the last, and every page under the split that reads it ran again with the
/// split's body, and with each of them everything it hands closures down to (each message of the conversation). The
/// split's body runs as a tab's page leaves the window and comes back: three or four times on each switch between the
/// open iPhone Duo's tabs (#10303).
@MainActor final class SplitShowList {
    private(set) var show: () -> Void
    private(set) var hide: (() -> Void)?
    /// Slides the list away if it lies over the page (#4852), for a pick that leaves the page as it was (the conversation
    /// picked while it is already there); beside the page it stays.
    private(set) var hideOverlay: (() -> Void)?

    init(show: @escaping () -> Void = {}, hide: (() -> Void)? = nil, hideOverlay: (() -> Void)? = nil) {
        self.show = show
        self.hide = hide
        self.hideOverlay = hideOverlay
    }

    /// The split's current actions; the object handed down stays the same.
    func set(show: @escaping () -> Void, hide: (() -> Void)? = nil, hideOverlay: (() -> Void)? = nil) -> SplitShowList {
        self.show = show
        self.hide = hide
        self.hideOverlay = hideOverlay
        return self
    }
}

/// Appends a value to a `SplitNavigation`'s path. One for the split's life, as `SplitShowList`.
@MainActor final class SplitPush {
    private(set) var push: (any Hashable) -> Void

    init(push: @escaping (any Hashable) -> Void = { _ in }) {
        self.push = push
    }

    func set(_ push: @escaping (any Hashable) -> Void) -> SplitPush {
        self.push = push
        return self
    }
}

extension View {
    /// A page the list opens: in the split's detail column its first page has no back button. `ownsBack`: the page
    /// shows or hides the system's back itself (an Agents chat); on iOS this leaves it alone, as an outer
    /// `navigationBarBackButtonHidden(false)` wins over the page's own `true` (iPhone Duo's side bars, 2026-11-04: the
    /// system's back beside the chat's own, two backs). On the Mac the title row draws no ‹ for it: the chat's header
    /// has its own, shown only with a page under the chat (#1337).
    func splitDetailPage(ownsBack: Bool = false) -> some View { modifier(SplitDetailPage(ownsBack: ownsBack)) }
}

private struct SplitDetailPage: ViewModifier {
    @Environment(\.splitDetail) private var split
    var ownsBack = false

    func body(content: Content) -> some View {
        #if os(macOS)
        content.modifier(MacDetailBack(shown: !ownsBack && split?.isFirstPage == false))
        #else
        // `ownsBack` is fixed for a page (no identity change as the path grows).
        Group {
            if ownsBack { content } else { content.navigationBarBackButtonHidden(split?.isFirstPage == true) }
        }
        #if os(iOS)
        // The Sidebar layout on a phone (#6332): a first page opens the drawer where its back would be.
        .modifier(DrawerButtonItem(shown: !ownsBack && split?.drawer == true && split?.isFirstPage == true))
        // A first page with its list folded beside or over it (#8603): the sidebar's button where a back would be.
        // Under side bars it is the side column's first item, where Back is on a sub-page (#11636).
        .modifier(ListButtonItem(shown: !ownsBack && split?.roots == true && split?.drawer != true
                                    && split?.isFirstPage == true && split?.listHidden == true))
        #endif
        #endif
    }
}

/// The detail column before anything is open: a quiet hint.
struct SplitPlaceholder: View {
    @Environment(\.splitDetail) private var split
    @Environment(\.splitShowList) private var showList
    var symbol: String
    var title: String
    var message: String

    var body: some View {
        ContentUnavailableView {
            Label(title, systemImage: symbol)
        } description: {
            Text(message)
        } actions: {
            // The list closed with nothing open: this page hides its bar, where the system's button to open the list
            // would be, so it filled the screen with no way back to the list.
            if split?.listHidden == true, let showList {
                Button { showList.show() } label: { Text("Show List").lineLimit(1) }.themedButton()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.pageBackground)
        .toolbarVisibility(.hidden, for: .navigationBar)
    }
}

/// Where a split's list lies, beside the page or over it, and whether it is shown, remembered under `key` (#2624, #4206;
/// iPad and iPhone Duo open).
///
/// - **Beside or over** follows the split's width alone (`SplitColumns.fitsBeside`, the rule the split itself lays the
///   columns by, `SplitPageMin`): beside where the page keeps its least width, over it elsewhere (the open Duo and an
///   iPad mini, upright). Turned to over, a list that was out goes away (as an iPad's sidebar does in portrait); turned
///   back, it comes back as it was left beside the page.
/// - **Remembered** only as a person leaves it beside the page: the column's own buttons and a finger's slide. Not over the
///   page (there its going away is a pick's or a tap on the page), not a turn's (beside by both SwiftUI's geometry and the
///   split's own size, `SplitColumnState`: as a turn lays the split out either can have the new size first; nor in a tab
///   not shown, whose size is not read until it shows), not a drag's moves (its end sets it), not in the background (the
///   system lays the app out in the other orientation for the app switcher's snapshots, and a write there ran the phone
///   out of the scene's 10 seconds, 0x8BADF00D). The split starts as the list was left. nil key: the posture only.
///
/// The list has one width (`SidebarWidth`); a width kept by the old handle (`<key>.width`) is no longer read. Not on the
/// Mac or Apple Vision Pro.
#if os(iOS)
struct SplitListMemory: ViewModifier {
    @Binding var columns: NavigationSplitViewVisibility
    let key: String?
    @StoredValue private var closed: Bool
    @Environment(\.scenePhase) private var scenePhase
    /// Where the list lies, set here for the split (nil before the split is laid out).
    var overlays: Binding<Bool?>?
    var state: SplitColumnState?
    /// The posture as last measured, and as last acted on (in front only).
    @State private var measured: Bool?
    @State private var fits: Bool?

    init(columns: Binding<NavigationSplitViewVisibility>, key: String?, overlays: Binding<Bool?>? = nil, state: SplitColumnState? = nil) {
        _columns = columns
        self.key = key
        _closed = StoredValue(wrappedValue: false, (key ?? "split.list") + ".closed")
        self.overlays = overlays
        self.state = state
    }

    /// Where a finger's slide left the list, kept as a person's choice beside the page once the finger is off (the split's
    /// columns already say so as a slide out begins, so their change shows nothing as it ends).
    @MainActor static func keep(_ key: String?, shown: Bool, state: SplitColumnState) {
        guard let key, !state.dragging, state.split?.view.window != nil, state.fitsBeside == true else { return }
        StoredValue(wrappedValue: false, key + ".closed").wrappedValue = !shown
        keptClosed[key] = !shown
    }

    /// Whether the list kept under `key` was closed when last left; the split starts so. Read where the split's state
    /// starts, which runs with every body of its parent: from UserDefaults once per key, then from `keptClosed` (P2).
    static func startsClosed(_ key: String?) -> Bool {
        guard let key, UIDevice.current.userInterfaceIdiom == .pad || UIDevice.current.userInterfaceIdiom == .phone else { return false }
        if let kept = keptClosed[key] { return kept }
        let kept = UserDefaults.standard.bool(forKey: key + ".closed")
        keptClosed[key] = kept
        return kept
    }

    /// Each list's closed as last kept, by key (`startsClosed`).
    private static var keptClosed: [String: Bool] = [:]

    private var shown: Bool { columns != .detailOnly }

    func body(content: Content) -> some View {
        if UIDevice.current.userInterfaceIdiom == .pad || UIDevice.current.userInterfaceIdiom == .phone {
            let list = SplitColumns.listWidth
            content
                .onAppear { if key != nil, closed, shown { columns = .detailOnly } }
                .onGeometryChange(for: Bool.self) { $0.size.width - list >= Theme.Size.splitPageMin } action: { now in
                    measured = now
                    if scenePhase == .active { turned(now) }
                }
                // Back in front: as it is laid out now (a turn in the background was not acted on).
                .onChange(of: scenePhase) { _, phase in if phase == .active, let measured { turned(measured) } }
                .onChange(of: columns) { _, now in
                    // Beside the page by both SwiftUI's geometry and the split's own size: as a turn lays the split out
                    // either can be the first to have the new one, and a list the turn put away was kept as closed. On
                    // screen only: with Separate tabs the tab not shown is turned too, its size not read until it shows.
                    guard let key, scenePhase == .active, state?.dragging != true, state?.split?.view.window != nil,
                          fits == true, state?.fitsBeside != false else { return }
                    closed = now == .detailOnly
                    Self.keptClosed[key] = closed
                }
        } else {
            content
        }
    }

    private func turned(_ now: Bool) {
        let was = fits
        fits = now
        if let overlays, overlays.wrappedValue != !now { overlays.wrappedValue = !now }
        guard was != now else { return }
        if !now {
            // Over the page: a list that was out goes away (a launch upright too); it comes from its button.
            if shown { columns = .detailOnly }
        } else if was == false {
            // Back beside the page: as it was left there.
            let kept: NavigationSplitViewVisibility = key != nil && closed ? .detailOnly : SplitColumns.shown
            if columns != kept { columns = kept }
        }
    }
}

/// A split's page is never narrower than `Theme.Size.splitPageMin` beside the list column: where the column would leave it
/// less, the split lays the column over the page (#4206). Said to SwiftUI, which sets the split's least page width
/// itself: set on the split from UIKit, SwiftUI set it back on every update.
struct SplitPageMin: ViewModifier {
    func body(content: Content) -> some View {
        content.navigationSplitViewColumnWidth(min: Theme.Size.splitPageMin, ideal: Theme.Size.splitPageMin)
    }
}
#endif

#if os(iOS)
/// The drawer's button at the leading edge of a first page's bar in the Sidebar layout on a phone (#6332): the persona's
/// face as on the conversation (`ShellPersonaButton`, the inbox's mark), with how many Agents need the user. Only where
/// it shows; everywhere else the page is left exactly as it was.
/// A root's button that brings its folded list back (#8603): the page was picked in the list, so where a back would be
/// it has the sidebar's button, as the main conversation has. Not the system's toggle (removed on the roots' column):
/// UIKit drew that on a root at first but not again after a page pushed on the root was popped (the open Duo upright,
/// Inbox → an Agent's chat → Back), which left the root with no way back to the sidebar.
struct ListButtonItem: ViewModifier {
    var shown: Bool
    @Environment(\.splitShowList) private var showList

    func body(content: Content) -> some View {
        if shown {
            content
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button { showList?.show() } label: {
                            Label(String(localized: "Show Sidebar"), systemImage: Theme.Icon.sidebar).labelStyle(.iconOnly)
                        }
                        .accessibilityIdentifier("sidebar-show-button")
                    }
                }
        } else {
            content
        }
    }
}

struct DrawerButtonItem: ViewModifier {
    var shown: Bool
    @Environment(\.splitShowList) private var showList

    func body(content: Content) -> some View {
        if !shown {
            content
        } else if #available(iOS 26.0, *) {
            content.toolbar { item.sharedBackgroundVisibility(.hidden) }
        } else {
            // iOS 18 (#6730): no glass behind the item to hide.
            content.toolbar { item }
        }
    }

    private var item: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            ShellPersonaButton(id: "sidebar-drawer-button") { showList?.show() }
                .overlay(alignment: .topTrailing) { SidebarWaitingBadge() }
        }
    }
}
#endif
