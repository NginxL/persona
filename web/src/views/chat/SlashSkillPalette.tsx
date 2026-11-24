// Skill-aware second stage of the / palette, loaded only after a keyword is typed after /.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { hasSlashSkillQuery, rankSlashSkills } from "../../lib/composer-commands";
import { imeKey, type ImeState } from "../../lib/ime";
import type { ComposerCommand as Command } from "../../lib/extension-prompts";
import type { MentionSkill } from "../../lib/skill-mentions";
import { SkillDetail, SkillRow } from "./SkillRow";
import { useSkillCatalog } from "./useSkillCatalog";
import { SlashSkillView, type SlashPaletteInput } from "./SlashSkillView";

type SkillCommand = Command & { skill: MentionSkill };
const attached = (row: Command | undefined) => row && (row as SkillCommand).skill;

export function SlashSkillPalette(input: SlashPaletteInput) {
  const { source, machineId } = input;
  const query = input.query ?? "";
  const searchingSkills = hasSlashSkillQuery(query);
  const catalog = useSkillCatalog(source?.skills, machineId, source?.cwd, input.memory, searchingSkills);
  const skillRows = useMemo<SkillCommand[]>(
    () => rankSlashSkills(catalog.catalog?.skills ?? [], query).map((skill) => ({
      key: `skill:${skill.id}`, word: skill.name, desc: skill.description ?? "", text: skill.name, source: skill.source, skill,
    })),
    [catalog.catalog, query],
  );
  const entries = useMemo<Command[]>(() => [...input.rows, ...skillRows], [input.rows, skillRows]);
  const [selection, setSelection] = useState<{ query: string; key: string }>();
  const rowKey = (row: Command) => row.key ?? row.word;
  const current = selection?.query === query
    ? Math.max(0, entries.findIndex(row => rowKey(row) === selection.key))
    : Math.min(selection ? 0 : input.active, entries.length - 1);
  const selectedKey = entries[current] && rowKey(entries[current]);
  const { active, onHover } = input;
  useLayoutEffect(() => {
    if (selectedKey) setSelection(old => old?.query === query && old.key === selectedKey ? old : { query, key: selectedKey });
    if (current >= 0 && active !== current) onHover(current);
  }, [active, current, onHover, query, selectedKey]);
  const select = (index: number) => {
    setSelection({ query, key: rowKey(entries[index]) });
    onHover(index);
  };
  const pick: SlashPaletteInput["onPick"] = (row) => {
    if ("kind" in row) return input.onPick(row);
    const skill = attached(row);
    input.onPick(skill ? { kind: "skill", id: `skill:${skill.id}`, skill } : row);
  };
  // the listener below reads the list as it is now, so it is added once per composer, not again on every render
  const latest = useRef({ entries, current, pick, select });
  latest.current = { entries, current, pick, select };
  // Capture inside this composer, before its special /model and /permissions handling. Commands fall through to it;
  // arrows and a highlighted skill are handled here across the combined list.
  const within = input.within;
  const shared = input.ime;
  useEffect(() => {
    const root = within?.current;
    if (!root) return;
    // The composer's input-method state where it passes one (lib/ime.ts): Safari's Enter right after compositionend is
    // still the IME's, and that compositionend came before this palette mounted. Without one, a state of its own.
    const own = { current: { active: false, endedAt: -Infinity } as ImeState };
    const ime = shared ?? own;
    const start = () => { own.current.active = true; };
    const end = () => { own.current = { active: false, endedAt: performance.now() }; };
    // a field that loses focus mid-composition may never get its compositionend
    const out = () => { own.current.active = false; };
    const key = (e: KeyboardEvent) => {
      if (imeKey(e, ime.current)) return;
      const { entries, current, pick, select } = latest.current;
      // a plain Enter picks; Shift+Enter and Alt+Enter stay a new line, Mod+Enter sends
      const pickEnter = e.key === "Enter" && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey;
      if ((e.key === "ArrowDown" || e.key === "ArrowUp") && entries.length) {
        e.preventDefault();
        e.stopPropagation();
        select((current + (e.key === "ArrowDown" ? 1 : entries.length - 1)) % entries.length);
      } else if ((pickEnter || e.key === "Tab") && attached(entries[current])) {
        e.preventDefault();
        e.stopPropagation();
        pick(entries[current]);
      }
    };
    root.addEventListener("keydown", key, true);
    if (!shared) {
      root.addEventListener("compositionstart", start, true);
      root.addEventListener("compositionend", end, true);
      root.addEventListener("focusout", out, true);
    }
    return () => {
      root.removeEventListener("keydown", key, true);
      root.removeEventListener("compositionstart", start, true);
      root.removeEventListener("compositionend", end, true);
      root.removeEventListener("focusout", out, true);
    };
  }, [within, shared]);
  return (
    <SlashSkillView
      {...input}
      entries={entries}
      active={current}
      onHover={select}
      onPick={pick}
      status={searchingSkills ? catalog.status : undefined}
      renderDetail={(row) => { const skill = attached(row); return skill && <SkillDetail skill={skill} />; }}
      renderRow={(row, common) => {
        const skill = attached(row);
        return skill && <SkillRow key={skill.id} skill={skill} common={common} />;
      }}
    />
  );
}
