// Mochi, the default persona (docs/apple/persona.md "Built-in drawing"): the app icon's white blob waving on its paper disc (a dark
// disc in the dark), outlined in ink, with its orange cheek. The same drawing as the app's PersonaFace (Mochi), in the
// icon's 1024 px coordinates moved into the disc, so the two stay one picture. It keeps the icon's colors (the
// --mochi-* tokens in styles.css), not the page's accent; asleep (offline) it turns gray with its eyes shut
// (--mochi-sleep-*), its outline and shadow from their own tokens so the dark disc keeps its ink edge (#12090). Each
// state reads from its first frame alone; the motion runs in face-motion.ts (Web Animations) and stops with reduced motion.
import { useId } from "react";
import type { PersonaState } from "../../lib/persona";
import { cx } from "../../ui/atoms";
import { useFaceMotion } from "./face-motion";

/** Icon pixels into the disc: a little larger than the icon's square, so the body fills the circle. */
const SCALE = 1.16;
const ORIGIN = [545, 548] as const;
const INTO_DISC = `translate(512 512) scale(${SCALE}) translate(${-ORIGIN[0]} ${-ORIGIN[1]})`;
/** Where the body rocks from (its bottom middle), in the disc. */
const PIVOT = `${512 + (500 - ORIGIN[0]) * SCALE}px ${512 + (780 - ORIGIN[1]) * SCALE}px`;
/** Below this the motion lines and the gloss go and the eyes and cheek grow, so the face still reads. */
const COMPACT_BELOW = 36;

const BODY =
  "M262 778C420 792 600 790 702 772C742 764 752 700 760 640C800 598 828 562 826 502C825 446 795 418 764 428" +
  "C736 440 722 468 712 494C690 440 650 350 560 304C470 260 330 288 254 382C184 462 158 582 168 672C176 742 206 772 262 778Z";
const GLOSS = "M312 380Q352 346 412 334M288 414L290 411";
const LINES = ["M792 352L818 298", "M838 384L884 340", "M864 428L924 408"];
const EYES = [
  [397, 497],
  [553, 480],
] as const;

export function MochiFace({ size, state = "idle", className }: { size: number; state?: PersonaState; className?: string }) {
  const id = useId().replace(/:/g, "");
  const compact = size < COMPACT_BELOW;
  const offline = state === "offline";
  const k = compact ? 1.3 : 1;
  // The outline never thinner than 1.3 px on screen.
  const stroke = Math.max(30, (1.3 * 1024) / SCALE / size);
  const ring = Math.max(0.07 * 1024, (1.5 * 1024) / size);
  const look = state === "thinking" ? [22, -30] : state === "working" ? [-24, 12] : [0, 0];
  const lines = state === "idle" || state === "needs_you" ? [1, 1, 1] : state === "working" ? [1, 0.35, 0.35] : [];
  const motion = useFaceMotion<SVGSVGElement>(`${state} ${compact}`);
  return (
    <svg ref={motion} viewBox="0 0 1024 1024" width={size} height={size} aria-hidden className={cx("shrink-0 rounded-full", className)} style={{ width: size, height: size }}>
      <defs>
        <linearGradient id={`${id}d`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" style={{ stopColor: "var(--mochi-disc-top)" }} />
          <stop offset="1" style={{ stopColor: "var(--mochi-disc-bottom)" }} />
        </linearGradient>
        <linearGradient id={`${id}b`} gradientUnits="userSpaceOnUse" x1="400" y1="300" x2="500" y2="820">
          <stop offset="0" style={{ stopColor: offline ? "var(--mochi-sleep-top)" : "var(--mochi-body-top)" }} />
          <stop offset="1" style={{ stopColor: offline ? "var(--mochi-sleep-bottom)" : "var(--mochi-body-bottom)" }} />
        </linearGradient>
        <filter id={`${id}s`} x="-20%" y="-200%" width="140%" height="500%">
          <feGaussianBlur stdDeviation="17" />
        </filter>
        <clipPath id={`${id}c`}>
          <circle cx="512" cy="512" r="512" />
        </clipPath>
      </defs>
      <g clipPath={`url(#${id}c)`}>
        <circle cx="512" cy="512" r="512" fill={`url(#${id}d)`} />
        {state === "working" && (
          <g className="mochi-spin">
            <circle
              cx="512" cy="512" r={512 - ring / 2} fill="none" stroke="var(--mochi-cheek)" strokeWidth={ring} strokeLinecap="round"
              pathLength={100} strokeDasharray="28 72" transform="rotate(-150 512 512)"
            />
          </g>
        )}
        <g className={state === "needs_you" ? "mochi-rock" : undefined} style={{ transformOrigin: PIVOT }}>
          <g transform={INTO_DISC}>
            <ellipse cx="490" cy="800" rx="265" ry="19" fill={offline ? "var(--mochi-sleep-shadow)" : "var(--mochi-shadow)"} filter={`url(#${id}s)`} />
            <path d={BODY} fill={`url(#${id}b)`} stroke={offline ? "var(--mochi-sleep-outline)" : "var(--mochi-outline)"} strokeWidth={stroke} strokeLinejoin="round" />
            <ellipse cx="312" cy="556" rx={37 * k} ry={33.3 * k} fill={offline ? "var(--mochi-sleep-cheek)" : "var(--mochi-cheek)"} />
            {!compact && <path d={GLOSS} fill="none" stroke="#fff" strokeWidth={stroke * 0.6} strokeLinecap="round" />}
            {!compact &&
              lines.map((o, i) => (
                <path key={i} d={LINES[i]} stroke="var(--mochi-motion)" strokeOpacity={o} strokeWidth={stroke * 0.85} strokeLinecap="round"
                  className={state === "working" ? "mochi-pulse" : undefined} data-loop-delay={state === "working" ? i * 250 : undefined} />
              ))}
            {offline ? (
              EYES.map(([x, y], i) => (
                <path key={i} d={`M${x - 29 * k} ${y}Q${x} ${y + 20 * k} ${x + 29 * k} ${y}`} fill="none" stroke="var(--mochi-sleep-ink)" strokeWidth={stroke * 0.8} strokeLinecap="round" />
              ))
            ) : (
              <g transform={`translate(${look[0] / SCALE} ${look[1] / SCALE})`}>
                <g className={state === "idle" ? "mochi-blink" : undefined}>
                  {EYES.map(([x, y], i) => (
                    <circle key={i} cx={x} cy={y} r={29 * k} fill="var(--mochi-ink)" />
                  ))}
                </g>
              </g>
            )}
          </g>
        </g>
        {state === "thinking" && (
          <g>
            <rect x={768 - 143} y={215 - 66} width="287" height="133" rx="66" fill="#fff" />
            {[691, 768, 845].map((x, i) => (
              <circle key={x} cx={x} cy="215" r="25.6" fill="var(--mochi-cheek)" className="mochi-pulse" data-loop-delay={i * 400} />
            ))}
          </g>
        )}
      </g>
      {state === "needs_you" && (
        <circle cx="512" cy="512" r={512 - ring / 2} fill="none" stroke="var(--mochi-needs-you)" strokeWidth={ring} className="mochi-pulse" />
      )}
    </svg>
  );
}
