/** Compatibility with the photo notes emitted by the Hub and older mobile composers. */
export interface MessageContent {
  text: string;
  images: string[];
  files: string[];
}
const imageNotes: RegExp[] = [
  /\s*<attached_images>[\s\S]*?<\/attached_images>\s*/g,
  /(?:^|\n)\s*相关图片：[^\n]*$/g,
  /\s*（我从手机发来了图片，请用 Read 查看：[^）]*）\s*$/g
];
export function parseMessageContent(raw: string): MessageContent {
  let text = raw.replace(/<\/?pasted_content[^>]*>/g, '');
  const images: string[] = [];
  for (const pattern of imageNotes) {
    const notes = text.match(pattern) || [];
    for (const note of notes) {
      const body = note.replace(/<\/?attached_images>|相关图片：|（我从手机发来了图片，请用 Read 查看：|）/g, '\n');
      for (const line of body.split(/[\n、]/)) {
        const path = line.trim().replace(/^path:\s*/, '');
        if ((path.startsWith('/') && !path.startsWith('//') || path.startsWith('~/')) && !/[\x00-\x1f]/.test(path) && path.length <= 4096 && !images.includes(path) && images.length < 32) images.push(path);
      }
    }
    text = text.replace(pattern, '\n');
  }
  text = text.replace(/\[Image #\d+\]\s*/g, '').trim();
  const files: string[] = [];
  const pattern = /(^|[^\w/:.\-~])(~(?:\/[\w.@+\-]+)+\/?|\/[\w.@+\-]+(?:\/[\w.@+\-]+)+\/?|[\w.@\-]+(?:\/[\w.@+\-]+)+\.[A-Za-z][A-Za-z0-9]{0,7})(?![\w/\-])/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(text)) !== null && files.length < 6) {
    const path = match[2].replace(/[.,;:)]+$/, '');
    if (!images.includes(path) && !files.includes(path) && path.length >= 4) files.push(path);
  }
  return { text, images, files };
}
