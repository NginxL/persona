const PREVIEW_EXTENSIONS: string[] = ['jpg', 'jpeg', 'png', 'webp', 'gif', 'bmp', 'heic', 'avif', 'pdf', 'txt', 'md', 'json', 'xml', 'csv', 'html', 'htm', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'mp3', 'm4a', 'wav', 'ogg', 'mp4', 'mov'];

export function attachmentExtension(name: string): string {
  const extension = name.split('.').pop()?.toLowerCase() || '';
  return PREVIEW_EXTENSIONS.includes(extension) ? extension : 'bin';
}
export function isAttachmentCopyName(name: string): boolean {
  const match = /^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.([a-z0-9]+)$/.exec(name);
  return !!match && (match[2] === 'bin' || PREVIEW_EXTENSIONS.includes(match[2]));
}
export function attachmentMimeType(name: string): string {
  switch (attachmentExtension(name)) {
    case 'jpg': case 'jpeg': return 'image/jpeg';
    case 'png': return 'image/png';
    case 'webp': return 'image/webp';
    case 'gif': return 'image/gif';
    case 'pdf': return 'application/pdf';
    case 'txt': case 'md': return 'text/plain';
    case 'json': return 'application/json';
    case 'csv': return 'text/csv';
    case 'html': case 'htm': return 'text/html';
    default: return '';
  }
}
