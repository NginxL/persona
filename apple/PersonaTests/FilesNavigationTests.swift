import Foundation
import Testing
import UniformTypeIdentifiers
@testable import Persona

/// The file page's navigation, finding and Info words (#5827): history like a browser, the path bar from the home or `/`,
/// which folders take changes, find's ranking, permissions in words, and the details the hub sends.
@MainActor
struct FilesNavigationTests {
    @Test func backAndForwardWalkTheHistory() {
        var n = FilesNavigation("/w/app")
        #expect(!n.canGoBack && !n.canGoForward)
        n.go("/w/app/src")
        n.go("/w/app/src/lib")
        n.goBack()
        #expect(n.current == "/w/app/src" && n.canGoForward)
        n.goBack()
        #expect(n.current == "/w/app" && !n.canGoBack)
        n.goForward()
        #expect(n.current == "/w/app/src")
        n.go("/w") // a new folder clears Forward
        #expect(!n.canGoForward && n.back == ["/w/app", "/w/app/src"])
        n.go("/w") // the same folder again changes nothing
        #expect(n.back.count == 2)
    }

    @Test func historyIsBounded() {
        var n = FilesNavigation("/0")
        for i in 1...(FilesNavigation.historyLimit + 10) { n.go("/\(i)") }
        #expect(n.back.count == FilesNavigation.historyLimit)
        #expect(n.back.first == "/10")
    }

    @Test func upStopsAtTheTop() {
        #expect(FilesNavigation.parent(of: "/Users/me/code") == "/Users/me")
        #expect(FilesNavigation.parent(of: "/Users") == "/")
        #expect(FilesNavigation.parent(of: "/") == nil)
    }

    @Test func thePathBarStartsAtTheHomeInsideIt() {
        let inHome = FilesNavigation.segments("/Users/me/code/app", home: "/Users/me")
        #expect(inHome.map(\.title) == ["~", "code", "app"])
        #expect(inHome.map(\.path) == ["/Users/me", "/Users/me/code", "/Users/me/code/app"])
        #expect(inHome[0].home)
        #expect(FilesNavigation.segments("/Users/me", home: "/Users/me").map(\.title) == ["~"])
        // A folder beside the home whose name starts like it is not in it.
        let outside = FilesNavigation.segments("/Users/meg/x", home: "/Users/me")
        #expect(outside.map(\.title) == ["/", "Users", "meg", "x"])
        #expect(outside.map(\.path) == ["/", "/Users", "/Users/meg", "/Users/meg/x"])
        #expect(FilesNavigation.segments("/", home: nil).map(\.path) == ["/"])
    }

    @Test func tildeShortensTheHomeOnly() {
        #expect(FilesNavigation.tilde("/Users/me/a", home: "/Users/me") == "~/a")
        #expect(FilesNavigation.tilde("/Users/me", home: "/Users/me") == "~")
        #expect(FilesNavigation.tilde("/Users/meg", home: "/Users/me") == "/Users/meg")
        #expect(FilesNavigation.tilde("/tmp", home: nil) == "/tmp")
    }

    @Test func changesAreForTheHomeButItsPrivateFolders() {
        let home = "/Users/me"
        #expect(FilesNavigation.writable("/Users/me/code", home: home))
        #expect(FilesNavigation.writable(home, home: home))
        #expect(!FilesNavigation.writable("/tmp", home: home))
        #expect(!FilesNavigation.writable("/Users/meg", home: home))
        #expect(!FilesNavigation.writable("/Users/me/.persona/x", home: home))
        #expect(!FilesNavigation.writable("/Users/me/Library", home: home))
        #expect(FilesNavigation.writable("/Users/me/Library2", home: home))
        #expect(FilesNavigation.writable("/anything", home: nil)) // unknown: the computer decides
    }

    @Test func findMatchesNamesBestFirst() {
        let entries = FileFind.entries(["src/config/index.ts", "src/loadConfig.ts", "config.ts", "docs/app-config.md", "README.md"])
        #expect(entries.filter(\.dir).map(\.path) == ["docs", "src", "src/config"])
        let hits = FileFind.search(entries, query: "config")
        // The folder named exactly, then a name starting with it, then a word in it, then anywhere.
        #expect(hits.map(\.path) == ["src/config", "config.ts", "docs/app-config.md", "src/loadConfig.ts"])
        #expect(FileFind.search(entries, query: "CONFIG ts").map(\.path) == ["config.ts", "src/loadConfig.ts"])
        #expect(FileFind.search(entries, query: "   ").isEmpty)
        #expect(FileFind.search(entries, query: "zzz").isEmpty)
        #expect(hits[2].folder == "docs" && hits[2].name == "app-config.md")
    }

    @Test func permissionsInWords() {
        #expect(FilePermissions.symbolic(0o644) == "rw-r--r--")
        #expect(FilePermissions.symbolic(0o755) == "rwxr-xr-x")
        #expect(FilePermissions.symbolic(0o4755) == "rwsr-xr-x")
        #expect(FilePermissions.symbolic(0o1777) == "rwxrwxrwt")
        #expect(FilePermissions.symbolic(0o2640) == "rw-r-S---")
        #expect(FilePermissions.octal(0o644) == "644")
        #expect(FilePermissions.octal(0o4755) == "4755")
        #expect(FilePermissions.words(0o644) == String(localized: "Owner: \(FilePermissions.access(6)) · Everyone else: \(FilePermissions.access(4))"))
        #expect(FilePermissions.words(0o750).contains(FilePermissions.access(5)))
        #expect(FilePermissions.access(0) != FilePermissions.access(7))
    }

    @Test func thePickerOpensDownToTheFolderShown() {
        #expect(FolderPicker.opened(from: "/w", to: "/w/a/b") == ["/w/a", "/w/a/b"])
        #expect(FolderPicker.opened(from: "/w", to: "/w") == [])
        #expect(FolderPicker.opened(from: "/w", to: "/x/y") == [])
        #expect(FolderPicker.opened(from: "/w", to: "/wx") == [])
        #expect(FolderPicker.opened(from: "/", to: "/a/b") == ["/a", "/a/b"])
    }

    @Test func decodesDetailsAndLinks() throws {
        let entry = try JSONDecoder().decode(FileEntry.self, from: Data(#"{"name":"old","dir":false,"symlink":true,"size":0,"link_target":"../x","link_broken":true}"#.utf8))
        #expect(entry.linkTarget == "../x" && entry.linkBroken == true)
        let plain = try JSONDecoder().decode(FileEntry.self, from: Data(#"{"name":"a","dir":false,"size":3}"#.utf8))
        #expect(plain.linkTarget == nil && plain.linkBroken == nil)
        let info = try JSONDecoder().decode(FileInfo.self, from: Data(#"{"path":"l","kind":"dir","mode":493,"owner":"me","group":"staff","items":3,"items_more":false,"symlink":true,"link_target":"../w","link_real":"/Users/me/w"}"#.utf8))
        #expect(info.mode == 0o755 && info.linkReal == "/Users/me/w" && info.items == 3 && info.detailed)
        let old = try JSONDecoder().decode(FileInfo.self, from: Data(#"{"path":"a","kind":"file","size":3}"#.utf8))
        #expect(!old.detailed)
    }

    @Test func theKindOfATSFileComesFromItsBytes() throws {
        #expect(FileKind.type(name: "app.ts", mime: "text/typescript") == .sourceCode)
        #expect(FileKind.type(name: "lib/esm.MTS", mime: "text/plain; charset=utf-8") == .sourceCode)
        #expect(FileKind.type(name: "clip.ts", mime: "video/mp2t") == .mpeg2TransportStream)
        #expect(FileKind.type(name: "cjs.cts", mime: nil) == nil) // an older node read nothing: just "File"
        #expect(FileKind.type(name: "main.swift", mime: nil) == .swiftSource)
        #expect(FileKind.type(name: "Makefile", mime: "text/plain") == nil)
        let info = try JSONDecoder().decode(FileInfo.self, from: Data(#"{"path":"a.ts","kind":"file","size":3,"mime":"text/typescript"}"#.utf8))
        #expect(info.mime == "text/typescript")
    }

    @Test func asksForDetailsAndFiles() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/m1/stat", json: #"{"stats":[{"path":"~/a","kind":"file","size":3,"mode":420}]}"#)
        hub.on("GET", "/api/v1/machines/m1/fs/find", json: #"{"cwd":"/w","files":["a.txt","src/b.swift"],"truncated":false}"#)
        let info = try await hub.client.fileInfo(machine: "m1", path: "~/a", cwd: "/w")
        #expect(info?.mode == 0o644)
        let stat = try #require(hub.sent("GET", "/api/v1/machines/m1/stat").first)
        #expect(stat.query["detail"] == "1" && stat.query["path"] == "~/a")
        let found = try await hub.client.findFiles(machine: "m1", dir: "/w")
        #expect(found.files == ["a.txt", "src/b.swift"])
        #expect(hub.sent("GET", "/api/v1/machines/m1/fs/find").first?.query["cwd"] == "/w")
    }
}
