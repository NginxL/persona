import Foundation

/// The icons a space may take (#13500): the server's tokens (the web's `SPACE_ICONS`, in its order), never a symbol's
/// name or a color, each drawn with the vocabulary's symbol for it.
nonisolated enum SpaceIcon {
    /// The tokens in the order the new space form offers them.
    static let tokens = ["layers", "book", "wallet", "home", "leaf", "car", "calendar", "star", "flag", "users", "music",
                         "camera", "chart", "target", "smile", "sun"]

    /// The symbol for a token; one this app does not know is the layers mark.
    static func symbol(_ token: String) -> String {
        switch token {
        case "book": Theme.Icon.spaceBook
        case "wallet": Theme.Icon.pluginWallet
        case "home": Theme.Icon.home
        case "leaf": Theme.Icon.pluginLeaf
        case "car": Theme.Icon.pluginCar
        case "calendar": Theme.Icon.pluginCalendar
        case "star": Theme.Icon.pluginStar
        case "flag": Theme.Icon.spaceFlag
        case "users": Theme.Icon.people
        case "music": Theme.Icon.music
        case "camera": Theme.Icon.camera
        case "chart": Theme.Icon.pluginChart
        case "target": Theme.Icon.goal
        case "smile": Theme.Icon.spaceSmile
        case "sun": Theme.Icon.spaceSun
        default: Theme.Icon.domainSpace
        }
    }

    /// A track's symbol: its own icon when it is a space icon, else its kind's, else layers.
    static func symbol(of track: AssistantTrack?) -> String {
        let own = track.map { $0.icon.isEmpty ? $0.config.sidebar.icon : $0.icon } ?? ""
        return tokens.contains(own) ? symbol(own) : Theme.Icon.domainSpace
    }

    /// What VoiceOver reads for a token in the icon picker.
    static func name(_ token: String) -> String {
        switch token {
        case "book": String(localized: "Book [Space Icon]")
        case "wallet": String(localized: "Wallet [Space Icon]")
        case "home": String(localized: "Home [Space Icon]")
        case "leaf": String(localized: "Leaf [Space Icon]")
        case "car": String(localized: "Car [Space Icon]")
        case "calendar": String(localized: "Calendar [Space Icon]")
        case "star": String(localized: "Star [Space Icon]")
        case "flag": String(localized: "Flag [Space Icon]")
        case "users": String(localized: "People [Space Icon]")
        case "music": String(localized: "Music [Space Icon]")
        case "camera": String(localized: "Camera [Space Icon]")
        case "chart": String(localized: "Chart [Space Icon]")
        case "target": String(localized: "Target [Space Icon]")
        case "smile": String(localized: "Smile [Space Icon]")
        case "sun": String(localized: "Sun [Space Icon]")
        default: String(localized: "Layers [Space Icon]")
        }
    }
}

/// Plain words for the information categories, everywhere a category shows; one this app does not know shows as the
/// hub wrote it.
nonisolated enum InfoCategoryWords {
    static func name(_ category: String) -> String {
        switch category {
        case "health": String(localized: "Health [Info Category]")
        case "finance": String(localized: "Money [Info Category]")
        case "relationships": String(localized: "Relationships [Info Category]")
        case "emotion": String(localized: "Feelings [Info Category]")
        case "legal_id": String(localized: "IDs and legal [Info Category]")
        default: category
        }
    }
}
