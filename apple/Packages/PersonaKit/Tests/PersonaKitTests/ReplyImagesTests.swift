import CoreGraphics
import Foundation
import Testing
@testable import PersonaKit

struct ReplyImagesTests {
    /// The shape replies take (made up): a bold line, two pictures right under it on their own lines, then a list.
    private let reply = """
    **The kitchen** (from the same post)
    ![kitchen](https://img.example.com/a.jpg)
    ![bathroom](https://img.example.com/b.jpg)
    - The kitchen is separate.

    One more ![icon](https://img.example.com/i.png) in a line.
    """

    @Test func picturesOnTheirOwnLinesBecomeOneBlockOutOfTheParagraph() throws {
        let out = ReplyImages.apply(reply)
        #expect(out == """
        **The kitchen** (from the same post)

        ~~~~persona-images
        show 0 1
        https://img.example.com/a.jpg\tkitchen
        https://img.example.com/b.jpg\tbathroom
        https://img.example.com/i.png\ticon
        ~~~~

        - The kitchen is separate.

        One more ![icon](https://img.example.com/i.png) in a line.
        """)
        let block = try #require(ReplyImages.block("show 0 1\nhttps://img.example.com/a.jpg\tkitchen\nhttps://img.example.com/b.jpg\tbathroom\nhttps://img.example.com/i.png\ticon"))
        #expect(block.images.map(\.alt) == ["kitchen", "bathroom"])
        #expect(block.all.count == 3)
        #expect(ReplyImages.all(in: reply).map(\.alt) == ["kitchen", "bathroom", "icon"])
    }

    @Test func blankLinesBetweenPicturesKeepOneRunAndTextEndsIt() {
        let text = "![a](https://x.test/a.png)\n\n![b](https://x.test/b.png)\nwords\n![c](https://x.test/c.png) ![d](https://x.test/d.png)"
        let out = ReplyImages.apply(text)
        #expect(out.components(separatedBy: "~~~~persona-images").count == 3)
        #expect(out.contains("show 0 1"))
        #expect(out.contains("show 2 3")) // two on one line are one run
        #expect(out.contains("\nwords\n"))
    }

    @Test func codeQuotesLinksAndOtherAddressesStayAsTheyAre() {
        for text in [
            "```\n![a](https://x.test/a.png)\n```",
            "    ![a](https://x.test/a.png)",
            "> ![a](https://x.test/a.png)",
            "[![a](https://x.test/a.png)](https://x.test/page)",
            "`![a](https://x.test/a.png)`",
            "![math](persona-math:inline/x)",
            "![file](file:///tmp/shot.png)",
            "![data](data:image/png;base64,iVBORw0KGgo=)",
            "![attachment](persona-attachment:shot.png)",
            "no pictures here",
        ] {
            #expect(ReplyImages.apply(text) == text, "\(text)")
        }
    }

    @Test func absoluteAndWorkingDirectoryRelativePicturesBecomeBlocks() throws {
        let text = "![截图](/tmp/shot.png)\n![相对](相对/路径/文件.png)\n![short](shot.png)"
        let out = ReplyImages.apply(text)
        #expect(out == """
        ~~~~persona-images
        show 0 2
        /tmp/shot.png\t截图
        相对/路径/文件.png\t相对
        shot.png\tshort
        ~~~~
        """)
        let block = try #require(ReplyImages.block("show 0 2\n/tmp/shot.png\t截图\n相对/路径/文件.png\t相对\nshot.png\tshort"))
        #expect(block.all.map(\.url) == ["/tmp/shot.png", "相对/路径/文件.png", "shot.png"])
        #expect(ReplyImages.all(in: text).map(\.url) == ["/tmp/shot.png", "相对/路径/文件.png", "shot.png"])
    }

    @Test func localPicturesKeepWebCompanyAndInlineOnesKeepTheirText() {
        let mixed = "![web](https://x.test/a.png) ![local](shot.png)"
        #expect(ReplyImages.apply(mixed) == """
        ~~~~persona-images
        show 0 1
        https://x.test/a.png\tweb
        shot.png\tlocal
        ~~~~
        """)

        let inline = "Before ![icon](shots/icon.png) after"
        #expect(ReplyImages.apply(inline) == """
        Before

        ~~~~persona-images
        show 0 0
        shots/icon.png\ticon
        ~~~~

        after
        """)
        #expect(ReplyImages.all(in: inline).map(\.url) == ["shots/icon.png"])
    }

    @Test func titlesAngleBracketsAndTabsInWords() throws {
        let out = ReplyImages.apply("![a\tb](<https://x.test/a b.png> \"Title\")")
        #expect(out.contains("https://x.test/a b.png\ta b"))
        #expect(ReplyImages.block("show 0 0\nhttps://x.test/a b.png\ta b")?.images.first?.url == "https://x.test/a b.png")
    }

    @Test func blocksThatDoNotReadAreNil() {
        #expect(ReplyImages.block("") == nil)
        #expect(ReplyImages.block("show 0 2\nhttps://x.test/a.png\ta") == nil)
        #expect(ReplyImages.block("show 1 0\nhttps://x.test/a.png\ta\nhttps://x.test/b.png\tb") == nil)
        #expect(ReplyImages.block("show 0 0\nfile:///etc/passwd\tx") == nil)
        #expect(ReplyImages.block("print(1)") == nil)
    }

    @Test func aRunInsideAListItemKeepsItsIndent() {
        let out = ReplyImages.apply("- the hall:\n\n  ![a](https://x.test/a.png)\n  ![b](https://x.test/b.png)\n- next")
        #expect(out.contains("\n  ~~~~persona-images\n  show 0 1\n"))
        #expect(out.hasSuffix("  ~~~~\n\n- next"))
    }

    // MARK: Layout

    @Test func aLonePictureIsOneHeightAndItsShapesWidth() {
        let column: CGFloat = 361
        // before it arrives: the 4:3 placeholder, already its final height
        #expect(ReplyImageLayout.single(pixels: nil, column: column, height: 240) == CGSize(width: 320, height: 240))
        // portrait 3:4, landscape 4:3, a panorama is cut to the column, a tall strip held at 3:4
        #expect(ReplyImageLayout.single(pixels: CGSize(width: 1080, height: 1440), column: column, height: 240) == CGSize(width: 180, height: 240))
        #expect(ReplyImageLayout.single(pixels: CGSize(width: 1600, height: 1200), column: column, height: 240) == CGSize(width: 320, height: 240))
        #expect(ReplyImageLayout.single(pixels: CGSize(width: 3000, height: 1000), column: column, height: 240) == CGSize(width: 361, height: 240))
        #expect(ReplyImageLayout.single(pixels: CGSize(width: 300, height: 2000), column: column, height: 240) == CGSize(width: 180, height: 240))
        // smaller than the box: as it is, never enlarged
        #expect(ReplyImageLayout.single(pixels: CGSize(width: 96, height: 96), column: column, height: 240) == CGSize(width: 96, height: 96))
        // a narrow column: never wider than it
        #expect(ReplyImageLayout.single(pixels: nil, column: 200, height: 240).width == 200)
    }

    @Test func runsAreSquaresAtMostTwoRows() {
        let two = ReplyImageLayout.grid(count: 2, column: 361, widest: 480, gap: 4)
        #expect(two == .init(columns: 2, tile: 178.5, shown: 2, more: 0))
        let three = ReplyImageLayout.grid(count: 3, column: 361, widest: 480, gap: 4)
        #expect(three.columns == 3 && three.rows == 1 && abs(three.tile - 117.67) < 0.01)
        let four = ReplyImageLayout.grid(count: 4, column: 361, widest: 480, gap: 4)
        #expect(four.columns == 2 && four.rows == 2 && four.tile == three.tile)
        #expect(ReplyImageLayout.grid(count: 6, column: 361, widest: 480, gap: 4).more == 0)
        let eight = ReplyImageLayout.grid(count: 8, column: 361, widest: 480, gap: 4)
        #expect(eight.shown == 6 && eight.more == 2 && eight.rows == 2)
        // a wide column (iPad) stops at the widest
        #expect(ReplyImageLayout.grid(count: 2, column: 700, widest: 480, gap: 4).tile == 238)
    }

    @Test func sizesAskedCoverWhatIsDrawn() {
        #expect(ReplyImageLayout.size(points: 240, scale: 3) == 768)
        #expect(ReplyImageLayout.size(points: 361, scale: 3) == 1536)
        #expect(ReplyImageLayout.size(points: 178.5, scale: 2) == 512)
        #expect(ReplyImageLayout.size(points: 2000, scale: 3) == 2048)
    }
}
