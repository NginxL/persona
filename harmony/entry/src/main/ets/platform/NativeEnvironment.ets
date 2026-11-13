import type { Configuration } from '@kit.AbilityKit';
import { display } from '@kit.ArkUI';

/** Native application configuration, shared by workspace and detached views. */
@ObservedV2
export class NativeEnvironment {
  @Trace fontScale: number = 1;
  @Trace nativeHoverAvailable: boolean = false;
  private refreshFoldCapability(): void {
    // Some emulator images report foldable but omit the crease region. The native
    // FoldSplitContainer dereferences that region when entering hover mode.
    try {
      const region = display.isFoldable() ? display.getCurrentFoldCreaseRegion() : undefined;
      this.nativeHoverAvailable = !!region && region.creaseRects.length > 0 && region.creaseRects.every((rect: display.Rect): boolean =>
        Number.isFinite(rect.left) && Number.isFinite(rect.top) && Number.isFinite(rect.width) && Number.isFinite(rect.height) && rect.width > 0 && rect.height > 0);
    } catch (_) { this.nativeHoverAvailable = false; }
  }
  update(configuration: Configuration): void {
    this.refreshFoldCapability();
    const scale = configuration.fontSizeScale;
    if (typeof scale === 'number' && Number.isFinite(scale) && scale > 0) this.fontScale = scale;
  }
}
export const nativeEnvironment: NativeEnvironment = new NativeEnvironment();
