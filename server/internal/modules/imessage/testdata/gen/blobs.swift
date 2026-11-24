// Synthetic attributedBody blobs: NSAttributedString archived with NSArchiver ("typedstream"), the way Messages
// stores a message's text in chat.db. Every text is made up. Run on a Mac:
//   swift blobs.swift > blobs.json
// make.py reads blobs.json into the synthetic chat.db; typedstream_test.go reads it directly.
import Foundation

func archive(_ s: NSAttributedString) -> String {
    NSArchiver.archivedData(withRootObject: s).map { String(format: "%02x", $0) }.joined()
}

var blobs: [String: String] = [:]
blobs["plain"] = archive(NSAttributedString(string: "示例消息：周五下午三点见"))
blobs["emoji"] = archive(NSAttributedString(string: "👍 好的 🎉"))
// Over 127 bytes: the length takes the two-byte form (0x81).
blobs["long"] = archive(NSAttributedString(string: String(repeating: "这是一条很长的示例消息。", count: 20)))
// (Over 65535 bytes the length takes four bytes, 0x82: typedstream_test.go builds that one from "plain".)
let mention = NSMutableAttributedString(string: "@张三 明天的示例会议改到十点")
mention.addAttribute(NSAttributedString.Key("__kIMMentionConfirmedMention"), value: "+15550100", range: NSRange(location: 0, length: 3))
blobs["mention"] = archive(mention)
let link = NSMutableAttributedString(string: "示例链接 https://example.com/a")
link.addAttribute(NSAttributedString.Key("__kIMLinkAttributeName"), value: URL(string: "https://example.com/a")!, range: NSRange(location: 5, length: 21))
blobs["link"] = archive(link)
blobs["mutable"] = archive(NSMutableAttributedString(string: "示例：可变的字符串"))

let out = try JSONSerialization.data(withJSONObject: blobs, options: [.sortedKeys, .prettyPrinted])
FileHandle.standardOutput.write(out)
FileHandle.standardOutput.write("\n".data(using: .utf8)!)
