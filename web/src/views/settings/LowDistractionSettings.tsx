// Settings › Notifications › Low-distraction mode (#8157): the hub's push grading switch (GET / PUT /push/ln-mode,
// docs/server/server.md "Push grading"). Unlike the rows above it, it is the hub's, so it holds for every device of the
// owner; persona_settings `notifications.low_distraction` changes the same switch after the owner approves a card. The
// group is always drawn, so a settings search for it always lands (search.ts): while the hub has not answered it shows
// no value, and a hub without the route (404 / 405) says to update it.
//
// A switch belongs to one hub: pairing or signing in elsewhere (onCredentialChange) reads it again from nothing, and an
// answer to an earlier read or write (an older credential's, or one the view no longer waits for) is dropped.
import { useCallback, useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, errorText, isAbort, onCredentialChange } from "../../lib/api";
import { lnModeEnabled } from "../../lib/push-intercepts";
import { setPushGrading } from "../../lib/store";
import { Spinner, toast } from "../../ui/atoms";
import { Group, Row, RowFailed, Switch } from "./SettingsParts";

type State = "loading" | "unsupported" | { on: boolean } | { error: string };

function missingRoute(e: unknown): boolean {
  return typeof e === "object" && e !== null && "status" in e && (e.status === 404 || e.status === 405);
}

export function LowDistractionSettings() {
  const [state, setState] = useState<State>("loading");
  const [busy, setBusy] = useState(false);
  // The request the view waits for: each read and write takes the next number, and only the latest one's answer counts.
  const latest = useRef(0);
  const abort = useRef<AbortController | null>(null);

  const load = useCallback(() => {
    const id = ++latest.current;
    abort.current?.abort();
    const controller = new AbortController();
    abort.current = controller;
    setBusy(false);
    setState("loading");
    api<unknown>("/push/ln-mode", { signal: controller.signal }).then(
      (r) => {
        if (id !== latest.current) return;
        const on = lnModeEnabled(r);
        setState({ on });
        setPushGrading(on);
      },
      (e) => {
        if (id !== latest.current || isAbort(e)) return;
        setState(missingRoute(e) ? "unsupported" : { error: errorText(e) });
      },
    );
  }, []);

  useEffect(() => {
    load();
    const stop = onCredentialChange(load);
    return () => {
      stop();
      latest.current++;
      abort.current?.abort();
    };
  }, [load]);

  const toggle = async (on: boolean) => {
    const id = ++latest.current; // a read still on its way must not undo this
    abort.current?.abort();
    setBusy(true);
    setState({ on });
    setPushGrading(on);
    try {
      const r = await api<unknown>("/push/ln-mode", { method: "PUT", json: { enabled: on } });
      if (id === latest.current) {
        const next = lnModeEnabled(r);
        setState({ on: next });
        setPushGrading(next);
      }
    } catch (e) {
      if (id !== latest.current) return;
      setState({ on: !on });
      setPushGrading(!on);
      if (!isAbort(e)) toast(t("settings-notifications.save_failed", { error: errorText(e) }), "error");
    } finally {
      if (id === latest.current) setBusy(false);
    }
  };

  const label = t("settings-notifications.low_distraction");
  return (
    <Group anchor="low-distraction" footer={t("settings-notifications.low_distraction_footer")}>
      {typeof state === "object" && "error" in state ? (
        <RowFailed text={t("settings-notifications.low_distraction_load_failed")} retry={t("settings-notifications.retry")} onRetry={load} />
      ) : state === "unsupported" ? (
        <Row icon="bellOff" label={label} hint={t("settings-notifications.low_distraction_unsupported")} wrapHint />
      ) : (
        <Row icon="bellOff" label={label} hint={t("settings-notifications.low_distraction_hint")}>
          {state === "loading" ? <Spinner /> : <Switch on={state.on} onChange={(next) => void toggle(next)} disabled={busy} label={label} />}
        </Row>
      )}
    </Group>
  );
}
