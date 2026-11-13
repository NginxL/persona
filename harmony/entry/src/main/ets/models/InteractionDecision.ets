import { Decision, DecisionField, Wire } from './Wire';

/** A key is the value sent to a screen dialog; label is only what the person sees. */
export class InteractionChoice {
  key: string = '';
  label: string = '';
  detail: string = '';
  kind: string = 'plain';
  checked: boolean = false;
  recommended: boolean = false;
}

/** One field in the deliberately bounded MCP elicitation JSON Schema subset. */
export class InteractionSchemaField {
  key: string = '';
  title: string = '';
  description: string = '';
  type: string = '';
  format: string = '';
  required: boolean = false;
  secret: boolean = false;
  choices: Object[] = [];
  choiceLabels: string[] = [];
  defaultValue: Object | undefined = undefined;
  schema: Record<string, Object> = Wire.payload([], []);
}

export class InteractionValidation {
  supported: boolean = false;
  valid: boolean = false;
  content: Record<string, Object> = Wire.payload([], []);
  errors: Record<string, string> = Wire.payload([], []) as Record<string, string>;
}

const ROOT_SCHEMA_KEYS: string[] = ['type', 'properties', 'required', 'title', 'description', 'additionalProperties', '$schema'];
const FIELD_SCHEMA_KEYS: string[] = ['type', 'title', 'description', 'default', 'enum', 'enumNames', 'minLength', 'maxLength',
  'format', 'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf', 'items', 'minItems', 'maxItems',
  'uniqueItems', 'writeOnly'];

function rawInteraction(request: Decision): Record<string, Object> {
  const root = Wire.parse(request.raw);
  const nested = Wire.object(root, 'interaction');
  return Object.keys(nested).length > 0 ? nested : root;
}

function rawRecord(request: Decision): Record<string, Object> { return Wire.parse(request.raw); }

/** The exact tool name is protocol data; it does not use the localized card title. */
export function interactionToolName(request: Decision): string {
  return Wire.text(rawInteraction(request), 'tool_name');
}

/** Only the assistant's own ask_user question is non-blocking in a Persona conversation. */
export function isPersonaAskUserQuestion(request: Decision, selectedKind: string): boolean {
  return selectedKind === 'conversation' && request.kind === 'question' && interactionToolName(request) === 'ask_user';
}

/** Keep every other open request blocking, even when an ask_user question precedes it. */
export function firstBlockingDecision(requests: Decision[], selectedKind: string): Decision | undefined {
  return requests.find((request: Decision): boolean =>
    !isPersonaAskUserQuestion(request, selectedKind) && !['resolved', 'closed', 'dismissed', 'expired', 'cancelled', 'ended'].includes(request.status));
}

function recommendedLabel(value: string): boolean {
  return value.endsWith('(Recommended)') || value.endsWith('（推荐）') || value.endsWith('(推荐)');
}

function decisionField(key: string, title: string, required: boolean, kind: string, choices: string[] = []): DecisionField {
  const field = new DecisionField();
  field.key = key; field.title = title; field.required = required; field.kind = kind; field.choices = choices;
  return field;
}

function schemaProperties(schema: Record<string, Object>): Record<string, Object> {
  return Wire.object(schema, 'properties');
}

function schemaEnum(schema: Record<string, Object>): Object[] {
  const value = schema['enum'];
  if (!Array.isArray(value)) return [];
  return (value as Object[]).filter((item: Object) => item !== null && typeof item !== 'object');
}

function schemaChoiceLabels(schema: Record<string, Object>, choices: Object[]): string[] {
  const values = schema['enumNames'];
  if (!Array.isArray(values)) return choices.map((item: Object) => String(item));
  const labels = (values as Object[]).map((item: Object) => typeof item === 'string' ? item as string : '');
  return choices.map((item: Object, index: number) => labels[index] || String(item));
}

function schemaRequired(schema: Record<string, Object>): string[] {
  return Wire.strings(schema, 'required');
}

function schemaFields(request: Decision): InteractionSchemaField[] {
  const schema = Wire.object(Wire.object(rawInteraction(request), 'elicitation'), 'requested_schema');
  const properties = schemaProperties(schema), required = schemaRequired(schema), result: InteractionSchemaField[] = [];
  Object.keys(properties).forEach((key: string) => {
    const item = Wire.record(properties[key]);
    const field = new InteractionSchemaField();
    field.key = key;
    field.title = Wire.text(item, 'title', key);
    field.description = Wire.text(item, 'description');
    field.type = Wire.text(item, 'type');
    field.format = Wire.text(item, 'format');
    field.required = required.includes(key);
    field.secret = Wire.bool(item, 'writeOnly') || field.format === 'password';
    field.schema = item;
    field.defaultValue = item['default'];
    if (field.type === 'array') {
      const arrayItem = Wire.object(item, 'items');
      field.choices = schemaEnum(arrayItem);
      field.choiceLabels = schemaChoiceLabels(arrayItem, field.choices);
    } else {
      field.choices = schemaEnum(item);
      field.choiceLabels = schemaChoiceLabels(item, field.choices);
    }
    result.push(field);
  });
  return result;
}

function supportedField(schema: Record<string, Object>): boolean {
  if (Object.keys(schema).some((key: string) => !FIELD_SCHEMA_KEYS.includes(key))) return false;
  if (['minLength', 'maxLength', 'minItems', 'maxItems'].some((key: string) =>
    Wire.has(schema, key) && (!Number.isInteger(schema[key] as number) || (schema[key] as number) < 0))) return false;
  if (['minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf'].some((key: string) =>
    Wire.has(schema, key) && (typeof schema[key] !== 'number' || !Number.isFinite(schema[key] as number)))) return false;
  if (['writeOnly', 'uniqueItems'].some((key: string) => Wire.has(schema, key) && typeof schema[key] !== 'boolean')) return false;
  if (!['', 'email', 'uri', 'date', 'date-time', 'password'].includes(Wire.text(schema, 'format'))) return false;
  const enumValue = schema['enum'];
  if (enumValue !== undefined && (!Array.isArray(enumValue) || (enumValue as Object[]).length === 0 ||
    (enumValue as Object[]).some((item: Object) => item === null || typeof item === 'object'))) return false;
  const type = Wire.text(schema, 'type');
  if (['string', 'number', 'integer', 'boolean'].includes(type)) return true;
  if (type !== 'array') return false;
  const item = Wire.object(schema, 'items');
  return Wire.text(item, 'type') === 'string' && Array.isArray(item['enum']) && supportedField(item);
}

function supportedSchema(schema: Record<string, Object>): boolean {
  if (Wire.text(schema, 'type') !== 'object' || !Array.isArray(schema['required']) && Wire.has(schema, 'required')) return false;
  if (!Wire.has(schema, 'properties') || Array.isArray(schema['properties'])) return false;
  if (Object.keys(schema).some((key: string) => !ROOT_SCHEMA_KEYS.includes(key))) return false;
  const properties = schemaProperties(schema), required = schemaRequired(schema);
  if (required.length !== (Array.isArray(schema['required']) ? (schema['required'] as Object[]).length : 0)) return false;
  if (required.some((key: string) => !Object.keys(properties).includes(key))) return false;
  return Object.keys(properties).every((key: string) => supportedField(Wire.record(properties[key])));
}

function requestStatus(request: Decision): string {
  const outer = rawRecord(request);
  return Wire.text(outer, 'status', request.status);
}

function screenDialog(request: Decision): Record<string, Object> {
  return Wire.object(rawInteraction(request), 'dialog');
}

function screenOptionKind(label: string, supplied: string): string {
  if (supplied) return supplied;
  const lowered = label.toLowerCase();
  if (lowered.startsWith('type something')) return 'type_something';
  if (lowered.startsWith('chat about this')) return 'chat';
  return 'plain';
}

function optionFrom(data: Record<string, Object>): InteractionChoice {
  const choice = new InteractionChoice();
  choice.key = Wire.text(data, 'key');
  choice.label = Wire.text(data, 'label');
  choice.detail = Wire.text(data, 'detail', Wire.text(data, 'description'));
  choice.kind = screenOptionKind(choice.label, Wire.text(data, 'kind'));
  choice.checked = Wire.bool(data, 'checked');
  choice.recommended = recommendedLabel(choice.label);
  return choice;
}

function fallbackScreenOptions(request: Decision): InteractionChoice[] {
  const lines = Wire.text(rawInteraction(request), 'screen').split('\n');
  const rows: InteractionChoice[] = [];
  lines.forEach((line: string, index: number) => {
    const match = /^\s*(?:[❯>]\s*)?([1-9])\.\s+(.+)$/.exec(line);
    if (match) {
      const row = new InteractionChoice();
      row.key = match[1]; row.label = match[2].trim(); row.kind = screenOptionKind(row.label, '');
      row.recommended = recommendedLabel(row.label); rows.push(row);
    }
  });
  let start = -1;
  rows.forEach((row: InteractionChoice, index: number) => { if (row.key === '1') start = index; });
  if (start < 0) return [];
  const result: InteractionChoice[] = [rows[start]];
  for (let index = start + 1; index < rows.length; index++) {
    if (Number(rows[index].key) !== Number(result[result.length - 1].key) + 1) break;
    result.push(rows[index]);
  }
  return result.length > 1 ? result : [];
}

function questionOptions(data: Record<string, Object>, key: string): InteractionChoice[] {
  const question = Wire.rows(data, 'questions').find((item: Record<string, Object>) => Wire.text(item, 'question') === key);
  if (!question) return [];
  return Wire.rows(question, 'options').map((item: Record<string, Object>) => {
    const choice = optionFrom(item);
    choice.key = choice.label;
    choice.kind = Wire.text(item, 'action');
    return choice;
  });
}

function hasValue(values: Record<string, Object>, key: string): boolean {
  return Wire.has(values, key) && values[key] !== undefined && values[key] !== null;
}

function equalJson(left: Object, right: Object): boolean { return JSON.stringify(left) === JSON.stringify(right); }

function validDate(value: string): boolean {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return false;
  const year = Number(match[1]), month = Number(match[2]), day = Number(match[3]);
  if (month < 1 || month > 12) return false;
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return day >= 1 && day <= days[month - 1];
}

function validDateTime(value: string): boolean {
  const match = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-](\d{2}):(\d{2}))$/i.exec(value);
  if (!match || !validDate(match[1])) return false;
  const hour = Number(match[2]), minute = Number(match[3]), second = Number(match[4]);
  if (hour >= 24 || minute >= 60 || second >= 60) return false;
  if (match[5].toUpperCase() === 'Z') return true;
  return Number(match[6]) < 24 && Number(match[7]) < 60;
}

function schemaError(field: InteractionSchemaField, value: Object): boolean {
  const schema = field.schema;
  if (field.type === 'string') {
    if (typeof value !== 'string') return true;
    const text = value as string, length = Array.from(text).length;
    if (Wire.has(schema, 'minLength') && length < Wire.number(schema, 'minLength')) return true;
    if (Wire.has(schema, 'maxLength') && length > Wire.number(schema, 'maxLength')) return true;
    if (field.format === 'email' && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(text)) return true;
    if (field.format === 'uri' && !/^[A-Za-z][A-Za-z0-9+.-]*:[^\s]*$/.test(text)) return true;
    if (field.format === 'date' && !validDate(text)) return true;
    if (field.format === 'date-time' && !validDateTime(text)) return true;
  } else if (field.type === 'number' || field.type === 'integer') {
    if (typeof value !== 'number' || !Number.isFinite(value as number)) return true;
    const number = value as number;
    if (field.type === 'integer' && !Number.isInteger(number)) return true;
    if (Wire.has(schema, 'minimum') && number < Wire.number(schema, 'minimum')) return true;
    if (Wire.has(schema, 'maximum') && number > Wire.number(schema, 'maximum')) return true;
    if (Wire.has(schema, 'exclusiveMinimum') && number <= Wire.number(schema, 'exclusiveMinimum')) return true;
    if (Wire.has(schema, 'exclusiveMaximum') && number >= Wire.number(schema, 'exclusiveMaximum')) return true;
    if (Wire.has(schema, 'multipleOf')) {
      const multiple = Wire.number(schema, 'multipleOf');
      const quotient = number / multiple;
      if (multiple <= 0 || Math.abs(quotient - Math.round(quotient)) >= 1e-9 * Math.max(1, Math.abs(quotient))) return true;
    }
  } else if (field.type === 'boolean') {
    if (typeof value !== 'boolean') return true;
  } else if (field.type === 'array') {
    if (!Array.isArray(value)) return true;
    const values = value as Object[];
    if (values.some((entry: Object) => typeof entry !== 'string' || !field.choices.some((choice: Object) => equalJson(choice, entry)))) return true;
    if (values.some((entry: Object, index: number) => values.slice(0, index).some((other: Object) => equalJson(entry, other)))) return true;
    if (Wire.has(schema, 'minItems') && values.length < Wire.number(schema, 'minItems')) return true;
    if (Wire.has(schema, 'maxItems') && values.length > Wire.number(schema, 'maxItems')) return true;
  } else return true;
  if (field.type !== 'array' && field.choices.length > 0 && !field.choices.some((choice: Object) => equalJson(choice, value))) return true;
  return false;
}

function convertedValue(field: InteractionSchemaField, value: Object): Object | undefined {
  if (field.type === 'number' || field.type === 'integer') {
    const text = String(value).trim();
    if (typeof value !== 'number' && !text) return undefined;
    if (typeof value !== 'number' && !/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(text)) return undefined;
    const parsed = typeof value === 'number' ? value as number : Number(text);
    if (!Number.isFinite(parsed)) return undefined;
    return schemaError(field, parsed) ? undefined : parsed;
  }
  return schemaError(field, value) ? undefined : value;
}

function historyResolution(request: Decision): Record<string, Object> {
  return Wire.object(rawRecord(request), 'resolution');
}

/** Convert an incoming event or a history record into the existing Wire.Decision model. */
export function decodeInteractionDecision(input: Record<string, Object>): Decision {
  const data = Wire.object(input, 'interaction');
  const interaction = Object.keys(data).length > 0 ? data : input;
  const decision = new Decision();
  decision.id = Wire.text(interaction, 'id');
  decision.machine = Wire.text(interaction, 'machine_id');
  decision.session = Wire.text(interaction, 'session_id');
  decision.kind = Wire.text(interaction, 'kind');
  decision.prompt = Wire.text(interaction, 'summary', Wire.text(interaction, 'plan', Wire.text(interaction, 'prompt', Wire.text(interaction, 'question'))));
  if (decision.kind === 'elicitation') decision.prompt = Wire.text(Wire.object(interaction, 'elicitation'), 'message', decision.prompt);
  decision.status = Wire.text(input, 'status', Wire.text(interaction, 'status', 'open'));
  decision.raw = JSON.stringify(input);
  if (decision.kind === 'question') {
    decision.fields = Wire.rows(interaction, 'questions').map((question: Record<string, Object>) =>
      decisionField(Wire.text(question, 'question'), Wire.text(question, 'question'), true,
        Wire.bool(question, 'multi_select') ? 'multi_choice' : 'choice', Wire.rows(question, 'options').map((option: Record<string, Object>) => Wire.text(option, 'label'))));
  } else if (decision.kind === 'permission') {
    decision.fields = Wire.rows(interaction, 'fields').map((source: Record<string, Object>) =>
      decisionField(Wire.text(source, 'key'), Wire.text(source, 'label', Wire.text(source, 'key')), !Wire.bool(source, 'optional'),
        Wire.bool(source, 'secret') ? 'secret' : 'text'));
  } else if (decision.kind === 'screen') {
    decision.revision = Wire.text(Wire.object(interaction, 'dialog'), 'rev');
    decision.options = interactionScreenOptions(decision).map((option: InteractionChoice) => option.label);
  } else if (decision.kind === 'elicitation') {
    decision.fields = schemaFields(decision).map((field: InteractionSchemaField) =>
      decisionField(field.key, field.title, field.required, field.type === 'array' ? 'multi_choice' : field.secret ? 'secret' : field.type,
        field.choiceLabels));
  }
  return decision;
}

/** Full question metadata, including descriptions and exact answer labels. */
export function interactionQuestionOptions(request: Decision, question: string): InteractionChoice[] {
  return questionOptions(rawInteraction(request), question);
}

/** Screen options retain the key shown by the terminal separately from the user-facing label. */
export function interactionScreenOptions(request: Decision): InteractionChoice[] {
  const dialog = screenDialog(request);
  const options = Wire.rows(dialog, 'options');
  if (options.length > 0) return options.map((item: Record<string, Object>) => optionFrom(item));
  return fallbackScreenOptions(request);
}

export function interactionScreenMulti(request: Decision): boolean { return Wire.bool(screenDialog(request), 'multi'); }
export function interactionScreenSubmitLabel(request: Decision): string { return Wire.text(screenDialog(request), 'submit', 'submit'); }
export function interactionScreenQuestion(request: Decision): string { return Wire.text(screenDialog(request), 'question', request.prompt); }
export function interactionScreenCanCancel(request: Decision): boolean { return request.kind === 'question' && Wire.bool(rawInteraction(request), 'can_cancel'); }

export function interactionSchemaFields(request: Decision): InteractionSchemaField[] { return schemaFields(request); }

export function interactionElicitationSupported(request: Decision): boolean {
  if (request.kind !== 'elicitation' || Wire.text(Wire.object(rawInteraction(request), 'elicitation'), 'mode') !== 'form') return false;
  return supportedSchema(Wire.object(Wire.object(rawInteraction(request), 'elicitation'), 'requested_schema'));
}

export function interactionElicitationMessage(request: Decision): string {
  return Wire.text(Wire.object(rawInteraction(request), 'elicitation'), 'message', request.prompt);
}

export function interactionElicitationMode(request: Decision): string {
  return Wire.text(Wire.object(rawInteraction(request), 'elicitation'), 'mode');
}

export function interactionPermissionFieldPlaceholder(request: Decision, key: string): string {
  const field = Wire.rows(rawInteraction(request), 'fields').find((item: Record<string, Object>) => Wire.text(item, 'key') === key);
  return field ? Wire.text(field, 'placeholder', Wire.text(field, 'label', key)) : key;
}

export function interactionElicitationDefaults(request: Decision): Record<string, Object> {
  const values = Wire.payload([], []);
  schemaFields(request).forEach((field: InteractionSchemaField) => {
    if (!field.secret && field.defaultValue !== undefined) values[field.key] = field.defaultValue;
  });
  return values;
}

export function validateInteractionElicitation(request: Decision, values: Record<string, Object>): InteractionValidation {
  const result = new InteractionValidation();
  result.supported = interactionElicitationSupported(request);
  if (!result.supported) return result;
  schemaFields(request).forEach((field: InteractionSchemaField) => {
    if (!hasValue(values, field.key)) {
      if (field.required) result.errors[field.key] = 'required';
      return;
    }
    const converted = convertedValue(field, values[field.key]);
    if (converted === undefined) result.errors[field.key] = 'invalid';
    else result.content[field.key] = converted;
  });
  result.valid = Object.keys(result.errors).length === 0;
  return result;
}

/** Exact URL safe to open; user-info and non-HTTP(S) schemes are refused. */
export function interactionElicitationUrl(request: Decision): string {
  const elicitation = Wire.object(rawInteraction(request), 'elicitation');
  if (Wire.text(elicitation, 'mode') !== 'url') return '';
  const raw = Wire.text(elicitation, 'url').trim();
  if (!raw || raw.length > 8192 || /[\\\r\n]/.test(raw)) return '';
  const match = /^(https?):\/\/([^/?#\s]+)(?:[/?#].*)?$/i.exec(raw);
  if (!match || match[2].includes('@')) return '';
  const authority = match[2], portMatch = /:(\d+)$/.exec(authority);
  if (authority.includes(':') && !authority.startsWith('[') && !portMatch) return '';
  if (portMatch && Number(portMatch[1]) > 65535) return '';
  const hostname = authority.replace(/:\d+$/, '').replace(/^\[|\]$/g, '');
  return hostname ? raw : '';
}

export function interactionCanSubmit(request: Decision, readOnly: boolean = false): boolean {
  return !readOnly && !!request.id && request.status === 'open' && requestStatus(request) === 'open';
}

/** Build the documented proto.Resolution JSON body. Screen labels are resolved to keys before the request is sent. */
export function interactionResolution(request: Decision, action: string, values: Record<string, Object>, expectedRevision: string = request.revision): Record<string, Object> {
  if (!interactionCanSubmit(request)) throw new Error('not_pending');
  const data = rawInteraction(request);
  if (request.kind === 'question') {
    if (action === 'cancel') {
      if (!Wire.bool(data, 'can_cancel')) throw new Error('unsupported');
      return Wire.payload(['cancel'], [true]);
    }
    const answers = Wire.payload([], []);
    const questions = Wire.rows(data, 'questions');
    if (questions.length === 0) throw new Error('request_updated');
    questions.forEach((question: Record<string, Object>) => {
      const key = Wire.text(question, 'question');
      if (hasValue(values, key) && String(values[key]).trim()) answers[key] = String(values[key]).trim();
    });
    if (Object.keys(answers).length !== questions.length) throw new Error('required');
    return Wire.payload(['answers'], [answers]);
  }
  if (request.kind === 'plan') {
    if (action !== 'approve' && action !== 'deny' && action !== 'reject') throw new Error('unsupported');
    const approved = action === 'approve';
    const body = Wire.payload(['approve'], [approved]);
    if (!approved) body['feedback'] = Wire.text(values, 'feedback');
    return body;
  }
  if (request.kind === 'permission') {
    if (action !== 'allow' && action !== 'approve' && action !== 'deny') throw new Error('unsupported');
    const allowed = action === 'allow' || action === 'approve';
    const body = Wire.payload(['allow'], [allowed]);
    if (allowed) {
      const fields = Wire.rows(data, 'fields'), filled = Wire.payload([], []);
      fields.forEach((field: Record<string, Object>) => {
        const key = Wire.text(field, 'key'), value = values[key];
        if (hasValue(values, key) && String(value).trim()) filled[key] = String(value);
        else if (!Wire.bool(field, 'optional')) throw new Error('required');
      });
      if (fields.length > 0) body['values'] = filled;
    }
    return body;
  }
  if (request.kind === 'screen') {
    const dialog = screenDialog(request), revision = Wire.text(dialog, 'rev');
    if (revision && (request.revision !== revision || expectedRevision !== revision)) throw new Error('request_updated');
    if (action === 'submit') {
      if (!Wire.bool(dialog, 'multi')) throw new Error('unsupported');
      const selected = Wire.strings(values, 'options');
      const allowed = interactionScreenOptions(request).filter((item: InteractionChoice) => item.kind === 'plain').map((item: InteractionChoice) => item.key);
      if (selected.some((key: string) => !allowed.includes(key))) throw new Error('request_updated');
      if (selected.length === 0 && !Wire.text(values, 'text').trim()) throw new Error('required');
      const body = Wire.payload(['options'], [selected]);
      const text = Wire.text(values, 'text').trim(); if (text) body['text'] = text;
      return body;
    }
    const choice = interactionScreenOptions(request).find((item: InteractionChoice) => item.key === action || item.label === action);
    if (!choice || !choice.key) throw new Error('request_updated');
    if (choice.kind === 'unknown') throw new Error('unsupported');
    const typed = Wire.text(values, 'text').trim();
    if (choice.kind === 'type_something' && !typed) throw new Error('required');
    if (dialog && Object.keys(dialog).length > 0) {
      const body = Wire.payload(['option'], [choice.key]); if (choice.kind === 'type_something') body['text'] = typed;
      return body;
    }
    if (choice.kind === 'type_something') return Wire.payload(['pre_keys', 'text', 'keys'], [[choice.key, 'ctrl+e', 'ctrl+u'], typed, ['enter']]);
    return Wire.payload(['keys'], [[choice.key]]);
  }
  if (request.kind === 'elicitation') {
    const elicitation = Wire.object(data, 'elicitation'), mode = Wire.text(elicitation, 'mode');
    if (!['accept', 'decline', 'cancel'].includes(action)) throw new Error('unsupported');
    if (action !== 'accept') return Wire.payload(['action'], [action]);
    if (mode === 'url') {
      const target = interactionElicitationUrl(request);
      if (!target || Wire.text(values, 'opened_url') !== target) throw new Error('required');
      return Wire.payload(['action'], ['accept']);
    }
    if (mode !== 'form') throw new Error('unsupported');
    const validation = validateInteractionElicitation(request, values);
    if (!validation.valid) throw new Error(validation.supported ? 'required' : 'unsupported');
    return Wire.payload(['action', 'content'], ['accept', validation.content]);
  }
  throw new Error('unsupported');
}

/** A redacted, localized-neutral outcome for history; it never exposes secret permission/form values. */
export function interactionHistoryResolution(request: Decision): Record<string, Object> {
  const result = historyResolution(request), safe = Wire.payload([], []);
  if (request.kind === 'question') {
    const answers = Wire.object(result, 'answers');
    Object.keys(answers).forEach((key: string) => { if (typeof answers[key] === 'string') safe[key] = answers[key]; });
  } else if (request.kind === 'plan') {
    if (Wire.has(result, 'approve')) safe['approve'] = result['approve'];
    if (Wire.text(result, 'feedback')) safe['feedback'] = Wire.text(result, 'feedback');
  } else if (request.kind === 'permission') {
    if (Wire.has(result, 'allow')) safe['allow'] = result['allow'];
  } else if (request.kind === 'screen') {
    const key = Wire.text(result, 'option'), choice = interactionScreenOptions(request).find((item: InteractionChoice) => item.key === key);
    if (choice) safe['option_label'] = choice.label;
    const labels = Wire.strings(result, 'options').map((option: string) => interactionScreenOptions(request).find((item: InteractionChoice) => item.key === option)?.label || '');
    if (labels.length > 0) safe['options_labels'] = labels;
    if (Wire.text(result, 'text')) safe['text'] = Wire.text(result, 'text');
  } else if (request.kind === 'elicitation') {
    if (Wire.text(result, 'action')) safe['action'] = Wire.text(result, 'action');
  }
  return safe;
}
