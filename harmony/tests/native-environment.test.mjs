import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

const screen = { foldable: false, region: undefined, failure: false };
globalThis.__personaNativeEnvironment = screen;
registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkUI') return { url: 'native-environment:display', shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'native-environment:display') return { format: 'module', shortCircuit: true, source: `
      const s = globalThis.__personaNativeEnvironment;
      export const display = { isFoldable: () => s.foldable, getCurrentFoldCreaseRegion() {
        if (s.failure) throw new Error('display unavailable'); return s.region;
      } };
    ` };
    if (url.endsWith('/NativeEnvironment.ets')) return { format: 'module', shortCircuit: true,
      source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '')) };
    return next(url, context);
  }
});
const { NativeEnvironment } = await import('../entry/src/main/ets/platform/NativeEnvironment.ets');

test('ordinary native layout survives absent, invalid or unavailable fold geometry', () => {
  const environment = new NativeEnvironment();
  screen.foldable = true;
  for (const region of [undefined, { creaseRects: [] }, { creaseRects: [{ left: 0, top: 100, width: 0, height: 10 }] },
    { creaseRects: [{ left: 0, top: NaN, width: 100, height: 10 }] }]) {
    screen.region = region; environment.update({}); assert.equal(environment.nativeHoverAvailable, false);
  }
  screen.region = { creaseRects: [{ left: 0, top: 100, width: 100, height: 10 }] };
  environment.update({ fontSizeScale: 3.2 }); assert.equal(environment.nativeHoverAvailable, true);
  environment.update({}); assert.equal(environment.fontScale, 3.2);
  screen.failure = true; environment.update({}); assert.equal(environment.nativeHoverAvailable, false);
  screen.failure = false; screen.foldable = false; environment.update({}); assert.equal(environment.nativeHoverAvailable, false);
});
