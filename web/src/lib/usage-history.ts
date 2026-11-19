// Token usage history (#9480, docs/server/machines.md "Token usage history") for the token calendar on the assistant's
// new-chat page (views/assistant/TokenCalendar.tsx): GET /api/v1/usage/days, every computer's tokens a day in the
// user's zone, the same count as today's tokens on the Machines page. Pure helpers here: the steps, where each day
// goes, the sums; the iPhone has the same rules (PersonaWire HeatLevels, TokenCalendarLayout).
import { api } from "./api";

export interface UsageDay {
  /** 2006-01-02, the day in the hub's user zone. */
  date: string;
  in?: number;
  cache_write?: number;
  cache_read?: number;
  out?: number;
  req?: number;
}

export interface UsageDays {
  zone: string;
  days: UsageDay[];
  stored?: boolean;
}

/** Every token of a day, cache reads included (today's tokens' count). */
export function dayTotal(d: UsageDay): number {
  return (d.in ?? 0) + (d.cache_write ?? 0) + (d.cache_read ?? 0) + (d.out ?? 0);
}

/** The last n days' tokens (today's with n 1). */
export function lastDays(days: UsageDay[], n: number): number {
  return days.slice(-n).reduce((s, d) => s + dayTotal(d), 0);
}

/** The last `days` days (1–371), oldest first, the last today. */
export async function fetchUsageDays(days: number): Promise<UsageDays> {
  const res = await api<{ usage: UsageDays }>(`/usage/days?days=${days}`);
  return res.usage;
}

/**
 * The steps of a heat calendar: 0 for nothing, else 1 to `count` by where a value ranks among the values above 0, as
 * GitHub does: `ceil(count × (values at or below it) / (values above 0))`. The largest is always the last step, equal
 * values share one, and one very large value does not push the others down (a scale from the largest made every
 * other day the faintest).
 */
export function heatLevels(values: number[], count = 4): number[] {
  const sorted = values.filter((v) => v > 0).sort((a, b) => a - b);
  if (!sorted.length || count <= 0) return values.map(() => 0);
  return values.map((v) => {
    if (v <= 0) return 0;
    let lo = 0;
    let hi = sorted.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (sorted[mid] <= v) lo = mid + 1;
      else hi = mid;
    }
    return Math.max(1, Math.min(count, Math.ceil((count * lo) / sorted.length)));
  });
}

/** One square of the calendar: its day, or null for a day still to come. */
export interface CalendarCell {
  date: string | null;
  level: number;
}

export interface CalendarLayout {
  /** `columns[week][row]`, Monday first, this week last. */
  columns: CalendarCell[][];
  /** The column and row of today. */
  today: [number, number] | null;
  /** A month's first day's date over the first week that starts in it (formatted by the view), at its column. */
  months: { column: number; first: string }[];
}

const DAY = 86_400_000;

/** A date's day number (days since 1970-01-01), the hub's days read as fixed calendar days whatever the browser's zone. */
function dayNumber(date: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  return m ? Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])) / DAY : null;
}

function dateOf(n: number): string {
  return new Date(n * DAY).toISOString().slice(0, 10);
}

/** Monday is row 0 (1970-01-01 was a Thursday). */
function row(n: number): number {
  return (((n + 3) % 7) + 7) % 7;
}

/** How many weeks fit `width` px of squares `cell` px wide and `gap` apart, between `min` and `max`. */
export function weeksThatFit(width: number, cell: number, gap: number, min = 8, max = 53): number {
  return Math.min(max, Math.max(min, Math.floor((width + gap) / (cell + gap))));
}

/**
 * Where each day goes: a column a week, Monday on top, this week last, the days to come left out; the steps from the
 * days shown; a month over the first week that starts in it, never two closer than three weeks.
 */
export function calendarLayout(days: UsageDay[], weeks: number): CalendarLayout {
  const last = days.length ? dayNumber(days[days.length - 1].date) : null;
  if (last === null || weeks <= 0) return { columns: [], today: null, months: [] };
  const totals = new Map(days.map((d) => [d.date, dayTotal(d)]));
  const first = last - row(last) - 7 * (weeks - 1);
  const dates: (string | null)[][] = [];
  const shown: number[] = [];
  for (let c = 0; c < weeks; c++) {
    const column: (string | null)[] = [];
    for (let r = 0; r < 7; r++) {
      const n = first + 7 * c + r;
      if (n > last) {
        column.push(null);
        continue;
      }
      const d = dateOf(n);
      column.push(d);
      shown.push(totals.get(d) ?? 0);
    }
    dates.push(column);
  }
  const steps = heatLevels(shown);
  let i = 0;
  const columns = dates.map((column) => column.map((date) => ({ date, level: date === null ? -1 : steps[i++] })));
  const months: CalendarLayout["months"] = [];
  let month = "";
  for (let c = 0; c < weeks; c++) {
    const d = dateOf(first + 7 * c);
    if (Number(d.slice(8)) > 7 || d.slice(0, 7) === month) continue;
    if (months.length && c - months[months.length - 1].column < 3) months.pop();
    months.push({ column: c, first: `${d.slice(0, 7)}-01` });
    month = d.slice(0, 7);
  }
  return { columns, today: [weeks - 1, row(last)], months };
}

/** One hour on the clock in the user's zone (a day the clocks change has 23 or 25 of them). */
export interface UsageHour extends Omit<UsageDay, "date"> {
  /** Unix seconds of its start. */
  t: number;
  /** Its day, 2006-01-02. */
  date: string;
  /** 0–23 on the clock. */
  hour: number;
}

/** A computer's share of the hours; `name` empty for a computer no longer there. */
export interface UsageMachineShare extends Omit<UsageDay, "date"> {
  id: string;
  name: string;
}

/** An agent's share of the hours (#9984): `Session.agent`'s names; "" for usage the hub kept before it counted agents
 *  apart. */
export interface UsageAgentShare extends Omit<UsageDay, "date"> {
  agent: string;
}

export interface UsageHours {
  zone: string;
  hours: UsageHour[];
  machines: UsageMachineShare[];
  /** Absent from an older hub. */
  agents?: UsageAgentShare[];
  total: Omit<UsageDay, "date">;
  stored?: boolean;
}

/** The last `days` days (1–7) by the hour, oldest first, the last the hour under way. */
export async function fetchUsageHours(days = 7): Promise<UsageHours> {
  const res = await api<{ usage: UsageHours }>(`/usage/hours?days=${days}`);
  return res.usage;
}

export interface HourLayout {
  /** The days, a row each, oldest first (the last is today). */
  dates: string[];
  /** `[row][hour]`: the step of each hour (0–4), -1 for an hour still to come today. */
  levels: number[][];
  /** `[row][hour]`: its tokens (an hour that comes twice adds into its square). */
  tokens: number[][];
}

/**
 * Where each hour of the last days goes (the iPhone's UsageHourLayout): a row a day, a column an hour on the clock;
 * the hour that comes twice on the day the clocks go back adds into its square, the one a day skips stays empty;
 * today's hours after the one under way are still to come (-1). The steps rank every hour that is not to come.
 */
export function hourLayout(hours: UsageHour[]): HourLayout {
  const dates: string[] = [];
  for (const h of hours) if (dates[dates.length - 1] !== h.date) dates.push(h.date);
  const row = new Map(dates.map((d, i) => [d, i]));
  const tokens = dates.map(() => Array.from({ length: 24 }, () => 0));
  const seen = dates.map(() => Array.from({ length: 24 }, () => false));
  for (const h of hours) {
    const r = row.get(h.date);
    if (r === undefined || h.hour < 0 || h.hour > 23) continue;
    tokens[r][h.hour] += dayTotal({ ...h, date: h.date });
    seen[r][h.hour] = true;
  }
  const last = hours.length ? hours[hours.length - 1].hour : 23;
  const toCome = (r: number, c: number) => r === dates.length - 1 && c > last && !seen[r][c];
  const flat: number[] = [];
  dates.forEach((_, r) => tokens[r].forEach((v, c) => !toCome(r, c) && flat.push(v)));
  const steps = heatLevels(flat);
  let i = 0;
  const levels = dates.map((_, r) => tokens[r].map((_, c) => (toCome(r, c) ? -1 : steps[i++])));
  return { dates, levels, tokens };
}
