import { describe, it, expect } from '@ohos/hypium';
import { util } from '@kit.ArkTS';
import { fileIo } from '@kit.CoreFileKit';
import { image } from '@kit.ImageKit';
import { filePreview } from '@kit.PreviewKit';
import { previewAttachment } from '../../../main/ets/platform/NativePreview';
import { nativeTestContext } from './NativeContext';

function saveFixture(path: string, bytes: ArrayBuffer): void {
  const file = fileIo.openSync(path, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
  try { expect(fileIo.writeSync(file.fd, bytes)).assertEqual(bytes.byteLength); } finally { fileIo.closeSync(file.fd); }
}
async function visiblePreview(path: string, name: string, mime: string): Promise<void> {
  const context = nativeTestContext(); let opened = false;
  try {
    await previewAttachment(context, path, name, mime); opened = true;
    await new Promise<void>((resolve) => setTimeout(resolve, 7000));
  } finally { if (opened) await filePreview.closePreview(context); }
}
export default function nativePreviewTests(): void {
  describe('NativePreview', (): void => {
    it('selected_text_uses_the_native_preview_service_with_a_typed_sandbox_copy', 0, async (): Promise<void> => {
      const context = nativeTestContext(), directory = context.filesDir + '/chat-attachments';
      try { fileIo.mkdirSync(directory); } catch (_) {}
      const path = directory + '/' + util.generateRandomUUID() + '.txt';
      const data = new util.TextEncoder().encodeInto('HARMONY_NATIVE_PREVIEW_OK\nSynthetic attachment preview.\n');
      try { saveFixture(path, data.buffer as ArrayBuffer); await visiblePreview(path, 'Native text preview.txt', 'text/plain'); }
      finally { try { fileIo.unlinkSync(path); } catch (_) {} }
    });
    it('native_png_and_jpeg_decode_encode_and_preview_use_sandbox_paths', 0, async (): Promise<void> => {
      const context = nativeTestContext(), directory = context.filesDir + '/chat-attachments';
      try { fileIo.mkdirSync(directory); } catch (_) {}
      const pngPath = directory + '/' + util.generateRandomUUID() + '.png', jpegPath = directory + '/' + util.generateRandomUUID() + '.jpg';
      let source: image.ImageSource | undefined, pixels: image.PixelMap | undefined, packer: image.ImagePacker | undefined, jpeg: image.ImageSource | undefined;
      try {
        const data = new util.Base64Helper().decodeSync('iVBORw0KGgoAAAANSUhEUgAAAQAAAAEACAYAAABccqhmAAADxUlEQVR4nO3UMRGAMAAEwSihRglyqBGBpsjBRrCQ7ovfYh3c3Bj3WvQ65kOxkQ4QA8AAMAAMAAPAADAADAADwAAwAAwAA8AAMAAMAAPAADAADAADwAAwAAwAA8AAMAAMAAPAADAADAADwAAwAAyAdIAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYQLl0gBgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABhAuXSAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGEC5dIAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYQLl0gBgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABhAuXSAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGwM4A1jwXvb7rpZgBlEsHiAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAGUSweIAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAZRLB4gBYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABlEsHiAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAGUSweIAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAZRLB4gBYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABlEsHiAFgABgABoABYAAYAAaAAWAAGAAGgAFgABgABoABYAAYAAaAAWAAGAAGwL4fNMemyz93QXsAAAAASUVORK5CYII=');
        saveFixture(pngPath, data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer);
        source = image.createImageSource(pngPath); const pngInfo = await source.getImageInfo();
        expect(pngInfo.mimeType).assertEqual('image/png'); expect(pngInfo.size.width).assertEqual(256); expect(pngInfo.size.height).assertEqual(256);
        pixels = await source.createPixelMap({ desiredSize: { width: 256, height: 256 } });
        packer = image.createImagePacker(); const encoded = await packer.packToData(pixels, { format: 'image/jpeg', quality: 75 });
        expect(encoded.byteLength > 0).assertTrue(); saveFixture(jpegPath, encoded);
        jpeg = image.createImageSource(jpegPath); const jpegInfo = await jpeg.getImageInfo();
        expect(jpegInfo.mimeType).assertEqual('image/jpeg'); expect(jpegInfo.size.width).assertEqual(256);
        await visiblePreview(pngPath, 'Native image preview.png', 'image/png');
        await visiblePreview(jpegPath, 'Native image preview.jpg', 'image/jpeg');
      } finally {
        await jpeg?.release(); await packer?.release(); await pixels?.release(); await source?.release();
        try { fileIo.unlinkSync(pngPath); } catch (_) {} try { fileIo.unlinkSync(jpegPath); } catch (_) {}
      }
    });
  });
}
