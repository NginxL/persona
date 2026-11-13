import { util } from '@kit.ArkTS';
import { AdminWorkspacePage } from '../components/AdminWorkspacePage';
import { ExtensionPage } from '../components/ExtensionPage';
import { ExtensionNavigationPage } from '../components/ExtensionNavigationPage';
import { ExtensionNavigation, ExtensionNavigationEntry } from '../models/ExtensionNavigation';
import { extensionIcon } from '../design/ExtensionIcons';
import type { ExtensionUINavigationEffect } from '../models/ExtensionUI';
import { ComputerScreenPage } from '../components/ComputerScreenPage';
import { isPairingAddress } from '../core/WireCompatibility';
import { scanBarcode, scanCore } from '@kit.ScanKit';
import { BusinessError } from '@kit.BasicServicesKit';
import { RemoteDocumentPage } from '../components/RemoteDocumentPage';
import { TodoWorkspacePage } from '../components/TodoWorkspacePage';
import { KnowledgeWorkspacePage } from '../components/KnowledgeWorkspacePage';
import { HealthWorkspacePage } from '../components/HealthWorkspacePage';
import { NavigationContext } from '../models/NavigationContext';
import { LinkReader } from '../components/LinkReader';
import { readerLink, readerHost } from '../platform/ReaderLink';
import { common } from '@kit.AbilityKit';
import { DevicePage } from '../components/DevicePage';
import { deviceFeatures } from '../models/DeviceFeatures';
import { SessionTools } from '../components/SessionTools';
import { SessionWorkspace } from '../models/SessionWorkspace';
import { hilog } from '@kit.PerformanceAnalysisKit';
import { ComposeListItemV2, ContentItemV2, KeyboardAvoidMode, SymbolGlyphModifier, CapsuleSegmentButtonV2, SegmentButtonV2Items } from '@kit.ArkUI';
import { ConfigurationConstant } from '@kit.AbilityKit';
import { ClientModel, client } from '../models/ClientModel';
import { ResourceRow, HubProfile, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Pairing, PairingTarget } from '../platform/Pairing';
import { Conversation } from '../components/Conversation';
import { AgentListPage } from '../components/AgentListPage';
import { InteractionHistoryPage } from '../components/InteractionHistoryPage';
import { InboxWorkspacePage } from '../components/InboxWorkspacePage';
import { ResourcePage } from '../components/ResourcePage';
import { SettingsPage } from '../components/SettingsPage';
import { SshSettingsPage } from '../components/SshSettingsPage';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { nativeLookTheme } from '../design/LookTheme';
import { IconTypeV2 } from '@kit.ArkUI';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { isHubStateKey } from '../core/StorageOwnership';

@Entry
@ComponentV2
struct Index {
  @Local model: ClientModel = client;
  @Local look: DeviceLook = deviceLook;
  @Local detailModel: ClientModel = client;
  @Local chatReady: boolean = true;
  @Local detailRoute: string = '';
  @Local adminRefreshRevision: number = 0;
  @Local inboxRefreshRevision: number = 0;
  @Local extensionTitle: string = '';
  @Local todoTitle: string = '';
  @Local extensionRefreshRevision: number = 0;
  @Local applications: ExtensionNavigation = new ExtensionNavigation();
  @Local shownName: string = '';
  @Local shownParam: string = '';
  @Local chatMenuActions: SheetInfo[] = [];
  @Local chatTitleMenuVisible: boolean = false;
  @Local chatBodyMenuVisible: boolean = false;
  @Local chatMenuRevision: number = 0;
  private routeRevision: number = 0;
  @Local stack: NavPathStack = new NavPathStack();
  @Local breakpoint: WidthBreakpoint = WidthBreakpoint.WIDTH_SM;
  @Local collapsed: boolean = false;
  @Local query: string = '';
  @Local filter: number = 0;
  @Local conversationRowBindings: BoundRow<ResourceRow>[] = [];
  private conversationBindingOwner: Object | undefined;
  private conversationBindingScope: string = '';
  @Local visibilityEpoch: number = 0;
  private rowVisibility: Map<string, number> = new Map();
  @Local readerAddress: string = '';
  @Local readerTitle: string = '';
  @Local readerShown: boolean = false;
  @Local sheetKind: string = '';
  @Local showSheet: boolean = false;
  @Local address: string = '';
  @Local pairCode: string = '';
  private pairingRevision: number = 0;
  @Local scanning: boolean = false;
  private scanRevision: number = 0;
  @Local resourceKind: string = 'todo';
  @Local newTask: string = '';
  @Local selectedMachine: number = 0;
  @Local creatingAgent: boolean = false;
  @Local agentModel: ClientModel = client;
  @Local agentTodo: ResourceRow | undefined = undefined;
  private agentFormRevision: number = 0;
  @Local launcher: string = '';
  @Local directory: string = '';
  @Local settingsPage: string = '';
  @Local activeChild: string = '';
  @Local tool: number = 0;
  private controllers: TabsController = new TabsController();
  private paths: Map<string, NavPathStack> = new Map();
  private activeScope: string = '';
  private restoring: number = 0;
  private labels(): string[] { return [tr('persona'), tr('agents'), tr('inbox'), tr('settings')]; }
  private wide(): boolean { return this.breakpoint === WidthBreakpoint.WIDTH_LG || this.breakpoint === WidthBreakpoint.WIDTH_XL; }
  private rail(): boolean { return this.wide() && this.collapsed; }
  private filtered(): ResourceRow[] {
    return this.model.sessions.filter((row: ResourceRow) =>
      (!this.query || (row.title + ' ' + row.path + ' ' + row.machine).toLowerCase().includes(this.query.toLowerCase())) &&
      (this.filter === 0 || this.filter === 1 && row.state === 'needs_you' || this.filter === 2 && row.state === 'working'));
  }
  private hideAgentTitle(): boolean {
    const epoch = this.visibilityEpoch;
    const ratio = this.rowVisibility.get(this.model.scope() + this.model.selected.key()) || 0;
    return this.detailModel === this.model && this.model.section === 1 && this.wide() && !this.rail() && this.model.selected.kind === 'session' && ratio >= 0.99;
  }
  private openReader(address: string): void { const safe = readerLink(address); if (safe) { this.readerAddress = safe; this.readerTitle = readerHost(safe); this.readerShown = true; } }
  @Builder readerContent() { LinkReader({ address: this.readerAddress, onHost: (host: string) => this.readerTitle = host }) }
  private openSheet(kind: string): void { if (kind === 'connect') this.pairingRevision++; this.readerShown = false; if (kind === 'newagent') { this.prepareAgent(); return; } this.sheetKind = kind; this.showSheet = true; }
  private sheetTitle(): string { return tr(this.sheetKind === 'hub' ? 'hub' : this.sheetKind === 'newagent' ? 'new_agent' : 'welcome'); }
  @Builder sheetContent() {
    if (this.sheetKind === 'hub') this.workspaceSheet()
    else if (this.sheetKind === 'newagent') this.newAgentForm()
    else this.pairingForm()
  }
  private updateBreakpoint(): void {
    const old = this.breakpoint; this.breakpoint = this.getUIContext().getWindowWidthBreakpoint();
    if (old !== this.breakpoint && this.wide() && this.model.section === 1 && this.stack.size() === 0 && this.model.sessions.length)
      this.openRow(this.model.sessions.find((row: ResourceRow) => row.key() === this.model.selected.key()) || this.model.sessions[0]);
  }
  private bindDeviceOwner(model: ClientModel): void {
    const host = this.getUIContext().getHostContext();
    if (host) deviceFeatures.bind(model, host as common.UIAbilityContext,
      (title: string, message: string, approve: () => void, deny: () => void): void => {
        this.getUIContext().showAlertDialog({ title: title, message: message,
          primaryButton: { value: tr('cancel'), action: deny }, secondaryButton: { value: tr('confirm'), action: approve } });
      });
  }
  private restoreDeviceOwner(owner: ClientModel): void { if (deviceFeatures.boundTo(owner)) this.bindDeviceOwner(this.model); }
  aboutToAppear(): void {
    this.syncConversationRows(); this.getUIContext().setKeyboardAvoidMode(KeyboardAvoidMode.RESIZE); this.updateBreakpoint(); this.consumePairingLink(); this.restoreScope(); this.consumeNotification();
    this.bindDeviceOwner(this.model);
    this.applications.bind(this.model); this.applications.refresh();
  }
  aboutToDisappear(): void { this.applications.dispose(); this.dismissChatActions(); }
  @Monitor('model.profile', 'model.connected', 'model.features') applicationOwnerChanged(): void {
    this.applications.bind(this.model); this.applications.connectionChanged();
  }
  @Monitor('model.profile') scopeChanged(): void { if (this.activeScope !== this.model.scope()) { this.dismissChatActions(); this.showSheet = false; this.agentFormRevision++; this.creatingAgent = false; this.agentTodo = undefined; this.readerShown = false; this.readerAddress = ''; this.readerTitle = ''; this.detailModel.hideConversation(); this.detailModel = this.model; this.chatReady = false; this.detailRoute = ''; this.routeRevision++; this.bindDeviceOwner(this.model); } this.syncConversationRows(); this.restoreScope(); deviceFeatures.context(); }
  @Monitor('model.conversations', 'model.mainId') conversationsChanged(): void { this.syncConversationRows(); }
  @Monitor('model.appearance') appearanceChanged(): void {
    this.getUIContext().getHostContext()?.getApplicationContext().setColorMode(this.model.appearance === 1 ? ConfigurationConstant.ColorMode.COLOR_MODE_LIGHT : this.model.appearance === 2 ? ConfigurationConstant.ColorMode.COLOR_MODE_DARK : ConfigurationConstant.ColorMode.COLOR_MODE_NOT_SET);
  }
  @Monitor('detailModel.connected') chatMenuConnectionChanged(): void { if (!this.detailModel.connected && (this.chatTitleMenuVisible || this.chatBodyMenuVisible)) this.dismissChatActions(); }
  @Monitor('model.connected') connectionChanged(): void { deviceFeatures.context(); if (this.model.connected && this.shownName && this.activeChild === this.childKey(this.shownName, this.shownParam)) this.restoreTarget(this.shownName, this.shownParam); }
  @Monitor('model.forgottenEpoch') forgetNavigation(): void {
    for (const key of this.paths.keys()) if (isHubStateKey('view:' + key, this.model.forgottenProfile)) this.paths.delete(key);
    this.rowVisibility.clear(); this.visibilityEpoch++;
  }
  private async restoreScope(): Promise<void> {
    const scope = this.model.scope(); if (scope === this.activeScope) return;
    this.activeScope = scope; this.rowVisibility.clear(); const epoch = ++this.restoring;
    const view = Wire.parse(await this.model.loadView('navigation'));
    if (epoch !== this.restoring || scope !== this.model.scope()) return;
    this.model.section = Math.max(0, Math.min(3, Wire.number(view, 'section')));
    Wire.rows(view, 'tabs').forEach((tab: Record<string, Object>) => {
      const key = scope + ':' + Wire.number(tab, 'index');
      if (!this.paths.has(key)) { const stack = new NavPathStack();
        Wire.rows(tab, 'routes').forEach((route: Record<string, Object>) => stack.pushPath({ name: Wire.text(route, 'name'), param: Wire.text(route, 'param') }, false));
        this.paths.set(key, stack);
      }
    });
    this.stack = this.paths.get(scope + ':' + this.model.section) || new NavPathStack();
    this.paths.set(scope + ':' + this.model.section, this.stack);
  }
  @Monitor('model.pairingLink') pairingChanged(): void { this.consumePairingLink(); }
  @Monitor('model.notificationTarget') notificationChanged(): void { this.consumeNotification(); }
  private consumeNotification(): void {
    if (!this.model.notificationTarget) return;
    const row = this.routeRow(this.model.notificationTarget); this.model.notificationTarget = '';
    if (!this.model.sessions.concat(this.model.conversations).some((item: ResourceRow) => item.key() === row.key())) return;
    this.changeSection(row.kind === 'conversation' ? 0 : 1); this.openRow(row);
  }
  private consumePairingLink(): void {
    if (!this.model.pairingLink) return; this.address = this.model.pairingLink; this.pairCode = ''; this.model.pairingLink = ''; this.openSheet('connect');
  }
  private savePath(): void {
    this.paths.set(this.model.scope() + ':' + this.model.section, this.stack);
    const tabs: Record<string, Object>[] = [];
    for (let index = 0; index < 4; index++) { const stack = this.paths.get(this.model.scope() + ':' + index);
      const routes: Record<string, Object>[] = [];
      stack?.getAllPathName().forEach((name: string, position: number) => routes.push(Wire.payload(['name', 'param'], [name, stack.getParamByIndex(position) as string || ''])));
      tabs.push(Wire.payload(['index', 'routes'], [index, routes]));
    }
    this.model.saveView('navigation', JSON.stringify(Wire.payload(['section', 'tabs'], [this.model.section, tabs]))).catch(() => this.model.error = 'failed');
  }
  private changeSection(index: number): void {
    this.savePath(); this.routeRevision++; this.activeChild = ''; this.shownName = ''; this.chatReady = false; this.detailRoute = ''; this.detailModel.hideConversation(); this.model.releaseTerminal(); this.model.section = index; this.stack = this.paths.get(this.model.scope() + ':' + index) || new NavPathStack(); this.query = ''; this.filter = 0;
    if (index === 1 && this.wide() && !this.stack.size() && this.model.sessions.length) this.openRow(this.model.sessions[0]);
    if (index === 2) this.model.listResources('inbox'); this.savePath();
  }
  private openRow(row: ResourceRow): void {
    const name = NavigationContext.destination(row);
    this.chatReady = false; this.detailRoute = '';
    if (row.kind === 'session') { this.stack.clear(); this.tool = 0; }
    this.stack.pushPath({ name: name, param: JSON.stringify(row) }); this.savePath();
  }
  private async openLinkedResource(row: ResourceRow): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), source = this.activeChild;
    try {
      if (!['inbox', 'todo', 'knowledge', 'file', 'todo_source', 'todo_thread'].includes(row.kind)) await this.model.viewFor(row);
      if (!this.model.owns(generation, scope) || this.activeChild !== source) return;
      this.chatReady = false; this.detailRoute = '';
      this.stack.pushPath({ name: NavigationContext.destination(row), param: JSON.stringify(row) }); this.savePath();
    } catch (error) { if (this.model.owns(generation, scope) && this.activeChild === source) this.model.reportError(error as Error); }
  }
  private openFrom(name: string, param: Object, row: ResourceRow): void {
    if (this.activeChild === this.childKey(name, param)) { if (!row.group) row.group = NavigationContext.from(name, param as string).row.group || this.model.profile.id; this.openLinkedResource(row); }
  }
  private createAgentFrom(name: string, param: Object, row: ResourceRow): void {
    if (this.activeChild === this.childKey(name, param)) this.prepareAgent(row);
  }
  private openComputer(row: ResourceRow): void {
    if (row.kind !== 'machine' || !row.id) return;
    const param = JSON.stringify(row), names = this.stack.getAllPathName(), top = names.length - 1;
    if (top >= 0 && names[top] === 'computer' && this.stack.getParamByIndex(top) === param) return;
    this.stack.pushPath({ name: 'computer', param: param }); this.savePath();
  }
  private selectedComputer(): ResourceRow | undefined {
    return this.detailModel.machines.find((row: ResourceRow) => row.id === this.detailModel.selected.machine);
  }
  private async handoffResource(row: ResourceRow): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), source = this.activeChild;
    try {
      const target = await this.model.handoff(row);
      if (this.model.owns(generation, scope) && this.activeChild === source) await this.openLinkedResource(target);
    } catch (error) { if (this.model.owns(generation, scope) && this.activeChild === source) this.model.reportError(error as Error); }
  }
  private childKey(name: string, param: Object): string { return name + ':' + (param as string); }
  private pushChat(row: ResourceRow): void { this.stack.pushPath({ name: 'conversation', param: JSON.stringify(row) }); this.savePath(); }
  private openMain(): void { const scope = this.model.scope(), generation = this.model.connectionGeneration(); this.model.openMain().then((row: ResourceRow) => { if (this.model.owns(generation, scope)) this.pushChat(row); }).catch((error: Error) => { if (this.model.owns(generation, scope)) this.model.reportError(error); }); }
  private newConversation(): void {
    const scope = this.model.scope(), generation = this.model.connectionGeneration();
    this.model.newConversation().then((row: ResourceRow) => { if (this.model.owns(generation, scope)) this.pushChat(row); })
      .catch((error: Error) => { if (this.model.owns(generation, scope)) this.model.reportError(error); });
  }
  private openResources(kind: string): void { this.resourceKind = kind; this.model.listResources(kind); this.stack.pushPath({ name: 'resources', param: kind }); this.savePath(); }
  private openSettings(page: string, contextual: boolean = false): void { this.settingsPage = page; this.stack.pushPath({ name: 'settings', param: NavigationContext.settings(page, contextual ? this.detailModel.selected : undefined) }); this.savePath(); }
  private settingsFamily(page: string): string { return ['extension_detail', 'extension_catalog'].includes(page) ? 'extensions' : page === 'connector_detail' ? 'connectors' : page === 'channel_detail' ? 'channels' : page; }
  private isAdminPage(page: string): boolean { return ['assistant', 'memory', 'managers', 'launchers', 'engine', 'hub_update', 'node_update'].includes(page); }
  private settingsLabel(page: string): string { return page === 'ssh' ? 'ssh_settings' : page === 'assistant' ? 'admin_identity' : page === 'memory' ? 'admin_memory' : page === 'managers' ? 'admin_managers' : page === 'launchers' ? 'admin_launchers' : page === 'engine' ? 'admin_machine_engine' : page === 'hub_update' ? 'admin_hub_update' : page === 'node_update' ? 'admin_node_update' : page; }
  private adminMenus(name: string, param: Object): NavigationMenuItem[] { return [{ value: tr('refresh'), action: () => { if (this.activeChild === this.childKey(name, param)) this.adminRefreshRevision++; } }]; }
  private settingsTitle(param: Object): string { const descriptor = NavigationContext.from('settings', param as string); return descriptor.scoped && ['extension_detail', 'connector_detail', 'channel_detail', 'extension_catalog'].includes(descriptor.page) ? descriptor.row.title : tr(this.settingsLabel(descriptor.page)); }
  private openSettingsDetail(page: string, row: ResourceRow): void { this.stack.pushPath({ name: 'settings', param: NavigationContext.settings(page, row) }); this.savePath(); }
  private openApplications(): void {
    const param = JSON.stringify([this.model.profile.id, this.model.profile.manager]);
    const found = this.stack.getAllPathName().findIndex((name: string): boolean => name === 'applications');
    if (found >= 0) this.stack.popToIndex(found);
    else this.stack.pushPath({ name: 'applications', param: param });
    this.savePath(); this.applications.refresh();
  }
  private openApplication(entry: ExtensionNavigationEntry): void {
    if (!this.applications.accepts(entry)) return;
    const found = this.stack.getAllPathName().findIndex((name: string): boolean => name === 'applications');
    if (found < 0) return;
    this.stack.popToIndex(found);
    this.openExtension(this.model, entry.extensionId, entry.uiId, entry.title, '', {}, entry.extensionId, entry.uiId);
  }
  private applicationAllowed(param: Object): boolean {
    const row = this.routeRow(param), data = row.data(), root = Wire.text(data, 'navigation_extension');
    return !root || this.applications.allows(row.group, row.manager, root, Wire.text(data, 'navigation_ui'));
  }
  private openExtension(owner: ClientModel, extensionId: string, uiId: string, title: string, state: string = '', context: Record<string, Object> = Wire.payload([], []), navigationExtension: string = '', navigationUI: string = ''): void {
    if (!extensionId || !uiId || !owner.profile.id) return;
    if (this.stack.getAllPathName().filter((name: string) => name === 'extension').length >= 8) { this.getUIContext().getPromptAction().showToast({ message: tr('extension_ui_stack_limit') }); return; }
    const row = new ResourceRow(extensionId, title || extensionId, 'extension'); row.group = owner.profile.id; row.manager = owner.profile.manager;
    row.raw = JSON.stringify(Wire.payload(['ui', 'state', 'context', 'instance', 'navigation_extension', 'navigation_ui'], [uiId, state, context, util.generateRandomUUID(), navigationExtension, navigationUI]));
    this.stack.pushPath({ name: 'extension', param: JSON.stringify(row) }); this.savePath();
  }
  private extensionNavigate(name: string, param: Object, effect: ExtensionUINavigationEffect): void {
    if (this.activeChild !== this.childKey(name, param) || !this.applicationAllowed(param) || effect.kind !== 'push') return;
    const source = this.routeRow(param).data();
    this.openExtension(this.detailModel, effect.extensionId, effect.uiId, effect.title, effect.state, effect.context, Wire.text(source, 'navigation_extension'), Wire.text(source, 'navigation_ui'));
  }
  private extensionMenus(name: string, param: Object): NavigationMenuItem[] { return [{ value: tr('refresh'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.arrow_clockwise')), action: () => { if (this.activeChild === this.childKey(name, param)) this.extensionRefreshRevision++; } }]; }
  private settingsKind(param: Object): string { return NavigationContext.from('settings', param as string).page; }
  private targetReady(name: string, param: Object): boolean { return this.chatReady && this.detailRoute === this.childKey(name, param); }
  private settingsModel(param: Object): ClientModel { return NavigationContext.from('settings', param as string).scoped ? this.detailModel : this.model; }
  private async connect(): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), address = this.address, code = this.pairCode, revision = this.pairingRevision;
    const current = (): boolean => this.showSheet && this.sheetKind === 'connect' && revision === this.pairingRevision && this.model.owns(generation, scope) && this.address === address && this.pairCode === code;
    this.model.busy = true; this.model.error = '';
    try {
      const pairing = isPairingAddress(this.address.trim()) ? Pairing.parse(this.address.trim()) : Pairing.manual(this.address.trim(), this.pairCode.trim());
      const target = await pairing.inspect(); if (!current()) return; this.model.busy = false;
      this.getUIContext().showAlertDialog({ title: tr('verify'), message: target.name + '\n' + target.baseUrl + '\n' + target.hubId,
        primaryButton: { value: tr('cancel'), action: () => {} },
        secondaryButton: { value: tr('trust'), action: () => { if (current()) this.redeem(pairing, target, current); } } });
    } catch (error) {
      if (!current()) return;
      const detail = error as Error as Object as Record<string, Object>; const code = detail['code'];
      this.model.diagnosticCode = error instanceof Error && /^[a-z_0-9]{1,64}$/.test(error.message) ? error.message : typeof code === 'number' ? String(code) : 'pairing_failed';
      hilog.error(0x0000, 'PersonaPair', '%{public}s', this.model.diagnosticCode);
      this.model.error = 'connection_failed';
    } finally { if (this.model.owns(generation, scope)) this.model.busy = false; }
  }
  private async redeem(pairing: Pairing, target: PairingTarget, admit: () => boolean): Promise<void> {
    const revision = this.pairingRevision, address = this.address, code = this.pairCode;
    try {
      await this.model.pair(pairing, target, admit);
      if (this.sheetKind === 'connect' && revision === this.pairingRevision && this.address === address && this.pairCode === code && this.model.profile.id === target.hubId) { this.pairCode = ''; this.address = ''; this.showSheet = false; }
    } catch (_) { if (admit()) this.model.error = 'connection_failed'; }
  }
  private menus(): NavigationMenuItem[] {
    const result: NavigationMenuItem[] = [];
    if (!this.rail()) result.push({ value: this.model.profile.name || tr('hub'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.trunk')), action: () => this.openSheet('hub') });
    if (!this.rail() && this.applications.visible) result.push({ value: tr('applications'), symbolIcon: new SymbolGlyphModifier(extensionIcon('plugin')), action: () => this.openApplications() });
    if (this.model.section === 1 && !this.rail()) result.push({ value: tr('new_agent'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.plus')), action: () => this.openSheet('newagent') });
    if (this.wide() && !this.rail()) result.push({ value: tr('collapse'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.chevron_left')), action: () => this.collapsed = true });
    return result;
  }
  private openHistory(): void { this.stack.pushPath({ name: 'history', param: JSON.stringify(this.detailModel.selected) }); this.savePath(); }
  private openTools(path: string = ''): void { const data = Wire.record(JSON.parse(JSON.stringify(this.detailModel.selected)) as Object); if (path) data['open_file'] = path; this.stack.pushPath({ name: 'tools', param: JSON.stringify(data) }); this.savePath(); }
  private destinationMenuItems(name: string, param: Object): NavigationMenuItem[] {
    if (name === 'settings' && this.isAdminPage(this.settingsKind(param))) return this.adminMenus(name, param);
    if (name === 'extension' && this.applicationAllowed(param)) return this.extensionMenus(name, param);
    if (name === 'session' || name === 'conversation') return [{ value: tr('more'), icon: $r('sys.media.ohos_ic_public_more'), isEnabled: this.targetReady(name, param), action: () => this.showChatActions(name, param) }];
    return [];
  }
  private chatMenuOptions(revision: number): MenuOptions {
    return { onDisappear: () => this.closeChatActions(revision) };
  }
  @Builder chatActionsMenu() {
    Menu() {
      ForEach(this.chatMenuActions, (item: SheetInfo) => {
        MenuItem({ content: item.title }).constraintSize({ minHeight: Tokens.touch }).onClick(() => item.action())
      }, (_item: SheetInfo, index: number) => String(this.chatMenuRevision) + ':' + String(index))
    }
  }
  private closeChatActions(revision: number): void {
    if (revision !== this.chatMenuRevision) return;
    this.chatTitleMenuVisible = false; this.chatBodyMenuVisible = false;
  }
  private dismissChatActions(): void {
    this.chatMenuRevision++; this.chatTitleMenuVisible = false; this.chatBodyMenuVisible = false; this.chatMenuActions = [];
  }
  private showChatActions(name: string, param: Object): void {
    const owner = this.detailModel, key = owner.selected.key(), generation = owner.connectionGeneration(), scope = owner.scope();
    const valid = (): boolean => this.activeChild === this.childKey(name, param) && this.targetReady(name, param) &&
      this.detailModel === owner && owner.owns(generation, scope) && owner.selected.key() === key;
    if (!valid()) { this.dismissChatActions(); return; }
    const sheets: SheetInfo[] = [];
    if (owner.isChat() && owner.selected.machine) sheets.push({ title: tr('interaction_history'), action: () => { if (valid()) this.openHistory(); } });
    const computer = this.selectedComputer();
    if (computer) sheets.push({ title: tr('computer_screen_open'), action: () => { if (valid()) this.openComputer(computer); } });
    if (owner.selected.machine) sheets.push({ title: tr('workspace'), action: () => { if (valid()) this.openTools(); } });
    for (const page of ['model', 'permissions', 'effort', 'voice']) sheets.push({ title: tr(page), action: () => {
      if (valid()) this.openSettings(page === 'model' ? 'models' : page === 'permissions' ? 'session_permissions' : page === 'effort' ? 'session_effort' : page, true);
    } });
    if (owner.canInterrupt()) sheets.push({ title: tr('stop'), action: () => { if (valid()) owner.stop(); } });
    this.chatMenuRevision++; this.chatMenuActions = sheets;
    const body = name === 'session' && this.hideAgentTitle();
    this.chatTitleMenuVisible = !body; this.chatBodyMenuVisible = body;
  }
  private syncConversationRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager]);
    const rows = this.model.conversations.filter((row: ResourceRow) => row.id !== this.model.mainId);
    this.conversationRowBindings = reconcileBoundRows(this.conversationRowBindings, rows,
      (row: ResourceRow) => row.key(), this.conversationBindingOwner, this.model, this.conversationBindingScope, scope);
    this.conversationBindingOwner = this.model;
    this.conversationBindingScope = scope;
  }
  @Builder row(binding: BoundRow<ResourceRow>) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || binding.value.id,
      secondaryText: binding.value.kind === 'session' ? [this.model.machineName(binding.value.machine), this.model.shortPath(binding.value.path), binding.value.state ? tr(binding.value.state) : ''].filter((value: string) => value.length > 0).join(' · ') : binding.value.detail || undefined }) })
  }
  @Builder state() {
    if (this.model.busy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
    if (this.model.error) {
      ListItem() { Text(tr(this.model.error)).fontSize(Tokens.meta).padding(Tokens.page) }
    }
  }
  @Builder connectionState() {
    Row({ space: Tokens.gap }) { Text(tr(this.model.error)).fontSize(Tokens.meta).layoutWeight(1)
      Button(tr('retry')).onClick(() => this.model.connected ? this.model.refresh() : this.model.foregrounded())
    }.padding(Tokens.inset)
  }
  @Builder personaList() {
    List() {
      if (this.model.error) ListItem() { this.connectionState() }
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('main_chat'),
        icon: this.look.avatarUri || $r('app.media.foreground'), iconStyle: IconTypeV2.HEAD_SCULPTURE }) }) }.onClick(() => this.openMain()).id('persona.main')
      ForEach(this.conversationRowBindings, (binding: BoundRow<ResourceRow>) => {
        ListItem() { this.row(binding) }.onClick(() => this.openRow(binding.value))
      }, (binding: BoundRow<ResourceRow>) => binding.key)
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('new_chat') }) }) }
        .onClick(() => this.newConversation())
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('todos') }) }) }.onClick(() => this.openResources('todo'))
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('knowledge') }) }) }.onClick(() => this.openResources('knowledge'))
      ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('health_home_title') }) }) }.onClick(() => this.openResources('health'))
    }.width('100%').height('100%').padding({ bottom: Tokens.listTail })
  }
  @Builder agentsList() {
    AgentListPage({ model: this.model, active: this.model.section === 1, selectedRow: this.detailModel.selected, rail: this.rail(), readingDetail: this.model.section === 1,
      onOpen: (row: ResourceRow) => this.openRow(row), onVisible: (row: ResourceRow, ratio: number) => {
        this.rowVisibility.set(this.model.scope() + row.key(), ratio); if (row.key() === this.detailModel.selected.key()) this.visibilityEpoch++;
      } })
  }
  @Builder inboxList() {
    InboxWorkspacePage({ model: this.model, visible: this.model.section === 2, refreshRevision: this.inboxRefreshRevision, onChanged: () => this.inboxRefreshRevision++, onOpen: (row: ResourceRow) => this.openRow(row), onLink: (address: string) => this.openReader(address),
      onHandoff: (row: ResourceRow) => this.openLinkedResource(row) })
  }
  @Builder settingsList() {
    List() {
      ForEach(['assistant', 'memory', 'machines', 'managers', 'launchers', 'engine', 'models', 'schedules', 'extensions', 'connectors', 'channels', 'translation', 'triage', 'ledger', 'phone', 'ssh', 'appearance', 'notifications', 'hub_update', 'node_update', 'privacy', 'diagnostics'], (page: string) => {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr(this.settingsLabel(page)) }) }) }.selectable(true).selected(!!this.activeChild && (this.shownName === 'extension' ? page === 'extensions' : this.shownName === 'settings' && this.settingsFamily(this.settingsKind(this.shownParam)) === page)).onClick(() => this.openSettings(page)).id('settings.' + page)
      }, (page: string) => page)
    }.width('100%').height('100%').padding({ bottom: Tokens.listTail })
  }
  @Builder primaryTabs() {
    Tabs({ barPosition: BarPosition.End, index: this.model.section, controller: this.controllers }) {
      TabContent() { this.personaList() }.tabBar(BottomTabBarStyle.of({ normal: new SymbolGlyphModifier($r('sys.symbol.wand_and_stars')) }, tr('persona')))
      TabContent() { this.agentsList() }.tabBar(BottomTabBarStyle.of({ normal: new SymbolGlyphModifier($r('sys.symbol.computer')) }, tr('agents')))
      TabContent() { this.inboxList() }.tabBar(BottomTabBarStyle.of({ normal: new SymbolGlyphModifier($r('sys.symbol.envelope')) }, tr('inbox')))
      TabContent() { this.settingsList() }.tabBar(BottomTabBarStyle.of({ normal: new SymbolGlyphModifier($r('sys.symbol.gearshape')) }, tr('settings')))
    }.onChange((index: number) => { if (index !== this.model.section) this.changeSection(index); })
      .background($r('app.color.persona_canvas')).barHeight(this.rail() ? 0 : Tokens.tabHeight)
      .barOverlap(!this.rail()).barFloatingStyle(this.rail() ? undefined : { barSideMargin: Tokens.gap, barBottomMargin: Tokens.tabMargin })
      .width('100%').height('100%')
  }
  @Builder workspaceSheet() {
    List() {
      ForEach(this.model.profiles, (profile: HubProfile) => { ListItem() { Text(profile.name).padding(Tokens.page) }
        .onClick(() => { this.model.selectProfile(profile); this.showSheet = false; }) }, (profile: HubProfile) => profile.id)
      ForEach(this.model.managers, (manager: ResourceRow) => { ListItem() { Text(manager.title).padding(Tokens.page) }
        .onClick(() => { this.model.selectManager(manager.id); this.showSheet = false; }) }, (manager: ResourceRow) => manager.id)
      ListItem() { Button(tr('connect')).onClick(() => { this.showSheet = false; this.openSheet('connect'); }) }
    }.width('100%')
  }
  private async scanPairing(): Promise<void> {
    const host = this.getUIContext().getHostContext(); if (!host || this.scanning) return;
    const revision = ++this.scanRevision, generation = this.model.connectionGeneration(), scope = this.model.scope(); this.scanning = true;
    try {
      const result = await scanBarcode.startScanForResult(host, { scanTypes: [scanCore.ScanType.QR_CODE], enableAlbum: true, enableMultiMode: false });
      if (revision !== this.scanRevision || !this.showSheet || this.sheetKind !== 'connect' || !this.model.owns(generation, scope)) return;
      Pairing.parse(result.originalValue); this.address = result.originalValue; this.pairCode = ''; this.model.error = '';
    } catch (error) { if (revision === this.scanRevision && (error as BusinessError).code !== 1000500002) this.model.error = 'connection_failed'; }
    finally { if (revision === this.scanRevision) this.scanning = false; }
  }
  @Builder pairingForm() {
    Column({ space: Tokens.page }) {
      if (canIUse('SystemCapability.Multimedia.Scan.ScanBarcode')) Button(tr('scan_qr')).enabled(!this.scanning && !this.model.busy).onClick(() => this.scanPairing()).id('pair.scan')
      TextInput({ text: this.address, placeholder: tr('hub_address') }).onChange((value: string) => this.address = value).id('pair.address')
      TextInput({ text: this.pairCode, placeholder: tr('pair_code') }).type(InputType.Password).onChange((value: string) => this.pairCode = value).id('pair.code')
      if (this.model.error) Text(tr(this.model.error)).fontColor($r('app.color.persona_danger'))
      Button(tr('verify')).enabled(!!this.address.trim() && (!!this.pairCode.trim() || isPairingAddress(this.address.trim())) && !this.model.busy).onClick(() => this.connect()).id('pair.verify')
    }.padding(Tokens.page).width('100%')
  }
  @Builder newAgentForm() {
    Column({ space: Tokens.page }) {
      TextArea({ text: this.newTask, placeholder: tr('task') }).onChange((value: string) => { this.newTask = value; this.saveAgentDraft(); }).height(Tokens.inputExpanded).id('agent.task')
      Select(this.agentMachines().map((row: ResourceRow): SelectOption => ({ value: row.title || row.id }))).selected(this.selectedMachine).value(this.agentMachines()[this.selectedMachine]?.title || this.agentMachines()[this.selectedMachine]?.id || tr('machines'))
        .onSelect((index: number) => this.selectedMachine = index).id('agent.machine')
      TextInput({ text: this.directory, placeholder: tr('directory') }).onChange((value: string) => { this.directory = value; this.saveAgentDraft(); }).id('agent.directory')
      TextInput({ text: this.launcher, placeholder: tr('launcher') }).onChange((value: string) => { this.launcher = value; this.saveAgentDraft(); }).id('agent.launcher')
      if (this.agentModel.error) Text(tr(this.agentModel.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      Button(tr(this.creatingAgent || this.agentModel.operationBlocked('/api/v1/sessions') ? 'pending' : 'create')).enabled(!this.creatingAgent && !this.agentModel.operationBlocked('/api/v1/sessions') && this.newTask.trim().length > 0 && this.agentMachines().length > 0)
        .onClick(() => this.confirmCreateAgent()).id('agent.create')
    }.padding(Tokens.page)
  }
  private agentDraftKey(): string { return 'new-agent:' + (this.agentTodo?.id || 'standalone'); }
  private async prepareAgent(todo?: ResourceRow): Promise<void> {
    const revision = ++this.agentFormRevision, generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.readerShown = false; this.creatingAgent = false; this.selectedMachine = 0; this.newTask = ''; this.directory = ''; this.launcher = ''; this.agentTodo = todo;
    try {
      const owner = todo ? await this.model.viewFor(todo) : this.model;
      if (revision !== this.agentFormRevision || !this.model.owns(generation, scope)) return;
      this.agentModel = owner;
      const raw = await owner.loadView(this.agentDraftKey());
      if (revision !== this.agentFormRevision || !this.model.owns(generation, scope)) return;
      const saved = Wire.parse(raw); this.newTask = Wire.text(saved, 'task', todo ? todo.title + (Wire.text(todo.data(), 'notes') ? '\n\n' + Wire.text(todo.data(), 'notes') : '') : '');
      this.directory = Wire.text(saved, 'directory'); this.launcher = Wire.text(saved, 'launcher');
      this.sheetKind = 'newagent'; this.showSheet = true;
    } catch (error) { if (revision === this.agentFormRevision && this.model.owns(generation, scope)) this.model.reportError(error as Error); }
  }
  private saveAgentDraft(): void {
    if (this.sheetKind !== 'newagent' || !this.showSheet) return;
    const owner = this.agentModel, generation = owner.connectionGeneration(), scope = owner.scope();
    owner.saveView(this.agentDraftKey(), JSON.stringify(Wire.payload(['task', 'directory', 'launcher'], [this.newTask, this.directory, this.launcher])))
      .catch(() => { if (owner.owns(generation, scope)) owner.error = 'cache_unavailable'; });
  }
  private agentMachines(): ResourceRow[] { return this.agentModel.machines.filter((row: ResourceRow) => row.manager === this.agentModel.profile.manager && Wire.text(row.data(), 'trust') !== 'pending' && !Wire.bool(row.data(), 'pending_confirm') && !Wire.bool(row.data(), 'pending') && Wire.bool(row.data(), 'online')); }
  private agentFingerprint(): string { return JSON.stringify([this.agentModel.scope(), this.agentTodo?.id || '', this.agentMachines()[this.selectedMachine]?.id || '', this.newTask, this.directory, this.launcher]); }
  private confirmCreateAgent(): void {
    const machine = this.agentMachines()[this.selectedMachine]; if (!machine || this.creatingAgent) return;
    const revision = this.agentFormRevision, fingerprint = this.agentFingerprint(), owner = this.agentModel, generation = owner.connectionGeneration(), scope = owner.scope();
    this.getUIContext().showAlertDialog({ title: tr('new_agent'), message: owner.profile.name + ' · ' + owner.profile.manager + '\n' + (machine.title || machine.id) + '\n' + this.directory + '\n\n' + this.newTask,
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('create'), action: () => {
        if (revision === this.agentFormRevision && owner.owns(generation, scope) && fingerprint === this.agentFingerprint()) this.createAgent();
      } } });
  }
  private async createAgent(): Promise<void> {
    const machine = this.agentMachines()[this.selectedMachine]; if (!machine || this.creatingAgent) return;
    const owner = this.agentModel, generation = owner.connectionGeneration(), scope = owner.scope(), rootGeneration = this.model.connectionGeneration(), rootScope = this.model.scope(), revision = this.agentFormRevision, fingerprint = this.agentFingerprint(), draftKey = this.agentDraftKey();
    const valid = (): boolean => owner.owns(generation, scope) && this.model.owns(rootGeneration, rootScope) && revision === this.agentFormRevision && this.showSheet && this.sheetKind === 'newagent' && fingerprint === this.agentFingerprint();
    this.creatingAgent = true;
    try {
      const row = await owner.createAgent(this.newTask, machine, this.directory, this.launcher, this.agentTodo?.id || '', valid);
      if (!valid()) return;
      this.showSheet = false; this.newTask = ''; owner.saveView(draftKey, '').catch(() => {}); this.openRow(row);
    } catch (error) { if (valid()) owner.reportError(error as Error); }
    finally { if (revision === this.agentFormRevision) this.creatingAgent = false; }
  }
  private routeRow(param: Object): ResourceRow { return NavigationContext.rowFrom(param as string); }
  private chatTitle(param: Object): string {
    const row = this.routeRow(param);
    const current = this.detailModel.sessions.concat(this.detailModel.conversations).find((item: ResourceRow) => item.key() === row.key());
    return current?.title || row.title || tr(row.kind === 'conversation' ? 'main_chat' : 'agents');
  }
  private async restoreTarget(name: string, param: Object): Promise<void> {
    const descriptor = NavigationContext.from(name, param as string);
    if (!descriptor.scoped) return;
    const key = this.childKey(name, param), revision = ++this.routeRevision, generation = this.model.connectionGeneration(), scope = this.model.scope();
    const keepContent = this.targetReady(name, param);
    if (!keepContent) { this.chatReady = false; this.detailRoute = ''; }
    try {
      let row = descriptor.row; const view = await this.model.viewFor(row);
      if (['session', 'conversation'].includes(row.kind) && !row.machine) {
        let resolved = view.conversations.concat(view.sessions).find((item: ResourceRow) => item.id === row.id || item.session === row.session && !!row.session);
        if (!resolved && view.connected) { await view.refresh(); resolved = view.conversations.concat(view.sessions).find((item: ResourceRow) => item.id === row.id || item.session === row.session && !!row.session); }
        if (!resolved) throw new Error('request_updated'); row = resolved;
      }
      if (revision !== this.routeRevision || this.activeChild !== key || !this.model.owns(generation, scope)) return;
      if (this.detailModel !== view) this.detailModel.hideConversation();
      this.detailModel = view;
      if (row.key() !== view.selected.key()) await view.select(row);
      else if (name === 'session' || name === 'conversation') { if (view.connected) view.showConversation(row); }
      if (revision !== this.routeRevision || this.activeChild !== key || !this.model.owns(generation, scope) || view.selected.key() !== row.key()) return;
      this.detailRoute = key; this.chatReady = true;
      if (name === 'settings' && descriptor.page === 'voice') view.watchDecisions(true);
    } catch (error) { if (revision === this.routeRevision && this.activeChild === key && this.model.owns(generation, scope)) this.model.reportError(error as Error); }
  }
  private routeShown(name: string, param: Object): void {
    if (this.activeChild !== this.childKey(name, param)) this.dismissChatActions();
    this.activeChild = this.childKey(name, param); if (name === 'extension') this.extensionTitle = this.routeRow(param).title; if (name === 'todo') this.todoTitle = this.routeRow(param).title; this.shownName = name; this.shownParam = param as string;
    if (!['session', 'conversation'].includes(name)) this.detailModel.hideConversation(name !== 'tools');
    if (name === 'applications') this.applications.refresh();
    const descriptor = NavigationContext.from(name, param as string);
    if (descriptor.scoped) this.restoreTarget(name, param);
    else { this.routeRevision++; this.chatReady = true; this.detailRoute = this.activeChild; if (name === 'resources') this.model.listResources(param as string); }
  }
  @Builder targetLoading(name: string, param: Object) {
    Column({ space: Tokens.gap }) {
      if (this.model.error) {
        Text(tr(this.model.error)).fontSize(Tokens.meta)
        Button(tr('retry')).onClick(() => this.restoreTarget(name, param))
      } else LoadingProgress().width(Tokens.touch).height(Tokens.touch)
    }.width('100%').height('100%').justifyContent(FlexAlign.Center)
  }
  @Builder destination(name: string, param: Object) {
    NavDestination() {
      if (name === 'session' || name === 'conversation') {
        if (this.targetReady(name, param)) Conversation({ model: this.detailModel, onOpenFile: (path: string) => this.openTools(path), active: this.activeChild === this.childKey(name, param), onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address), showContextMenu: name === 'session' && this.hideAgentTitle(), onShowActions: () => this.showChatActions(name, param), actionItems: this.chatMenuActions, actionsVisible: this.chatBodyMenuVisible && this.activeChild === this.childKey(name, param), actionsRequest: this.chatMenuRevision, onHideActions: (request: number) => this.closeChatActions(request) })
        else this.targetLoading(name, param)
      } else if (name === 'history') {
        if (this.targetReady(name, param)) InteractionHistoryPage({ model: this.detailModel, active: this.activeChild === this.childKey(name, param), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'tools') {
        if (this.targetReady(name, param)) SessionTools({ model: this.detailModel, onOpenLink: (address: string) => this.openReader(address), initialFile: Wire.text(Wire.parse(param as string), 'open_file'), onWorkspaceReady: (workspace: SessionWorkspace | undefined) => this.detailModel.setWorkspace(workspace) })
        else this.targetLoading(name, param)
      } else if (name === 'applications') {
        ExtensionNavigationPage({ directory: this.applications, onOpen: (entry: ExtensionNavigationEntry): void => { if (this.activeChild === this.childKey(name, param)) this.openApplication(entry); } })
      } else if (name === 'extension') {
        Column() {
          if (!this.applicationAllowed(param)) Row({ space: Tokens.gap }) {
            Text(tr('application_unavailable')).fontSize(Tokens.meta).layoutWeight(1)
            Button(tr('retry')).height(Tokens.touch).enabled(!this.applications.loading).onClick(() => this.applications.refresh())
          }.width('100%').padding(Tokens.page)
          Column() {
        if (this.targetReady(name, param)) ExtensionPage({ model: this.detailModel, extensionId: this.routeRow(param).id, uiId: Wire.text(this.routeRow(param).data(), 'ui'), state: Wire.text(this.routeRow(param).data(), 'state'), context: Wire.object(this.routeRow(param).data(), 'context'), active: this.activeChild === this.childKey(name, param), authorized: this.applicationAllowed(param), refreshRevision: this.extensionRefreshRevision + (Wire.text(this.routeRow(param).data(), 'navigation_extension') ? this.applications.revision : 0), onTitle: (title: string) => { if (this.activeChild === this.childKey(name, param)) this.extensionTitle = title; }, onNavigate: (effect: ExtensionUINavigationEffect) => this.extensionNavigate(name, param, effect), onLink: (address: string) => { if (this.activeChild === this.childKey(name, param)) this.openReader(address); }, onBack: () => { if (this.activeChild === this.childKey(name, param)) { this.stack.pop(); this.savePath(); } } })
            else this.targetLoading(name, param)
          }.width('100%').layoutWeight(1).visibility(this.applicationAllowed(param) ? Visibility.Visible : Visibility.None)
        }.width('100%').height('100%')
      } else if (name === 'computer') {
        if (this.targetReady(name, param)) ComputerScreenPage({ model: this.detailModel, row: this.routeRow(param), active: this.activeChild === this.childKey(name, param), onClose: () => { if (this.activeChild === this.childKey(name, param)) { this.stack.pop(); this.savePath(); } } })
        else this.targetLoading(name, param)
      } else if (name === 'file') {
        if (this.targetReady(name, param)) RemoteDocumentPage({ model: this.detailModel, row: this.routeRow(param), active: this.activeChild === this.childKey(name, param), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'inbox' || name === 'todo_source' || name === 'todo_thread' || name === 'resources' && param === 'inbox') {
        if (name === 'resources' || this.targetReady(name, param)) InboxWorkspacePage({ model: name === 'resources' ? this.model : this.detailModel, row: name === 'resources' ? undefined : this.routeRow(param), list: name === 'resources', visible: this.activeChild === this.childKey(name, param), refreshRevision: this.inboxRefreshRevision,
          onChanged: () => { if (this.activeChild === this.childKey(name, param)) this.inboxRefreshRevision++; }, onOpen: (row: ResourceRow) => this.openFrom(name, param, row), onHandoff: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'todo' || name === 'resources' && param === 'todo') {
        if (name === 'resources' || this.targetReady(name, param)) TodoWorkspacePage({ model: name === 'resources' ? this.model : this.detailModel, row: name === 'todo' ? this.routeRow(param) : undefined, list: name === 'resources', visible: this.activeChild === this.childKey(name, param),
          onTitle: (title: string) => { if (name === 'todo' && this.activeChild === this.childKey(name, param)) this.todoTitle = title; }, onOpen: (row: ResourceRow) => this.openFrom(name, param, row), onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address), onCreateAgent: (todo: ResourceRow) => this.createAgentFrom(name, param, todo) })
        else this.targetLoading(name, param)
      } else if (name === 'knowledge' || name === 'resources' && param === 'knowledge') {
        if (name === 'resources' || this.targetReady(name, param)) KnowledgeWorkspacePage({ model: name === 'resources' ? this.model : this.detailModel, row: name === 'knowledge' ? this.routeRow(param) : undefined, visible: this.activeChild === this.childKey(name, param),
          onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onChat: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'resources' && param === 'health') {
        HealthWorkspacePage({ model: this.model, onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onChat: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
      } else if (name === 'resources' || name === 'resource') {
        if (name === 'resources' || this.targetReady(name, param)) ResourcePage({ model: name === 'resources' ? this.model : this.detailModel, onLink: (address: string) => this.openReader(address), kind: name === 'resources' ? param as string : this.routeRow(param).kind, list: name === 'resources', onOpen: (row: ResourceRow) => this.openFrom(name, param, row),
          onHandoff: (row: ResourceRow) => this.handoffResource(row) })
        else this.targetLoading(name, param)
      } else if (name === 'settings' && (this.settingsKind(param) === 'phone' || this.settingsKind(param) === 'voice')) {
        if (!NavigationContext.from(name, param as string).scoped || this.targetReady(name, param)) DevicePage({ model: this.settingsModel(param), page: this.settingsKind(param), active: this.activeChild === this.childKey(name, param), onLeave: (owner: ClientModel) => this.restoreDeviceOwner(owner), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'settings' && this.isAdminPage(this.settingsKind(param))) {
        if (!NavigationContext.from(name, param as string).scoped || this.targetReady(name, param)) AdminWorkspacePage({ model: this.settingsModel(param), page: this.settingsKind(param), row: NavigationContext.from(name, param as string).scoped ? NavigationContext.from(name, param as string).row : undefined, active: this.activeChild === this.childKey(name, param), refreshRevision: this.adminRefreshRevision,
          onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onOpenMachine: (row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openSettingsDetail('machines', row); } })
        else this.targetLoading(name, param)
      } else if (name === 'settings' && this.settingsKind(param) === 'ssh') {
        SshSettingsPage({ tunnel: this.settingsModel(param).ssh, active: this.activeChild === this.childKey(name, param),
          onSaved: (): void => { const owner = this.settingsModel(param); if (this.activeChild === this.childKey(name, param)) owner.selectProfile(owner.profile, true); } })
      } else if (name === 'settings') {
        if (!NavigationContext.from(name, param as string).scoped || this.targetReady(name, param)) SettingsPage({ model: this.settingsModel(param), onChat: (row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openLinkedResource(row); }, active: this.activeChild === this.childKey(name, param), page: this.settingsKind(param), onDetail: (page: string, row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openSettingsDetail(page, row); }, onExtension: (extensionId: string, uiId: string, title: string) => { if (this.activeChild === this.childKey(name, param)) this.openExtension(this.settingsModel(param), extensionId, uiId, title); }, onBack: () => { if (this.activeChild === this.childKey(name, param)) { this.stack.pop(); this.savePath(); } }, onComputer: (row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openComputer(row); }, onOpen: (page: string) => this.openSettings(page, NavigationContext.from(name, param as string).scoped) })
        else this.targetLoading(name, param)
      }
    }.title(name === 'session' || name === 'conversation' ? this.chatTitle(param) : name === 'resources' ? tr(param === 'todo' ? 'todos' : param === 'inbox' ? 'inbox' : param === 'health' ? 'health_home_title' : 'knowledge') : name === 'applications' ? tr('applications') : name === 'extension' ? this.extensionTitle : name === 'todo' ? this.todoTitle || this.routeRow(param).title || this.routeRow(param).id : name === 'settings' ? this.settingsTitle(param) : name === 'tools' ? tr('workspace') : name === 'history' ? tr('interaction_history') : this.routeRow(param).title || this.routeRow(param).id)
      .hideTitleBar(name === 'session' && this.hideAgentTitle()).hideBackButton(name === 'session' && this.wide() && this.model.section === 1)
      .menus(this.destinationMenuItems(name, param))
      .bindMenu(this.chatTitleMenuVisible && this.activeChild === this.childKey(name, param), this.chatActionsMenu, this.chatMenuOptions(this.chatMenuRevision))
      .onShown(() => this.routeShown(name, param))
      .onHidden(() => { if (this.activeChild === this.childKey(name, param)) { this.activeChild = ''; this.dismissChatActions(); } if ((name === 'session' || name === 'conversation') && this.routeRow(param).key() === this.detailModel.selected.key()) this.detailModel.hideConversation(false); if (name === 'tools') this.detailModel.releaseTerminal(); })
      .onBackPressed((): boolean => { this.detailModel.releaseTerminal(); this.stack.pop(); this.savePath(); return true; })
  }
  build() {
    Stack() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style) }) {
      if (!this.model.profile.id) {
        Column({ space: Tokens.section }) {
          Text(tr('welcome')).fontSize(Tokens.title).fontWeight(FontWeight.Bold)
          if (this.model.error) Text(tr(this.model.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
          Button(tr('connect')).onClick(() => this.openSheet('connect')).id('welcome.connect')
          Button(tr('experience')).onClick(() => this.model.useDemo()).id('welcome.demo')
          if (this.model.pendingCleanupCount > 0) Button(tr('retry_cleanup')).onClick(() => this.model.retryCleanup())
        }.justifyContent(FlexAlign.Center).width('100%').height('100%')
          .bindSheet($$this.showSheet, this.sheetContent(), { title: { title: this.sheetTitle() }, height: SheetSize.LARGE, showClose: true })
      } else {
        Navigation(this.stack) { this.primaryTabs() }.title(this.labels()[this.model.section]).titleMode(NavigationTitleMode.Mini)
          .hideBackButton(true).hideTitleBar(this.rail()).menus(this.menus())
          .toolbarConfiguration(this.rail() ? [{ value: tr('expand'), icon: $r('sys.media.ohos_ic_public_arrow_right'), action: () => this.collapsed = false }] : [])
          .mode(this.wide() ? NavigationMode.Split : NavigationMode.Stack)
          .navBarWidth(this.rail() ? Tokens.collapsed : Tokens.master).navBarWidthRange(this.rail() ? [Tokens.collapsed, Tokens.collapsed] : [Tokens.masterMin, Tokens.masterMax])
          .minContentWidth(Tokens.detailMin).enableDragBar(!this.rail()).navDestination(this.destination)
          .onAreaChange(() => this.updateBreakpoint()).width('100%').height('100%')
          .bindSheet($$this.showSheet, this.sheetContent(), { title: { title: this.sheetTitle() }, height: SheetSize.LARGE, showClose: true })
      }
    }
    }.width('100%').height('100%')
      .bindSheet($$this.readerShown, this.readerContent(), { title: { title: this.readerTitle }, height: Tokens.readerFull, width: Tokens.readerFull, preferType: SheetType.BOTTOM,
        detents: [Tokens.readerFull, Tokens.readerPartial], enableOutsideInteractive: true, showClose: true })
  }
}
