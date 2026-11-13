/** Native-order BGRA framebuffer for the RFB 32-bit, little-endian pixel format. */
export class RFBFramebuffer {
  static readonly maxDimension: number = 16384;
  static readonly maxPixels: number = 16000000;
  static readonly maxBytes: number = RFBFramebuffer.maxPixels * 4;

  width: number = 1;
  height: number = 1;
  pixels: Uint8Array = new Uint8Array(4);

  constructor(width: number, height: number) { this.resize(width, height); }

  static validSize(width: number, height: number): boolean {
    return Number.isInteger(width) && Number.isInteger(height) && width > 0 && height > 0 &&
      width <= RFBFramebuffer.maxDimension && height <= RFBFramebuffer.maxDimension &&
      width * height <= RFBFramebuffer.maxPixels;
  }

  resize(width: number, height: number): void {
    if (!RFBFramebuffer.validSize(width, height)) throw new Error('rfb_size_limit');
    this.width = width;
    this.height = height;
    this.pixels = new Uint8Array(width * height * 4);
    for (let index: number = 3; index < this.pixels.length; index += 4) this.pixels[index] = 255;
  }

  inside(x: number, y: number, width: number, height: number): boolean {
    return Number.isInteger(x) && Number.isInteger(y) && Number.isInteger(width) && Number.isInteger(height) &&
      x >= 0 && y >= 0 && width >= 0 && height >= 0 && x + width <= this.width && y + height <= this.height;
  }

  requireRect(x: number, y: number, width: number, height: number): void {
    if (!this.inside(x, y, width, height)) throw new Error('rfb_bad_rect');
  }

  raw(x: number, y: number, width: number, height: number, data: Uint8Array): void {
    this.requireRect(x, y, width, height);
    if (data.length !== width * height * 4) throw new Error('rfb_bad_rect');
    for (let row: number = 0; row < height; row++) {
      let source: number = row * width * 4;
      let destination: number = ((y + row) * this.width + x) * 4;
      for (let column: number = 0; column < width; column++) {
        this.pixels[destination] = data[source];
        this.pixels[destination + 1] = data[source + 1];
        this.pixels[destination + 2] = data[source + 2];
        this.pixels[destination + 3] = 255;
        source += 4;
        destination += 4;
      }
    }
  }

  copy(x: number, y: number, width: number, height: number, sourceX: number, sourceY: number): void {
    this.requireRect(x, y, width, height);
    this.requireRect(sourceX, sourceY, width, height);
    const first: number = sourceY < y ? height - 1 : 0;
    const limit: number = sourceY < y ? -1 : height;
    const step: number = sourceY < y ? -1 : 1;
    for (let row: number = first; row !== limit; row += step) {
      const destination: number = ((y + row) * this.width + x) * 4;
      const source: number = ((sourceY + row) * this.width + sourceX) * 4;
      this.pixels.copyWithin(destination, source, source + width * 4);
    }
  }

  fill(x: number, y: number, width: number, height: number, blue: number, green: number, red: number): void {
    this.requireRect(x, y, width, height);
    for (let row: number = 0; row < height; row++) {
      let destination: number = ((y + row) * this.width + x) * 4;
      for (let column: number = 0; column < width; column++) {
        this.pixels[destination] = blue;
        this.pixels[destination + 1] = green;
        this.pixels[destination + 2] = red;
        this.pixels[destination + 3] = 255;
        destination += 4;
      }
    }
  }

  set(x: number, y: number, blue: number, green: number, red: number): void {
    if (x < 0 || y < 0 || x >= this.width || y >= this.height) throw new Error('rfb_bad_rect');
    const at: number = (y * this.width + x) * 4;
    this.pixels[at] = blue;
    this.pixels[at + 1] = green;
    this.pixels[at + 2] = red;
    this.pixels[at + 3] = 255;
  }

  pixel(x: number, y: number): number[] {
    if (x < 0 || y < 0 || x >= this.width || y >= this.height) throw new Error('rfb_bad_rect');
    const at: number = (y * this.width + x) * 4;
    return [this.pixels[at], this.pixels[at + 1], this.pixels[at + 2], this.pixels[at + 3]];
  }

  /** Decodes the tile subencodings used by RFB ZRLE into the same persistent framebuffer. */
  drawZRLE(data: Uint8Array, x: number, y: number, width: number, height: number): void {
    this.requireRect(x, y, width, height);
    let at: number = 0;
    const byte = (): number => {
      if (at >= data.length) throw new Error('rfb_zrle_tile');
      const value: number = data[at];
      at++;
      return value;
    };
    const color = (): number[] => {
      if (at + 3 > data.length) throw new Error('rfb_zrle_tile');
      const value: number[] = [data[at], data[at + 1], data[at + 2]];
      at += 3;
      return value;
    };
    const runLength = (): number => {
      let length: number = 1;
      let next: number = 0;
      do {
        next = byte();
        length += next;
        if (length > RFBFramebuffer.maxPixels) throw new Error('rfb_zrle_tile');
      } while (next === 255);
      return length;
    };
    let tileY: number = y;
    while (tileY < y + height) {
      const tileHeight: number = Math.min(64, y + height - tileY);
      let tileX: number = x;
      while (tileX < x + width) {
        const tileWidth: number = Math.min(64, x + width - tileX);
        const subencoding: number = byte();
        if (subencoding === 0) {
          for (let row: number = 0; row < tileHeight; row++) {
            for (let column: number = 0; column < tileWidth; column++) {
              const value: number[] = color();
              this.set(tileX + column, tileY + row, value[0], value[1], value[2]);
            }
          }
        } else if (subencoding === 1) {
          const value: number[] = color();
          this.fill(tileX, tileY, tileWidth, tileHeight, value[0], value[1], value[2]);
        } else if (subencoding >= 2 && subencoding <= 16) {
          const palette: number[][] = [];
          for (let item: number = 0; item < subencoding; item++) palette.push(color());
          const bits: number = subencoding === 2 ? 1 : subencoding <= 4 ? 2 : 4;
          const mask: number = (1 << bits) - 1;
          for (let row: number = 0; row < tileHeight; row++) {
            let packed: number = 0;
            let left: number = 0;
            for (let column: number = 0; column < tileWidth; column++) {
              if (left === 0) { packed = byte(); left = 8; }
              left -= bits;
              const index: number = (packed >>> left) & mask;
              if (index >= palette.length) throw new Error('rfb_zrle_tile');
              const value: number[] = palette[index];
              this.set(tileX + column, tileY + row, value[0], value[1], value[2]);
            }
          }
        } else if (subencoding === 128) {
          let pixel: number = 0;
          const count: number = tileWidth * tileHeight;
          while (pixel < count) {
            const value: number[] = color();
            const length: number = runLength();
            if (length > count - pixel) throw new Error('rfb_zrle_tile');
            for (let offset: number = 0; offset < length; offset++) {
              this.set(tileX + (pixel % tileWidth), tileY + Math.floor(pixel / tileWidth), value[0], value[1], value[2]);
              pixel++;
            }
          }
        } else if (subencoding >= 130) {
          const paletteSize: number = subencoding - 128;
          const palette: number[][] = [];
          for (let item: number = 0; item < paletteSize; item++) palette.push(color());
          let pixel: number = 0;
          const count: number = tileWidth * tileHeight;
          while (pixel < count) {
            const packed: number = byte();
            const index: number = packed & 127;
            if (index >= palette.length) throw new Error('rfb_zrle_tile');
            const length: number = (packed & 128) !== 0 ? runLength() : 1;
            if (length > count - pixel) throw new Error('rfb_zrle_tile');
            const value: number[] = palette[index];
            for (let offset: number = 0; offset < length; offset++) {
              this.set(tileX + (pixel % tileWidth), tileY + Math.floor(pixel / tileWidth), value[0], value[1], value[2]);
              pixel++;
            }
          }
        } else throw new Error('rfb_zrle_tile');
        tileX += 64;
      }
      tileY += 64;
    }
  }
}
