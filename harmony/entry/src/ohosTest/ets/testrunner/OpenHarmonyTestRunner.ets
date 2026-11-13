import { abilityDelegatorRegistry, TestRunner } from '@kit.TestKit';

export default class OpenHarmonyTestRunner implements TestRunner {
  onPrepare(): void {}
  onRun(): void {
    const delegator = abilityDelegatorRegistry.getAbilityDelegator();
    delegator.startAbility({ bundleName: abilityDelegatorRegistry.getArguments().bundleName, moduleName: 'entry_test', abilityName: 'TestAbility' })
      .catch(() => delegator.finishTest('Unable to start native test context', -1, () => {}));
  }
}
