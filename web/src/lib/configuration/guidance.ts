// Optional presentation supplied by a Web module alongside its source. All copy is localized by the module.
import { checkValue, sameValue, type FieldValue, type KnownField } from "./contract";

export interface GuidanceNote {
  kind: "info" | "warning";
  text: string;
}

export interface ConfirmationCopy {
  title: string;
  body: string;
  scope: string;
  impact: string;
  activation: string;
  confirmLabel: string;
}

export interface ChangeRule {
  when: { action: "change" | "reset"; from?: FieldValue; to?: FieldValue };
  confirm?: ConfirmationCopy;
  afterSave?: string;
}

export interface ConfigurationGuidance {
  notes?: readonly GuidanceNote[];
  fields?: Readonly<Record<string, { notes?: readonly GuidanceNote[]; rules?: readonly ChangeRule[] }>>;
}

const hasText = (v: unknown): v is string => typeof v === "string" && v.trim().length > 0;

/** A broken selector cannot safely identify which direction to protect. Disable only this field. */
export function invalidConditions(field: KnownField, rules?: readonly ChangeRule[]): boolean {
  if (rules === undefined) return false;
  return !Array.isArray(rules) || rules.some((rule: ChangeRule) => {
    const when = rule?.when;
    return !when || (when.action !== "change" && when.action !== "reset")
      || Object.keys(when).some((k) => k !== "action" && k !== "from" && k !== "to")
      || ("from" in when && !!checkValue(field, when.from))
      || ("to" in when && !!checkValue(field, when.to));
  });
}

/** Call after validating selectors. Refuse an ambiguous match or a required confirmation missing any of its copy. */
export function changeGuidance(rules: readonly ChangeRule[] | undefined, action: "change" | "reset", from: FieldValue | undefined, to: unknown) {
  const matches = rules?.filter(({ when }) => when.action === action
    && (when.from === undefined || sameValue(when.from, from))
    && (when.to === undefined || sameValue(when.to, to)));
  if (!matches?.length) return {};
  const rule = matches[0];
  if (matches.length > 1) return null;
  const c = rule.confirm;
  if ("confirm" in rule && !(c && [c.title, c.body, c.scope, c.impact, c.activation, c.confirmLabel].every(hasText))) return null;
  return { confirm: c, afterSave: hasText(rule.afterSave) ? rule.afterSave : undefined };
}
