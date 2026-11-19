// A conversation an extension opened, marked with that extension's own icon (#10358): the one its manifest brings,
// as Settings › Extensions draws it (ExtensionLogo), in the size and corner of the tile Lark's mark has (ChannelMark.tsx).
// Until the icon is in, and when the extension has none, is not installed (any more), is the one the origin's own mark
// stands for (`brand`: the Lark extension for Lark /agent) or its icon fails, the origin's own mark stays (`children`).
// `onName` is told the extension's name while its icon is drawn and nothing otherwise, so the words that name the
// mark ("From <name>") never disagree with what it shows. A lazy chunk of OriginMark's: the first screen keeps only the
// check for an opener.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { BrandId } from "../lib/connectors";
import { useOpenerIcon } from "../lib/opener-icons";
import { ExtensionLogo } from "./ExtensionLogo";

export function OpenerTile({ id, manager, brand, size, radius, onName, children }: { id: string; manager?: string; brand?: BrandId; size: number; radius: number; onName: (name: string | undefined) => void; children: ReactNode }) {
  const icon = useOpenerIcon(id, manager, brand);
  const url = icon?.url;
  // the icon whose image is drawn now: a new one is not until it loads
  const [drawn, setDrawn] = useState<string>();
  const shown = useCallback((ok: boolean) => setDrawn(ok ? url : undefined), [url]);
  const name = icon && drawn === icon.url ? icon.name : undefined;
  useEffect(() => {
    onName(name);
    return () => onName(undefined);
  }, [onName, name]);
  if (!icon) return children;
  return (
    <span data-opener={id} className="flex shrink-0" style={{ width: size, height: size, borderRadius: radius }}>
      <ExtensionLogo url={icon.url} manager={icon.manager} iconSize={size} fallback={children} onShown={shown} className="flex size-full items-center justify-center rounded-[inherit]" />
    </span>
  );
}
