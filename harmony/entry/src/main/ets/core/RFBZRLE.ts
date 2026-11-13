import { zlib } from '@kit.BasicServicesKit';

const MAX_COMPRESSED_BYTES: number = 1024 * 1024;
const MAX_INFLATED_BYTES: number = 72 * 1024 * 1024;

function copyBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  return copy.buffer;
}

/** One persistent raw-DEFLATE stream per RFB connection, as required by ZRLE. */
export class RFBZRLE {
  private zip: zlib.Zip = zlib.createZipSync();
  private stream: zlib.ZStream | undefined;
  private started: boolean = false;
  private headerBytesLeft: number = 2;
  private closed: boolean = false;

  async inflate(compressed: Uint8Array, outputLimit: number): Promise<Uint8Array> {
    if (this.closed || compressed.length > MAX_COMPRESSED_BYTES || !Number.isInteger(outputLimit) ||
      outputLimit < 1 || outputLimit > MAX_INFLATED_BYTES) throw new Error('rfb_zrle_limit');
    await this.ensureStarted();
    let input: Uint8Array = compressed;
    if (this.headerBytesLeft > 0) {
      const skip: number = Math.min(this.headerBytesLeft, input.length);
      this.headerBytesLeft -= skip;
      input = input.slice(skip);
    }
    if (input.length === 0) return new Uint8Array(0);

    const output = new Uint8Array(outputLimit);
    const stream: zlib.ZStream = { nextIn: copyBuffer(input), availableIn: input.length, nextOut: output.buffer as ArrayBuffer, availableOut: output.length };
    const status: zlib.ReturnStatus = await this.zip.inflate(stream, zlib.CompressFlushMode.NO_FLUSH);
    this.stream = await this.zip.getZStream();
    const updated: zlib.ZStream = this.stream as zlib.ZStream;
    const remaining: number = updated.availableIn === undefined ? input.length : updated.availableIn;
    const unusedOutput: number = updated.availableOut === undefined ? output.length : updated.availableOut;
    const produced: number = output.length - unusedOutput;
    if ((status !== zlib.ReturnStatus.OK && status !== zlib.ReturnStatus.BUF_ERROR) || remaining !== 0 ||
      produced < 0 || produced > output.length) throw new Error('rfb_zrle_inflate');
    return output.slice(0, produced);
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    const stream: zlib.ZStream | undefined = this.stream;
    if (this.started && stream) this.zip.inflateEnd(stream).catch(() => {});
    this.stream = undefined;
  }

  private async ensureStarted(): Promise<void> {
    if (this.started) return;
    const stream: zlib.ZStream = { nextIn: new ArrayBuffer(1), availableIn: 1, nextOut: new ArrayBuffer(1), availableOut: 1 };
    const status: zlib.ReturnStatus = await this.zip.inflateInit2(stream, -15);
    if (status !== zlib.ReturnStatus.OK) throw new Error('rfb_zrle_inflate');
    this.stream = await this.zip.getZStream();
    this.started = true;
  }
}
