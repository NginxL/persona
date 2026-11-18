import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode, type RefObject } from "react";
import { credentialOwner, subscribeCredentialOwner } from "../lib/api";
import { acquireExtensionLogo, extensionLogoSource } from "../lib/extension-logo";
import { defaultManager, managerHeaderFor, onDefaultManager, useChosenManager } from "../lib/manager-choice";
import { Icon } from "./icons";

const subscribeDefault = (listener: () => void) => onDefaultManager(listener);

/** Keep the caller's existing tile geometry and status tint, including when artwork fails. `fallback`: what is drawn
 *  until the artwork is in and when it fails (the extensions symbol at `iconSize` when none); `onShown`: told true once
 *  the artwork is drawn, false when it fails. */
export function ExtensionLogo({ url, manager, className, iconSize, fallback, onShown }: { url?: string; manager?: string; className: string; iconSize: number; fallback?: ReactNode; onShown?: (shown: boolean) => void }) {
  const source = extensionLogoSource(url);
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner);
  useChosenManager();
  const fallbackOwner = useSyncExternalStore(subscribeDefault, defaultManager);
  const resolvedManager = (manager ?? (source?.path ? managerHeaderFor(source.path) : undefined)) || fallbackOwner;
  const tile = useRef<HTMLSpanElement>(null);
  // Remount on URL/owner changes: failed or retired artwork cannot replace a new request.
  return <span ref={tile} aria-hidden="true" data-extension-logo className={`relative ${className}`}>
    {source?.path ? <HubLogo key={`${owner}:${resolvedManager}:${source.path}`} path={source.path} manager={resolvedManager} tile={tile} iconSize={iconSize} fallback={fallback} onShown={onShown} />
      : <LogoImage key={source?.publicURL} src={source?.publicURL ?? null} iconSize={iconSize} fallback={fallback} onShown={onShown} />}
  </span>;
}

function HubLogo({ path, manager, tile, iconSize, fallback, onShown }: { path: string; manager?: string; tile: RefObject<HTMLSpanElement | null>; iconSize: number; fallback?: ReactNode; onShown?: (shown: boolean) => void }) {
  const [src, setSrc] = useState<string | null>(null);
  const told = useRef(onShown);
  useEffect(() => { told.current = onShown; });
  useEffect(() => {
    let active = true;
    let objectURL: string | undefined;
    let request: ReturnType<typeof acquireExtensionLogo> | undefined;
    const start = () => {
      if (!active || request) return;
      request = acquireExtensionLogo(path, manager);
      request.promise.then(blob => {
        if (!active) return;
        objectURL = URL.createObjectURL(blob);
        setSrc(objectURL);
      }).catch(() => { if (active) told.current?.(false); /* Keep the existing generic mark on unavailable artwork. */ });
    };
    // loading="lazy" on the eventual <img> cannot defer the preceding authenticated fetch.
    // Observe the existing tile with the default zero margin, including its scrolling ancestors' clipping.
    let observer: IntersectionObserver | undefined;
    if (typeof IntersectionObserver !== "undefined" && tile.current) {
      observer = new IntersectionObserver(entries => {
        if (entries.some(entry => entry.isIntersecting)) { observer?.disconnect(); start(); }
      });
      observer.observe(tile.current);
    } else start();
    return () => {
      active = false;
      observer?.disconnect();
      request?.release();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [path, manager, tile]);
  return <LogoImage key={src} src={src} iconSize={iconSize} fallback={fallback} onShown={onShown} />;
}

function LogoImage({ src, iconSize, fallback, onShown }: { src: string | null; iconSize: number; fallback?: ReactNode; onShown?: (shown: boolean) => void }) {
  const [failed, setFailed] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const generic = fallback ?? <Icon name="extension" size={iconSize} />;
  if (!src || failed) return generic;
  return <>
    {!loaded && generic}
    <img src={src} alt="" loading="lazy" decoding="async" referrerPolicy="no-referrer" crossOrigin="anonymous"
      className={`absolute inset-0 size-full rounded-[inherit] object-contain ${loaded ? "bg-[var(--extension-logo-backing)]" : "opacity-0"}`}
      onLoad={() => { setLoaded(true); onShown?.(true); }} onError={() => { setFailed(true); onShown?.(false); }} />
  </>;
}
