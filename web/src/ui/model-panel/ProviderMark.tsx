// A model provider's mark (#5032, #5497): the Agent its launcher runs, as `AgentMark` draws it in the Agents list, so every
// place that needs one (the model pill, the panel's rail, rows and flyout) says which Agent a pick runs on with its real
// mark, drawn once and first. An Agent with no mark of its own gets the generic `agent` icon, not a letter, unless its
// runtime names it. Its own file so the pill imports it without the panel.
import { isMarked, markId } from "../../lib/agent-kind";
import { AgentMark } from "../AgentMark";
import { cx } from "../atoms";
import { Icon } from "../icons";

/** Decorative: the provider's name is said by what it stands beside. `engine` is the launcher's (AgentMark ids), `label`
 * the runtime's own name for an engine the app has no id for (Paseo's Grok, #8142). */
export function ProviderMark({ engine, label, size = 15, className }: { engine: string; label?: string; size?: number; className?: string }) {
  // a runtime that names itself (Paseo's Grok, #8142) gets what the Agents list draws for it: its mark, or its name's letter
  if (isMarked(markId(engine, label)) || label?.trim()) return <AgentMark agent={engine} label={label} size={size} decorative className={cx("shrink-0", className)} />;
  return (
    <span aria-hidden data-agent-mark="generic" className={cx("inline-flex shrink-0 items-center justify-center", className)} style={{ width: size, height: size }}>
      <Icon name="agent" size={size} />
    </span>
  );
}
