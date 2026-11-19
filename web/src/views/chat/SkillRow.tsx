import type { MentionSkill } from "../../lib/skill-mentions";
import { Icon } from "../../ui/icons";
import type { PickerButton } from "./PickerMenu";

export function SkillRow({ skill, common }: { skill: MentionSkill; common: PickerButton }) {
  return (
    <button {...common} className="relative flex h-9 w-full items-center gap-2 rounded-control px-2.5 text-left text-sm text-ink [@media(pointer:coarse)]:h-11">
      <Icon name="spark" size={16} className="shrink-0 text-ink-3" />
      <span data-picker-title className="max-w-[65%] shrink-0 truncate font-medium">{skill.name}</span>
      <span className="min-w-0 flex-1 truncate text-xs text-ink-3">{skill.description}</span>
    </button>
  );
}

export function SkillDetail({ skill }: { skill: MentionSkill }) {
  return <>
    {skill.description && <p className="whitespace-pre-line text-ink-2">{skill.description}</p>}
    <p className="mt-1 text-xs text-ink-3">{skill.source}</p>
  </>;
}
