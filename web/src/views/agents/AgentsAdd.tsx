import { useState } from "react";
import { t } from "../../i18n";
import { navigate } from "../../lib/router";
import { useStore } from "../../lib/store";
import { IconButton, Menu, useMenu } from "../../ui/atoms";
import { SectionAdd } from "../SectionAdd";
import { lazyDialog } from "../LazyOverlay";
const NewTerminal = lazyDialog(() => import("../terminal/NewTerminalDialog").then(m => m.NewTerminalDialog));

/** Agent and shell creation share the section's +; the terminal inventory is an explicit destination. `className`: the
 *  section's + (SectionAdd's), not the header's. */
export function AgentsAdd({ onNewAgent, onNavigate, header = false, className }: { onNewAgent: () => void; onNavigate?: () => void; header?: boolean; className?: string }) {
  const menu = useMenu();
  const [terminal, setTerminal] = useState(false);
  const panes = useStore(s => s.features.includes("terminal_panes"));
  return <>
    {header ? <IconButton icon="plus" label={t("sidebar.new_agent")} onClick={menu.open} /> : <SectionAdd label={t("sidebar.new_agent")} onClick={menu.open} className={className} />}
    {menu.anchor && <Menu anchor={menu.anchor} align="right" onClose={menu.close} items={[
      { key: "agent", label: t("sidebar.new_agent"), icon: "plus", onSelect: onNewAgent },
      ...(panes ? [
        { key: "terminal", label: t("terminal-terminalpage.new_terminal"), icon: "terminal" as const, onSelect: () => setTerminal(true) },
        { key: "terminals", label: t("session-history.open_terminals"), icon: "list" as const, onSelect: () => { navigate({ name: "terminal", view: "terminals" }); onNavigate?.(); } },
      ] : []),
    ]} />}
    <NewTerminal open={terminal} onClose={() => setTerminal(false)} onOpened={onNavigate} />
  </>;
}
