import Foundation

/// Add Mail Account's form as a value: what the fields become as a request to the hub.
/// `AddMailAccountView` binds to it and talks to the hub; `MailAccountFormTests` checks it.
struct MailAccountForm: Equatable {
    enum Kind: String, CaseIterable, Identifiable {
        case gmail = "Gmail", outlook = "Outlook", feishu = "飞书", imap = "其他 IMAP" // i18n-ignore: ids; shown via `title`
        var id: String { rawValue }
        /// The segment's label (the raw value is the id).
        var title: String {
            switch self {
            case .gmail: "Gmail"
            case .outlook: "Outlook"
            case .feishu: String(localized: "Lark [Inbox Event Views 7e4419]")
            case .imap: String(localized: "Other IMAP")
            }
        }
    }

    var kind: Kind = .gmail
    var email = ""
    var password = ""
    var imapHost = ""
    var imapPort = "993"
    var smtpHost = ""
    var smtpPort = "465"
    /// Gmail on a hub without Google sign-in (no google.client_id, #2442): added with an app password instead.
    var gmailAppPassword = false

    var canSave: Bool { !email.isEmpty && !password.isEmpty }

    /// The account to add with a password; nil for Outlook and for Gmail on a hub that can sign in with Google.
    var request: APIClient.AddAccount? {
        var req = APIClient.AddAccount(name: email, email: email, auth: "password")
        switch kind {
        case .gmail:
            guard gmailAppPassword else { return nil }
            // Google's servers spelled out: a Google Workspace address has its own domain, which no preset knows.
            req.provider = "gmail"
            req.password = password
            req.imapHost = "imap.gmail.com"; req.imapPort = 993
            req.smtpHost = "smtp.gmail.com"; req.smtpPort = 465; req.smtpSecurity = "tls"
        case .outlook:
            return nil
        case .feishu:
            req.provider = "feishu"
            req.password = password
        case .imap:
            req.provider = "imap"
            req.password = password
            if !imapHost.isEmpty { req.imapHost = imapHost; req.imapPort = Int(imapPort) }
            if !smtpHost.isEmpty { req.smtpHost = smtpHost; req.smtpPort = Int(smtpPort); req.smtpSecurity = smtpPort == "587" ? "starttls" : "tls" }
        }
        return req
    }

    /// Where a QQ / NetEase / Gmail / iCloud address typed under Other IMAP makes its authorization code or app password; nil for any other address,
    /// and once servers are typed (they may not be the provider's).
    var codeGuide: CodeGuide? {
        guard kind == .imap, imapHost.isEmpty, smtpHost.isEmpty, let at = email.lastIndex(of: "@") else { return nil }
        return CodeGuide(domain: email[email.index(after: at)...].trimmingCharacters(in: .whitespaces).lowercased())
    }
}

/// A mail provider whose clients sign in with an authorization code or app password made on its website, not the account's password. The hub
/// says the same steps for the same domains (`server/internal/modules/mail/preset.go`, `Guide`): change them together.
struct CodeGuide: Equatable {
    enum Provider: Equatable { case qq, netease, gmail, icloud }
    let provider: Provider
    /// The page that makes the code (the web mail's settings, Google's App passwords, Apple Account).
    let webMail: URL

    init?(domain: String) {
        let site: String
        switch domain {
        case "qq.com", "foxmail.com", "vip.qq.com": provider = .qq; site = "mail.qq.com"
        case "163.com", "126.com", "yeah.net": provider = .netease; site = "mail." + domain
        case "vip.163.com", "vip.126.com": provider = .netease; site = domain
        case "188.com": provider = .netease; site = "www.188.com"
        case "gmail.com", "googlemail.com": provider = .gmail; site = "myaccount.google.com/apppasswords"
        case "icloud.com", "me.com": provider = .icloud; site = "account.apple.com"
        default: return nil
        }
        guard let url = URL(string: "https://" + site) else { return nil }
        webMail = url
    }

    /// Gmail added with an app password whatever the address's domain is (Google Workspace has its own).
    static let gmailAppPassword = CodeGuide(domain: "gmail.com")!
}
