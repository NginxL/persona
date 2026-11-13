import type { ClientModel } from './ClientModel';
import { segment } from './HubRepository';

export class MessageImageData {
  data: ArrayBuffer = new ArrayBuffer(0);
  mime: string = '';
}
/** Both lazy transcript images and mobile photo-note paths use the captured authenticated session owner. */
export async function loadMessageImage(model: ClientModel, itemId: string, path: string): Promise<MessageImageData> {
  const repository = model.repo(), generation = model.connectionGeneration(), scope = model.scope(), target = model.target(), row = model.selected;
  const route = path ? '/api/v1/machines/' + segment(row.machine) + '/file/download?path=' + segment(path) + '&cwd=' + segment(row.path || '~') :
    '/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/items/' + segment(itemId) + '/image';
  const response = await repository.transport.requestBytes(route);
  if (!model.owns(generation, scope) || model.target() !== target || model.repo() !== repository) throw new Error('request_updated');
  const mime = response.contentType.split(';')[0].trim().toLowerCase();
  if (!['image/jpeg', 'image/png', 'image/webp', 'image/gif', 'image/heif', 'image/heic'].includes(mime) || !response.data.byteLength || response.data.byteLength > 32 * 1024 * 1024) throw new Error('preview_unavailable');
  const result = new MessageImageData(); result.data = response.data; result.mime = mime; return result;
}
