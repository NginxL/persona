import { util } from '@kit.ArkTS';
import type { ClientModel } from './ClientModel';
import { segment } from './HubRepository';
import { ResourceRow } from './Wire';

export class RemoteDocument {
  data: ArrayBuffer = new ArrayBuffer(0);
  mime: string = '';
  name: string = '';
  text: string = '';
}
/** Capture the authenticated file owner; a document path never becomes a web URL with credentials. */
export async function loadRemoteDocument(model: ClientModel, row: ResourceRow, valid: () => boolean): Promise<RemoteDocument> {
  if (!row.machine || !row.path || row.manager !== model.profile.manager || !valid()) throw new Error('request_updated');
  const repo = model.repo(), generation = model.connectionGeneration(), scope = model.scope();
  const response = await repo.transport.requestBytes('/api/v1/machines/' + segment(row.machine) + '/file/download?path=' + segment(row.path));
  if (!valid() || !model.owns(generation, scope) || model.repo() !== repo) throw new Error('request_updated');
  if (response.data.byteLength > 32 * 1024 * 1024) throw new Error('chat_attachment_file_too_large');
  const document = new RemoteDocument(); document.data = response.data; document.mime = response.contentType.split(';')[0].trim(); document.name = row.path.split('/').pop() || row.title;
  if (response.data.byteLength <= 1024 * 1024 && (/\.(md|txt|json|csv|log|xml)$/i.test(document.name) || /^text\/(plain|markdown|csv)$/.test(document.mime))) {
    document.text = util.TextDecoder.create('utf-8', { fatal: true }).decodeToString(new Uint8Array(document.data));
  }
  return document;
}
