import { cryptoFramework } from '@kit.CryptoArchitectureKit';
import { util } from '@kit.ArkTS';
import { RFBBigUInt } from './RFBBigUInt';

export class RFBCredentials {
  account: string = '';
  password: string = '';
  constructor(account: string = '', password: string = '') { this.account = account; this.password = password; }
}

export class RFBARDReply {
  sealed: Uint8Array = new Uint8Array(0);
  publicKey: Uint8Array = new Uint8Array(0);
  constructor(sealed: Uint8Array, publicKey: Uint8Array) { this.sealed = sealed; this.publicKey = publicKey; }
}

export interface RFBAuthCrypto {
  random(length: number): Promise<Uint8Array>;
  md5(bytes: Uint8Array): Promise<Uint8Array>;
  aesEcb(key: Uint8Array, bytes: Uint8Array): Promise<Uint8Array>;
}

class RFBHarmonyCrypto implements RFBAuthCrypto {
  async random(length: number): Promise<Uint8Array> {
    const result = await cryptoFramework.createRandom().generateRandom(length);
    return result.data.slice();
  }

  async md5(bytes: Uint8Array): Promise<Uint8Array> {
    const digest = cryptoFramework.createMd('MD5');
    await digest.update({ data: bytes });
    return (await digest.digest()).data.slice();
  }

  async aesEcb(keyBytes: Uint8Array, bytes: Uint8Array): Promise<Uint8Array> {
    const generator = cryptoFramework.createSymKeyGenerator('AES128');
    const key = await generator.convertKey({ data: keyBytes });
    try {
      const cipher = cryptoFramework.createCipher('AES128|ECB|NoPadding');
      await cipher.init(cryptoFramework.CryptoMode.ENCRYPT_MODE, key, null);
      return (await cipher.doFinal({ data: bytes })).data.slice();
    } finally { key.clearMem(); }
  }
}

/** Security types and credential encodings used by the Apple RFB client. */
export class RFBAuth {
  static readonly none: number = 1;
  static readonly veNCrypt: number = 19;
  static readonly securityARD: number = 30;
  static readonly plain: number = 256;
  static readonly maxPrimeBytes: number = 512;
  static readonly preferred: number[] = [RFBAuth.securityARD, RFBAuth.veNCrypt, RFBAuth.none];

  static be32(value: number): Uint8Array {
    return new Uint8Array([(value >>> 24) & 255, (value >>> 16) & 255, (value >>> 8) & 255, value & 255]);
  }

  static plainCredentials(account: string, password: string): Uint8Array {
    const encoder = new util.TextEncoder();
    const user: Uint8Array = encoder.encodeInto(account);
    const pass: Uint8Array = encoder.encodeInto(password);
    const out = new Uint8Array(8 + user.length + pass.length);
    out.set(RFBAuth.be32(user.length), 0);
    out.set(RFBAuth.be32(pass.length), 4);
    out.set(user, 8);
    out.set(pass, 8 + user.length);
    user.fill(0);
    pass.fill(0);
    return out;
  }

  static async ard(generator: Uint8Array, prime: Uint8Array, serverKey: Uint8Array, account: string, password: string,
    crypto: RFBAuthCrypto = new RFBHarmonyCrypto(), privateKey?: Uint8Array, fill?: Uint8Array): Promise<RFBARDReply> {
    if (prime.length < 1 || prime.length > RFBAuth.maxPrimeBytes || (prime[prime.length - 1] & 1) !== 1 ||
      generator.length < 1 || generator.length > 4 || serverKey.length !== prime.length ||
      privateKey !== undefined && privateKey.length !== prime.length || fill !== undefined && fill.length !== 128)
      throw new Error('rfb_crypto');
    const exponent: Uint8Array = privateKey === undefined ? await crypto.random(prime.length) : privateKey.slice();
    let shared: Uint8Array = new Uint8Array(0);
    let key: Uint8Array = new Uint8Array(0);
    let block: Uint8Array = new Uint8Array(0);
    let user: Uint8Array = new Uint8Array(0);
    let pass: Uint8Array = new Uint8Array(0);
    try {
      if (exponent.length !== prime.length) throw new Error('rfb_crypto');
      const modulus: RFBBigUInt = RFBBigUInt.fromBytes(prime);
      const publicKey: Uint8Array = RFBBigUInt.modPow(RFBBigUInt.fromBytes(generator), RFBBigUInt.fromBytes(exponent), modulus).toBytes(prime.length);
      shared = RFBBigUInt.modPow(RFBBigUInt.fromBytes(serverKey), RFBBigUInt.fromBytes(exponent), modulus).toBytes(prime.length);
      key = await crypto.md5(shared);
      if (key.length !== 16) throw new Error('rfb_crypto');
      block = fill === undefined ? await crypto.random(128) : fill.slice();
      if (block.length !== 128) throw new Error('rfb_crypto');
      const encoder = new util.TextEncoder();
      user = encoder.encodeInto(account).slice(0, 63);
      pass = encoder.encodeInto(password).slice(0, 63);
      block.set(user, 0);
      block[user.length] = 0;
      block.set(pass, 64);
      block[64 + pass.length] = 0;
      const sealed: Uint8Array = await crypto.aesEcb(key, block);
      if (sealed.length !== 128) throw new Error('rfb_crypto');
      return new RFBARDReply(sealed, publicKey);
    } finally {
      exponent.fill(0); shared.fill(0); key.fill(0); block.fill(0); user.fill(0); pass.fill(0);
    }
  }
}
