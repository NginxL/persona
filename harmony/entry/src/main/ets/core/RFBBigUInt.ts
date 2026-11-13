/** Small unsigned integer for the 4096-bit maximum used by Apple Remote Desktop's RFB authentication. */
export class RFBBigUInt {
  private static readonly limbBits: number = 16;
  private static readonly base: number = 65536;
  private static readonly mask: number = 65535;
  private limbs: number[] = [];

  private constructor(limbs: number[]) {
    this.limbs = limbs.length > 0 ? limbs : [0];
    this.trim();
  }

  static fromBytes(bytes: Uint8Array): RFBBigUInt {
    const limbs: number[] = [];
    let index: number = bytes.length;
    while (index > 0) {
      let limb: number = bytes[index - 1];
      index--;
      if (index > 0) {
        limb += bytes[index - 1] * 256;
        index--;
      }
      limbs.push(limb);
    }
    return new RFBBigUInt(limbs);
  }

  toBytes(count: number): Uint8Array {
    const out = new Uint8Array(count);
    for (let i: number = 0; i < this.limbs.length; i++) {
      const offset: number = count - 1 - i * 2;
      if (offset >= 0) out[offset] = this.limbs[i] & RFBBigUInt.mask;
      if (offset - 1 >= 0) out[offset - 1] = (this.limbs[i] >>> 8) & 255;
    }
    return out;
  }

  bitWidth(): number {
    let top: number = this.limbs.length - 1;
    while (top > 0 && this.limbs[top] === 0) top--;
    let value: number = this.limbs[top];
    let bits: number = 0;
    while (value > 0) { bits++; value = Math.floor(value / 2); }
    return top * RFBBigUInt.limbBits + bits;
  }

  bit(index: number): boolean {
    const limb: number = Math.floor(index / RFBBigUInt.limbBits);
    return limb < this.limbs.length && ((this.limbs[limb] >>> (index % RFBBigUInt.limbBits)) & 1) === 1;
  }

  static modPow(baseValue: RFBBigUInt, exponent: RFBBigUInt, modulus: RFBBigUInt): RFBBigUInt {
    const n: number = modulus.limbs.length;
    if (n === 0 || (modulus.limbs[0] & 1) !== 1) throw new Error('rfb_crypto');
    const mod = RFBBigUInt.fixed(modulus.limbs, n);
    let inverse: number = 1;
    for (let i: number = 0; i < 5; i++) {
      inverse = (inverse * (2 - mod[0] * inverse)) % RFBBigUInt.base;
      if (inverse < 0) inverse += RFBBigUInt.base;
    }
    const mPrime: number = (RFBBigUInt.base - inverse) % RFBBigUInt.base;

    const reduce = (value: RFBBigUInt): number[] => {
      const result: number[] = new Array<number>(n + 1).fill(0);
      let bitIndex: number = value.bitWidth() - 1;
      while (bitIndex >= 0) {
        let carry: number = value.bit(bitIndex) ? 1 : 0;
        for (let limb: number = 0; limb < result.length; limb++) {
          const next: number = Math.floor(result[limb] / 32768);
          result[limb] = ((result[limb] * 2) & RFBBigUInt.mask) + carry;
          carry = next;
        }
        if (RFBBigUInt.compare(result, mod) >= 0) RFBBigUInt.subtract(result, mod);
        bitIndex--;
      }
      return result.slice(0, n);
    };

    const powerOfTwo = (bits: number): number[] => {
      const result: number[] = new Array<number>(n + 1).fill(0);
      result[0] = 1;
      if (RFBBigUInt.compare(result, mod) >= 0) RFBBigUInt.subtract(result, mod);
      for (let bitIndex: number = 0; bitIndex < bits; bitIndex++) {
        let carry: number = 0;
        for (let limb: number = 0; limb < result.length; limb++) {
          const next: number = Math.floor(result[limb] / 32768);
          result[limb] = ((result[limb] * 2) & RFBBigUInt.mask) + carry;
          carry = next;
        }
        if (RFBBigUInt.compare(result, mod) >= 0) RFBBigUInt.subtract(result, mod);
      }
      return result.slice(0, n);
    };

    const multiply = (left: number[], right: number[]): number[] => {
      const temp: number[] = new Array<number>(n + 2).fill(0);
      for (let i: number = 0; i < n; i++) {
        let carry: number = 0;
        const a: number = left[i];
        for (let j: number = 0; j < n; j++) {
          const sum: number = temp[j] + a * right[j] + carry;
          temp[j] = sum & RFBBigUInt.mask;
          carry = Math.floor(sum / RFBBigUInt.base);
        }
        let sum: number = temp[n] + carry;
        temp[n] = sum & RFBBigUInt.mask;
        temp[n + 1] += Math.floor(sum / RFBBigUInt.base);

        const q: number = (temp[0] * mPrime) & RFBBigUInt.mask;
        sum = temp[0] + q * mod[0];
        carry = Math.floor(sum / RFBBigUInt.base);
        for (let j: number = 1; j < n; j++) {
          sum = temp[j] + q * mod[j] + carry;
          temp[j - 1] = sum & RFBBigUInt.mask;
          carry = Math.floor(sum / RFBBigUInt.base);
        }
        sum = temp[n] + carry;
        temp[n - 1] = sum & RFBBigUInt.mask;
        temp[n] = temp[n + 1] + Math.floor(sum / RFBBigUInt.base);
        temp[n + 1] = 0;
      }
      const result: number[] = temp.slice(0, n + 1);
      if (RFBBigUInt.compare(result, mod) >= 0) RFBBigUInt.subtract(result, mod);
      return result.slice(0, n);
    };

    const rSquared: number[] = powerOfTwo(2 * RFBBigUInt.limbBits * n);
    let accumulator: number[] = powerOfTwo(RFBBigUInt.limbBits * n);
    const montgomeryBase: number[] = multiply(reduce(baseValue), rSquared);
    let bitIndex: number = exponent.bitWidth() - 1;
    while (bitIndex >= 0) {
      accumulator = multiply(accumulator, accumulator);
      if (exponent.bit(bitIndex)) accumulator = multiply(accumulator, montgomeryBase);
      bitIndex--;
    }
    const one: number[] = new Array<number>(n).fill(0);
    one[0] = 1;
    return new RFBBigUInt(multiply(accumulator, one));
  }

  private static fixed(value: number[], length: number): number[] {
    const result: number[] = new Array<number>(length).fill(0);
    for (let index: number = 0; index < length && index < value.length; index++) result[index] = value[index];
    return result;
  }

  private static compare(left: number[], right: number[]): number {
    let index: number = Math.max(left.length, right.length) - 1;
    while (index >= 0) {
      const a: number = index < left.length ? left[index] : 0;
      const b: number = index < right.length ? right[index] : 0;
      if (a !== b) return a > b ? 1 : -1;
      index--;
    }
    return 0;
  }

  private static subtract(left: number[], right: number[]): void {
    let borrow: number = 0;
    for (let index: number = 0; index < left.length; index++) {
      let value: number = left[index] - (index < right.length ? right[index] : 0) - borrow;
      borrow = value < 0 ? 1 : 0;
      if (value < 0) value += RFBBigUInt.base;
      left[index] = value;
    }
  }

  private trim(): void {
    while (this.limbs.length > 1 && this.limbs[this.limbs.length - 1] === 0) this.limbs.pop();
  }
}
