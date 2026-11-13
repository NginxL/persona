import { describe, it, expect } from '@ohos/hypium';
import { abilityDelegatorRegistry } from '@kit.TestKit';
import { nativeTestContext } from './NativeContext';
import { fixtureArgument } from './NativeTransport.test';
import { display } from '@kit.ArkUI';

function logDisplay(): void {
  const screen = display.getDefaultDisplaySync();
  console.info('PersonaNativeDisplay ' + JSON.stringify({ fold: display.getFoldStatus(), orientation: screen.orientation,
    rotation: screen.rotation, width: screen.width, height: screen.height, density: screen.densityPixels,
    crease: display.isFoldable() ? display.getCurrentFoldCreaseRegion() : undefined }));
}

/** Optional device-layout probe: changes only this test application's font configuration. */
export default function nativeFontTests(): void {
  describe('NativeFont', (): void => {
    it('requested_native_font_scale_is_applied_before_the_layout_inspection', 0, async (): Promise<void> => {
      const context = nativeTestContext(), application = context.getApplicationContext();
      const scale = Number(fixtureArgument('fontScale'));
      expect(scale === 2 || scale === 3.2).assertTrue();
      const holdArgument = fixtureArgument('fontHoldSeconds');
      const holdSeconds = holdArgument ? Number(holdArgument) : 45;
      expect(Number.isInteger(holdSeconds) && holdSeconds >= 15 && holdSeconds <= 120).assertTrue();
      const original = context.config.fontSizeScale || 1;
      const foldChanged = (_status: display.FoldStatus): void => logDisplay();
      const displayChanged = (_id: number): void => logDisplay();
      try {
        application.setFontSizeScale(scale);
        for (let attempt = 0; attempt < 50 && Math.abs((context.config.fontSizeScale || 1) - scale) > 0.01; attempt++) {
          await new Promise<void>((resolve) => setTimeout(resolve, 100));
        }
        expect(Math.abs((context.config.fontSizeScale || 1) - scale) < 0.01).assertTrue();
        console.info('PersonaNativeFont actualScale=' + String(context.config.fontSizeScale));
        await abilityDelegatorRegistry.getAbilityDelegator().startAbility({
          bundleName: abilityDelegatorRegistry.getArguments().bundleName, moduleName: 'entry', abilityName: 'EntryAbility'
        });
        logDisplay(); display.on('foldStatusChange', foldChanged); display.on('change', displayChanged);
        // The owning test driver captures and inspects the running application during this interval.
        await new Promise<void>((resolve) => setTimeout(resolve, holdSeconds * 1000));
      } finally {
        display.off('foldStatusChange', foldChanged); display.off('change', displayChanged);
        application.setFontSizeScale(original);
      }
    });
  });
}
