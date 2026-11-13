export type ScheduleTemplateRepeat = 'daily' | 'interval';

export interface ScheduleTemplate {
  id: string;
  titleKey: string;
  promptKey: string;
  repeat: ScheduleTemplateRepeat;
  time: string;
  weekdays: number[];
  everyMinutes: number;
}

const templates: ScheduleTemplate[] = [
  { id: 'meeting_prep', titleKey: 'schedule_template_meeting_prep', promptKey: 'schedule_template_meeting_prep_prompt',
    repeat: 'daily', time: '08:45', weekdays: [1, 2, 3, 4, 5], everyMinutes: 60 },
  { id: 'weekly_report', titleKey: 'schedule_template_weekly_report', promptKey: 'schedule_template_weekly_report_prompt',
    repeat: 'daily', time: '17:00', weekdays: [5], everyMinutes: 60 },
  { id: 'end_of_day', titleKey: 'schedule_template_end_of_day', promptKey: 'schedule_template_end_of_day_prompt',
    repeat: 'daily', time: '18:30', weekdays: [1, 2, 3, 4, 5], everyMinutes: 60 },
];

function copy(template: ScheduleTemplate): ScheduleTemplate {
  return { ...template, weekdays: template.weekdays.slice() };
}

/** Templates are editable starting points; callers provide localized title and prompt text. */
export function listScheduleTemplates(): ScheduleTemplate[] { return templates.map((template: ScheduleTemplate) => copy(template)); }

export function findScheduleTemplate(id: string): ScheduleTemplate | undefined {
  const template = templates.find((item: ScheduleTemplate) => item.id === id);
  return template ? copy(template) : undefined;
}
