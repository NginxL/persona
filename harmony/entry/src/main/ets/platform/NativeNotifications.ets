import { notificationManager } from '@kit.NotificationKit';
import { bundleManager, common, wantAgent } from '@kit.AbilityKit';
import type { ClientModel } from '../models/ClientModel';
import { Wire } from '../models/Wire';
import { tr } from '../design/Strings';
import { isHubStateKey } from '../core/StorageOwnership';

export const NOTIFICATION_ACTION: string = 'persona.notification.OPEN';

@ObservedV2
export class NativeNotifications {
  @Trace granted: boolean = false;
  @Trace changing: boolean = false;
  @Trace error: string = '';
  private context: common.UIAbilityContext | undefined;
  private seen: string[] = [];
  private inFlight: Set<string> = new Set();
  private sequence: number = Math.floor(Date.now() % 2147483646);
  private epoch: number = 0;
  private hubEpochs: Map<string, number> = new Map();
  private published: Map<number, string> = new Map();

  async initialize(context: common.UIAbilityContext): Promise<void> { this.context = context; await this.refresh(); }
  async refresh(): Promise<void> {
    try { this.granted = await notificationManager.isNotificationEnabled(); }
    catch (_) { this.granted = false; }
  }
  features(enabled: boolean): string[] {
    const features: string[] = ['managers', 'extension_operations', 'extension_ui_stream_v1', 'elicitation', 'file_transfer'];
    if (enabled && this.granted) features.push('ws_notify');
    return features;
  }
  async setEnabled(model: ClientModel, enabled: boolean): Promise<void> {
    if (this.changing) return;
    this.changing = true; this.error = '';
    try {
      if (enabled) {
        if (!this.context) throw new Error('notifications_unavailable');
        await notificationManager.requestEnableNotification(this.context);
        // A successful request is not evidence of a grant; the native API requires this second query.
        await this.refresh();
        if (!this.granted) { this.error = 'notifications_denied'; return; }
      }
      await model.setAlerts(enabled);
      if (model.connected) model.repo().transport.hello(this.features(enabled));
      if (!enabled) await this.clear();
    } catch (_) { this.error = 'notifications_unavailable'; }
    finally { this.changing = false; }
  }
  private identity(model: ClientModel, data: Record<string, Object>): string {
    return JSON.stringify([model.scope(), Wire.text(data, 'kind'), Wire.text(data, 'interaction_id'), Wire.text(data, 'id'),
      Wire.text(data, 'machine_id'), Wire.text(data, 'session_id'), Wire.text(data, 'title'), Wire.text(data, 'body')]);
  }
  private async cancel(id: number): Promise<void> {
    try { await notificationManager.cancel(id); }
    catch (error) { if (Wire.number(Wire.record(error as Object), 'code') !== 1600007) throw new Error('notifications_unavailable'); }
  }
  async receive(model: ClientModel, data: Record<string, Object>): Promise<void> {
    if (!model.alerts || !this.granted || model.demo || !model.profile.id || Wire.text(data, 'type') !== 'notify') return;
    const owner = Wire.text(data, 'manager');
    if (owner && owner !== model.profile.manager) return;
    const machine = Wire.text(data, 'machine_id'), session = Wire.text(data, 'session_id');
    const row = model.sessions.concat(model.conversations).find((item) => item.machine === machine && (item.session || item.id) === session);
    if (row && Wire.bool(row.data(), 'quiet')) return;
    const key = this.identity(model, data); if (this.seen.includes(key) || this.inFlight.has(key)) return;
    this.inFlight.add(key);
    const generation = model.connectionGeneration(), scope = model.scope(), profile = model.profile.id, manager = model.profile.manager;
    const id = this.sequence = this.sequence % 2147483646 + 1, epoch = this.epoch, hubEpoch = this.hubEpochs.get(profile) || 0;
    try {
      const info = bundleManager.getBundleInfoForSelfSync(bundleManager.BundleFlag.GET_BUNDLE_INFO_DEFAULT);
      // Only the authenticated catalog can establish a notification's navigable owner.
      const target = Wire.payload(['profile', 'manager', 'machine', 'session'], [profile, manager, row ? machine : '', row ? session : '']);
      const agent = await wantAgent.getWantAgent({ wants: [{ bundleName: info.name, abilityName: 'EntryAbility', action: NOTIFICATION_ACTION, parameters: target }],
        actionType: wantAgent.OperationType.START_ABILITY, requestCode: id, actionFlags: [wantAgent.WantAgentFlags.UPDATE_PRESENT_FLAG] });
      if (epoch !== this.epoch || hubEpoch !== (this.hubEpochs.get(profile) || 0) || !model.owns(generation, scope) || !model.alerts || !this.granted) return;
      const request: notificationManager.NotificationRequest = {
        id: id, content: { notificationContentType: notificationManager.ContentType.NOTIFICATION_CONTENT_BASIC_TEXT,
          normal: { title: Wire.text(data, 'title', tr('persona')).slice(0, 256), text: Wire.text(data, 'body', Wire.text(data, 'subtitle')).slice(0, 2048) } },
        notificationSlotType: notificationManager.SlotType.SOCIAL_COMMUNICATION,
        isAlertOnce: true, tapDismissed: true, wantAgent: agent, extraInfo: Wire.payload(['persona_hub'], [profile])
      };
      // API 23+ allows reducing reminders per notification without changing the user's channel settings.
      if (Wire.bool(data, 'quiet')) request.notificationFlags = { soundEnabled: 2, vibrationEnabled: 2, bannerEnabled: 2 };
      this.published.set(id, profile);
      for (const previous of this.published.keys()) { if (this.published.size <= 128) break; this.published.delete(previous); }
      await notificationManager.publish(request);
      if (epoch !== this.epoch || hubEpoch !== (this.hubEpochs.get(profile) || 0) || !model.owns(generation, scope) || !model.alerts || !this.granted) {
        await this.cancel(id); this.published.delete(id); return;
      }
      this.seen = this.seen.concat([key]).slice(-128);
    } catch (_) { if (epoch === this.epoch && model.owns(generation, scope)) this.error = 'notifications_unavailable'; }
    finally { this.inFlight.delete(key); }
  }
  async clear(hub: string = ''): Promise<void> {
    if (!hub) { this.epoch++; this.seen = []; this.inFlight.clear(); this.published.clear(); await notificationManager.cancelAll(); return; }
    this.hubEpochs.set(hub, (this.hubEpochs.get(hub) || 0) + 1);
    this.seen = this.seen.filter((key: string) => !isHubStateKey('draft:' + key, hub));
    for (const key of this.inFlight) if (isHubStateKey('draft:' + key, hub)) this.inFlight.delete(key);
    const ids = new Set<number>();
    for (const id of this.published.keys()) if (this.published.get(id) === hub) ids.add(id);
    let failed = false;
    try {
      for (const card of await notificationManager.getActiveNotifications()) if (Wire.text(Wire.record(card.extraInfo), 'persona_hub') === hub && card.id !== undefined) ids.add(card.id);
    } catch (_) { failed = true; }
    for (const id of ids) {
      try { await this.cancel(id); this.published.delete(id); } catch (_) { failed = true; }
    }
    if (failed) throw new Error('notifications_unavailable');
  }
}
export const nativeNotifications: NativeNotifications = new NativeNotifications();
