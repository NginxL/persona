import { common } from '@kit.AbilityKit';
import { contact } from '@kit.ContactsKit';
import { calendarManager } from '@kit.CalendarKit';
import { geoLocationManager } from '@kit.LocationKit';
import { i18n } from '@kit.LocalizationKit';
import { Wire } from '../models/Wire';

export interface NativePhoneWritePlan {
  before: Record<string, Object>;
  after: Record<string, Object>;
  execute: (admit: () => boolean) => Promise<Record<string, Object>>;
}

function text(args: Record<string, Object>, key: string, maximum: number = 1000): string {
  if (!Wire.has(args, key)) return '';
  const value = args[key];
  if (typeof value !== 'string' || value.length > maximum || value.includes('\u0000')) throw new Error('invalid_phone_argument');
  return value.trim();
}
function admitted(admit: () => boolean): void { if (!admit()) throw new Error('phone_action_cancelled'); }
function copyContact(value: contact.Contact): contact.Contact { return JSON.parse(JSON.stringify(value)) as contact.Contact; }
function copyEvent(value: calendarManager.Event): calendarManager.Event { return JSON.parse(JSON.stringify(value)) as calendarManager.Event; }
function fingerprint(value: Object): string { return JSON.stringify(value); }
function contactResult(value: contact.Contact, id: string = ''): Record<string, Object> {
  return Wire.payload(['id', 'name', 'company', 'job_title', 'phones', 'emails', 'birthday'], [
    id || value.key || String(value.id || ''), value.name?.fullName || '', value.organization?.name || '', value.organization?.title || '',
    (value.phoneNumbers || []).map((phone: contact.PhoneNumber): Object => Wire.payload(['label', 'number'], [phone.labelName || 'other', phone.phoneNumber])),
    (value.emails || []).map((email: contact.Email): Object => Wire.payload(['label', 'email'], [email.labelName || 'other', email.email])),
    (value.events || []).find((event: contact.Event) => event.labelId === contact.Event.EVENT_BIRTHDAY)?.eventDate || ''
  ]);
}
function labeled(args: Record<string, Object>, key: string): Record<string, Object>[] {
  if (!Wire.has(args, key)) return [];
  if (!Array.isArray(args[key]) || (args[key] as Object[]).length > 20) throw new Error('invalid_phone_argument');
  return (args[key] as Object[]).map((entry: Object): Record<string, Object> => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) throw new Error('invalid_phone_argument');
    const row = Wire.record(entry), value = text(row, 'value', 256), label = text(row, 'label', 40) || 'other';
    if (!value) throw new Error('invalid_phone_argument');
    return Wire.payload(['value', 'label'], [value, label]);
  });
}
function mergedField(previous: string, args: Record<string, Object>, key: string, replace: boolean): string {
  if (!Wire.has(args, key)) return previous;
  const value = text(args, key);
  if (!replace && previous && value !== previous) throw new Error('contact_replace_required');
  return value;
}

/** Prepare the exact before/after shown by the phone confirmation; never mutate until final admission. */
export async function prepareContactWrite(context: common.UIAbilityContext, args: Record<string, Object>, update: boolean): Promise<NativePhoneWritePlan> {
  const id = text(args, 'id', 256);
  if (update && !id) throw new Error('contact_id_required');
  const key = update && /^\d+$/.test(id) ? await contact.queryKey(context, Number(id)) : id;
  const previous: contact.Contact = update ? await contact.queryContact(context, key) : {};
  if (update && (!previous || previous.id === contact.Contact.INVALID_CONTACT_ID)) throw new Error('contact_not_found');
  const before = update ? contactResult(previous) : Wire.payload([], []), baseline = fingerprint(previous);
  const next = copyContact(previous), replace = Wire.bool(args, 'replace');
  const attributes: contact.Attribute[] = [];
  const name = previous.name || { fullName: '' }, organization = previous.organization || { name: '' };
  const changesName = Wire.has(args, 'given_name') || Wire.has(args, 'family_name');
  if (changesName && !replace && name.fullName && !name.givenName && !name.familyName && !name.middleName) throw new Error('contact_replace_required');
  const given = mergedField(name.givenName || '', args, 'given_name', replace), family = mergedField(name.familyName || '', args, 'family_name', replace);
  if (changesName) {
    attributes.push(contact.Attribute.ATTR_NAME);
    const edited = next.name || { fullName: '' };
    edited.givenName = given; edited.familyName = family;
    edited.fullName = [name.namePrefix || '', given, name.middleName || '', family, name.nameSuffix || ''].filter((part: string) => !!part).join(' ');
    next.name = edited;
  }
  const company = mergedField(organization.name || '', args, 'company', replace), job = mergedField(organization.title || '', args, 'job_title', replace);
  if (Wire.has(args, 'company') || Wire.has(args, 'job_title')) { attributes.push(contact.Attribute.ATTR_ORGANIZATION); next.organization = { name: company, title: job }; }
  if (Wire.has(args, 'phones')) {
    attributes.push(contact.Attribute.ATTR_PHONE);
    const phones = replace ? [] : (next.phoneNumbers || []).slice();
    for (const row of labeled(args, 'phones')) {
      const value = Wire.text(row, 'value');
      if (!phones.some((phone: contact.PhoneNumber) => phone.phoneNumber.replace(/[\s()-]/g, '') === value.replace(/[\s()-]/g, ''))) {
        phones.push({ phoneNumber: value, labelName: Wire.text(row, 'label'), labelId: 0 });
      }
    }
    if (phones.length > 200) throw new Error('contact_limit'); next.phoneNumbers = phones;
  }
  if (Wire.has(args, 'emails')) {
    attributes.push(contact.Attribute.ATTR_EMAIL);
    const emails = replace ? [] : (next.emails || []).slice();
    for (const row of labeled(args, 'emails')) {
      const value = Wire.text(row, 'value');
      if (!emails.some((email: contact.Email) => email.email.toLocaleLowerCase() === value.toLocaleLowerCase())) {
        emails.push({ email: value, labelName: Wire.text(row, 'label'), labelId: 0 });
      }
    }
    if (emails.length > 200) throw new Error('contact_limit'); next.emails = emails;
  }
  if (!update && !next.name?.fullName && !next.organization?.name) throw new Error('contact_name_required');
  if (update && fingerprint(next) === baseline) throw new Error('contact_no_changes');
  const after = contactResult(next);
  return { before: before, after: after, execute: async (admit: () => boolean): Promise<Record<string, Object>> => {
    admitted(admit);
    if (update) {
      const current = await contact.queryContact(context, key);
      if (fingerprint(current) !== baseline) throw new Error('contact_changed');
      const patch: contact.Contact = { id: previous.id, key: previous.key };
      if (attributes.includes(contact.Attribute.ATTR_NAME)) patch.name = next.name;
      if (attributes.includes(contact.Attribute.ATTR_ORGANIZATION)) patch.organization = next.organization;
      if (attributes.includes(contact.Attribute.ATTR_PHONE)) patch.phoneNumbers = next.phoneNumbers;
      if (attributes.includes(contact.Attribute.ATTR_EMAIL)) patch.emails = next.emails;
      admitted(admit); await contact.updateContact(context, patch, { attributes: attributes });
      return Wire.payload(['ok', 'contact'], [true, after]);
    }
    admitted(admit); const created = await contact.addContact(context, next);
    if (!Number.isSafeInteger(created) || created <= 0) throw new Error('contact_not_saved');
    return Wire.payload(['ok', 'contact'], [true, contactResult(next, String(created))]);
  } };
}

interface CalendarMatch { calendar: calendarManager.Calendar; event: calendarManager.Event; }
export function hasNativeRecurrence(event: calendarManager.Event): boolean {
  const rule = event.recurrenceRule;
  if (!rule) return false;
  // API 26 projects an explicit -1 sentinel even for a non-repeating event.
  // Unknown frequencies or additional rule fields remain conservatively blocked.
  return Number(rule.recurrenceFrequency) !== -1 || Object.keys(rule).some((key: string): boolean => key !== 'recurrenceFrequency');
}
async function calendarEvent(context: common.UIAbilityContext, id: number): Promise<CalendarMatch> {
  const matches: CalendarMatch[] = [];
  for (const calendar of await calendarManager.getCalendarManager(context).getAllCalendars()) {
    const events = await calendar.getEvents(calendarManager.EventFilter.filterById([id]), ['id', 'type', 'title', 'startTime', 'endTime', 'isAllDay', 'description', 'timeZone', 'location', 'service', 'attendee', 'reminderTime', 'identifier', 'isLunar', 'recurrenceRule']);
    for (const event of events) if (event.id === id) matches.push({ calendar: calendar, event: event });
  }
  if (matches.length !== 1) throw new Error(matches.length ? 'calendar_event_ambiguous' : 'calendar_event_not_found');
  return matches[0];
}
function instant(value: string): number {
  const number = Date.parse(value);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d(?::\d\d(?:\.\d{1,3})?)?(?:Z|[+-]\d\d:\d\d)$/.test(value) || !Number.isFinite(number)) throw new Error('invalid_calendar_time');
  return number;
}
function eventResult(event: calendarManager.Event, calendar: calendarManager.Calendar): Record<string, Object> {
  const account = calendar.getAccount();
  return Wire.payload(['id', 'title', 'start', 'end', 'all_day', 'calendar', 'location', 'notes', 'recurring'], [String(event.id || ''),
    event.title || '', new Date(event.startTime).toISOString(), new Date(event.endTime).toISOString(), !!event.isAllDay,
    account.displayName || account.name || '', event.location?.location || '', event.description || '', hasNativeRecurrence(event)]);
}
export async function prepareCalendarWrite(context: common.UIAbilityContext, args: Record<string, Object>, remove: boolean): Promise<NativePhoneWritePlan> {
  const raw = text(args, 'id', 64), id = Number(raw);
  if (!/^\d+$/.test(raw) || !Number.isSafeInteger(id) || id <= 0) throw new Error('invalid_calendar_id');
  const match = await calendarEvent(context, id), before = eventResult(match.event, match.calendar), baseline = fingerprint(match.event);
  // Calendar Kit cannot atomically update an occurrence or split a series. Never turn "this" into the whole series.
  if (hasNativeRecurrence(match.event) || Wire.has(args, 'occurrence_start') || Wire.has(args, 'span') && Wire.text(args, 'span') !== 'this') throw new Error('calendar_occurrence_unsupported');
  const next = copyEvent(match.event);
  if (!remove) {
    if (Wire.has(args, 'title')) { next.title = text(args, 'title'); if (!next.title) throw new Error('calendar_title_required'); }
    if (Wire.has(args, 'start')) { const duration = next.endTime - next.startTime; next.startTime = instant(text(args, 'start')); if (!Wire.has(args, 'end')) next.endTime = next.startTime + duration; }
    if (Wire.has(args, 'end')) next.endTime = instant(text(args, 'end'));
    if (next.endTime <= next.startTime) throw new Error('invalid_calendar_time');
    if (Wire.has(args, 'location')) next.location = { location: text(args, 'location') };
    if (Wire.has(args, 'notes')) { text(args, 'notes', 10000); next.description = args['notes'] as string; }
    if (Wire.has(args, 'all_day')) { if (typeof args['all_day'] !== 'boolean') throw new Error('invalid_phone_argument'); next.isAllDay = Wire.bool(args, 'all_day'); }
    if (fingerprint(next) === baseline) throw new Error('calendar_no_changes');
  }
  const after = remove ? Wire.payload(['deleted'], [true]) : eventResult(next, match.calendar);
  return { before: before, after: after, execute: async (admit: () => boolean): Promise<Record<string, Object>> => {
    admitted(admit); const current = await calendarEvent(context, id);
    if (fingerprint(current.event) !== baseline) throw new Error('calendar_event_changed');
    admitted(admit);
    if (remove) await current.calendar.deleteEvent(id); else await current.calendar.updateEvent(next);
    return Wire.payload(['ok', 'id', 'event'], [true, raw, after]);
  } };
}

export async function searchNativePlaces(args: Record<string, Object>): Promise<Record<string, Object>> {
  const query = text(args, 'query', 1000), rawLimit = Wire.has(args, 'limit') ? args['limit'] : 5;
  if (!query || typeof rawLimit !== 'number' || !Number.isInteger(rawLimit) || rawLimit < 1 || rawLimit > 10) throw new Error('invalid_phone_argument');
  const found = await geoLocationManager.getAddressesFromLocationName({ description: query, maxItems: rawLimit, locale: i18n.getSystemLocale() });
  const results = found.slice(0, rawLimit).map((place: geoLocationManager.GeoAddress): Object => {
    const parts: string[] = [place.countryName || '', place.administrativeArea || '', place.locality || '', place.subLocality || '', place.roadName || '', place.premises || ''];
    const result = Wire.payload(['name', 'address'], [place.placeName || '', parts.filter((part: string) => !!part).join(', ')]);
    if (typeof place.latitude === 'number' && Number.isFinite(place.latitude)) result['latitude'] = place.latitude;
    if (typeof place.longitude === 'number' && Number.isFinite(place.longitude)) result['longitude'] = place.longitude;
    return result;
  });
  return Wire.payload(['query', 'count', 'results'], [query, results.length, results]);
}
