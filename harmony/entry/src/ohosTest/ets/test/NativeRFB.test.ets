import { describe, it, expect } from '@ohos/hypium';
import { RFBAuth } from '../../../main/ets/core/RFBAuth';
import { RFBZRLE } from '../../../main/ets/core/RFBZRLE';

function hex(bytes: Uint8Array): string { return Array.from(bytes).map((byte: number): string => byte.toString(16).padStart(2, '0')).join(''); }
export default function nativeRFBTests(): void {
  describe('NativeRFB', (): void => {
    it('ard_credentials_use_the_native_md5_and_aes_ecb_without_padding', 0, async (): Promise<void> => {
      const reply = await RFBAuth.ard(new Uint8Array([5]), new Uint8Array([23]), new Uint8Array([8]),
        'synthetic-user', 'synthetic-password', undefined, new Uint8Array([6]), new Uint8Array(128).fill(170));
      expect(hex(reply.publicKey)).assertEqual('08');
      expect(hex(reply.sealed)).assertEqual('a14b16149b1da36dac529cc3bdcf33179ad934ce50d947549bb01b79928164e29ad934ce50d947549bb01b79928164e29ad934ce50d947549bb01b79928164e25519195228436b02acb9eb9c40779313819e1f6d84a3e8146e8f8ccc8f691cf19ad934ce50d947549bb01b79928164e29ad934ce50d947549bb01b79928164e2');
    });
    it('zrle_uses_one_native_inflater_across_sync_flushed_rectangles', 0, async (): Promise<void> => {
      const inflater = new RFBZRLE();
      try {
        const first = new Uint8Array([120,156,98,20,50,9,99,196,131,1,0,0,0,255,255]);
        const second = new Uint8Array([34,132,1,0,0,0,255,255]);
        const expected = '01123456'.repeat(8);
        expect(hex(await inflater.inflate(first, 64))).assertEqual(expected);
        expect(hex(await inflater.inflate(second, 64))).assertEqual(expected);
      } finally { inflater.close(); }
    });
    it('zrle_rejects_a_rectangle_that_exceeds_its_output_budget', 0, async (): Promise<void> => {
      const inflater = new RFBZRLE(); let failed = false;
      try { await inflater.inflate(new Uint8Array([120,156,98,20,50,9,99,196,131,1,0,0,0,255,255]), 8); }
      catch (_) { failed = true; } finally { inflater.close(); }
      expect(failed).assertTrue();
    });
  });
}
