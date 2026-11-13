import { common } from '@kit.AbilityKit';
let context: common.UIAbilityContext | undefined;
export function bindNativeTestContext(value: common.UIAbilityContext): void { context = value; }
export function nativeTestContext(): common.UIAbilityContext {
  if (!context) throw new Error('test_context_unavailable');
  return context;
}
