// The help the readiness card's buttons and the local example questions open (docs/assistant/onboarding.md 4.4): how to
// wake, install, sign in or add a computer, what Persona does, how to connect a model. A chunk of its own, loaded the
// first time help is opened or ahead of it (views/assistant/Readiness.tsx HelpDialog), and with the provider wizard
// (InstallClaudeHelp): none of it is drawn on a first screen.
import { actionLabel, ADMIN_MACHINES, knownAction, sendLabel, stepActions, type HelpTopic, type ReadinessStep } from "../../lib/readiness";
import { Button, copyText, Dialog, IconButton } from "../../ui/atoms";
import { t } from "../../i18n";

/** A command to copy in the help (what to type on that computer). */
function Command({ text }: { text: string }) {
  return (
    <div className="mt-2 flex items-center gap-2 rounded-[8px] bg-inset px-2.5 py-1.5 shadow-hairline">
      <code className="min-w-0 flex-1 font-mono text-label break-all text-ink">{text}</code>
      <IconButton icon="copy" label={t("assistant-readiness.copy")} onClick={() => void copyText(text)} size={24} iconSize={12} />
    </div>
  );
}

function P({ children }: { children: React.ReactNode }) {
  return <p className="text-meta leading-relaxed text-ink-2">{children}</p>;
}

/** How to install Claude Code on that computer: the line to paste in its terminal (the help dialog, the provider wizard). */
export function InstallClaudeHelp({ machine }: { machine: string }) {
  return (
    <div>
      <P>{t("assistant-readiness.help_install_body", { machine: machine || t("assistant-readiness.that_computer") })}</P>
      {/* i18n-ignore: Claude Code's own installer, typed as it is */}
      <Command text="curl -fsSL https://claude.ai/install.sh | bash" />
      <p className="mt-2 text-label leading-relaxed text-ink-3">{t("assistant-readiness.help_install_after")}</p>
    </div>
  );
}

/** The help a card's button or a local example question opens (wake, install, sign in, add a computer, what Persona
 *  does, how to connect a model): local words, nothing sent to the model. */
/** Without `onAction` the help is only words (no "Use an API key instead"). */
export function HelpDialog({ topic, machine, step, onClose, onAction }: { topic: HelpTopic; machine: string; step: ReadinessStep | null; onClose: () => void; onAction?: (id: string) => void }) {
  const where = machine || t("assistant-readiness.that_computer");
  const then = (id: string) => {
    onClose();
    onAction?.(id);
  };
  const useKey = onAction ? <Button tone="secondary" onClick={() => then("connect_model")}>{t("assistant-readiness.use_api_key")}</Button> : undefined;
  const footer = (main?: React.ReactNode) => (
    <>
      <Button tone="quiet" onClick={onClose}>
        {t("assistant-readiness.got_it")}
      </Button>
      {main}
    </>
  );
  switch (topic) {
    case "add_computer":
      return (
        <Dialog open onClose={onClose} title={t("assistant-readiness.help_add_title")} footer={footer(<Button tone="primary" href={ADMIN_MACHINES} icon="external">{t("assistant-readiness.open_admin_machines")}</Button>)}>
          <div className="flex flex-col gap-2">
            <P>{t("assistant-readiness.help_add_why")}</P>
            <P>{t("assistant-readiness.help_add_how")}</P>
            <p className="text-label leading-relaxed text-ink-3">{t("assistant-readiness.help_add_note")}</p>
          </div>
        </Dialog>
      );
    case "wake_computer":
      return (
        <Dialog open onClose={onClose} title={t("assistant-readiness.help_wake_title", { machine: where })} footer={footer()}>
          <div className="flex flex-col gap-2">
            <P>{t("assistant-readiness.help_wake_body", { machine: where })}</P>
            <P>{t("assistant-readiness.help_wake_after")}</P>
          </div>
        </Dialog>
      );
    case "install_claude":
      return (
        <Dialog open onClose={onClose} title={t("assistant-readiness.help_install_title")} footer={footer(useKey)}>
          <InstallClaudeHelp machine={machine} />
        </Dialog>
      );
    case "login_claude":
      return (
        <Dialog open onClose={onClose} title={t("assistant-readiness.help_login_title")} footer={footer(useKey)}>
          <P>{t("assistant-readiness.help_login_body", { machine: where })}</P>
          <Command text="claude auth login" />
          <p className="mt-2 text-label leading-relaxed text-ink-3">{t("assistant-readiness.help_login_after")}</p>
        </Dialog>
      );
    case "what_can_you_do": {
      const [main] = step ? stepActions(step) : [];
      return (
        <Dialog open onClose={onClose} title={t("assistant-readiness.help_what_title")} footer={footer(main ? <Button tone="primary" onClick={() => then(main)}>{actionLabel(main)}</Button> : undefined)}>
          <div className="flex flex-col gap-2">
            <P>{t("assistant-readiness.help_what_body")}</P>
            <ul className="flex list-disc flex-col gap-1 pl-5 text-meta leading-relaxed text-ink-2">
              <li>{t("assistant-readiness.help_what_computer")}</li>
              <li>{t("assistant-readiness.help_what_data")}</li>
              <li>{t("assistant-readiness.help_what_memory")}</li>
            </ul>
            {step && <p className="text-label leading-relaxed text-ink-3">{t("assistant-readiness.help_what_missing", { step: step.title || sendLabel(step) })}</p>}
          </div>
        </Dialog>
      );
    }
    case "how_to_connect_model": {
      // an API key alone does not make the main conversation work: it still runs on a computer (#4480), so with none
      // the first thing to do is add one
      // only "add a computer" when there is none: an offline, unconfirmed or outdated one is the same step with its own
      // action (wake / confirm / update), and adding a second computer would not help
      const computerStep = step?.id === "computer" ? step : undefined;
      const computerAction = computerStep?.action && knownAction(computerStep.action.id) ? computerStep.action.id : undefined;
      const noComputer = computerStep !== undefined && (computerAction === "add_computer" || (!computerAction && computerStep.code === "no_machine"));
      const main = noComputer ? "add_computer" : computerAction ?? "connect_model";
      return (
        <Dialog open onClose={onClose} title={t("assistant-readiness.help_model_title")} footer={footer(<Button tone="primary" onClick={() => then(main)}>{actionLabel(main)}</Button>)}>
          <div className="flex flex-col gap-2">
            <P>{t("assistant-readiness.help_model_body")}</P>
            <ol className="flex list-decimal flex-col gap-1 pl-5 text-meta leading-relaxed text-ink-2">
              <li>{t("assistant-readiness.help_model_key")}</li>
              <li>{t("assistant-readiness.help_model_claude")}</li>
            </ol>
            <div data-testid="help-model-computer" className="flex flex-col gap-1 text-[12.5px] leading-relaxed">
              <p className="text-ink-2">{t("assistant-readiness.help_model_computer")}</p>
              {noComputer && <p className="font-medium text-ink">{t("assistant-readiness.help_model_no_computer")}</p>}
              {computerStep && !noComputer && (
                <p className="font-medium text-ink">{t("assistant-readiness.help_model_computer_not_ready", { step: computerStep.title || sendLabel(computerStep) })}</p>
              )}
            </div>
          </div>
        </Dialog>
      );
    }
  }
}
