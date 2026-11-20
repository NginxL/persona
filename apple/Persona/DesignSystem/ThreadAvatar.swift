import SwiftUI

/// A sender or author in a connector conversation. Older hubs list only the connector media ref.
struct ThreadAvatar: View {
    var connector: String
    var person: ThreadPerson
    var size: CGFloat = Theme.Size.wellM
    @Environment(\.larkManager) private var manager
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let path = person.picture(in: connector, manager: manager)
        ChatAvatar(url: path, name: person.name, size: size * Theme.sizeScale(typeSize))
    }
}
