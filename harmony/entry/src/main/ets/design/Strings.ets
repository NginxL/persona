import { resourceManager } from '@kit.LocalizationKit';

@ObservedV2
class LanguageRevision { @Trace value: number = 0; }

export class Strings {
  private static revision: LanguageRevision = new LanguageRevision();
  static refresh(): void { Strings.revision.value++; }
  private static resources: resourceManager.ResourceManager | undefined;
  static bind(resources: resourceManager.ResourceManager): void { Strings.resources = resources; }
  static get(key: string): string {
    // Register translations with ArkUI's observation graph when the application language changes.
    const revision = Strings.revision.value;
    if (!Strings.resources) return key;
    try { return Strings.resources.getStringByNameSync(key); } catch (_) { return key; }
  }
}
export function tr(key: string): string { return Strings.get(key); }
