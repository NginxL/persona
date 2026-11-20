import SwiftUI

/// Someone to show a profile card for (from a sender avatar / name, a mention, a leader link).
struct LarkPersonRef: Identifiable, Hashable {
    var id: String
    var name: String
    var avatar: String?
    /// The chat the person was tapped in: the card then shows their group nickname.
    var feedID: String?
}

extension EnvironmentValues {
    /// Opens a person's profile card; set by the chat view.
    @Entry var larkOpenProfile: ((LarkPersonRef) -> Void)? = nil
}

/// A person's profile card, laid out like the Lark gateway's (profile-dialog): cover, avatar, names, status, signature,
/// Message, then the info rows (first five, Show More for the rest). Leader rows open the leader's card.
struct LarkProfileView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.feishuOpen) private var feishuOpen
    @Environment(\.larkAccount) private var account
    @Environment(\.larkManager) private var manager
    @Environment(\.larkGroup) private var group
    var person: LarkPersonRef
    /// Pushed inside an existing navigation stack (a route) rather than presented as its own sheet.
    var embedded = false
    @State private var profile: LarkProfile?
    @State private var error: String?
    @State private var showAll = false
    @State private var path = NavigationPath()
    @State private var opening = false

    var body: some View {
        if embedded {
            card
        } else {
            NavigationStack(path: $path) {
                card
                    .pageToolbar { SheetClose { dismiss() } }
                    .feishuRoutes()
            }
            .environment(\.feishuOpen) { path.append($0) }
        }
    }

    /// Opens a chat / another profile in whichever stack shows this card.
    private func push(_ route: FeishuRoute) {
        if embedded, let feishuOpen { feishuOpen(route) } else { path.append(route) }
    }

    private var card: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                header
                if let error { Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger) }
                if let sig = profile?.description, !sig.isEmpty {
                    Text(sig).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(6)
                        .padding(Theme.Space.m).frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color.primary.opacity(0.04), in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
                }
                if profile?.userType != "bot" { messageButton }
                fields
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.bottom, Theme.Space.xl)
        }
        .ignoresSafeArea(edges: .top)
        .task {
            await model.readMessagingSource(group: group, manager: manager) // a profile also opens outside its chat (a document)
            guard let api = model.larkAPI(group: group, manager: manager, account: account) else { return }
            do { profile = try await api.larkProfile(person.id, feedID: person.feedID) } catch { self.error = error.localizedDescription }
        }
    }

    private var displayName: String { profile?.alias.flatMap { $0.isEmpty ? nil : $0 } ?? profile?.displayName ?? person.name }

    private var header: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            ZStack(alignment: .bottomLeading) {
                Group {
                    if let cover = profile?.coverUrl, !cover.isEmpty {
                        LarkRemoteImage(url: cover, maxPixel: 1200)
                    } else {
                        LinearGradient(colors: [LarkColor.blue.opacity(0.35), LarkColor.blue.opacity(0.12)], startPoint: .topLeading, endPoint: .bottomTrailing)
                    }
                }
                .scaledFrame(height: Theme.Size.box150)
                .frame(maxWidth: .infinity)
                .clipped()
                .padding(.horizontal, -16)
                LarkAvatarImage(name: displayName, url: profile?.picture ?? person.avatar, size: 80)
                    .overlay(Circle().strokeBorder(Color(.systemBackground), lineWidth: 3))
                    .offset(y: 40)
            }
            .padding(.bottom, 40)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    Text(displayName).font(Theme.Font.title).lineLimit(2)
                    if profile?.userType == "bot" { LarkNameTag(text: String(localized: "Bot")) }
                    if profile?.relation?.external == true { LarkNameTag(text: String(localized: "External")) }
                }
                let subtitle = [profile?.enName, profile?.alias?.isEmpty == false ? profile?.displayName : nil, profile?.groupNickname.map { String(localized: "Group nickname: \($0)") }]
                    .compactMap { $0 }.filter { !$0.isEmpty && $0 != displayName }
                if !subtitle.isEmpty { Text(subtitle.joined(separator: " · ")).font(Theme.Font.secondary).foregroundStyle(.secondary) }
                HStack(spacing: Theme.Space.sm) {
                    if let s = profile?.workStatus?.status, s == 1 || s == 2 { chip(s == 1 ? String(localized: "On leave") : String(localized: "In a meeting"), color: .orange) }
                    if let t = profile?.customStatus?.title, !t.isEmpty { chip(t, color: LarkColor.blue) }
                    if let m = profile?.medalName, !m.isEmpty { chip(m, color: .purple) }
                    if let tz = profile?.timezone, let zone = TimeZone(identifier: tz), zone.secondsFromGMT() != TimeZone.current.secondsFromGMT() {
                        let localTime = Date.now.formatted(Date.FormatStyle(date: .omitted, time: .shortened, timeZone: zone))
                        chip(String(localized: "Local time \(localTime)"), color: .gray)
                    }
                }
                if profile?.relation?.external == true, let t = profile?.relation?.tenantName { Text(t).font(Theme.Font.caption).foregroundStyle(.secondary) }
            }
        }
    }

    private func chip(_ text: String, color: Color) -> some View {
        Text(text).font(Theme.Font.labelMedium).lineLimit(1).foregroundStyle(color)
            .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xs).background(color.opacity(0.12), in: .capsule)
    }

    private var messageButton: some View {
        Button {
            Task {
                guard let api = model.larkAPI(group: group, manager: manager, account: account) else { return }
                opening = true
                defer { opening = false }
                do {
                    let feed = try await api.larkDirectChat(person.id)
                    var chat = LarkConversation(id: feed, name: displayName, avatar: profile?.picture ?? person.avatar, isGroup: false)
                    chat.account = account
                    push(.chat(chat, manager: manager)) // a pushed page doesn't inherit this one's manager
                } catch {
                    model.show(error)
                }
            }
        } label: {
            Group { if opening { ProgressView() } else { Label("Message [Lark People fd351b]", systemImage: Theme.Icon.message).lineLimit(1) } }
                .font(Theme.Font.secondaryStrong)
                .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
        }
        .buttonStyle(.borderedProminent)
        .tint(LarkColor.blue)
        .disabled(opening)
    }

    /// Rows with a value; relation / alias rows (shown in the header) and empty ones are skipped.
    private var rows: [LarkProfile.Field] {
        (profile?.fields ?? []).filter { f in
            guard let v = f.value?.string, !v.isEmpty else { return false }
            return !["B-ALIAS", "B-ALIAS-AND-MEMO", "B-CHATGROUPNICKNAME", "B-MORE"].contains(f.key)
        }
    }

    @ViewBuilder private var fields: some View {
        let all = rows
        let shown = showAll ? all : Array(all.prefix(5))
        if !all.isEmpty {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(shown.enumerated()), id: \.offset) { i, f in
                    if i > 0 { Divider() }
                    row(f)
                }
                if all.count > 5 {
                    Divider()
                    Button { withAnimation(Theme.Motion.standard) { showAll.toggle() } } label: {
                        Text(showAll ? String(localized: "Collapse") : String(localized: "Show More")).font(Theme.Font.secondary).foregroundStyle(LarkColor.blue).lineLimit(1)
                            .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .background(Color.primary.opacity(0.04), in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
    }

    private func row(_ f: LarkProfile.Field) -> some View {
        let value = f.value?.string ?? ""
        let label = switch f.key {
        case "B-DEPARTMENT": String(localized: "Department")
        case "B-LEADER": String(localized: "Manager")
        case "B-EMPLOYEETYPE": String(localized: "Employee type")
        default: f.label ?? f.key
        }
        let leader = f.url.flatMap { u in u.hasPrefix("lark://client/profile") ? URLComponents(string: u)?.queryItems?.first { $0.name == "uid" }?.value : nil }
        let link = f.url.flatMap { $0.hasPrefix("http") ? URL(string: $0) : nil }
        return HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            Text(label).font(Theme.Font.secondary).foregroundStyle(.secondary).scaledFrame(width: Theme.Size.box76, alignment: .leading).lineLimit(2)
            Group {
                if let leader {
                    Button { push(.profile(LarkPersonRef(id: leader, name: value), manager: manager)) } label: { Text(value).foregroundStyle(LarkColor.blue) }.buttonStyle(.plain)
                } else if let link {
                    Link(value, destination: link).foregroundStyle(LarkColor.blue)
                } else {
                    Text(value).textSelection(.enabled)
                }
            }
            .font(Theme.Font.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, Theme.Space.m)
        .contextMenu {
            Button { UIPasteboard.general.string = value } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
        }
    }
}
