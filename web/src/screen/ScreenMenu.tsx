// The live screen's picture menu (#3954, docs/extensions/computer-use.md "Live screen video"): the hardware video
// channel (smoother, far less bandwidth) or VNC/RFB, and — for video — a quality bias. ScreenView imports it only when
// this Mac's node advertises the channel, so its words (screen-video) stay out of the first screen's catalog, which
// every page of the app sends, and no Mac shows a control it can't use. Input always rides RFB; only the picture path
// changes. The choice is kept per Mac in this browser (video.ts), as the Displays choice is, and as the iOS app keeps
// it on the device (ScreenVideoUI).
import { useState } from "react";
import { cx, Menu } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { t } from "../i18n";
import type { MenuItem } from "../ui/menu-model";
import type { ScreenPicture, ScreenQuality } from "./video";

const PICTURES: ScreenPicture[] = ["video", "vnc"];
const QUALITIES: ScreenQuality[] = ["auto", "sharper", "smoother"];
const pictureLabel = (p: ScreenPicture) => (p === "video" ? t("screen-video.picture_video") : t("screen-video.picture_vnc"));
const qualityLabel = (q: ScreenQuality) =>
  q === "auto" ? t("screen-video.quality_auto") : q === "sharper" ? t("screen-video.quality_sharper") : t("screen-video.quality_smoother");

/** The picture and (for video) quality the live screen uses, remembered per Mac. `onPicture` / `onQuality` persist and
 *  apply the choice; the parent holds the current value so the menu reads it back as the chosen radio. */
export default function ScreenMenu({ picture, quality, onPicture, onQuality }: {
  picture: ScreenPicture;
  quality: ScreenQuality;
  onPicture: (p: ScreenPicture) => void;
  onQuality: (q: ScreenQuality) => void;
}) {
  const [at, setAt] = useState<HTMLElement | null>(null); // the button, while the menu is open
  const items: MenuItem[] = [
    ...PICTURES.map((p): MenuItem => ({ key: `picture-${p}`, label: pictureLabel(p), radio: true, checked: picture === p, onSelect: () => onPicture(p) })),
    ...(picture === "video"
      ? [
          { key: "quality", label: t("screen-video.quality"), heading: true, divider: true, onSelect: () => {} } as MenuItem,
          ...QUALITIES.map((q): MenuItem => ({ key: `quality-${q}`, label: qualityLabel(q), radio: true, checked: quality === q, onSelect: () => onQuality(q) })),
        ]
      : []),
  ];
  return (
    <>
      <button
        type="button"
        onClick={(e) => setAt(at ? null : e.currentTarget)}
        aria-haspopup="menu"
        aria-expanded={at !== null}
        aria-label={t("screen-video.picture_current", { current: pictureLabel(picture) })}
        title={t("screen-video.menu")}
        data-testid="screen-video"
        className={cx(
          "touch-hit flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-control px-2 text-label font-medium text-ink-2 transition-colors hover:bg-hover hover:text-ink",
          at && "bg-hover text-ink",
        )}
      >
        <Icon name="video" size={14} />
        {/* a narrow bar keeps the icon only; the label says the current picture */}
        <span className="hidden sm:inline">{pictureLabel(picture)}</span>
        <Icon name="chevron" size={12} />
      </button>
      {at && <Menu anchor={at} onClose={() => setAt(null)} align="right" width={200} header={t("screen-video.menu")} items={items} />}
    </>
  );
}
