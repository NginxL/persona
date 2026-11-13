// New agent's parts for a start in a desktop app (lib/desktop-start.ts; #5480 Claude's desktop app, #5726 the Codex App):
// the temporary session's row, the way back to one with its note, and naming a new folder. Loaded only when such a
// launcher is the one that runs (NewAgent.tsx), so its words stay off the first screen (docs/lessons/web.md, "The live
// screen's later words go in a lazy component").
import { useState } from "react";
import { baseName } from "../lib/format";
import { newFolderPath } from "../lib/desktop-start";
import { Button } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { t } from "../i18n";

/** No folder picked: the computer puts the session in the app's temporary folder (the Codex App's chats in no project). */
export function DesktopTemporaryRow({ codex }: { codex: boolean }) {
  return (
    <div data-testid="new-agent-temporary" className="mb-1.5 flex min-w-0 items-center gap-2 rounded-control bg-accent-tint px-3 py-2">
      <Icon name="folder" size={13} className="shrink-0 text-accent-ink" />
      <span className="min-w-0 truncate text-meta font-medium text-accent-ink">{t("newagent.temporary_session")}</span>
      <span className="min-w-0 truncate text-label text-ink-3">{t(codex ? "newagent-desktop.temporary_codex" : "newagent-desktop.temporary_claude")}</span>
    </div>
  );
}

/** Under the folder: back to a temporary session when a folder is picked, and what a start in the app does with one. */
export function DesktopFolderNote({ codex, scratch, onUseTemporary }: { codex: boolean; scratch: boolean; onUseTemporary: () => void }) {
  return (
    <div className="mt-1.5 flex flex-col items-start gap-1">
      {!scratch && <Button tone="quiet" onClick={onUseTemporary}>{t("newagent-desktop.use_temporary")}</Button>}
      <p data-testid="new-agent-desktop-hint" className="text-label leading-snug text-ink-3">
        {t(codex ? "newagent-desktop.desktop_hint_codex" : "newagent-desktop.desktop_hint_claude")}
      </p>
    </div>
  );
}

/** "New folder…" in the browser's header line. */
export function NewFolderButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" data-testid="new-agent-new-folder" onClick={onClick} className="flex shrink-0 items-center gap-1 rounded-[4px] px-1 text-xs font-medium tracking-normal normal-case text-ink-2 hover:bg-hover hover:text-ink">
      <Icon name="plus" size={11} stroke={2.4} />
      {t("newagent-desktop.new_folder")}
    </button>
  );
}

/** A new folder's name in `folder`, the one browsed; the computer makes it when the session starts. */
export function NewFolderNaming({ folder, onUse, onCancel }: { folder: string; onUse: (path: string) => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const made = newFolderPath(name, folder);
  return (
    <div data-testid="new-agent-naming" className="px-2 pt-0.5 pb-1.5">
      <div className="flex items-center gap-1.5">
        <input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              onCancel();
            } else if (e.key === "Enter" && made) {
              e.preventDefault();
              onUse(made);
            }
          }}
          aria-label={t("newagent-desktop.new_folder_name")}
          placeholder={t("newagent-desktop.new_folder_name")}
          spellCheck={false}
          className="h-7 min-w-0 flex-1 rounded-control bg-inset px-2 text-meta text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong)]"
        />
        <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
        <Button tone="primary" disabled={!made} onClick={() => { if (made) onUse(made); }}>{t("newagent-desktop.new_folder_use")}</Button>
      </div>
      <p className="mt-1 text-xs text-ink-3">{t("newagent-desktop.new_folder_hint", { folder: baseName(folder) })}</p>
    </div>
  );
}
