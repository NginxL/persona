import { assistantViewOpenTarget } from './AssistantView';
import { ResourceRow } from './Wire';

/** Native card targets keep the card's paired Hub and manager; web links use the separate reader. */
export function assistantCardResource(address: string, profile: string, manager: string, rows: ResourceRow[]): ResourceRow | undefined {
  const target = assistantViewOpenTarget(address);
  if (!profile || !manager || target?.kind !== 'resource') return undefined;
  const suffix = target.address.slice('persona://'.length), slash = suffix.indexOf('/'), host = suffix.slice(0, slash).toLowerCase();
  let parts: string[];
  try { parts = suffix.slice(slash + 1).split('/').map((part: string) => decodeURIComponent(part)); } catch (_) { return undefined; }
  let result: ResourceRow;
  if (host === 'session') {
    const known = rows.find((row: ResourceRow) => row.machine === parts[0] && (row.session || row.id) === parts[1] &&
      (!row.group || row.group === profile) && (!row.manager || row.manager === manager));
    result = new ResourceRow(known?.id || parts[1], known?.title || '', known?.kind || 'session');
    result.machine = parts[0]; result.session = parts[1];
    if (known) { result.path = known.path; result.raw = known.raw; result.detail = known.detail; result.state = known.state; }
  } else if (host === 'task') result = new ResourceRow(parts[0], '', 'todo');
  else if (host === 'kb') { result = new ResourceRow(parts.slice(1).join('/'), '', 'knowledge'); result.path = parts[0]; }
  else if (host === 'file') {
    const path = parts.slice(1).join('/'); result = new ResourceRow(parts[0] + ':' + path, path.split('/').pop() || '', 'file');
    result.machine = parts[0]; result.path = path;
  } else return undefined;
  result.group = profile; result.manager = manager; return result;
}
