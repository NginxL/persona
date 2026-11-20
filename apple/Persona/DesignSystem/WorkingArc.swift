#if os(iOS)
import QuartzCore
import SwiftUI
import UIKit

/// ◐: a to-do being worked on (the To-dos list's mark, #10465; DESIGN_SYSTEM 2.5, 2.7 and 3.8): Working's amber arc,
/// turning in the compositor while it can be seen (`VisibleWork`: off screen, on another tab or with the scene away it
/// stops) and still with Reduce Motion. Not `WorkingIndicator`: the system's mini spinner neither pauses off screen nor
/// holds still with Reduce Motion, and it is drawn at its own size rather than a text mark's (`markXS`, scaled).
struct WorkingArc: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        VisibleWork { eligible in
            WorkingArcRing(color: StatusPalette.working.dot, turning: eligible && !reduceMotion)
        }
        .scaledFrame(width: Theme.Size.markXS, height: Theme.Size.markXS)
        .allowsHitTesting(false)
        .accessibilityHidden(true) // the row's label says the state in words
    }
}

/// The arc as a Core Animation layer: a SwiftUI rotation would redraw its row on every frame (P9).
private struct WorkingArcRing: UIViewRepresentable {
    var color: Color
    var turning: Bool

    func makeUIView(context: Context) -> WorkingArcView { WorkingArcView() }

    func updateUIView(_ view: WorkingArcView, context: Context) {
        view.configure(color: color.resolve(in: context.environment).cgColor, displayScale: context.environment.displayScale,
                       turning: turning)
    }

    static func dismantleUIView(_ view: WorkingArcView, coordinator: ()) {
        view.stop()
    }
}

final class WorkingArcView: UIView {
    private let arc = CAShapeLayer()
    private static let key = "turn"
    private var turning = false

    override init(frame: CGRect) {
        super.init(frame: frame)
        isUserInteractionEnabled = false
        arc.fillColor = nil
        arc.lineWidth = Theme.Size.workingArcLine
        arc.lineCap = .round
        arc.strokeEnd = Theme.Size.workingArcSweep
        layer.addSublayer(arc)
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }

    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? { nil }

    func configure(color: CGColor, displayScale: CGFloat, turning: Bool) {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        arc.strokeColor = color
        arc.contentsScale = displayScale
        CATransaction.commit()
        self.turning = turning
        updateAnimation()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        let side = min(bounds.width, bounds.height) - Theme.Size.workingArcLine
        guard side > 0 else { return }
        // A local square around the middle of the view, turned about its own middle (never `frame` on a layer that turns).
        let local = CGRect(origin: .zero, size: bounds.size)
        let circle = CGRect(x: local.midX - side / 2, y: local.midY - side / 2, width: side, height: side)
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        arc.bounds = local
        arc.position = CGPoint(x: bounds.midX, y: bounds.midY)
        arc.path = CGPath(ellipseIn: circle, transform: nil)
        CATransaction.commit()
        updateAnimation()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        updateAnimation()
    }

    private func updateAnimation() {
        guard turning, window != nil, bounds.width > 0 else {
            arc.removeAnimation(forKey: Self.key)
            return
        }
        guard arc.animation(forKey: Self.key) == nil else { return }
        let turn = CABasicAnimation(keyPath: "transform.rotation.z")
        turn.fromValue = 0
        turn.toValue = 2 * Double.pi
        turn.duration = Theme.Motion.workingArcTurn
        turn.repeatCount = .infinity
        turn.preferredFrameRateRange = CAFrameRateRange(minimum: Theme.Motion.breathMinimumFPS, maximum: Theme.Motion.breathPreferredFPS,
                                                        preferred: Theme.Motion.breathPreferredFPS)
        arc.add(turn, forKey: Self.key)
    }

    func stop() {
        turning = false
        arc.removeAnimation(forKey: Self.key)
    }
}
#endif
