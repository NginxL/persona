import { ComposeListItemV2, ContentItemV2 } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { ManagementModel, ManagementRow, ManagementInput } from '../models/ManagementModel';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { nativeNotifications, NativeNotifications } from '../platform/NativeNotifications';
import { listScheduleTemplates } from '../core/ScheduleTemplates';
import type { ScheduleTemplate } from '../core/ScheduleTemplates';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { DeviceLookPicker } from './DeviceLookPicker';
import { LicenseNotices } from './LicenseNotices';
import { common } from '@kit.AbilityKit';

@ComponentV2
export struct SettingsPage {
  @Require @Param model: ClientModel;
  @Param page: string = '';
  @Param active: boolean = true;
  @Local machineOwner: ClientModel | undefined;
  @Local machineDirectoryKey: string = '';
  private machineRevision: number = 0;
  private pairingOwnerScope: string = '';
  private mounted: boolean = false;
  @Event onOpen: (page: string) => void = () => {};
  @Event onDetail: (page: string, row: ResourceRow) => void = () => {};
  @Event onExtension: (extensionId: string, uiId: string, title: string) => void = () => {};
  @Event onBack: () => void = () => {};
  @Event onComputer: (row: ResourceRow) => void = () => {};
  @Event onChat: (row: ResourceRow) => void = () => {};
  @Local management: ManagementModel = new ManagementModel();
  @Local extensionConfigFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local extensionSecretFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local authFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local connectorFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local channelFieldsBindings: BoundRow<ManagementInput>[] = [];
  private inputBindingScope: string = '';
  private inputBindingOwner: Object | undefined;
  @Local channelFeatureBindings: BoundRow<Record<string, Object>>[] = [];
  private channelFeatureScope: string = '';
  @Local managementRowBindings: BoundRow<ManagementRow>[] = [];
  @Local scheduleRunBindings: BoundRow<ManagementRow>[] = [];
  private managementBindingOwner: Object | undefined;
  private managementBindingScope: string = '';
  @Local catalogRowBindings: BoundRow<ManagementRow>[] = [];
  private catalogBindingOwner: Object | undefined;
  private catalogBindingScope: string = '';
  @Local notifications: NativeNotifications = nativeNotifications;
  @Local addComputerOpen: boolean = false;
  @Local computerName: string = '';
  @Local computerOs: string = 'mac';
  @Local purposeIndex: number = 0;
  @Local permissionMode: string = '';
  @Local effortMode: string = '';
  @Local scheduleEditOpen: boolean = false;
  @Local extensionPreviewOpen: boolean = false;
  @Local licenseNoticesOpen: boolean = false;
  @Local channelTargetDraft: string = '';
  @Local look: DeviceLook = deviceLook;

  aboutToAppear(): void { this.mounted = true; this.syncCatalogRows(); this.syncManagementRows(); this.syncInputRows(); this.loadSettingsPage(); this.loadLook(); }
  private async loadLook(): Promise<void> {
    if (!this.mounted || !this.active || this.page !== 'appearance') return;
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration(), pairing = owner.pairingRevision();
    const context = this.getUIContext().getHostContext(); if (!context) return;
    try { await this.look.initialize(context as common.UIAbilityContext); }
    catch (_) { this.look.error = 'device_look_storage_failed'; return; }
    if (!this.mounted || !this.active || this.page !== 'appearance' || this.model !== owner ||
      !owner.owns(generation, scope) || pairing !== owner.pairingRevision()) return;
    await this.look.bind(owner);
  }
  private basePage(): string { return this.page === 'extension_detail' || this.page === 'extension_catalog' ? 'extensions' : this.page === 'connector_detail' ? 'connectors' : this.page === 'channel_detail' ? 'channels' : this.page; }
  private async loadSettingsPage(): Promise<void> {
    if (!this.mounted || !this.active) return;
    if (this.page === 'machines') { await this.loadMachineDirectory(); return; }
    const page = this.page, owner = this.model, generation = owner.connectionGeneration(), scope = owner.scope(), revision = ++this.machineRevision, target = owner.selected;
    const current = (): boolean => this.mounted && this.active && this.page === page && this.model === owner && revision === this.machineRevision && owner.owns(generation, scope);
    this.management.setAdmission(current);
    await this.management.load(this.basePage(), owner); if (!current()) return;
    const row = new ManagementRow(target.id, target.title, target.detail, target.data());
    if (page === 'extension_detail') await this.management.openExtension(owner, row);
    else if (page === 'connector_detail') this.management.openConnector(row);
    else if (page === 'channel_detail') await this.management.openChannel(owner, row);
    else if (page === 'extension_catalog') await this.management.loadExtensionCatalog(owner);
    if (!current()) return;
    this.purposeIndex = Math.max(0, this.management.catalogPurposes.findIndex((item: Record<string, Object>) => Wire.text(item, 'id') === this.management.selectedId));
    this.permissionMode = Wire.text(owner.selected.data(), 'permission_mode'); this.effortMode = Wire.text(owner.selected.data(), 'effort');
  }
  private openDetail(page: string, item?: ManagementRow): void {
    if (!this.mounted || !this.active) return;
    const row = new ResourceRow(item?.id || 'catalog', item?.title || tr('extension_market'), 'settings_detail');
    row.group = this.model.profile.id; row.manager = this.model.profile.manager; row.detail = item?.detail || ''; row.raw = JSON.stringify(item?.raw || Wire.payload([], [])); this.onDetail(page, row);
  }

  aboutToDisappear(): void { this.mounted = false; this.machineRevision++; if (this.page === 'appearance') this.look.bind(undefined); }
  @Monitor('management.extensionDetailMissing') extensionRemoved(): void { if (this.mounted && this.active && this.page === 'extension_detail' && this.management.extensionDetailMissing) this.onBack(); }
  @Monitor('active') machineVisibilityChanged(): void { if (!this.active) { this.machineRevision++; if (this.page === 'appearance') this.look.bind(undefined); } else if (this.mounted) { this.loadSettingsPage(); this.loadLook(); } }
  @Monitor('model.connected') settingsConnectionChanged(): void { if (this.mounted && this.active) { if (this.model.connected) this.loadSettingsPage(); this.loadLook(); } }
  @Monitor('model.profile', 'model.forgottenEpoch') scheduleOwnerChanged(): void { this.syncCatalogRows(); this.syncManagementRows(); this.management.scheduleReceiptChat(this.model); this.loadLook(); }
  @Monitor('management.catalogRows') catalogRowsChanged(): void { this.syncCatalogRows(); }

  @Monitor('management.rows', 'management.scheduleRuns', 'management.channelDetail', 'management.selectedId', 'page', 'model.profile', 'machineOwner')
  managementRowsChanged(): void { this.syncManagementRows(); }

  @Monitor('management.extensionConfigFields', 'management.extensionSecretFields', 'management.authFields', 'management.connectorFields', 'management.channelFields', 'management.selectedId', 'page', 'model.profile')
  inputRowsChanged(): void { this.syncInputRows(); }

  private syncInputRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, this.page, this.management.selectedId]);
    this.extensionConfigFieldsBindings = reconcileBoundRows(this.extensionConfigFieldsBindings, this.management.extensionConfigFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.extensionSecretFieldsBindings = reconcileBoundRows(this.extensionSecretFieldsBindings, this.management.extensionSecretFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.authFieldsBindings = reconcileBoundRows(this.authFieldsBindings, this.management.authFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.connectorFieldsBindings = reconcileBoundRows(this.connectorFieldsBindings, this.management.connectorFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.channelFieldsBindings = reconcileBoundRows(this.channelFieldsBindings, this.management.channelFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.inputBindingOwner = this.model; this.inputBindingScope = scope;
  }

  private syncManagementRows(): void {
    const owner = this.page === 'machines' ? this.machineOwner || this.model : this.model;
    const scope = JSON.stringify([owner.scope(), owner.profile.id, owner.profile.manager, this.page]);
    this.managementRowBindings = reconcileBoundRows(this.managementRowBindings, this.management.rows,
      (row: ManagementRow) => row.id, this.managementBindingOwner, owner, this.managementBindingScope, scope);
    this.scheduleRunBindings = reconcileBoundRows(this.scheduleRunBindings, this.management.scheduleRuns,
      (row: ManagementRow) => row.id, this.managementBindingOwner, owner, this.managementBindingScope, scope);
    const featureScope = JSON.stringify([scope, this.management.selectedId]);
    this.channelFeatureBindings = reconcileBoundRows(this.channelFeatureBindings, Wire.rows(this.management.channelData(), 'features'),
      (feature: Record<string, Object>) => Wire.text(feature, 'id'), this.managementBindingOwner, owner,
      this.channelFeatureScope, featureScope);
    this.channelFeatureScope = featureScope;
    this.managementBindingOwner = owner; this.managementBindingScope = scope;
  }

  private syncCatalogRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, 'extension_catalog']);
    this.catalogRowBindings = reconcileBoundRows(this.catalogRowBindings, this.management.catalogRows,
      (row: ManagementRow) => Wire.text(row.raw, 'market') + '.' + row.id, this.catalogBindingOwner, this.model,
      this.catalogBindingScope, scope);
    this.catalogBindingOwner = this.model; this.catalogBindingScope = scope;
  }
  private machineOptions(): ResourceRow[] { return this.model.directoryReady ? this.model.directoryMachines : this.model.machines; }
  private machineLabel(row: ResourceRow): string { return [this.model.groupName(row), row.manager, row.title || row.id].filter((value: string) => !!value).join(' · '); }
  private async loadMachineDirectory(): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), revision = ++this.machineRevision;
    await this.model.refreshDirectory();
    if (!this.mounted || !this.active || revision !== this.machineRevision || !this.model.owns(generation, scope)) return;
    const selected = this.machineOptions().find((row: ResourceRow) => row.key() === this.machineDirectoryKey || row.key() === this.model.selected.key()) || this.machineOptions()[0];
    if (selected) await this.chooseMachine(selected);
    else { this.machineOwner = this.model; this.machineDirectoryKey = ''; this.management.setAdmission((): boolean => this.mounted && this.active && this.machineOwner === this.model); await this.management.load('machines', this.model); }
  }
  private async chooseMachine(row: ResourceRow): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), revision = ++this.machineRevision;
    if (this.machineDirectoryKey !== row.key()) { this.management.pairing = {}; this.pairingOwnerScope = ''; this.computerName = ''; this.computerOs = 'mac'; this.addComputerOpen = false; }
    this.machineDirectoryKey = row.key(); this.machineOwner = undefined; this.management.rows = []; this.management.selectedId = ''; this.management.error = ''; this.management.loading = true;
    this.management.setAdmission((): boolean => false);
    try {
      const owner = await this.model.viewFor(row);
      if (!this.mounted || !this.active || revision !== this.machineRevision || !this.model.owns(generation, scope)) return;
      this.machineOwner = owner;
      this.management.setAdmission((): boolean => this.mounted && this.active && revision === this.machineRevision && this.machineOwner === owner && this.machineDirectoryKey === row.key() && this.model.owns(generation, scope));
      await this.management.load('machines', owner);
      if (!this.mounted || !this.active || revision !== this.machineRevision || !this.model.owns(generation, scope)) return;
      this.management.selectedId = row.id;
    } catch (_) { if (this.mounted && this.active && revision === this.machineRevision && this.model.owns(generation, scope)) this.management.error = 'offline'; }
    finally { if (revision === this.machineRevision) this.management.loading = false; }
  }

  private purposeLabel(item: Record<string, Object>): string { return Wire.text(item, 'name', Wire.text(item, 'id')); }
  private selectOptions(values: string[]): SelectOption[] {
    return values.map((value: string): SelectOption => {
      const option: SelectOption = { value: value };
      return option;
    });
  }
  private providerOptions(): SelectOption[] {
    const values: SelectOption[] = [];
    const inherited: SelectOption = { value: tr('automatic') }; values.push(inherited);
    for (const provider of this.purposeProviders()) {
      const option: SelectOption = { value: Wire.text(provider, 'name', Wire.text(provider, 'id')) };
      values.push(option);
    }
    return values;
  }
  private modeOptionsFor(field: string): string[] {
    return field === 'permission_mode' ? this.modeOptions() : Wire.strings(this.model.selected.data(), 'efforts');
  }
  private purposeProviders(): Record<string, Object>[] {
    const purpose = this.management.purpose, kind = Wire.text(purpose, 'kind', 'launcher');
    const ids = Wire.strings(purpose, 'provider_ids');
    return this.management.providers.filter((item: Record<string, Object>) =>
      ids.length ? ids.includes(Wire.text(item, 'id')) : Wire.text(item, 'kind', 'launcher') === kind);
  }
  private providerIndex(): number {
    const id = this.management.purposeDraft.provider;
    if (!id) return 0;
    const index = this.purposeProviders().findIndex((item: Record<string, Object>) => Wire.text(item, 'id') === id);
    return index < 0 ? 0 : index + 1;
  }
  private modelIndex(): number {
    const index = this.management.providerModels.findIndex((item: Record<string, Object>) =>
      Wire.text(item, 'id') === this.management.purposeDraft.model);
    return Math.max(0, index);
  }
  private effortOptions(): string[] {
    const model = this.management.providerModels.find((item: Record<string, Object>) =>
      Wire.text(item, 'id') === this.management.purposeDraft.model);
    return Wire.strings(model || {}, 'efforts');
  }
  private modelProvider(): Record<string, Object> {
    const id = this.management.purposeDraft.provider || Wire.text(Wire.object(this.management.purpose, 'effective'), 'provider');
    return this.management.providers.find((item: Record<string, Object>) => Wire.text(item, 'id') === id) || {};
  }
  private fieldSupported(key: string): boolean {
    const fields = this.management.purpose['fields'];
    return Array.isArray(fields) ? (fields as Object[]).includes(key) : key === 'provider' || key === 'model';
  }
  private purposeChanged(): boolean {
    return this.management.purposeChanged();
  }
  private modeOptions(): string[] {
    const values = ['default', 'acceptEdits', 'plan', 'auto'];
    if (Wire.bool(this.model.selected.data(), 'bypass_available')) values.push('bypassPermissions');
    return values;
  }
  private modeSupported(): boolean {
    const capabilities = Wire.object(this.model.selected.data(), 'capabilities');
    return this.model.selected.kind === 'session' && Wire.bool(capabilities, 'modes');
  }
  private machineRow(): ManagementRow | undefined {
    return this.management.rows.find((item: ManagementRow) => item.id === this.management.selectedId);
  }
  private screenMachine(): ResourceRow | undefined {
    const selected = this.machineRow();
    if (!selected || !this.machineOwner) return undefined;
    return this.machineOwner.machines.find((row: ResourceRow) => row.id === selected.id && row.key() === this.machineDirectoryKey);
  }
  private canOpenScreen(): boolean {
    const row = this.screenMachine(); if (!this.mounted || !this.active || !row || !this.machineOwner?.connected || this.model.demo || this.management.loading) return false;
    const data = row.data(), os = Wire.text(data, 'os', Wire.text(Wire.object(data, 'system'), 'os'));
    return ['', 'mac', 'darwin'].includes(os) && Wire.bool(data, 'online') && !Wire.bool(data, 'pending') && !Wire.bool(data, 'pending_confirm') && ['', 'confirmed', 'personal', 'work'].includes(Wire.text(data, 'trust'));
  }
  private rowEnabled(row: ManagementRow): boolean { return Wire.bool(row.raw, 'enabled'); }
  private canPairComputer(): boolean { return this.mounted && this.active && !!this.machineOwner?.connected && !this.management.loading && (!this.machineDirectoryKey || !!this.screenMachine()); }
  private async createComputerPairing(): Promise<void> {
    const owner = this.machineOwner; if (!owner || !this.canPairComputer()) return;
    const generation = owner.connectionGeneration(), scope = owner.scope(), revision = this.machineRevision;
    const ok = await this.management.createPairing(owner, this.computerName, this.computerOs);
    if (ok && this.mounted && this.active && this.machineOwner === owner && revision === this.machineRevision && owner.owns(generation, scope)) { const selected = owner.machines.find((row: ResourceRow) => row.key() === this.machineDirectoryKey); if (selected) this.management.selectedId = selected.id; this.pairingOwnerScope = scope; this.computerName = ''; this.addComputerOpen = false; }
  }
  private canRenew(): boolean {
    return this.canPairComputer() && this.pairingOwnerScope === this.machineOwner?.scope() && this.management.canRenewPairing();
  }
  private async saveTriage(key: string, value: boolean): Promise<void> {
    const next: Record<string, Object> = Wire.payload([], []);
    next['enabled'] = Wire.bool(this.management.data, 'enabled');
    next['auto_continue'] = Wire.bool(this.management.data, 'auto_continue');
    next['wake'] = Wire.bool(this.management.data, 'wake');
    next['decide'] = Wire.bool(this.management.data, 'decide');
    next['reply_reminder'] = Wire.bool(this.management.data, 'reply_reminder', true);
    if (!Object.keys(this.management.data).includes(key)) return;
    next[key] = value;
    await this.management.setTriage(this.model, next);
  }
  private confirmMachine(row: ManagementRow, trust: boolean): void {
    if (!this.machineOwner || !this.screenMachine() || this.screenMachine()?.id !== row.id || this.management.loading) return;
    const owner = this.machineOwner, scope = owner.scope(), generation = owner.connectionGeneration(), selected = this.machineDirectoryKey;
    this.getUIContext().showAlertDialog({ title: tr(trust ? 'confirm_trust' : 'reject'),
      message: row.title + '\n' + row.id,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr(trust ? 'confirm_computer' : 'reject'), action: () => {
        if (this.mounted && this.active && selected === this.machineDirectoryKey && owner.owns(generation, scope)) this.management.trustMachine(owner, row, trust);
      } } });
  }
  private confirmForget(): void {
    const id = this.model.profile.id, scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('forget_hub'), message: this.model.profile.name + '\n' + this.model.profile.baseUrl,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('forget_hub'), action: () => {
        if (this.model.scope() === scope) this.model.forgetProfile(id).catch(() => this.management.error = 'failed');
      } } });
  }
  private saveMode(field: string): void {
    const value = field === 'permission_mode' ? this.permissionMode : this.effortMode;
    const row = this.model.selected, scope = this.model.scope(), target = this.model.target(row);
    this.getUIContext().showAlertDialog({ title: tr('confirm'),
      message: row.title + '\n' + tr(field === 'permission_mode' ? 'permission_mode' : 'reasoning_effort') + ': ' + value,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('save'), action: () => {
        if (this.model.scope() === scope && this.model.target() === target) this.management.saveMode(this.model, field, value);
      } } });
  }
  private showError(): boolean { return this.management.error.length > 0; }

  private selectedSchedule(): ManagementRow | undefined {
    return this.management.rows.find((row: ManagementRow) => row.id === this.management.scheduleDraft.id);
  }
  private selectedConnector(): ManagementRow | undefined {
    return this.management.rows.find((row: ManagementRow) => row.id === this.management.selectedId);
  }
  private selectedChannel(): ManagementRow | undefined {
    return this.management.rows.find((row: ManagementRow) => row.id === this.management.selectedId);
  }
  private catalogMarketName(): string {
    const market = this.management.catalogMarkets.find((item: Record<string, Object>) => Wire.text(item, 'id') === this.management.catalogMarket);
    return Wire.text(market || {}, 'name', this.management.catalogMarket || tr('all'));
  }
  private catalogMarketOptions(): string[] {
    return [tr('all')].concat(this.management.catalogMarkets.map((market: Record<string, Object>) => Wire.text(market, 'name', Wire.text(market, 'id'))));
  }
  private channelTargetIndex(): number {
    return ['main', 'new', 'conversation'].indexOf(this.channelTargetDraft || Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind', 'main'));
  }
  private channelTargetLabel(kind: string): string {
    if (kind === 'main') return tr('main_chat');
    if (kind === 'new') return tr('new_chat');
    if (kind === 'agent') return tr('agent');
    return tr('conversation');
  }
  private channelBound(): boolean {
    const account = Wire.object(this.management.channelData(), 'account');
    return Object.keys(account).length > 0 || Wire.text(this.management.channelData(), 'state') === 'connected';
  }
  private channelWaitingText(): string {
    const waiting = Wire.object(this.management.channelDetail, 'waiting');
    if (!Object.keys(waiting).length) return '';
    return String(Wire.number(waiting, 'answers')) + ' · ' + String(Wire.number(waiting, 'asks'));
  }
  private channelConversationIndex(): number {
    const target = Wire.object(this.management.channelData(), 'target');
    const index = this.model.conversations.findIndex((row: ResourceRow) => row.machine === Wire.text(target, 'machine_id') &&
      (row.session || row.id) === Wire.text(target, 'session_id'));
    return Math.max(0, index);
  }
  private selectChannelTarget(index: number): void {
    const id = this.management.selectedId, kind = ['main', 'new', 'conversation'][index];
    this.channelTargetDraft = kind === 'conversation' ? kind : '';
    if (kind && kind !== 'conversation') this.management.setChannelTarget(this.model, id, kind);
  }
  private selectChannelConversation(index: number): void {
    const row = this.model.conversations[index];
    const id = this.management.selectedId;
    if (row) this.management.setChannelTarget(this.model, id, 'conversation', row.machine, row.session || row.id).then((ok: boolean) => {
      if (ok && this.management.selectedId === id) this.channelTargetDraft = '';
    });
  }
  private async openCatalog(): Promise<void> {
    this.openDetail('extension_catalog');
  }
  private previewCatalogRow(row: ManagementRow): void {
    this.management.previewExtension(this.model, row).then((ok: boolean) => {
      if (ok && Object.keys(this.management.preview).length > 0) this.extensionPreviewOpen = true;
    });
  }
  private refreshOperation(): void {
    this.management.refreshExtensionOperation(this.model).then((ok: boolean) => {
      if (ok && Object.keys(this.management.preview).length > 0) this.extensionPreviewOpen = true;
    });
  }
  private confirmScheduleSave(): void {
    const scope = this.model.scope(), owner = this.model.profile.manager, id = this.management.scheduleDraft.id;
    const target = JSON.stringify([scope, owner, id]);
    this.getUIContext().showAlertDialog({ title: tr('confirm'), message: this.management.scheduleDraft.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('save'), action: () => {
        if (this.model.scope() !== scope || this.model.profile.manager !== owner || JSON.stringify([this.model.scope(), this.model.profile.manager, this.management.scheduleDraft.id]) !== target) return;
        this.management.saveSchedule(this.model).then((ok: boolean) => { if (ok) this.scheduleEditOpen = false; });
      } } });
  }
  private confirmScheduleDelete(row: ManagementRow): void {
    const scope = this.model.scope(), owner = this.model.profile.manager, id = row.id;
    this.getUIContext().showAlertDialog({ title: tr('delete'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (this.model.scope() === scope && this.model.profile.manager === owner && this.management.rows.some((item: ManagementRow) => item.id === id))
          this.management.deleteSchedule(this.model, row);
      } } });
  }
  private confirmScheduleRun(row: ManagementRow): void {
    const scope = this.model.scope(), owner = this.model.profile.manager, id = row.id;
    this.getUIContext().showAlertDialog({ title: tr('run_now'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('run_now'), action: () => {
        if (this.model.scope() === scope && this.model.profile.manager === owner && this.management.rows.some((item: ManagementRow) => item.id === id))
          this.management.runSchedule(this.model, row).then((): void => {
            if (!this.mounted || !this.active || this.model.scope() !== scope || this.model.profile.manager !== owner) return;
            const target = this.management.scheduleReceiptChat(this.model); if (target) this.onChat(target);
          });
      } } });
  }
  private confirmExtensionSave(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const scope = this.model.scope(), owner = this.model.profile.manager, id = this.management.selectedId;
    this.getUIContext().showAlertDialog({ title: tr('save'), message: Wire.text(this.management.extensionDetail, 'name', id),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('save'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.saveExtensionConfig(this.model);
      } } });
  }
  private confirmExtensionRemove(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const scope = this.model.scope(), owner = this.model.profile.manager, id = this.management.selectedId;
    this.getUIContext().showAlertDialog({ title: tr('remove'), message: Wire.text(this.management.extensionDetail, 'name', id),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('remove'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.removeExtension(this.model).then((done: boolean) => {
            if (done && this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && !Object.keys(this.management.operation).length) this.onBack();
          });
      } } });
  }
  private confirmConnectorLogout(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const row = this.selectedConnector(); if (!row) return;
    const scope = this.model.scope(), owner = this.model.profile.manager, id = row.id;
    this.getUIContext().showAlertDialog({ title: tr('sign_out'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('sign_out'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.connectorLogout(this.model, row);
      } } });
  }
  private confirmChannelUnbind(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const channel = this.management.channelData(), id = this.management.selectedId, scope = this.model.scope(), owner = this.model.profile.manager;
    this.getUIContext().showAlertDialog({ title: tr('unbind'), message: Wire.text(channel, 'name', id),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('unbind'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.channelUnbind(this.model, id);
      } } });
  }

  @Builder status() {
    if (this.management.loading) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
    if (this.showError()) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
  }

  @Builder modelsPage() {
    Column() {
      if (this.management.catalogPurposes.length > 0) {
        Select(this.selectOptions(this.management.catalogPurposes.map((item: Record<string, Object>) => this.purposeLabel(item))))
          .selected(this.purposeIndex).value(this.purposeLabel(this.management.catalogPurposes[this.purposeIndex] || {}))
          .onSelect((index: number) => {
            this.purposeIndex = index;
            const item = this.management.catalogPurposes[index];
            this.management.selectPurpose(this.model, Wire.text(item, 'id'));
          }).margin(Tokens.inset)
      }
      List() {
        if (Object.keys(this.management.purpose).length > 0) {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('purpose'),
            secondaryText: Wire.text(this.management.purpose, 'name', Wire.text(this.management.purpose, 'id')) }) }) }
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('model_provider'),
            secondaryText: Wire.text(this.modelProvider(), 'name', Wire.text(this.modelProvider(), 'id', tr('automatic'))) }) }) }
            .enabled(this.fieldSupported('provider'))
          if (this.fieldSupported('provider')) ListItem() {
            Select(this.providerOptions())
              .selected(this.providerIndex()).value(this.providerIndex() === 0 ? tr('automatic') : Wire.text(this.purposeProviders()[this.providerIndex() - 1], 'name', Wire.text(this.purposeProviders()[this.providerIndex() - 1], 'id')))
              .onSelect((index: number) => {
                const provider = index === 0 ? '' : Wire.text(this.purposeProviders()[index - 1], 'id');
                this.management.changePurposeDraft('provider', provider);
                this.management.loadPurposeModels(this.model);
              })
          }
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('model_id'),
            secondaryText: this.management.purposeDraft.model || Wire.text(Wire.object(this.management.purpose, 'effective'), 'model', tr('automatic')) }) }) }
          if (this.fieldSupported('model') && (Wire.text(this.modelProvider(), 'kind', 'launcher') !== 'launcher' || this.management.providerModels.length === 0)) {
            ListItem() { TextInput({ text: this.management.purposeDraft.model, placeholder: tr('model_id') })
              .onChange((value: string) => this.management.changePurposeDraft('model', value)) }
          } else if (this.fieldSupported('model') && this.management.providerModels.length > 0) {
            ListItem() {
              Select(this.selectOptions(this.management.providerModels.map((item: Record<string, Object>) => Wire.text(item, 'name', Wire.text(item, 'id')))))
                .selected(this.modelIndex()).value(Wire.text(this.management.providerModels[this.modelIndex()] || {}, 'name', this.management.purposeDraft.model))
                .onSelect((index: number) => this.management.changePurposeDraft('model', Wire.text(this.management.providerModels[index], 'id')))
            }
          }
          if (this.fieldSupported('effort') && this.effortOptions().length > 0) ListItem() {
            Select(this.selectOptions(this.effortOptions().map((item: string) => tr(item))))
              .value(this.management.purposeDraft.effort || tr('automatic'))
              .onSelect((index: number) => this.management.changePurposeDraft('effort', this.effortOptions()[index]))
          }
          if (Wire.text(this.management.purpose, 'status')) ListItem() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('status'), secondaryText: Wire.text(this.management.purpose, 'status') }) })
          }
          ListItem() { Button(tr('save')).enabled(this.purposeChanged() && !this.management.saving)
            .onClick(() => this.management.savePurpose(this.model)) }
        }
        if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
    }.width('100%').height('100%').enabled(!this.management.saving)
  }

  @Builder sessionModePage(field: string) {
    Column() {
      if (!this.modeSupported() || field === 'effort' && Wire.strings(this.model.selected.data(), 'efforts').length === 0) {
        Text(tr('no_capability')).padding(Tokens.page)
      } else {
        List() {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr(field === 'permission_mode' ? 'permission_mode' : 'reasoning_effort'),
            secondaryText: Wire.text(this.model.selected.data(), field, tr('automatic')) }) }) }
          ListItem() {
            Select(this.selectOptions(this.modeOptionsFor(field).map((item: string) => tr(item))))
              .value(tr(field === 'permission_mode' ? this.permissionMode : this.effortMode))
              .onSelect((index: number) => {
                const values = this.modeOptionsFor(field);
                if (field === 'permission_mode') this.permissionMode = values[index]; else this.effortMode = values[index];
              })
          }
          ListItem() { Button(tr('save')).enabled(!this.management.saving &&
            (field === 'permission_mode' ? this.permissionMode !== Wire.text(this.model.selected.data(), field) : this.effortMode !== Wire.text(this.model.selected.data(), field)))
            .onClick(() => this.saveMode(field)) }
        }.width('100%')
      }
      if (this.management.error) Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
    }.width('100%').height('100%')
  }

  @Builder computerForm() {
    Column({ space: Tokens.page }) {
      TextInput({ text: this.computerName, placeholder: tr('name') }).onChange((value: string) => this.computerName = value)
      Select(this.selectOptions(['Mac', 'Linux', 'Windows'])).selected(this.computerOs === 'mac' ? 0 : this.computerOs === 'linux' ? 1 : 2).value(this.computerOs === 'mac' ? 'Mac' : this.computerOs === 'linux' ? 'Linux' : 'Windows')
        .onSelect((index: number) => this.computerOs = ['mac', 'linux', 'windows'][index])
      Button(tr('generate_code')).enabled(this.canPairComputer() && this.computerName.trim().length > 0 && !this.management.saving)
        .onClick(() => this.createComputerPairing())
      if (this.management.error) Text(tr(this.management.error)).fontColor($r('app.color.persona_danger'))
    }.padding(Tokens.page).width('100%')
  }

  @Builder machinesPage() {
    Column() {
      List() {
        if ((this.machineOwner || this.model).features.includes('machines_add')) ListItem() { Button(tr('add_computer')).enabled(this.canPairComputer()).onClick(() => this.addComputerOpen = true).constraintSize({ minHeight: Tokens.touch }) }.padding({ left: Tokens.page, right: Tokens.page })
        if (this.machineOptions().length > 0) ListItem() {
          Select(this.selectOptions(this.machineOptions().map((row: ResourceRow) => this.machineLabel(row))))
            .selected(Math.max(0, this.machineOptions().findIndex((row: ResourceRow) => row.key() === this.machineDirectoryKey)))
            .value(this.machineOptions().find((row: ResourceRow) => row.key() === this.machineDirectoryKey) ? this.machineLabel(this.machineOptions().find((row: ResourceRow) => row.key() === this.machineDirectoryKey) as ResourceRow) : tr('machine'))
            .onSelect((index: number) => { const row = this.machineOptions()[index]; if (row) this.chooseMachine(row); }).enabled(!this.management.loading && !this.addComputerOpen).width('100%')
        }.padding({ left: Tokens.page, right: Tokens.page })
        if (this.machineRow()) {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('platform'),
            secondaryText: [Wire.text(this.machineRow()?.raw || {}, 'platform'), Wire.text(this.machineRow()?.raw || {}, 'engine'), Wire.text(this.machineRow()?.raw || {}, 'version')].filter((v: string) => !!v).join(' · ') }) }) }
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('status'),
            secondaryText: Wire.text(this.machineRow()?.raw || {}, 'error', Wire.text(this.machineRow()?.raw || {}, 'state', tr(Wire.bool(this.machineRow()?.raw || {}, 'online') ? 'online' : 'offline'))) }) }) }
          ListItem() { Button(tr('computer_screen_open')).enabled(this.canOpenScreen()).onClick(() => { const row = this.screenMachine(); if (row && this.canOpenScreen()) this.onComputer(row); }).id('machines.screen').constraintSize({ minHeight: Tokens.touch }) }.padding({ left: Tokens.page, right: Tokens.page })
          if (Wire.text(this.machineRow()?.raw || {}, 'trust') === 'pending') {
            ListItem() { Button(tr('confirm_computer')).enabled(!!this.screenMachine() && !this.management.loading).onClick(() => this.confirmMachine(this.machineRow() as ManagementRow, true)) }
            ListItem() { Button(tr('reject')).enabled(!!this.screenMachine() && !this.management.loading).onClick(() => this.confirmMachine(this.machineRow() as ManagementRow, false)) }
          }
        }
        if (this.machineOwner && this.pairingOwnerScope === this.machineOwner.scope() && this.management.pairing['pair_url']) {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(this.management.pairing, 'name'),
            secondaryText: Wire.text(this.management.pairing, 'machine') }) }) }
          ListItem() { Text(Wire.text(this.management.pairing, 'pair_url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.page) }
          ListItem() { Text(tr('expires_at') + ': ' + Wire.text(this.management.pairing, 'expires_at')).padding(Tokens.page) }
          if (this.canRenew()) ListItem() { Button(tr('new_pairing_code')).onClick(() => this.management.renewPairing(this.machineOwner || this.model)) }
          if (Wire.text(this.management.pairing, 'command')) ListItem() { Text(Wire.text(this.management.pairing, 'command')).copyOption(CopyOptions.LocalDevice).padding(Tokens.page) }
        }
        if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('no_computer')).padding(Tokens.page) }
        if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
    }.width('100%').height('100%')
      .bindSheet($$this.addComputerOpen, this.computerForm(), { title: { title: tr('add_computer') }, height: SheetSize.MEDIUM, showClose: true })
  }

  @Builder scheduleForm() {
    Column({ space: Tokens.page }) {
      TextInput({ text: this.management.scheduleDraft.title, placeholder: tr('title') })
        .onChange((value: string) => this.management.changeSchedule('title', value))
      TextArea({ text: this.management.scheduleDraft.prompt, placeholder: tr('task') })
        .onChange((value: string) => this.management.changeSchedule('prompt', value)).height(Tokens.inputExpanded)
      Select(this.selectOptions(['once', 'daily', 'interval'].map((value: string) => tr(value))))
        .selected(Math.max(0, ['once', 'daily', 'interval'].indexOf(this.management.scheduleDraft.repeat)))
        .value(tr(this.management.scheduleDraft.repeat))
        .onSelect((index: number) => this.management.changeSchedule('repeat', ['once', 'daily', 'interval'][index]))
      if (this.management.scheduleDraft.repeat === 'once') TextInput({ text: this.management.scheduleDraft.at, placeholder: tr('run_at') })
        .onChange((value: string) => this.management.changeSchedule('at', value))
      if (this.management.scheduleDraft.repeat === 'daily') {
        TextInput({ text: this.management.scheduleDraft.time, placeholder: tr('time') })
          .onChange((value: string) => this.management.changeSchedule('time', value))
        TextInput({ text: this.management.scheduleDraft.weekdays, placeholder: tr('weekdays') })
          .onChange((value: string) => this.management.changeSchedule('weekdays', value))
      }
      if (this.management.scheduleDraft.repeat === 'interval') TextInput({ text: this.management.scheduleDraft.everyMinutes, placeholder: tr('every_minutes') })
        .type(InputType.Number).onChange((value: string) => this.management.changeSchedule('every_minutes', value))
      Row() {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('enabled') }) }).layoutWeight(1)
        Toggle({ type: ToggleType.Switch, isOn: this.management.scheduleDraft.enabled })
          .onChange((value: boolean) => this.management.changeSchedule('enabled', value)).accessibilityText(tr('enabled'))
      }
      Button(tr('save')).enabled(this.management.scheduleValid() && !this.management.saving).onClick(() => this.confirmScheduleSave())
      Button(tr('cancel')).onClick(() => { if (this.management.cancelSchedule()) this.scheduleEditOpen = false; })
      if (this.management.error) Text(tr(this.management.error)).fontColor($r('app.color.persona_danger'))
    }.padding(Tokens.page).width('100%')
  }

  @Builder scheduleCreateMenu() {
    Menu() {
      MenuItem({ content: tr('create') }).onClick(() => { this.management.beginSchedule(); this.scheduleEditOpen = true; })
      ForEach(listScheduleTemplates(), (template: ScheduleTemplate) => {
        MenuItem({ content: tr(template.titleKey) }).onClick(() => {
          if (this.management.applyScheduleTemplate(template.id, tr(template.titleKey), tr(template.promptKey))) this.scheduleEditOpen = true;
        })
      }, (template: ScheduleTemplate) => template.id)
    }
  }

  @Builder schedulesPage() {
    List() {
      ListItem() { Button(tr('add')).bindMenu(this.scheduleCreateMenu) }
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            Row() {
              ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
                secondaryText: Wire.text(binding.value.raw, 'next_run', Wire.text(binding.value.raw, 'last_status')) }) }).layoutWeight(1)
              Toggle({ type: ToggleType.Switch, isOn: Wire.bool(binding.value.raw, 'enabled') })
                .onChange((enabled: boolean) => this.management.setScheduleEnabled(this.model, binding.value, enabled))
                .accessibilityText(binding.value.title)
            }
            Row() {
              Button(tr('edit')).onClick(() => { this.management.beginSchedule(binding.value); this.scheduleEditOpen = true; })
              Button(tr('run_now')).onClick(() => this.confirmScheduleRun(binding.value))
              Button(tr('delete')).onClick(() => this.confirmScheduleDelete(binding.value))
            }
          }
        }.id('schedule.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (this.management.lastRunReceipt['session_id']) ListItem() {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('run_started') }) })
      }.onClick(() => { const target = this.management.scheduleReceiptChat(this.model); if (target) this.onChat(target); }).id('schedule.receipt')
      ListItem() { Button(tr('refresh')).onClick(() => this.management.load('schedules', this.model)) }
      ForEach(this.scheduleRunBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
          secondaryText: Wire.text(binding.value.raw, 'status') + ' · ' + Wire.text(binding.value.raw, 'at', Wire.text(binding.value.raw, 'due')) }) }) }
          .id('schedule.run.' + binding.value.id)
          .onClick(() => { const target = this.management.scheduleRunChat(this.model, binding.value); if (target) this.onChat(target); })
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
      .bindSheet($$this.scheduleEditOpen, this.scheduleForm(), { title: { title: tr(this.management.scheduleDraft.id ? 'edit' : 'create') }, height: SheetSize.LARGE, showClose: true,
        onDisappear: (): void => { this.management.cancelSchedule(); } })
  }

  @Builder triagePage() {
    List() {
      ForEach(['enabled', 'auto_continue', 'wake', 'decide', 'reply_reminder'], (key: string) => {
        if (key !== 'reply_reminder' || Object.keys(this.management.data).includes(key)) ListItem() {
          Row() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr(key) }) }).layoutWeight(1)
            Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.management.data, key, key === 'reply_reminder') })
              .enabled(!this.management.saving && (key === 'enabled' || Wire.bool(this.management.data, 'enabled')))
              .onChange((value: boolean) => this.saveTriage(key, value)).accessibilityText(tr(key))
          }
        }.id('triage.' + key)
      }, (key: string) => key)
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  private secretSet(name: string): boolean {
    return Wire.rows(this.management.extensionDetail, 'secrets').some((item: Record<string, Object>) =>
      Wire.text(item, 'name') === name && Wire.bool(item, 'set'));
  }
  private qrPayload(prompt: Record<string, Object>): string {
    const value = Wire.text(prompt, 'qr_text', Wire.text(prompt, 'url', Wire.text(prompt, 'verification_uri_complete')));
    return value.length <= 4096 && /^https:\/\//.test(value) ? value : '';
  }

  @Builder extensionOperationPanel() {
    if (Object.keys(this.management.operation).length > 0) ListItem() {
      Column({ space: Tokens.inset }) {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('operation'),
          secondaryText: tr(Wire.text(this.management.operation, 'state')) + ' · ' + Wire.text(this.management.operation, 'step') }) })
        if (Wire.text(this.management.operation, 'error')) Text(Wire.text(this.management.operation, 'error')).fontColor($r('app.color.persona_danger'))
        ForEach(Wire.rows(this.management.operation, 'steps'), (step: Record<string, Object>) => {
          Text(Wire.text(step, 'id') + ' · ' + Wire.text(step, 'state') + (Wire.bool(step, 'cached') ? ' · ' + tr('cached') : ''))
            .fontSize(Tokens.meta).padding(Tokens.inset)
        }, (step: Record<string, Object>, index: number) => JSON.stringify([index, step]))
        Row() {
          if (['running', 'needs_consent'].includes(Wire.text(this.management.operation, 'state')))
            Button(tr('refresh')).onClick(() => this.refreshOperation())
          if (['running', 'needs_consent'].includes(Wire.text(this.management.operation, 'state')))
            Button(tr('cancel')).onClick(() => this.management.cancelExtensionOperation(this.model))
          if (Wire.text(this.management.operation, 'state') === 'failed' && Wire.bool(this.management.operation, 'retryable'))
            Button(tr('retry')).onClick(() => this.management.retryExtensionOperation(this.model))
          if (Wire.text(this.management.operation, 'state') === 'needs_consent' && Object.keys(this.management.preview).length > 0)
            Button(tr('review')).onClick(() => this.extensionPreviewOpen = true)
        }
      }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
    }.id('extension.operation')
  }

  @Builder bundleHeader(member: Record<string, Object>) {
    Text(Wire.text(Wire.object(member, 'manifest'), 'name', Wire.text(Wire.object(member, 'manifest'), 'id')))
      .fontSize(Tokens.meta).fontWeight(FontWeight.Medium).padding(Tokens.inset)
  }
  @Builder extensionPreviewForm() {
    Column() {
      List() {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(Wire.object(this.management.preview, 'manifest'), 'name', this.management.catalogSelectedId),
          secondaryText: Wire.text(Wire.object(this.management.preview, 'manifest'), 'version') }) }) }
        ForEach(Wire.rows(this.management.preview, 'permissions'), (permission: Record<string, Object>) => {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(permission, 'title', Wire.text(permission, 'key')),
            secondaryText: Wire.text(permission, 'detail') }) }) }.id('permission.' + Wire.text(permission, 'key'))
        }, (permission: Record<string, Object>) => Wire.text(permission, 'key'))
        ForEach(Wire.rows(this.management.preview, 'added_permissions'), (permission: Record<string, Object>) => {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(permission, 'title', Wire.text(permission, 'key')),
            secondaryText: Wire.text(permission, 'detail') }) }) }.id('added.permission.' + Wire.text(permission, 'key'))
        }, (permission: Record<string, Object>) => Wire.text(permission, 'key'))
        ForEach(Wire.strings(this.management.preview, 'warnings'), (warning: string) => {
          ListItem() { Text(warning).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
        }, (warning: string) => warning)
        ForEach(Wire.rows(this.management.preview, 'bundle'), (member: Record<string, Object>) => {
          ListItemGroup({ header: () => this.bundleHeader(member) }) {
            ForEach(Wire.rows(member, 'permissions'), (permission: Record<string, Object>) => {
              ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(permission, 'title', Wire.text(permission, 'key')),
                secondaryText: Wire.text(permission, 'detail') }) }) }
            }, (permission: Record<string, Object>) => Wire.text(permission, 'key'))
            ForEach(Wire.strings(member, 'warnings'), (warning: string) => {
              ListItem() { Text(warning).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
            }, (warning: string) => warning)
          }
        }, (member: Record<string, Object>) => Wire.text(member, 'preview_id'))
        if (Wire.strings(this.management.preview, 'unresolved').length > 0 || !this.management.previewReady())
          ListItem() { Text(tr('dependencies_unresolved')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
      Row() {
        Button(tr('cancel')).onClick(() => {
          this.extensionPreviewOpen = false;
          if (Wire.text(this.management.operation, 'state') === 'needs_consent') this.management.cancelExtensionOperation(this.model);
          else this.management.preview = {};
        })
        Button(tr(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install'))
          .enabled(this.management.previewReady() && !this.management.saving)
          .onClick(() => this.management.confirmExtensionPreview(this.model).then((ok: boolean) => { if (ok) this.extensionPreviewOpen = false; }))
      }.justifyContent(FlexAlign.SpaceBetween).width('100%').padding(Tokens.inset)
      if (this.management.error) Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
    }.width('100%').height('100%')
  }

  @Builder extensionCatalogPage() {
    List() {
      ListItem() {
        Row() {
          TextInput({ text: this.management.catalogQuery, placeholder: tr('search') })
            .onChange((value: string) => this.management.catalogQuery = value).layoutWeight(1)
          Button(tr('search')).onClick(() => this.management.loadExtensionCatalog(this.model))
        }
      }
      if (this.management.catalogMarkets.length > 0) ListItem() {
        Select(this.selectOptions(this.catalogMarketOptions()))
          .selected(this.management.catalogMarket ? Math.max(0, this.management.catalogMarkets.findIndex((market: Record<string, Object>) => Wire.text(market, 'id') === this.management.catalogMarket) + 1) : 0)
          .value(this.catalogMarketName())
          .onSelect((index: number) => this.management.loadExtensionCatalog(this.model, this.management.catalogQuery,
            index === 0 ? '' : Wire.text(this.management.catalogMarkets[index - 1], 'id')))
      }
      this.extensionOperationPanel()
      ForEach(this.catalogRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
              secondaryText: Wire.text(binding.value.raw, 'problem', Wire.text(binding.value.raw, 'description', Wire.text(binding.value.raw, 'version'))) }) })
            Button(tr(Wire.bool(binding.value.raw, 'upgradable') ? 'upgrade' : 'preview'))
              .enabled(Wire.bool(this.management.data, 'can_install') && !Wire.text(binding.value.raw, 'problem') && !this.management.saving)
              .onClick(() => this.previewCatalogRow(binding.value))
          }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
        }.id('catalog.' + Wire.text(binding.value.raw, 'market') + '.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.catalogRows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder extensionDetailPage() {
    List() {
      if (Wire.text(this.management.extensionDetail, 'version', Wire.text(this.management.extensionDetail, 'source'))) ListItem() { Text(Wire.text(this.management.extensionDetail, 'version', Wire.text(this.management.extensionDetail, 'source'))).fontColor($r('sys.color.font_secondary')).padding(Tokens.inset) }
      if (Wire.bool(this.management.extensionDetail, 'read_only')) ListItem() { Text(tr('read_only')).padding(Tokens.inset) }
      this.extensionOperationPanel()
      if (this.management.extensionConfigFields.length > 0 && this.management.extensionSchemaSupported) {
        ForEach(this.extensionConfigFieldsBindings, (binding: BoundRow<ManagementInput>) => {
          ListItem() {
            Column() {
              Text(binding.value.label).fontSize(Tokens.body)
              if (binding.value.kind === 'boolean') Toggle({ type: ToggleType.Switch, isOn: binding.value.value === 'true' })
                .onChange((value: boolean) => this.management.changeInput(this.management.extensionConfigFields, binding.value.key, String(value)))
              else if (binding.value.kind === 'enum') Select(this.selectOptions(binding.value.options))
                .selected(Math.max(0, binding.value.options.indexOf(binding.value.value))).value(binding.value.value)
                .onSelect((index: number) => this.management.changeInput(this.management.extensionConfigFields, binding.value.key, binding.value.options[index]))
              else TextInput({ text: binding.value.value, placeholder: binding.value.label })
                .type(binding.value.kind === 'integer' || binding.value.kind === 'number' ? InputType.Number : InputType.Normal)
                .onChange((value: string) => this.management.changeInput(this.management.extensionConfigFields, binding.value.key, value))
            }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
          }.id('extension.config.' + binding.value.key)
        }, (binding: BoundRow<ManagementInput>) => binding.key)
        ListItem() { Button(tr('save')).enabled(this.management.extensionConfigChanged() && !this.management.saving && !Wire.bool(this.management.extensionDetail, 'read_only'))
          .onClick(() => this.confirmExtensionSave()) }
      } else if (!this.management.extensionSchemaSupported) ListItem() { Text(tr('unknown_field')).padding(Tokens.inset) }
      ForEach(this.extensionSecretFieldsBindings, (binding: BoundRow<ManagementInput>) => {
        ListItem() {
          Column() {
            TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(InputType.Password).showPasswordIcon(false)
              .onChange((value: string) => this.management.changeInput(this.management.extensionSecretFields, binding.value.key, value))
            Row() {
              Button(tr('save')).enabled(!!binding.value.value && !this.management.saving).onClick(() => this.management.saveExtensionSecret(this.model, binding.value))
              if (this.secretSet(binding.value.key)) Button(tr('clear')).onClick(() => this.management.clearExtensionSecret(this.model, binding.value))
            }
          }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
        }.id('extension.secret.' + binding.value.key)
      }, (binding: BoundRow<ManagementInput>) => binding.key)
      if (this.model.features.includes('extension_auth') && Object.keys(this.management.auth).length > 0) {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('sign_in'),
          secondaryText: Wire.text(this.management.auth, 'account', Wire.text(this.management.auth, 'state')) }) }) }
        if (['signed_out', 'expired'].includes(Wire.text(this.management.auth, 'state')))
          ListItem() { Button(tr('sign_in')).enabled(!this.management.saving).onClick(() => this.management.extensionAuthStep(this.model, this.management.selectedId, true)) }
        if (Wire.text(this.management.auth, 'state') === 'signed_in')
          ListItem() { Button(tr('sign_out')).onClick(() => this.management.extensionLogout(this.model, this.management.selectedId)) }
        if (Object.keys(this.management.authPrompt).length > 0) {
          if (Wire.text(this.management.authPrompt, 'url')) ListItem() { Text(Wire.text(this.management.authPrompt, 'url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.authPrompt, 'user_code')) ListItem() { Text(Wire.text(this.management.authPrompt, 'user_code')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.authPrompt, 'hint')) ListItem() { Text(Wire.text(this.management.authPrompt, 'hint')).padding(Tokens.inset) }
          if (this.qrPayload(this.management.authPrompt)) ListItem() { QRCode(this.qrPayload(this.management.authPrompt)).width(Tokens.qr).height(Tokens.qr).margin(Tokens.page) }
          ForEach(this.authFieldsBindings, (binding: BoundRow<ManagementInput>) => ListItem() {
            TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(binding.value.secret ? InputType.Password : InputType.Normal)
              .onChange((value: string) => this.management.changeInput(this.management.authFields, binding.value.key, value))
          }.id('extension.auth.' + binding.value.key), (binding: BoundRow<ManagementInput>) => binding.key)
          if (Wire.text(this.management.authPrompt, 'error')) ListItem() { Text(Wire.text(this.management.authPrompt, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
          ListItem() { Button(tr(Wire.text(this.management.authPrompt, 'kind') ? 'check_status' : 'continue'))
            .onClick(() => this.management.extensionAuthStep(this.model, this.management.selectedId, false)) }
        }
      }
      ForEach(Wire.rows(this.management.extensionDetail, 'ui'), (page: Record<string, Object>) => {
        if (Wire.text(page, 'id')) ListItem() { Button(Wire.text(page, 'title', Wire.text(page, 'id'))).onClick(() => this.onExtension(this.management.selectedId, Wire.text(page, 'id'), Wire.text(page, 'title', Wire.text(page, 'id')))) }
      }, (page: Record<string, Object>) => Wire.text(page, 'id'))
      if (Wire.text(this.management.extensionDetail, 'origin').startsWith('market:') && !Wire.bool(this.management.extensionDetail, 'read_only'))
        ListItem() { Button(tr('remove')).onClick(() => this.confirmExtensionRemove()) }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder connectorDetailPage() {
    List() {
      if (Wire.text(this.selectedConnector()?.raw || {}, 'status')) ListItem() { Text(Wire.text(this.selectedConnector()?.raw || {}, 'status')).padding(Tokens.inset) }
      if (this.selectedConnector() && Wire.text(this.selectedConnector()?.raw || {}, 'account'))
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('account'), secondaryText: Wire.text(this.selectedConnector()?.raw || {}, 'account') }) }) }
      if (this.selectedConnector() && Wire.text(this.selectedConnector()?.raw || {}, 'via'))
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('via'), secondaryText: Wire.text(this.selectedConnector()?.raw || {}, 'via') }) }) }
      if (this.selectedConnector() && Wire.text(this.selectedConnector()?.raw || {}, 'error'))
        ListItem() { Text(Wire.text(this.selectedConnector()?.raw || {}, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      if (this.selectedConnector() && Wire.has(this.selectedConnector()?.raw || {}, 'enabled')) ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('enabled') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.selectedConnector()?.raw || {}, 'enabled') })
            .onChange((value: boolean) => this.management.setConnectorSetting(this.model, this.selectedConnector() as ManagementRow, 'enabled', value))
        }
      }
      if (this.selectedConnector() && Wire.has(this.selectedConnector()?.raw || {}, 'inbox')) ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.selectedConnector()?.raw || {}, 'inbox') })
            .onChange((value: boolean) => this.management.setConnectorSetting(this.model, this.selectedConnector() as ManagementRow, 'inbox', value))
        }
      }
      if (this.selectedConnector() && Wire.has(this.selectedConnector()?.raw || {}, 'archive')) ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('archive') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.selectedConnector()?.raw || {}, 'archive') })
            .onChange((value: boolean) => this.management.setConnectorSetting(this.model, this.selectedConnector() as ManagementRow, 'archive', value))
        }
      }
      if (this.selectedConnector() && Wire.strings(this.selectedConnector()?.raw || {}, 'guide').length > 0) {
        ForEach(Wire.strings(this.selectedConnector()?.raw || {}, 'guide'), (line: string) => {
          ListItem() { Text(line).padding(Tokens.inset) }
        }, (line: string) => line)
      }
      if (this.selectedConnector() && Wire.strings(this.selectedConnector()?.raw || {}, 'capabilities').includes('login')) {
        if (Wire.text(this.management.connectorPrompt, 'url')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
        if (Wire.text(this.management.connectorPrompt, 'user_code')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'user_code')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
        if (Wire.text(this.management.connectorPrompt, 'hint')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'hint')).padding(Tokens.inset) }
        if (this.qrPayload(this.management.connectorPrompt)) ListItem() { QRCode(this.qrPayload(this.management.connectorPrompt)).width(Tokens.qr).height(Tokens.qr).margin(Tokens.page) }
        ForEach(this.connectorFieldsBindings, (binding: BoundRow<ManagementInput>) => {
          ListItem() { TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(binding.value.secret ? InputType.Password : InputType.Normal)
            .onChange((value: string) => this.management.changeInput(this.management.connectorFields, binding.value.key, value)) }
            .id('connector.login.' + binding.value.key)
        }, (binding: BoundRow<ManagementInput>) => binding.key)
        if (Wire.text(this.management.connectorPrompt, 'error')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
        if (!Wire.text(this.selectedConnector()?.raw || {}, 'account')) ListItem() {
          Button(tr(Object.keys(this.management.connectorPrompt).length === 0 ? 'sign_in' : Wire.text(this.management.connectorPrompt, 'kind') ? 'check_status' : 'continue'))
            .enabled(!this.management.saving)
            .onClick(() => this.management.connectorLogin(this.model, this.selectedConnector() as ManagementRow, Object.keys(this.management.connectorPrompt).length === 0))
        }
        if (Wire.text(this.selectedConnector()?.raw || {}, 'account'))
          ListItem() { Button(tr('sign_out')).onClick(() => this.confirmConnectorLogout()) }
      }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder extensionConnectorPage(page: string) {
    List() {
      if (page === 'extensions' && Wire.bool(this.management.data, 'markets')) ListItem() {
        Button(tr('extension_market')).onClick(() => this.openCatalog())
      }
      this.extensionOperationPanel()
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            Row() {
              ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
                secondaryText: Wire.text(binding.value.raw, 'error', binding.value.detail || Wire.text(binding.value.raw, 'via', Wire.text(binding.value.raw, 'status'))) }) }).layoutWeight(1)
              Toggle({ type: ToggleType.Switch, isOn: this.rowEnabled(binding.value) })
                .onChange((value: boolean) => page === 'extensions' ? this.management.setExtensionEnabled(this.model, binding.value, value) : this.management.setConnectorSetting(this.model, binding.value, 'enabled', value))
                .accessibilityText(binding.value.title)
            }
            Button(tr('details')).onClick(() => {
              this.openDetail(page === 'extensions' ? 'extension_detail' : 'connector_detail', binding.value);
            })
          }
        }.id(page + '.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder channelDetailPage() {
    List() {
      if (Object.keys(this.management.channelDetail).length > 0) {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(this.management.channelData(), 'name', this.management.selectedId),
          secondaryText: Wire.text(this.management.channelData(), 'state') }) }) }
        if (Wire.bool(this.management.channelData(), 'read_only')) ListItem() { Text(tr('read_only')).padding(Tokens.inset) }
        if (!Wire.bool(this.management.channelData(), 'read_only')) {
          ListItem() {
            Row() {
              ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('enabled') }) }).layoutWeight(1)
              Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.management.channelData(), 'enabled') })
                .onChange((value: boolean) => this.management.setChannelEnabled(this.model, this.selectedChannel() as ManagementRow, value))
            }
          }
          if (Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind')) ListItem() {
            Select(this.selectOptions(['main', 'new', 'conversation'].map((kind: string) => this.channelTargetLabel(kind))))
              .selected(this.channelTargetIndex()).value(this.channelTargetLabel(this.channelTargetDraft || Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind', 'main')))
              .enabled(!this.management.saving)
              .onSelect((index: number) => this.selectChannelTarget(index))
          }
          if ((this.channelTargetDraft === 'conversation' || Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind') === 'conversation') && this.model.conversations.length > 0) ListItem() {
            Select(this.selectOptions(this.model.conversations.map((row: ResourceRow) => row.title)))
              .selected(this.channelTargetDraft ? -1 : this.channelConversationIndex()).value(this.channelTargetDraft ? tr('conversation') : this.model.conversations[this.channelConversationIndex()]?.title || '')
              .enabled(!this.management.saving)
              .onSelect((index: number) => this.selectChannelConversation(index))
          }
          ForEach(this.channelFeatureBindings, (binding: BoundRow<Record<string, Object>>) => {
            ListItem() {
              Row() {
                ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(binding.value, 'title', Wire.text(binding.value, 'id')) }) }).layoutWeight(1)
                Toggle({ type: ToggleType.Switch, isOn: Wire.bool(binding.value, 'on') })
                  .onChange((value: boolean) => this.management.setChannelFeature(this.model, this.management.selectedId, Wire.text(binding.value, 'id'), value))
                  .accessibilityText(Wire.text(binding.value, 'title', Wire.text(binding.value, 'id')))
              }
            }.id('channel.feature.' + Wire.text(binding.value, 'id'))
          }, (binding: BoundRow<Record<string, Object>>) => binding.key)
        }
        if (Wire.text(this.management.channelData(), 'bind', 'none') !== 'none' && !Wire.bool(this.management.channelData(), 'read_only')) {
          if (this.channelBound())
            ListItem() { Button(tr('unbind')).onClick(() => this.confirmChannelUnbind()) }
          else if (Object.keys(this.management.channelPrompt).length === 0)
            ListItem() { Button(tr('sign_in')).onClick(() => this.management.channelBind(this.model, this.management.selectedId, true)) }
          if (Wire.text(this.management.channelPrompt, 'url')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.channelPrompt, 'user_code')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'user_code')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.channelPrompt, 'hint')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'hint')).padding(Tokens.inset) }
          if (this.qrPayload(this.management.channelPrompt)) ListItem() { QRCode(this.qrPayload(this.management.channelPrompt)).width(Tokens.qr).height(Tokens.qr).margin(Tokens.page) }
          ForEach(this.channelFieldsBindings, (binding: BoundRow<ManagementInput>) => {
            ListItem() { TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(binding.value.secret ? InputType.Password : InputType.Normal)
              .onChange((value: string) => this.management.changeInput(this.management.channelFields, binding.value.key, value)) }
              .id('channel.bind.' + binding.value.key)
          }, (binding: BoundRow<ManagementInput>) => binding.key)
          if (Wire.text(this.management.channelPrompt, 'error')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
          if (Object.keys(this.management.channelPrompt).length > 0) ListItem() {
            Button(tr(Wire.text(this.management.channelPrompt, 'kind') ? 'check_status' : 'continue'))
              .enabled(!this.management.saving).onClick(() => this.management.channelBind(this.model, this.management.selectedId, false))
          }
        }
        if (Wire.text(this.management.channelData(), 'state') === 'connected' && !Wire.bool(this.management.channelData(), 'read_only'))
          ListItem() { Button(tr('test')).onClick(() => this.management.channelTest(this.model, this.management.selectedId)) }
        if (this.channelWaitingText()) ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('waiting'),
          secondaryText: this.channelWaitingText() }) }) }
      }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder channelsPage() {
    List() {
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            Row() {
              ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
                secondaryText: Wire.text(binding.value.raw, 'status', binding.value.detail) }) }).layoutWeight(1)
              Toggle({ type: ToggleType.Switch, isOn: this.rowEnabled(binding.value) })
                .enabled(Wire.bool(binding.value.raw, 'installed', true) && !Wire.bool(binding.value.raw, 'read_only'))
                .onChange((value: boolean) => this.management.setChannelEnabled(this.model, binding.value, value)).accessibilityText(binding.value.title)
            }
            Button(tr('details')).enabled(Wire.bool(binding.value.raw, 'installed', true)).onClick(() => {
              this.openDetail('channel_detail', binding.value);
            })
          }
        }.id('channel.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder ledgerPage() {
    List() {
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
              secondaryText: binding.value.detail || Wire.text(binding.value.raw, 'action') }) })
            Text(Wire.text(binding.value.raw, 'text')).fontSize(Tokens.body).copyOption(CopyOptions.LocalDevice).width('100%')
            Text(Wire.text(binding.value.raw, 'at')).fontSize(Tokens.meta).width('100%')
            Text(Wire.text(Wire.object(binding.value.raw, 'origin'), 'session_id')).fontSize(Tokens.meta).width('100%')
          }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
        }.id('sent.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (Wire.identifier(this.management.data, 'next')) ListItem() { Button(tr('load_more')).enabled(!this.management.loading).onClick(() => this.management.loadMoreLedger(this.model)) }
      if (this.management.error) ListItem() { Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder appearancePage() {
    List() {
      DeviceLookPicker({ model: this.look, active: this.active, onAvatarSelected: (key: string): void => { this.look.setAvatar(key); },
        onAccentSelected: (id: string): void => { this.look.setAccent(id); }, onRefresh: (): void => { this.loadLook(); } })
      ListItem() {
        Row({ space: Tokens.gap }) {
          Text(tr('language')).fontSize(Tokens.body).layoutWeight(1)
          Select([{ value: tr('system') }, { value: 'English' }, { value: '简体中文' }, { value: '日本語' }])
            .selected(['default', 'en-US', 'zh-CN', 'ja-JP'].indexOf(this.model.language))
            .value(this.model.language === 'default' ? tr('system') : this.model.language === 'en-US' ? 'English' : this.model.language === 'zh-CN' ? '简体中文' : '日本語')
            .enabled(!this.model.languageChanging).onSelect((index: number) => this.model.setLanguage(['default', 'en-US', 'zh-CN', 'ja-JP'][index]).then(() => { if (index === 0 && this.model.language === 'default') this.getUIContext().getPromptAction().showToast({ message: tr('language_after_restart') }); })).id('appearance.language')
        }.padding(Tokens.inset).width('100%')
      }
      ListItem() {
        Row({ space: Tokens.gap }) {
          Text(tr('color_mode')).fontSize(Tokens.body).layoutWeight(1)
          Select([{ value: tr('system') }, { value: tr('light') }, { value: tr('dark') }]).selected(this.model.appearance).value(tr(['system', 'light', 'dark'][this.model.appearance] || 'system'))
            .onSelect((index: number) => this.model.setAppearance(index)).id('appearance.color-mode')
        }.padding(Tokens.inset).width('100%')
      }
      ListItem() {
        Row({ space: Tokens.gap }) {
          Text(tr('style')).fontSize(Tokens.body).layoutWeight(1)
          Select([{ value: tr('app_style_glass') }, { value: tr('app_style_brief') }, { value: tr('app_style_board') }])
            .selected(['glass', 'brief', 'board'].indexOf(this.model.style)).value(tr('app_style_' + this.model.style))
            .onSelect((index: number) => this.model.setStyle(['glass', 'brief', 'board'][index]))
        }.padding(Tokens.inset).width('100%')
      }
    }.width('100%').height('100%')
  }

  @Builder notificationsPage() {
    List() {
      ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('message_alerts') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: this.model.alerts && this.notifications.granted })
            .enabled(!this.notifications.changing && !this.model.demo)
            .onChange((value: boolean) => this.notifications.setEnabled(this.model, value))
            .accessibilityText(tr('message_alerts'))
        }
      }
      if (this.notifications.error) ListItem() { Text(tr(this.notifications.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.page) }
    }.width('100%').height('100%')
  }

  @Builder privacyPage() {
    List() {
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('hub'), secondaryText: this.model.profile.name }) }) }
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('manager'), secondaryText: this.model.profile.manager }) }) }
      ListItem() { Button(tr('forget_hub')).onClick(() => this.confirmForget()) }
      ListItem() { Button(tr('open_source_licenses')).onClick(() => this.licenseNoticesOpen = true) }
      if (this.model.pendingCleanupCount > 0) ListItem() { Button(tr('retry_cleanup')).onClick(() => this.model.retryCleanup()) }
    }.width('100%').height('100%')
  }

  @Builder licenseNoticesForm() { LicenseNotices() }

  @Builder candidateAddressesHeader() { Text(tr('candidate_addresses')).fontSize(Tokens.meta).padding(Tokens.page) }
  @Builder diagnosticsPage() {
    List() {
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('status'), secondaryText: tr(this.model.connected ? 'online' : 'offline') }) }) }
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('hub'), secondaryText: this.model.profile.hubId }) }) }
      ListItem() { Text(this.model.profile.baseUrl).copyOption(CopyOptions.LocalDevice).padding(Tokens.page) }
      if (this.model.profile.addresses.length > 0) ListItemGroup({ header: this.candidateAddressesHeader }) {
        ForEach(this.model.profile.addresses, (address: string) => { ListItem() { Text(address).copyOption(CopyOptions.LocalDevice).padding(Tokens.page) } }, (address: string) => address)
      }
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('features'), secondaryText: this.model.features.join(', ') }) }) }
      if (this.model.error) ListItem() { Text(tr(this.model.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      ListItem() { Button(tr('refresh')).onClick(() => { if (this.model.connected) this.model.refresh(); else this.model.foregrounded(); this.management.load(this.page, this.model); }) }
    }.width('100%').height('100%')
  }

  @Builder delegatedPage() {
    List() {
      ListItem() { Button(tr(this.page)).onClick(() => this.onOpen(this.page)) }
    }.width('100%').height('100%')
  }

  build() {
    Column() {
      if (this.management.loading) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
      if (this.page === 'models' || this.page === 'translation') this.modelsPage()
      else if (this.page === 'session_permissions') this.sessionModePage('permission_mode')
      else if (this.page === 'session_effort') this.sessionModePage('effort')
      else if (this.page === 'machines') this.machinesPage()
      else if (this.page === 'schedules') this.schedulesPage()
      else if (this.page === 'triage') this.triagePage()
      else if (this.page === 'extension_detail') this.extensionDetailPage()
      else if (this.page === 'connector_detail') this.connectorDetailPage()
      else if (this.page === 'channel_detail') this.channelDetailPage()
      else if (this.page === 'extension_catalog') this.extensionCatalogPage()
      else if (this.page === 'extensions' || this.page === 'connectors') this.extensionConnectorPage(this.page)
      else if (this.page === 'channels') this.channelsPage()
      else if (this.page === 'ledger') this.ledgerPage()
      else if (this.page === 'appearance') this.appearancePage()
      else if (this.page === 'notifications') this.notificationsPage()
      else if (this.page === 'privacy') this.privacyPage()
      else if (this.page === 'diagnostics') this.diagnosticsPage()
      else this.delegatedPage()
      if (this.management.error && !['models', 'session_permissions', 'session_effort', 'machines', 'schedules', 'triage', 'extensions', 'connectors', 'channels', 'ledger', 'extension_detail', 'connector_detail', 'channel_detail', 'extension_catalog'].includes(this.page))
        Text(tr(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
    }.width('100%').height('100%')
      .bindSheet($$this.extensionPreviewOpen, this.extensionPreviewForm(), { title: { title: tr('permissions') }, height: SheetSize.LARGE, showClose: true })
      .bindSheet($$this.licenseNoticesOpen, this.licenseNoticesForm(), { title: { title: tr('open_source_licenses') }, height: SheetSize.LARGE, showClose: true })
  }
}
