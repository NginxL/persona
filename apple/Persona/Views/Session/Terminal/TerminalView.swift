import CoreText
import PersonaKit
import SwiftUI
import UIKit

/// What the terminal view shows: rows of cells (scrollback pages above, the live screen below) and the cursor.
struct TerminalContent: Equatable {
    var rows: [TermRow] = []
    /// The screen's width in cells (the pane's): what an unwrapped row is laid out against.
    var cols = 0
    /// Row and column of the cursor when it is shown.
    var cursor: (row: Int, col: Int)?
    /// Where the live screen starts in `rows` (history above it): a tap is a click at a cell of the screen.
    var screenStart = 0

    static func == (a: TerminalContent, b: TerminalContent) -> Bool {
        a.cols == b.cols && a.cursor?.row == b.cursor?.row && a.cursor?.col == b.cursor?.col && a.screenStart == b.screenStart
            && a.rows == b.rows
    }
}

/// The terminal tab's screen: every character drawn in its own cell (wide ones in two), so columns and Chinese line up
/// whatever the font's own widths are. Wrapped, each row is folded at the view's width; unwrapped, rows keep the pane's
/// width and the view scrolls sideways. Long press selects (Copy / Select All), a tap opens a link. With `onInput` it takes the
/// keyboard: a tap brings it up and what is typed goes to the terminal (TerminalInput.swift).
struct TerminalView: UIViewRepresentable {
    var content: TerminalContent
    var wrap: Bool
    var fontSize: CGFloat
    var font: TerminalFont = .jetbrains
    var theme: TerminalTheme
    /// Whether the keyboard is up on the terminal; it follows the view as well (a tap, the keyboard going down).
    var focused: Binding<Bool>?
    var onInput: ((TerminalInputEvent) -> Void)?
    /// Called while the user scrolls near the top (load the page before; the view keeps its place when it arrives).
    var onNearTop: (() -> Void)?
    var onOpenURL: ((URL) -> Void)?
    var onCopy: (() -> Void)?
    /// Pinch: the font size to use now (continuous while the fingers move).
    var onFontSize: ((CGFloat) -> Void)?
    /// Two fingers swiped: left / right / up / down.
    var onSwipe: ((UISwipeGestureRecognizer.Direction) -> Void)?
    var onDoubleTap: (() -> Void)?
    /// Driving the terminal: a vertical drag is the mouse wheel for it, in rows (> 0 back), not a scroll of this view
    /// (herdr gives it to the program — Claude Code, Codex scroll their conversation — or scrolls its own history, as in
    /// Moshi). Over the cell of the screen where the finger went down (column, row; 0-based), as a mouse's wheel is:
    /// Codex scrolls only over its conversation, not over the prompt it pins on top or its input box.
    var onWheel: ((Int, (col: Int, row: Int)?) -> Void)?
    /// Driving the terminal: a tap is a click at a cell of its screen (column, row; 0-based) — Claude Code jumps to
    /// the prompt pinned on top, or to the bottom from "Jump to bottom". A tap never brings the keyboard up (as in
    /// Moshi: the key bar's keyboard button does).
    var onClick: ((Int, Int) -> Void)?
    /// The cols × rows that fit the view at the font size (it changes with the keyboard, a rotation, a pinch).
    var onGrid: ((TerminalGridSize) -> Void)?

    func makeUIView(context: Context) -> TerminalScrollView { TerminalScrollView() }

    func updateUIView(_ v: TerminalScrollView, context: Context) {
        v.onNearTop = onNearTop
        v.onOpenURL = onOpenURL
        v.onCopy = onCopy
        v.onFontSize = onFontSize
        v.onSwipe = onSwipe
        v.onDoubleTap = onDoubleTap
        v.setWheel(onWheel)
        v.onClick = onClick
        v.onInput = onInput
        v.onGrid = onGrid
        v.onFocus = focused.map { b in { on in if b.wrappedValue != on { b.wrappedValue = on } } }
        v.update(content: content, wrap: wrap, fontSize: fontSize, font: font, theme: theme,
                 dark: context.environment.colorScheme == .dark)
        if let focused, onInput != nil, focused.wrappedValue != v.isFirstResponder {
            let on = focused.wrappedValue
            DispatchQueue.main.async { // not while SwiftUI is updating views: the keyboard's frame changes the layout
                guard on != v.isFirstResponder, v.window != nil else { return }
                _ = on ? v.becomeFirstResponder() : v.resignFirstResponder()
            }
        }
    }

    static func dismantleUIView(_ v: TerminalScrollView, coordinator: ()) {
        if v.isFirstResponder { _ = v.resignFirstResponder() }
    }
}

final class TerminalScrollView: UIScrollView, UIScrollViewDelegate, UIEditMenuInteractionDelegate {
    var onNearTop: (() -> Void)?
    var onOpenURL: ((URL) -> Void)?
    var onCopy: (() -> Void)?
    var onFontSize: ((CGFloat) -> Void)?
    var onSwipe: ((UISwipeGestureRecognizer.Direction) -> Void)?
    var onDoubleTap: (() -> Void)?
    var onWheel: ((Int, (col: Int, row: Int)?) -> Void)?
    var onClick: ((Int, Int) -> Void)?
    private let wheelPan = UIPanGestureRecognizer()
    private var wheelRows: CGFloat = 0
    private var wheelAt: (col: Int, row: Int)?
    private var glide: CADisplayLink?
    private var glideVelocity: CGFloat = 0
    var onInput: ((TerminalInputEvent) -> Void)?
    var onFocus: ((Bool) -> Void)?
    var onGrid: ((TerminalGridSize) -> Void)?
    private var reportedGrid: TerminalGridSize?
    private var pinchFrom: CGFloat = TerminalPrefs.defaultFontSize

    // Typing (TerminalInput.swift): the input method's composition, drawn over the cursor until it is chosen.
    var marked = ""
    var markedSelection = NSRange()
    weak var inputDelegate: UITextInputDelegate?
    var markedTextStyle: [NSAttributedString.Key: Any]?
    var autocorrectionType = UITextAutocorrectionType.no
    var autocapitalizationType = UITextAutocapitalizationType.none
    var spellCheckingType = UITextSpellCheckingType.no
    var smartQuotesType = UITextSmartQuotesType.no
    var smartDashesType = UITextSmartDashesType.no
    var smartInsertDeleteType = UITextSmartInsertDeleteType.no
    var inlinePredictionType = UITextInlinePredictionType.no
    var keyboardAppearance = UIKeyboardAppearance.dark
    private let markedLabel = UILabel()

    let canvas = TerminalCanvas()
    private var content = TerminalContent()
    private var wrap = true
    private var fontSize: CGFloat = TerminalPrefs.defaultFontSize
    private var font = TerminalFont.jetbrains
    private var theme = TerminalTheme.ink
    private var dark = true
    private var metrics = CellMetrics(size: TerminalPrefs.defaultFontSize)
    private var laidOutHeight: CGFloat = 0
    private var lines: [DisplayLine] = []
    private var lineLayout = TerminalLineLayout()
    private var maxCells = 0
    private var laidOutWidth: CGFloat = 0
    private var first = true
    private let inset = UIEdgeInsets(top: 8, left: 10, bottom: 10, right: 10)
    private var cache: [CacheKey: LineRender] = [:]
    /// The selection, in display-line coordinates: anchor and focus cells (inclusive).
    private var selection: (anchor: CellPoint, focus: CellPoint)? { didSet { placeHandles() } }
    /// The selection's ends, to drag (as the system's text selection; Moshi has them too).
    private let startHandle = SelectionHandle(atTop: true)
    private let endHandle = SelectionHandle(atTop: false)
    private lazy var editMenu = UIEditMenuInteraction(delegate: self)

    struct CellPoint: Comparable {
        var line: Int; var col: Int
        static func < (a: CellPoint, b: CellPoint) -> Bool { a.line < b.line || (a.line == b.line && a.col < b.col) }
    }
    private struct CacheKey: Hashable { var row: Int; var from: Int; var to: Int }

    override init(frame: CGRect) {
        super.init(frame: frame)
        delegate = self
        alwaysBounceVertical = true
        showsHorizontalScrollIndicator = false
        showsVerticalScrollIndicator = false
        canvas.host = self
        canvas.isOpaque = false
        canvas.backgroundColor = .clear
        canvas.contentMode = .redraw
        addSubview(canvas)
        markedLabel.isHidden = true
        markedLabel.layer.cornerRadius = 3
        markedLabel.layer.masksToBounds = true
        addSubview(markedLabel)
        addInteraction(editMenu)
        let press = UILongPressGestureRecognizer(target: self, action: #selector(longPress(_:)))
        press.minimumPressDuration = 0.35
        addGestureRecognizer(press)
        let double = UITapGestureRecognizer(target: self, action: #selector(doubleTap(_:)))
        double.numberOfTapsRequired = 2
        addGestureRecognizer(double)
        let tap = UITapGestureRecognizer(target: self, action: #selector(tap(_:)))
        tap.require(toFail: double)
        addGestureRecognizer(tap)
        addGestureRecognizer(UIPinchGestureRecognizer(target: self, action: #selector(pinch(_:))))
        // One finger scrolls; two fingers are for swipes and the pinch.
        panGestureRecognizer.maximumNumberOfTouches = 1
        for h in [startHandle, endHandle] {
            h.isHidden = true
            h.addGestureRecognizer(UIPanGestureRecognizer(target: self, action: #selector(handleDragged(_:))))
            addSubview(h)
        }
        wheelPan.addTarget(self, action: #selector(wheelPanned(_:)))
        wheelPan.maximumNumberOfTouches = 1
        wheelPan.isEnabled = false
        addGestureRecognizer(wheelPan)
        for d: UISwipeGestureRecognizer.Direction in [.left, .right, .up, .down] {
            let swipe = UISwipeGestureRecognizer(target: self, action: #selector(swipe(_:)))
            swipe.direction = d
            swipe.numberOfTouchesRequired = 2
            addGestureRecognizer(swipe)
        }
        isAccessibilityElement = true
        accessibilityLabel = String(localized: "Terminal")
        accessibilityIdentifier = "terminal-screen"
        accessibilityTraits = .staticText
    }

    required init?(coder: NSCoder) { fatalError() }

    // MARK: Content

    func update(content new: TerminalContent, wrap: Bool, fontSize: CGFloat, font: TerminalFont, theme: TerminalTheme, dark: Bool) {
        let styleChanged = wrap != self.wrap || fontSize != self.fontSize || font != self.font || theme.id != self.theme.id || dark != self.dark
        guard first || styleChanged || new != content else { return }
        if styleChanged || new.rows.count != content.rows.count {
            cache.removeAll(keepingCapacity: true)
        } else {
            // Only rows that changed are drawn again.
            for i in new.rows.indices where new.rows[i] != content.rows[i] { invalidate(row: i) }
        }
        if fontSize != self.fontSize || font != self.font || first { metrics = CellMetrics(size: fontSize, font: font) }
        let fromEnd = contentSize.height - contentOffset.y
        let atEnd = isAtEnd
        // Rows added under what is read (the scrollback grows) keep it where it is; pages added above, from the end.
        let grewBelow = !content.rows.isEmpty && new.rows.count > content.rows.count && new.rows.first == content.rows.first
        let top = contentOffset.y
        self.wrap = wrap; self.fontSize = fontSize; self.font = font; self.theme = theme; self.dark = dark
        content = new
        backgroundColor = UIColor(hex: theme.side(dark).background)
        indicatorStyle = theme.side(dark).isDark ? .white : .black
        if keyboardAppearance != (theme.side(dark).isDark ? .dark : .light) {
            keyboardAppearance = theme.side(dark).isDark ? .dark : .light
            if isFirstResponder { reloadInputViews() }
        }
        relayout()
        let maxY = max(0, contentSize.height - bounds.height)
        if first || atEnd {
            contentOffset.y = maxY
        } else if grewBelow {
            contentOffset.y = min(top, maxY)
        } else {
            contentOffset.y = min(max(0, contentSize.height - fromEnd), maxY) // pages added above keep what you read in place
        }
        first = false
        accessibilityValue = content.rows.suffix(12).map(TerminalGrid.plain).joined(separator: "\n")
        reportGrid()
        placeMarked()
        canvas.setNeedsDisplay()
    }

    /// The grid that fits, when it changed (not while SwiftUI is updating: the feed it goes to is observed).
    private func reportGrid() {
        let w = bounds.width - inset.left - inset.right, h = bounds.height - inset.top - inset.bottom
        guard w > 0, h > 0, let onGrid else { return }
        let g = TerminalGridSize(cols: Int(w / metrics.cell.width), rows: Int(h / metrics.cell.height))
        guard g != reportedGrid else { return }
        reportedGrid = g
        DispatchQueue.main.async { onGrid(g) }
    }

    private var isAtEnd: Bool { contentOffset.y >= contentSize.height - bounds.height - 4 }

    /// The input method's composition, over the cursor (or the end of the screen when it is hidden).
    func setMarked(_ text: String, selected: NSRange) {
        marked = text
        markedSelection = selected
        placeMarked()
    }

    private func placeMarked() {
        guard !marked.isEmpty else { markedLabel.isHidden = true; return }
        let side = theme.side(dark)
        markedLabel.attributedText = NSAttributedString(string: marked, attributes: [
            .font: metrics.regular, .foregroundColor: UIColor(hex: side.foreground),
            .underlineStyle: NSUnderlineStyle.single.rawValue, .underlineColor: UIColor(hex: side.cursor),
        ])
        markedLabel.backgroundColor = UIColor(hex: side.background).withAlphaComponent(0.92)
        markedLabel.sizeToFit()
        let r = cursorRect()
        markedLabel.frame = CGRect(x: min(r.minX, max(inset.left, bounds.width - markedLabel.bounds.width - inset.right)),
                                   y: r.minY, width: markedLabel.bounds.width + 2, height: r.height)
        markedLabel.isHidden = false
    }

    /// The terminal cursor's cell in this view (the end of the last line when it is hidden).
    func cursorRect() -> CGRect {
        let w = metrics.cell.width, h = metrics.cell.height
        var at: (line: Int, col: Int)?
        if let c = content.cursor {
            if let i = lines.lastIndex(where: { $0.row == c.row && c.col >= $0.from }) { at = (i, c.col - lines[i].from + lines[i].indent) }
        }
        let point = at ?? (max(0, lines.count - 1), lines.last.map { $0.to - $0.from + $0.indent } ?? 0)
        return CGRect(x: inset.left + CGFloat(point.col) * w, y: inset.top + CGFloat(point.line) * h, width: w, height: h)
    }

    private func invalidate(row: Int) {
        for k in cache.keys where k.row == row { cache[k] = nil }
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        if bounds.width != laidOutWidth || bounds.height != laidOutHeight {
            // The keyboard coming up shrinks the view: what was at the bottom (the cursor) stays there.
            let wasAtEnd = contentOffset.y >= contentSize.height - laidOutHeight - 4
            laidOutHeight = bounds.height
            if bounds.width != laidOutWidth { relayout() }
            if wasAtEnd { contentOffset.y = max(0, contentSize.height - bounds.height) }
            reportGrid()
            placeMarked()
        }
        canvas.frame = CGRect(origin: contentOffset, size: bounds.size)
    }

    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        canvas.setNeedsDisplay()
        if (isDragging || isDecelerating), contentOffset.y < 600 { onNearTop?() }
    }

    /// Cuts rows into display lines and sizes the scrollable area.
    private func relayout() {
        laidOutWidth = bounds.width
        let perLine = max(10, Int((bounds.width - inset.left - inset.right) / metrics.cell.width))
        let out = lineLayout.lines(content.rows, cursor: content.cursor, perLine: perLine, wrap: wrap)
        defer { placeHandles() }
        maxCells = out.reduce(content.cols) { max($0, $1.to + $1.indent) }
        if out != lines { selection = nil }
        lines = out
        let width = wrap ? bounds.width : max(bounds.width, CGFloat(maxCells) * metrics.cell.width + inset.left + inset.right)
        let size = CGSize(width: width, height: CGFloat(lines.count) * metrics.cell.height + inset.top + inset.bottom)
        if contentSize != size { contentSize = size }
    }

    // MARK: Drawing

    /// A continuation line's cells: the borders of the row's indent again, blanks (on the row's background), then its
    /// own cells.
    private static func hanging(_ row: TermRow, _ line: DisplayLine) -> TermRow {
        var cells = row.prefix(line.indent).map { TerminalWrap.borders.contains($0.text) ? $0 : TermCell(text: " ", width: 1, style: $0.style) }
        cells += row[line.from..<min(line.to, row.count)]
        return cells
    }

    fileprivate func draw(in ctx: CGContext, visible: CGRect) {
        let side = theme.side(dark)
        let h = metrics.cell.height, w = metrics.cell.width
        let firstLine = max(0, Int((visible.minY - inset.top) / h))
        let lastLine = min(lines.count - 1, Int((visible.maxY - inset.top) / h))
        guard firstLine <= lastLine else { return }
        ctx.saveGState()
        ctx.translateBy(x: -visible.minX, y: -visible.minY)
        ctx.textMatrix = CGAffineTransform(scaleX: 1, y: -1)
        let sel = selection.map { ($0.anchor < $0.focus ? $0.anchor : $0.focus, $0.anchor < $0.focus ? $0.focus : $0.anchor) }
        for i in firstLine...lastLine {
            let line = lines[i]
            let origin = CGPoint(x: inset.left, y: inset.top + CGFloat(i) * h)
            let key = CacheKey(row: line.row, from: line.from, to: line.to)
            let render = cache[key] ?? {
                let row = content.rows[line.row]
                let r = line.indent == 0
                    ? LineRender(row, from: line.from, to: line.to, metrics: metrics, side: side)
                    : LineRender(Self.hanging(row, line), from: 0, to: line.indent + line.to - line.from, metrics: metrics, side: side)
                cache[key] = r
                return r
            }()
            if let (a, b) = sel, i >= a.line, i <= b.line {
                let from = i == a.line ? a.col : 0
                let to = i == b.line ? b.col + 1 : max(line.to - line.from, 1)
                ctx.setFillColor(UIColor(hex: side.selection).withAlphaComponent(0.32).cgColor)
                ctx.fill(CGRect(x: origin.x + CGFloat(line.indent + from) * w, y: origin.y, width: CGFloat(max(0, to - from)) * w, height: h))
            }
            render.draw(in: ctx, at: origin)
            if let c = content.cursor, c.row == line.row, c.col >= line.from, c.col < max(line.to, line.from + 1) {
                // Typing here: a solid block; otherwise an outline (the keyboard is not on the terminal).
                let rect = CGRect(x: origin.x + CGFloat(line.indent + c.col - line.from) * w, y: origin.y + 1, width: w, height: h - 2)
                if isFirstResponder {
                    ctx.setFillColor(UIColor(hex: side.cursor).withAlphaComponent(0.7).cgColor)
                    ctx.fill(rect)
                } else {
                    ctx.setStrokeColor(UIColor(hex: side.cursor).withAlphaComponent(0.8).cgColor)
                    ctx.setLineWidth(1)
                    ctx.stroke(rect.insetBy(dx: 0.5, dy: 0.5))
                }
            }
        }
        ctx.restoreGState()
    }

    // MARK: Touches

    private func cell(at p: CGPoint) -> CellPoint? {
        guard !lines.isEmpty else { return nil }
        let line = min(max(0, Int((p.y - inset.top) / metrics.cell.height)), lines.count - 1)
        let span = max(1, lines[line].to - lines[line].from)
        var col = min(max(0, Int((p.x - inset.left) / metrics.cell.width) - lines[line].indent), span - 1)
        let row = content.rows[lines[line].row]
        if lines[line].from + col < row.count, row[lines[line].from + col].width == 0, col > 0 { col -= 1 }
        return CellPoint(line: line, col: col)
    }

    @objc private func longPress(_ g: UILongPressGestureRecognizer) {
        guard let c = cell(at: g.location(in: self)) else { return }
        switch g.state {
        case .began:
            Haptics.tap()
            selection = (wordStart(c), wordEnd(c))
        case .changed:
            if let s = selection { selection = (s.anchor, c) }
        case .ended:
            if let s = selection {
                let a = min(s.anchor, s.focus)
                let p = CGPoint(x: inset.left + CGFloat(a.col + lines[a.line].indent) * metrics.cell.width,
                                y: inset.top + CGFloat(a.line) * metrics.cell.height)
                editMenu.presentEditMenu(with: UIEditMenuConfiguration(identifier: nil, sourcePoint: p))
            }
        default: break
        }
        canvas.setNeedsDisplay()
    }

    // MARK: The wheel (driving)

    /// Driving: drags go to the terminal as its wheel and this view stays at the end; otherwise it scrolls itself.
    func setWheel(_ wheel: ((Int, (col: Int, row: Int)?) -> Void)?) {
        onWheel = wheel
        let on = wheel != nil
        guard on != wheelPan.isEnabled else { return }
        wheelPan.isEnabled = on
        isScrollEnabled = !on
        if on { contentOffset.y = max(0, contentSize.height - bounds.height) } else { stopGlide() }
    }

    @objc private func wheelPanned(_ g: UIPanGestureRecognizer) {
        switch g.state {
        case .began:
            stopGlide()
            wheelRows = 0
            let t = g.translation(in: self) // recognized a few points in: where the finger went down
            let p = g.location(in: self)
            wheelAt = screenCell(at: CGPoint(x: p.x - t.x, y: p.y - t.y))
        case .changed:
            let dy = g.translation(in: self).y
            g.setTranslation(.zero, in: self)
            wheel(by: dy)
        case .ended:
            glideVelocity = g.velocity(in: self).y / 60
            guard abs(glideVelocity) > 1 else { return }
            let link = CADisplayLink(target: self, selector: #selector(glided))
            link.add(to: .main, forMode: .common)
            glide = link
        default:
            stopGlide()
        }
    }

    @objc private func glided() {
        glideVelocity *= 0.94
        if abs(glideVelocity) < 0.5 { stopGlide(); return }
        wheel(by: glideVelocity)
    }

    private func stopGlide() {
        glide?.invalidate()
        glide = nil
    }

    /// Points dragged to whole rows of the font (the rest waits for the next move): down the screen is back in history.
    private func wheel(by dy: CGFloat) {
        guard let onWheel, metrics.cell.height > 0 else { return }
        wheelRows += dy / metrics.cell.height
        let n = Int(wheelRows)
        guard n != 0 else { return }
        wheelRows -= CGFloat(n)
        onWheel(n, wheelAt)
    }

    @objc private func pinch(_ g: UIPinchGestureRecognizer) {
        switch g.state {
        case .began: pinchFrom = fontSize
        case .changed: onFontSize?(min(max(pinchFrom * g.scale, Self.fontRange.lowerBound), Self.fontRange.upperBound).rounded(toPlaces: 1))
        default: break
        }
    }

    static let fontRange: ClosedRange<CGFloat> = 7...20

    @objc private func swipe(_ g: UISwipeGestureRecognizer) { onSwipe?(g.direction) }

    @objc private func doubleTap(_ g: UITapGestureRecognizer) {
        guard selection == nil else { return }
        onDoubleTap?()
    }

    @objc private func tap(_ g: UITapGestureRecognizer) {
        if selection != nil {
            if onHandle(g.location(in: self)) { return }
            selection = nil
            editMenu.dismissMenu()
            canvas.setNeedsDisplay()
            return
        }
        if let c = cell(at: g.location(in: self)), let url = link(at: c) {
            onOpenURL?(url)
            return
        }
        // A click for the program (driving it); the keyboard comes up only from the key bar's button, so a tap meant
        // for the screen never covers it with the keyboard.
        if let onClick, let at = screenCell(at: g.location(in: self)) { onClick(at.col, at.row) }
    }

    /// The cell of the live screen under a point (not clamped to the text: a click may go anywhere on the row).
    private func screenCell(at p: CGPoint) -> (col: Int, row: Int)? {
        guard !lines.isEmpty, metrics.cell.height > 0, metrics.cell.width > 0 else { return nil }
        let i = Int((p.y - inset.top) / metrics.cell.height)
        guard lines.indices.contains(i) else { return nil }
        let line = lines[i]
        let row = line.row - content.screenStart
        let col = line.from + Int((p.x - inset.left) / metrics.cell.width) - line.indent
        guard row >= 0, col >= 0, content.cols == 0 || col < content.cols else { return nil }
        return (col, row)
    }

    /// The OSC 8 link under a cell, else a URL written in its row.
    private func link(at c: CellPoint) -> URL? {
        let line = lines[c.line]
        let row = content.rows[line.row]
        let col = line.from + c.col
        if col < row.count, let s = row[col].style.link, let url = URL(string: s) { return url }
        let text = row.map { $0.width == 0 ? "" : $0.text }.joined()
        // Cell columns to string offsets: wide characters take one character but two cells.
        var offset = 0
        for i in 0..<min(col, row.count) where row[i].width > 0 { offset += row[i].text.utf16.count }
        return Links.scan(text).first { NSLocationInRange(offset, $0.range) }?.url // where links end: `Links`
    }

    private func isWordCell(_ c: CellPoint) -> Bool {
        let line = lines[c.line], row = content.rows[line.row], i = line.from + c.col
        guard i < row.count else { return false }
        let t = row[i].width == 0 && i > 0 ? row[i - 1].text : row[i].text
        return !(t == " " || t.isEmpty)
    }

    private func wordStart(_ c: CellPoint) -> CellPoint {
        guard isWordCell(c) else { return c }
        var p = c
        while p.col > 0, isWordCell(CellPoint(line: p.line, col: p.col - 1)) { p.col -= 1 }
        return p
    }

    private func wordEnd(_ c: CellPoint) -> CellPoint {
        guard isWordCell(c) else { return c }
        var p = c
        let span = lines[c.line].to - lines[c.line].from
        while p.col + 1 < span, isWordCell(CellPoint(line: p.line, col: p.col + 1)) { p.col += 1 }
        return p
    }

    // MARK: Selection handles

    /// Puts the handles at the selection's first and last cells (hidden without one).
    private func placeHandles() {
        guard let s = selection, !lines.isEmpty, lines.indices.contains(s.anchor.line), lines.indices.contains(s.focus.line) else {
            startHandle.isHidden = true
            endHandle.isHidden = true
            return
        }
        let a = min(s.anchor, s.focus), b = max(s.anchor, s.focus)
        let w = metrics.cell.width, h = metrics.cell.height
        startHandle.place(x: inset.left + CGFloat(a.col + lines[a.line].indent) * w, y: inset.top + CGFloat(a.line) * h, lineHeight: h)
        endHandle.place(x: inset.left + CGFloat(b.col + 1 + lines[b.line].indent) * w, y: inset.top + CGFloat(b.line) * h, lineHeight: h)
        startHandle.isHidden = false
        endHandle.isHidden = false
        bringSubviewToFront(startHandle)
        bringSubviewToFront(endHandle)
    }

    private func onHandle(_ p: CGPoint) -> Bool {
        [startHandle, endHandle].contains { !$0.isHidden && $0.frame.insetBy(dx: -8, dy: -8).contains(p) }
    }

    /// A handle dragged: that end of the selection follows the finger (the other stays), the menu comes back after.
    @objc private func handleDragged(_ g: UIPanGestureRecognizer) {
        guard let s = selection, let handle = g.view as? SelectionHandle else { return }
        // Where the finger points, at the text: the knob sits above the first line / below the last.
        var p = g.location(in: self)
        p.y += handle.atTop ? metrics.cell.height * 0.6 : -metrics.cell.height * 0.6
        guard let c = cell(at: p) else { return }
        switch g.state {
        case .began:
            editMenu.dismissMenu()
            let a = min(s.anchor, s.focus), b = max(s.anchor, s.focus)
            selection = handle.atTop ? (b, c) : (a, c) // the other end stays put
        case .changed:
            selection = (s.anchor, c)
        case .ended, .cancelled:
            let a = min(s.anchor, s.focus)
            let at = CGPoint(x: inset.left + CGFloat(a.col + lines[a.line].indent) * metrics.cell.width,
                             y: inset.top + CGFloat(a.line) * metrics.cell.height)
            editMenu.presentEditMenu(with: UIEditMenuConfiguration(identifier: nil, sourcePoint: at))
        default: break
        }
        canvas.setNeedsDisplay()
    }

    /// A drag that starts on a handle moves the handle: it neither scrolls this view nor is the terminal's wheel.
    override func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        if g === panGestureRecognizer || g === wheelPan, onHandle(g.location(in: self)) { return false }
        return super.gestureRecognizerShouldBegin(g)
    }

    /// The selected text: lines of the same row join, rows end in a newline, trailing blanks go.
    func selectedText() -> String {
        guard let s = selection else { return "" }
        return text(from: min(s.anchor, s.focus), to: max(s.anchor, s.focus))
    }

    private func text(from a: CellPoint, to b: CellPoint) -> String {
        var out = ""
        var current = ""
        for i in a.line...b.line {
            let line = lines[i], row = content.rows[line.row]
            let from = line.from + (i == a.line ? a.col : 0)
            let to = i == b.line ? min(line.from + b.col + 1, row.count) : min(line.to, row.count)
            if from < to { for c in row[from..<to] where c.width > 0 { current += c.text } }
            if i == b.line || lines[i + 1].row != line.row {
                while current.hasSuffix(" ") { current.removeLast() }
                out += current + (i == b.line ? "" : "\n")
                current = ""
            }
        }
        return out
    }

    /// The menu goes beside the whole selection (above it, or under it with no room), never over it or its handles.
    func editMenuInteraction(_ interaction: UIEditMenuInteraction, targetRectFor configuration: UIEditMenuConfiguration) -> CGRect {
        guard let s = selection, lines.indices.contains(s.anchor.line), lines.indices.contains(s.focus.line) else {
            return CGRect(origin: configuration.sourcePoint, size: .zero)
        }
        let a = min(s.anchor, s.focus), b = max(s.anchor, s.focus)
        let h = metrics.cell.height
        let top = inset.top + CGFloat(a.line) * h - 14, bottom = inset.top + CGFloat(b.line + 1) * h + 14 // the knobs too
        return CGRect(x: inset.left, y: top, width: bounds.width - inset.left - inset.right, height: bottom - top)
    }

    func editMenuInteraction(_ interaction: UIEditMenuInteraction, menuFor configuration: UIEditMenuConfiguration,
                             suggestedActions: [UIMenuElement]) -> UIMenu? {
        UIMenu(children: [
            UIAction(title: String(localized: "Copy [Readiness Views d37380]"), image: UIImage(systemName: Theme.Icon.copy)) { [weak self] _ in
                guard let self else { return }
                UIPasteboard.general.string = self.selectedText()
                self.onCopy?()
                self.selection = nil
                self.canvas.setNeedsDisplay()
            },
            UIAction(title: String(localized: "Select All"), image: UIImage(systemName: Theme.Icon.select)) { [weak self] _ in
                guard let self, !self.lines.isEmpty else { return }
                self.selection = (CellPoint(line: 0, col: 0),
                                  CellPoint(line: self.lines.count - 1, col: max(0, self.lines[self.lines.count - 1].to - self.lines[self.lines.count - 1].from - 1)))
                self.canvas.setNeedsDisplay()
                self.editMenu.presentEditMenu(with: UIEditMenuConfiguration(identifier: nil, sourcePoint: CGPoint(x: self.bounds.midX, y: self.contentOffset.y + 40)))
            },
        ])
    }
}

/// The visible window of the scroll view: it moves with the scroll position and draws only the lines in it.
final class TerminalCanvas: UIView {
    weak var host: TerminalScrollView?

    override func draw(_ rect: CGRect) {
        guard let ctx = UIGraphicsGetCurrentContext() else { return }
        host?.draw(in: ctx, visible: frame)
    }
}

/// The terminal's typeface: JetBrains Mono (bundled, SIL OFL) or the system's SF Mono. Chinese and other scripts
/// neither has come from the system's fallback, each drawn in its own cells.
enum TerminalFont: String, CaseIterable {
    case jetbrains, system

    var name: String { self == .jetbrains ? "JetBrains Mono" : String(localized: "SF Mono (System)") }

    func font(size: CGFloat, bold: Bool) -> UIFont {
        if self == .jetbrains, Self.registered, let f = UIFont(name: bold ? "JetBrainsMono-Bold" : "JetBrainsMono-Regular", size: size) {
            return f
        }
        return .monospacedSystemFont(ofSize: size, weight: bold ? .bold : .regular)
    }

    /// The bundled files register on first use (no Info.plist list to keep in step).
    private static let registered: Bool = {
        var any = false
        for name in ["JetBrainsMono-Regular", "JetBrainsMono-Bold"] {
            guard let url = Bundle.main.url(forResource: name, withExtension: "ttf") else { continue }
            CTFontManagerRegisterFontsForURL(url as CFURL, .process, nil)
            any = true
        }
        return any
    }()
}

@MainActor final class TerminalGlyphMemo {
    private nonisolated struct Key: Hashable {
        let text: String
        let width: UInt8
        let font: UIFont
        let color: UIColor
    }

    struct Glyph {
        let line: CTLine
        let scale: CGFloat
        let inset: CGFloat
    }

    private var values: [Key: Glyph] = [:]

    func glyph(_ text: String, width: UInt8, font: UIFont, color: UIColor, cell: CGSize) -> Glyph {
        let key = Key(text: text, width: width, font: font, color: color)
        if let value = values[key] { return value }
        var attributes: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: color]
        var line = CTLineCreateWithAttributedString(NSAttributedString(string: text, attributes: attributes))
        var advance = CTLineGetTypographicBounds(line, nil, nil, nil)
        let room = CGFloat(width) * cell.width
        if width == 2, advance > 0, advance < room * 0.9 {
            let grow = min(room * 0.92 / advance, cell.height * 0.86 / font.pointSize, 1.25)
            if grow > 1.02 {
                attributes[.font] = font.withSize(font.pointSize * grow)
                line = CTLineCreateWithAttributedString(NSAttributedString(string: text, attributes: attributes))
                advance = CTLineGetTypographicBounds(line, nil, nil, nil)
            }
        }
        let scale = advance > room * 1.02 ? room / advance : 1
        let value = Glyph(line: line, scale: scale, inset: max(0, (room - advance * scale) / 2))
        if values.count >= 4096 { values.removeAll(keepingCapacity: true) }
        values[key] = value
        return value
    }
}

/// Cell size of the monospaced font at a size.
struct CellMetrics {
    let glyphs = TerminalGlyphMemo()
    let size: CGFloat
    let regular: UIFont
    let bold: UIFont
    let cell: CGSize
    let baseline: CGFloat
    /// A light box-drawing line's width.
    let line: CGFloat

    init(size: CGFloat, font: TerminalFont = .jetbrains) {
        self.size = size
        regular = font.font(size: size, bold: false)
        bold = font.font(size: size, bold: true)
        let w = ("M" as NSString).size(withAttributes: [.font: regular]).width
        // The same line spacing whatever the font's own (JetBrains Mono's is taller than SF Mono's).
        let h = ceil(max(size * 1.42, regular.ascender - regular.descender))
        cell = CGSize(width: w, height: h)
        baseline = floor((h - (regular.ascender - regular.descender)) / 2 + regular.ascender)
        line = max(1, (size / 11 * 2).rounded() / 2)
    }

    func font(_ s: TermStyle) -> UIFont {
        let f = s.bold ? bold : regular
        guard s.italic, let d = f.fontDescriptor.withSymbolicTraits(.traitItalic) else { return f }
        return UIFont(descriptor: d, size: size)
    }
}

/// One display line ready to draw: background rects, text runs placed on their cells, underlines.
final class LineRender {
    private var fills: [(CGRect, CGColor)] = []
    private var texts: [(CTLine, CGPoint, CGFloat)] = [] // line, position, horizontal scale
    private var strokes: [(CGRect, CGColor)] = []
    private var paths: [(CGPath, CGFloat, CGColor)] = [] // box drawing: path, line width, color

    init(_ row: TermRow, from: Int, to: Int, metrics m: CellMetrics, side: TerminalTheme.Side) {
        let w = m.cell.width, h = m.cell.height
        var i = from
        while i < min(to, row.count) {
            let style = row[i].style
            var j = i
            while j < min(to, row.count), row[j].style == style { j += 1 }
            var fg = side.color(style.fg, foreground: true)
            var bg: UIColor? = style.bg == .standard ? nil : side.color(style.bg, foreground: false)
            if style.inverse {
                let back = bg ?? UIColor(hex: side.background)
                bg = fg
                fg = back
            }
            if style.dim { fg = fg.withAlphaComponent(0.6) }
            let x0 = CGFloat(i - from) * w
            if let bg { fills.append((CGRect(x: x0, y: 0, width: CGFloat(j - i) * w, height: h), bg.cgColor)) }
            if style.underline || style.link != nil {
                strokes.append((CGRect(x: x0, y: m.baseline + 2, width: CGFloat(j - i) * w, height: 1), fg.withAlphaComponent(style.underline ? 1 : 0.5).cgColor))
            }
            if style.strike { strokes.append((CGRect(x: x0, y: h / 2, width: CGFloat(j - i) * w, height: 1), fg.cgColor)) }
            if !style.hidden { place(row, i, j, from: from, font: m.font(style), color: fg, metrics: m) }
            i = j
        }
    }

    /// ASCII goes out in runs (the font's own advance is the cell); everything else one character per cell, centered
    /// in its one or two cells and squeezed when its glyph is wider.
    private func place(_ row: TermRow, _ start: Int, _ end: Int, from: Int, font: UIFont, color: UIColor, metrics m: CellMetrics) {
        let attrs: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: color]
        var run = "", runStart = start
        func flush() {
            guard !run.isEmpty, run.contains(where: { $0 != " " }) else { run = ""; return }
            let line = CTLineCreateWithAttributedString(NSAttributedString(string: run, attributes: attrs))
            texts.append((line, CGPoint(x: CGFloat(runStart - from) * m.cell.width, y: m.baseline), 1))
            run = ""
        }
        for i in start..<end {
            let c = row[i]
            if c.width == 0 { continue }
            if c.text.count == 1, let a = c.text.first?.asciiValue, a >= 0x20, a < 0x7F {
                if run.isEmpty { runStart = i }
                run += c.text
                continue
            }
            flush()
            let cellRect = CGRect(x: CGFloat(i - from) * m.cell.width, y: 0, width: m.cell.width, height: m.cell.height)
            if let s = c.text.unicodeScalars.first, c.text.unicodeScalars.count == 1 {
                if let arms = BoxDrawing.arms(s.value) {
                    for (path, width) in BoxDrawing.strokes(arms, in: cellRect, light: m.line) {
                        paths.append((path, width, color.cgColor))
                    }
                    continue
                }
                if let (rect, alpha) = BoxDrawing.block(s.value, in: cellRect) {
                    fills.append((rect, color.withAlphaComponent(alpha).cgColor))
                    continue
                }
            }
            // Symbols that have an emoji form (⏺, ⏵) are text here: a colored picture would not be in the cell's color.
            var text = c.text
            if c.width == 1, let s = text.unicodeScalars.first, text.unicodeScalars.count == 1, s.properties.isEmoji {
                text.unicodeScalars.append("\u{FE0E}")
            }
            let glyph = m.glyphs.glyph(text, width: c.width, font: font, color: color, cell: m.cell)
            let x = CGFloat(i - from) * m.cell.width + glyph.inset
            texts.append((glyph.line, CGPoint(x: x, y: m.baseline), glyph.scale))
        }
        flush()
    }

    func draw(in ctx: CGContext, at origin: CGPoint) {
        for (r, c) in fills { ctx.setFillColor(c); ctx.fill(r.offsetBy(dx: origin.x, dy: origin.y)) }
        for (line, p, scale) in texts {
            if scale == 1 {
                ctx.textPosition = CGPoint(x: origin.x + p.x, y: origin.y + p.y)
                CTLineDraw(line, ctx)
            } else {
                ctx.saveGState()
                ctx.translateBy(x: origin.x + p.x, y: origin.y + p.y)
                ctx.scaleBy(x: scale, y: 1)
                ctx.textPosition = .zero
                CTLineDraw(line, ctx)
                ctx.restoreGState()
            }
        }
        for (r, c) in strokes { ctx.setFillColor(c); ctx.fill(r.offsetBy(dx: origin.x, dy: origin.y)) }
        if !paths.isEmpty {
            ctx.saveGState()
            ctx.translateBy(x: origin.x, y: origin.y)
            ctx.setLineCap(.butt)
            for (path, width, color) in paths {
                ctx.addPath(path)
                ctx.setLineWidth(width)
                ctx.setStrokeColor(color)
                ctx.strokePath()
            }
            ctx.restoreGState()
        }
    }
}

private extension CGFloat {
    func rounded(toPlaces n: Int) -> CGFloat {
        let m = pow(10, CGFloat(n))
        return (self * m).rounded() / m
    }
}

/// One end of a text selection, as the system draws it: a thin bar the height of a line with a knob above (start) or
/// below (end); a finger-sized area around it takes the drag.
final class SelectionHandle: UIView {
    let atTop: Bool
    private static let knob: CGFloat = 10
    private static let hit: CGFloat = 44

    init(atTop: Bool) {
        self.atTop = atTop
        super.init(frame: .zero)
        isOpaque = false
        backgroundColor = .clear
        isAccessibilityElement = true
        accessibilityLabel = atTop ? String(localized: "Selection start") : String(localized: "Selection end")
        accessibilityIdentifier = atTop ? "selection-start" : "selection-end"
    }

    required init?(coder: NSCoder) { fatalError() }

    /// The bar at x from y, a line high (content coordinates of the terminal).
    func place(x: CGFloat, y: CGFloat, lineHeight: CGFloat) {
        let h = lineHeight + Self.knob + 4
        frame = CGRect(x: x - Self.hit / 2, y: atTop ? y - Self.knob - 4 : y, width: Self.hit, height: h)
        setNeedsDisplay()
    }

    override func point(inside point: CGPoint, with event: UIEvent?) -> Bool {
        bounds.insetBy(dx: 0, dy: -12).contains(point)
    }

    override func draw(_ rect: CGRect) {
        guard let ctx = UIGraphicsGetCurrentContext() else { return }
        let color = tintColor ?? .systemBlue
        ctx.setFillColor(color.cgColor)
        let mid = bounds.midX
        let knob = Self.knob
        if atTop {
            ctx.fillEllipse(in: CGRect(x: mid - knob / 2, y: 0, width: knob, height: knob))
            ctx.fill(CGRect(x: mid - 1, y: knob / 2, width: 2, height: bounds.height - knob / 2))
        } else {
            ctx.fill(CGRect(x: mid - 1, y: 0, width: 2, height: bounds.height - knob / 2))
            ctx.fillEllipse(in: CGRect(x: mid - knob / 2, y: bounds.height - knob, width: knob, height: knob))
        }
    }
}
