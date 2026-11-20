// "Disconnect this browser?" (Settings › General and the account menu, DisconnectBrowser.tsx):
// asked before the hub revokes this browser's token, since getting back in takes pairing again. Its own chunk and
// namespace, so the first screen, which carries the account menu, carries neither. The failure question stays
// here too: it can only follow a confirmed revoke, after this chunk has loaded.
import { t } from "../i18n";
import { Button, Dialog } from "../ui/atoms";

export type DisconnectProblem = { error: string; id: string; nothing?: never } | { nothing: true; error?: never; id?: never };

export function DisconnectConfirm({ open, busy, onCancel, onConfirm, problem, onProblemClose, onSignOut }: {
  open: boolean; busy: boolean; onCancel: () => void; onConfirm: () => void;
  problem: DisconnectProblem | null; onProblemClose: () => void; onSignOut: () => void;
}) {
  const nothing = problem?.nothing;
  return (
    <>
      <Dialog
        open={open}
        onClose={() => !busy && onCancel()}
        title={t("disconnect-confirm.title")}
        width={420}
        footer={
          <>
            <Button tone="quiet" onClick={onCancel} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={busy} onClick={onConfirm}>
              {t("disconnect-confirm.confirm")}
            </Button>
          </>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">{t("disconnect-confirm.body")}</p>
      </Dialog>
      <Dialog
        open={!!problem}
        onClose={onProblemClose}
        title={nothing ? t("disconnect.nothing_title") : t("disconnect.failed_title")}
        footer={
          <div className="flex flex-wrap justify-end gap-2">
            <Button tone="quiet" onClick={onProblemClose} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" onClick={onSignOut} disabled={!nothing && busy}>
              {t(nothing ? "disconnect.sign_out" : "disconnect.sign_out_anyway")}
            </Button>
            {!nothing && <Button tone="primary" onClick={onConfirm} busy={busy}>
              {t("disconnect.retry")}
            </Button>}
          </div>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">{nothing ? t("disconnect.nothing_body") : t("disconnect.failed_body")}</p>
        {/* why, in the hub's (or the page's) own words */}
        {problem?.error !== undefined && <p className="mt-2 text-label leading-relaxed text-ink-3">{problem.error}</p>}
      </Dialog>
    </>
  );
}
