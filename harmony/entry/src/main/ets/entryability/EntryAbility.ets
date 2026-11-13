import { isPairingLink } from '../core/WireCompatibility';
import { deviceFeatures } from '../models/DeviceFeatures';
import { AbilityConstant, ConfigurationConstant, UIAbility, Want } from '@kit.AbilityKit';
import type { Configuration } from '@kit.AbilityKit';
import { window } from '@kit.ArkUI';
import { Strings } from '../design/Strings';
import { client } from '../models/ClientModel';
import { nativeNotifications, NOTIFICATION_ACTION } from '../platform/NativeNotifications';
import { Wire } from '../models/Wire';
import { KnowledgeRecording } from '../models/KnowledgeRecording';
import { deviceLook } from '../models/DeviceLook';
import { nativeEnvironment } from '../platform/NativeEnvironment';

export default class EntryAbility extends UIAbility {
  private lifecycle: number = 0;
  private visible: boolean = true;
  private pendingNotification: Record<string, Object> | undefined;
  onCreate(want: Want, launchParam: AbilityConstant.LaunchParam): void {
    nativeEnvironment.update(this.context.config);
    Strings.bind(this.context.resourceManager);
    deviceLook.initialize(this.context).catch(() => { deviceLook.error = 'device_look_storage_failed'; });
    this.receiveWant(want);
    if (want.action === NOTIFICATION_ACTION) this.pendingNotification = Wire.record(want.parameters);
    client.initialize(this.context).then(() => {
      if (!this.visible) return;
      this.context.getApplicationContext().setColorMode(client.appearance === 1 ? ConfigurationConstant.ColorMode.COLOR_MODE_LIGHT :
        client.appearance === 2 ? ConfigurationConstant.ColorMode.COLOR_MODE_DARK : ConfigurationConstant.ColorMode.COLOR_MODE_NOT_SET);
      this.dispatchNotification();
    }).catch(() => { client.error = 'failed'; client.ready = true; });
  }
  private receiveWant(want: Want): void {
    const uri = want.uri || '';
    if (uri.length <= 16384 && isPairingLink(uri)) client.pairingLink = uri;
  }
  onNewWant(want: Want, launchParam: AbilityConstant.LaunchParam): void {
    nativeEnvironment.update(this.context.config);
    this.receiveWant(want);
    if (want.action === NOTIFICATION_ACTION) { this.pendingNotification = Wire.record(want.parameters); this.dispatchNotification(); }
  }
  onConfigurationUpdate(_configuration: Configuration): void {
    nativeEnvironment.update(_configuration);
    if (client.language === 'default') { Strings.refresh(); client.announceLanguage(); }
  }
  private dispatchNotification(): void {
    if (!this.visible || !client.ready || !this.pendingNotification) return;
    const target = this.pendingNotification; this.pendingNotification = undefined;
    client.openNotification(target).catch(() => {});
  }
  onWindowStageCreate(stage: window.WindowStage): void {
    stage.loadContent('pages/Index', (error) => { if (error.code) client.error = 'failed'; });
  }
  onBackground(): void { this.visible = false; this.lifecycle++; deviceLook.bind(undefined).catch(() => {}); Promise.all([KnowledgeRecording.backgroundAll(), deviceFeatures.background(), client.background()]).catch(() => {}); }
  onForeground(): void {
    nativeEnvironment.update(this.context.config);
    const epoch = ++this.lifecycle; this.visible = true; client.activateForeground();
    if (!client.ready) return;
    nativeNotifications.refresh().then(async (): Promise<void> => {
      if (!this.visible || epoch !== this.lifecycle) return;
      await client.foregrounded();
      if (this.visible && epoch === this.lifecycle) this.dispatchNotification();
    }).catch(() => { if (this.visible && epoch === this.lifecycle) client.error = 'failed'; });
    deviceFeatures.foreground().catch(() => {});
  }
  onDestroy(): void { this.visible = false; this.lifecycle++; this.pendingNotification = undefined; deviceLook.bind(undefined).catch(() => {}); KnowledgeRecording.backgroundAll().catch(() => {}); deviceFeatures.background().catch(() => {}); client.shutdown().catch(() => {}); }
}
