import type { MouseEvent } from "react";
import { href, type Route } from "../lib/router";
import { cx } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";
import type { GoFn } from "./SidebarParts";

export function SectionAdd({ label, to, go, onClick, icon: name = "plus", active }: { label: string; to?: Route; go?: GoFn; onClick?: (event: MouseEvent<HTMLElement>) => void; icon?: IconName; active?: boolean }) {
  const cls = cx("touch-hit relative z-10 flex size-5 shrink-0 items-center justify-center rounded-[5px] hover:bg-hover hover:text-ink-2", active ? "bg-hover text-ink" : "text-ink-3");
  const icon = <Icon name={name} size={12} stroke={2} />;
  if (to && go) return <a href={href(to)} onClick={go(to)} title={label} aria-label={label} aria-current={active ? "page" : undefined} className={cls}>{icon}</a>;
  return <button type="button" onClick={onClick} title={label} aria-label={label} className={cls}>{icon}</button>;
}
