import SwiftUI
import UIKit

/// What `CodeLinesView` shows: lines of monospaced text with a gutter (line numbers, or JSON's fold arrows).
@MainActor protocol CodeLineSource: AnyObject {
    var lineCount: Int { get }
    /// The widest line in columns (the content's width; an over-estimate is fine).
    var columns: Int { get }
    /// The gutter's width in columns.
    var gutterColumns: Int { get }
    func line(_ i: Int) -> NSAttributedString
    func gutter(_ i: Int) -> NSAttributedString
    /// A tap on a line; true when the lines changed (a fold opened or closed).
    func tap(_ i: Int) -> Bool
    func menu(_ i: Int) -> UIMenu?
    /// Set by the view: call with the lines that changed (nil: all of them, or how many there are).
    var changed: ((Range<Int>?) -> Void)? { get set }
}

nonisolated enum CodeMetrics {
    /// 12.5 pt at the default text size, following Dynamic Type.
    @MainActor static var font: UIFont {
        .monospacedSystemFont(ofSize: UIFontMetrics(forTextStyle: .footnote).scaledValue(for: 12.5), weight: .regular)
    }
}

/// Lines of code that scroll both ways like an editor, any number of them (docs/apple/ios.md「File viewer」): only the
/// lines on screen (and a few around) are labels, reused as they scroll, so a file of 100 000 lines opens and scrolls
/// like one of ten. The gutter stays put while the code scrolls sideways under it. A long press offers the line's
/// menu (Copy Line…); a tap goes to the source (JSON opens or closes a node).
final class CodeLinesView: UIScrollView, UIScrollViewDelegate, UIContextMenuInteractionDelegate {
    weak var source: CodeLineSource? {
        didSet { reload() }
    }
    var onNearEnd: (() -> Void)?
    /// Once, when the first lines are on screen.
    var onFirstLines: (() -> Void)?
    /// A line (0-based) to mark and, once, bring into view: a message's `path:12`.
    var focus: Int? {
        didSet {
            guard focus != oldValue else { return }
            focusPending = focus != nil
            setNeedsLayout()
        }
    }
    private var focusPending = false
    private let focusBand = UIView()

    let font = CodeMetrics.font
    private(set) lazy var lineHeight: CGFloat = ceil(font.lineHeight * 1.25)
    private(set) lazy var charWidth: CGFloat = ("0" as NSString).size(withAttributes: [.font: font]).width
    private let pad = UIEdgeInsets(top: 10, left: 10, bottom: 24, right: 24)
    private var rows: [Int: UILabel] = [:]
    private var gutters: [Int: UILabel] = [:]
    private var pool: [UILabel] = []
    private let gutterBack = UIView()
    private let gutterRule = UIView()
    private var gutterWidth: CGFloat = 0
    private var autoScroller: AutoScroller?

    override init(frame: CGRect) {
        super.init(frame: frame)
        showsHorizontalScrollIndicator = false
        showsVerticalScrollIndicator = false // no scroll bars anywhere (RootView)
        alwaysBounceVertical = true
        isDirectionalLockEnabled = true
        delegate = self
        gutterBack.backgroundColor = UIColor(Theme.codeBackground)
        gutterRule.backgroundColor = UIColor.separator
        focusBand.backgroundColor = UIColor(Theme.lineFocus)
        focusBand.isHidden = true
        focusBand.isUserInteractionEnabled = false
        addSubview(focusBand) // under the lines
        addSubview(gutterBack)
        addSubview(gutterRule)
        addInteraction(UIContextMenuInteraction(delegate: self))
        addGestureRecognizer(UITapGestureRecognizer(target: self, action: #selector(tapped(_:))))
    }

    required init?(coder: NSCoder) { fatalError() }

    /// Everything again: the line count or the gutter changed.
    func reload() {
        for (_, l) in rows { recycle(l) }
        for (_, l) in gutters { recycle(l) }
        rows = [:]
        gutters = [:]
        source?.changed = { [weak self] range in
            guard let self else { return }
            if let range { self.refresh(range) } else { self.reload() }
        }
        guard let source else { return }
        gutterWidth = CGFloat(source.gutterColumns) * charWidth + 12
        contentSize = CGSize(width: pad.left + gutterWidth + CGFloat(source.columns) * charWidth + pad.right,
                             height: pad.top + CGFloat(source.lineCount) * lineHeight + pad.bottom)
        setNeedsLayout()
    }

    /// The lines in `range` again (their colors came in).
    func refresh(_ range: Range<Int>) {
        guard let source else { return }
        for (i, l) in rows where range.contains(i) && i < source.lineCount {
            l.attributedText = source.line(i)
            size(l, at: i)
        }
    }

    private func dequeue() -> UILabel {
        if let l = pool.popLast() { return l }
        let l = UILabel()
        l.numberOfLines = 1
        l.lineBreakMode = .byClipping
        l.isUserInteractionEnabled = false
        return l
    }

    private func recycle(_ l: UILabel) {
        l.removeFromSuperview()
        l.attributedText = nil
        l.textAlignment = .natural // a gutter's label may come back as a line's
        pool.append(l)
    }

    private func size(_ l: UILabel, at i: Int) {
        let w = ceil(l.attributedText?.size().width ?? 0) + 2
        l.frame = CGRect(x: pad.left + gutterWidth + 8, y: pad.top + CGFloat(i) * lineHeight, width: w, height: lineHeight)
    }

    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        FrameMeter.shared.noteScroll()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard let source else { return }
        let n = source.lineCount
        let first = max(0, Int((contentOffset.y - pad.top) / lineHeight) - 6)
        let last = min(n, Int((contentOffset.y + bounds.height - pad.top) / lineHeight) + 8)
        let visible = first..<max(first, last)
        for (i, l) in rows where !visible.contains(i) {
            recycle(l)
            rows[i] = nil
        }
        for (i, l) in gutters where !visible.contains(i) {
            recycle(l)
            gutters[i] = nil
        }
        for i in visible {
            if rows[i] == nil {
                let l = dequeue()
                l.attributedText = source.line(i)
                insertSubview(l, belowSubview: gutterBack)
                size(l, at: i)
                rows[i] = l
            }
            let g: UILabel
            if let have = gutters[i] {
                g = have
            } else {
                g = dequeue()
                g.attributedText = source.gutter(i)
                g.textAlignment = .right
                addSubview(g)
                gutters[i] = g
            }
            // Pinned: the gutter moves with the vertical scroll only.
            g.frame = CGRect(x: contentOffset.x + pad.left, y: pad.top + CGFloat(i) * lineHeight, width: gutterWidth - 8, height: lineHeight)
        }
        placeFocus(source)
        gutterBack.frame = CGRect(x: contentOffset.x, y: contentOffset.y - bounds.height, width: pad.left + gutterWidth, height: bounds.height * 3)
        gutterRule.frame = CGRect(x: contentOffset.x + pad.left + gutterWidth, y: gutterBack.frame.minY, width: 1 / traitCollection.displayScale,
                                  height: gutterBack.frame.height)
        if contentOffset.y + bounds.height > contentSize.height - bounds.height * 2 { onNearEnd?() }
        if !rows.isEmpty, let f = onFirstLines {
            onFirstLines = nil
            f()
            if AutoScroller.on {
                let s = AutoScroller { [weak self] dy in
                    guard let self else { return }
                    let maxY = max(0, self.contentSize.height - self.bounds.height)
                    self.contentOffset.y = min(maxY, max(0, self.contentOffset.y + dy))
                }
                autoScroller = s
                s.start()
            }
        }
    }

    /// The marked line's band across the code, and the first time it is there, the scroll to it (a third down).
    private func placeFocus(_ source: CodeLineSource) {
        guard let f = focus, f < source.lineCount else {
            focusBand.isHidden = true
            return
        }
        focusBand.isHidden = false
        focusBand.frame = CGRect(x: 0, y: pad.top + CGFloat(f) * lineHeight, width: max(contentSize.width, bounds.width), height: lineHeight)
        guard focusPending, bounds.height > 0 else { return }
        focusPending = false
        let top = -adjustedContentInset.top
        let bottom = max(top, contentSize.height - bounds.height + adjustedContentInset.bottom)
        contentOffset.y = min(bottom, max(top, focusBand.frame.minY - bounds.height / 3))
    }

    func line(at p: CGPoint) -> Int? {
        guard let source else { return nil }
        let i = Int((p.y - pad.top) / lineHeight)
        return i >= 0 && i < source.lineCount ? i : nil
    }

    @objc private func tapped(_ g: UITapGestureRecognizer) {
        guard let i = line(at: g.location(in: self)), source?.tap(i) == true else { return }
        reload()
    }

    func contextMenuInteraction(_ interaction: UIContextMenuInteraction, configurationForMenuAtLocation location: CGPoint) -> UIContextMenuConfiguration? {
        guard let i = line(at: location), let menu = source?.menu(i) else { return nil }
        return UIContextMenuConfiguration(identifier: nil, previewProvider: nil) { _ in menu }
    }

    func contextMenuInteraction(_ interaction: UIContextMenuInteraction, previewForHighlightingMenuWithConfiguration configuration: UIContextMenuConfiguration) -> UITargetedPreview? {
        guard let i = line(at: interaction.location(in: self)), let row = rows[i] else { return nil }
        let p = UIPreviewParameters()
        p.backgroundColor = UIColor(Theme.codeBackground)
        return UITargetedPreview(view: row, parameters: p)
    }
}

/// `CodeLinesView` in SwiftUI. `version` changes when the source is a new one.
struct CodeLines: UIViewRepresentable {
    var source: CodeLineSource
    var version: Int = 0
    var onNearEnd: (() -> Void)? = nil
    var onFirstLines: (() -> Void)? = nil
    var focus: Int? = nil

    final class Coordinator {
        var version = -1
        var source: ObjectIdentifier?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> CodeLinesView {
        let v = CodeLinesView()
        v.backgroundColor = UIColor(Theme.codeBackground)
        v.onFirstLines = onFirstLines
        return v
    }

    func updateUIView(_ v: CodeLinesView, context: Context) {
        v.onNearEnd = onNearEnd
        defer { v.focus = focus }
        let c = context.coordinator
        let id = ObjectIdentifier(source)
        guard c.source != id || c.version != version else { return }
        c.source = id
        c.version = version
        v.source = source
    }
}
