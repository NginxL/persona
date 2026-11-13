import { FoldSplitContainer, HoverModeStatus } from '@kit.ArkUI';
import { hilog } from '@kit.PerformanceAnalysisKit';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { nativeMessageTheme } from '../design/LookTheme';
import { AssistantMessage } from './AssistantMessage';
import { pasteboard } from '@kit.BasicServicesKit';
import { SessionRevision, SessionRevisionOutcome } from '../models/SessionRevision';
import type { SessionRevisionRestore } from '../core/SessionRevision';
import { ConversationRevisionLease, restoreAcknowledgedSessionDraft } from '../core/ConversationRevisionLease';
import { TranscriptImage } from './TranscriptImage';
import { conversationImageEntries, conversationImageKey } from '../core/MessageImageGallery';
import type { MessageImageGalleryEntry, LoadedMessageImage } from '../core/MessageImageGallery';
import { MarkdownText } from './MarkdownText';
import { AssistantCards } from './AssistantCards';
import { InteractionDecisionForm } from './InteractionDecisionForm';
import { AssistantFeedModel } from '../models/AssistantFeed';
import { ChatAttachments, ChatAttachment } from '../models/ChatAttachments';
import { AttachmentStrip } from './AttachmentStrip';
import { common } from '@kit.AbilityKit';
import { previewAttachment } from '../platform/NativePreview';
import { mergeConversationTimeline, TimelineMessage, TimelineCard } from '../core/ConversationTimeline';
import { ClientModel } from '../models/ClientModel';
import { ChatMessage, Decision, ResourceRow, Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

class ConversationLine {
  id: string;
  message: ChatMessage | undefined;
  card: Record<string, Object> | undefined;
  decision: Decision | undefined;
  constructor(id: string, message?: ChatMessage, card?: Record<string, Object>, decision?: Decision) {
    this.id = id; this.message = message; this.card = card; this.decision = decision;
  }
}

@ComponentV2
export struct Conversation {
  @Require @Param model: ClientModel;
  @Param active: boolean = true;
  @Param showContextMenu: boolean = false;
  @Param actionItems: SheetInfo[] = [];
  @Param actionsVisible: boolean = false;
  @Param actionsRequest: number = 0;
  @Event onShowActions: () => void = () => {};
  @Event onHideActions: (request: number) => void = () => {};
  @Event onOpenFile: (path: string) => void = () => {};
  @Event onLink: (address: string) => void = () => {};
  @Event onOpenResource: (row: ResourceRow) => void = () => {};
  @Local feed: AssistantFeedModel = new AssistantFeedModel();
  @Local look: DeviceLook = deviceLook;
  @Local attachments: ChatAttachments = new ChatAttachments();
  @Local sending: boolean = false;
  @Local revisions: SessionRevision = new SessionRevision();
  @Local revisionResult: string = '';
  @Local revisionCacheError: string = '';
  private revisionKey: string = '';
  private revisionContextKey: string = '';
  private revisionVisibilityRevision: number = 0;
  private revisionMenuLease: ConversationRevisionLease | undefined = undefined;
  private attachmentKey: string = '';
  @Local lines: ConversationLine[] = [];
  @Local imageGallery: MessageImageGalleryEntry[] = [];
  private imageIndices: Map<string, number> = new Map();
  private feedKey: string = '';
  private feedTimer: number = -1;
  @Local expanded: string[] = [];
  private scroller: Scroller = new Scroller();
  @Local atEnd: boolean = true;
  @Local hasNew: boolean = false;
  @Local availableHeight: number = 800;
  @Local availableWidth: number = 360;
  @Local environment: NativeEnvironment = nativeEnvironment;
  // Keep native input, IME composition and transcript nodes stable for this view.
  // Re-entering the conversation may sample a newly available native capability.
  private useNativeHover: boolean = nativeEnvironment.nativeHoverAvailable;
  @Local controlsHeight: number = 80;
  private stackedComposer(): boolean {
    return this.environment.fontScale >= 2 || this.availableWidth < 160 + 112 * Math.max(1, this.environment.fontScale) + 2 * Tokens.inset + 2 * Tokens.gap;
  }
  private ordinarySplitRatio(): number {
    const secondary = Math.max(1, Math.min(this.controlsHeight, this.availableHeight * 0.65));
    return Math.max(1, this.availableHeight - secondary) / secondary;
  }
  @Monitor('model.messages') messagesChanged(): void {
    this.syncTimeline(); this.refreshFeed(); this.syncRevision();
    if (this.atEnd) setTimeout((): void => { this.scroller.scrollEdge(Edge.Bottom); }, 0); else this.hasNew = true;
  }
  @Monitor('feed.items') feedChanged(): void { this.syncTimeline(); }
  @Monitor('active', 'model.selected', 'model.connected') feedContextChanged(): void { this.syncFeed(); this.syncAttachments(); this.syncRevision(); }
  @Monitor('model.decision') decisionChanged(): void { this.refreshFeed(); }
  @Monitor('model.personaQuestions') personaQuestionsChanged(): void {
    this.syncTimeline(); this.refreshFeed();
    if (this.atEnd) setTimeout((): void => { this.scroller.scrollEdge(Edge.Bottom); }, 0); else this.hasNew = true;
  }
  private syncFeed(): void {
    if (!this.active || !this.model.connected || this.model.demo || this.model.selected.kind !== 'conversation') {
      if (this.feedTimer !== -1) clearTimeout(this.feedTimer); this.feedTimer = -1;
      this.feed.close(); this.feedKey = ''; this.syncTimeline(); return;
    }
    const key = JSON.stringify([this.model.connectionGeneration(), this.model.target()]);
    if (key === this.feedKey) return;
    this.feedKey = key; this.feed.open(this.model, this.model.selected).catch(() => { if (this.feedKey === key) this.feed.error = 'failed'; });
  }
  private refreshFeed(): void {
    if (!this.active || !this.feedKey || this.feedTimer !== -1) return;
    this.feedTimer = setTimeout(() => { this.feedTimer = -1; if (this.active && this.feedKey) this.feed.refresh(); }, 200);
  }
  private syncAttachments(): void {
    if (!this.active && this.attachments.picking && !this.model.demo && this.model.isChat() && this.attachmentKey === this.model.target()) return;
    if (!this.active || !this.model.isChat() || this.model.demo) { this.attachmentKey = ''; this.attachments.close(); return; }
    // System pickers may pause the ability and reconnect its transport; the local draft keeps its owner.
    const key = this.model.target(); if (key === this.attachmentKey) return;
    const host = this.getUIContext().getHostContext(); if (!host) return;
    this.attachmentKey = key;
    const row = this.model.selected; this.attachments.busy = true;
    this.model.restoreLocalDelivery().then(async (): Promise<void> => {
      if (!this.active || this.attachmentKey !== key || this.model.target() !== key) return;
      await this.attachments.open(this.model, host as common.UIAbilityContext, row);
    }).catch(() => { if (this.attachmentKey === key) { this.attachments.errorKey = 'chat_attachment_storage_unavailable'; this.attachments.busy = true; } });
  }
  private async sendDraft(): Promise<void> {
    if (this.sending || this.attachments.busy || !this.model.canSend()) return;
    if (!this.attachments.attachments.length) { await this.model.send(); return; }
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.model.target(), draft = this.model.draft, context = this.model.sourceContext;
    this.sending = true;
    try {
      const prepared = await this.attachments.prepareSend(this.model.prepareMessageText(draft));
      await this.model.sendPrepared(prepared, draft, context);
    } catch (error) { if (this.model.owns(generation, scope) && this.model.target() === target) this.attachments.errorKey = error instanceof Error && error.message.startsWith('chat_attachment_') ? error.message : 'chat_attachment_send_failed'; }
    finally { this.sending = false; }
  }
  private async preview(item: ChatAttachment): Promise<void> {
    const host = this.getUIContext().getHostContext(), key = this.attachmentKey; if (!host) return;
    try { await previewAttachment(host as common.UIAbilityContext, item.localPath, item.name, item.mime); }
    catch (_) { if (key === this.attachmentKey) this.attachments.errorKey = 'preview_unavailable'; }
  }
  @Builder attachmentMenu() {
    Menu() {
      MenuItem({ content: tr('photos') }).onClick(() => this.attachments.choosePhotos())
      MenuItem({ content: tr('files') }).onClick(() => this.attachments.chooseFiles())
    }
  }
  private syncRevision(): void {
    if (!this.active || !this.model.connected || !this.model.isChat() || this.model.demo) {
      if (this.revisionContextKey) this.invalidateRevisionMenuContext();
      this.revisionContextKey = '';
      this.revisions.hide(); this.revisionKey = ''; return;
    }
    const key = JSON.stringify([this.model.connectionGeneration(), this.model.scope(), this.model.target()]);
    if (key !== this.revisionContextKey) {
      this.revisionContextKey = key;
      this.invalidateRevisionMenuContext();
    }
    if (key !== this.revisionKey) {
      this.revisionKey = key; this.revisionResult = ''; this.revisionCacheError = '';
      this.revisions.open(this.model, this.model.selected, this.model.transcriptItems());
    }
    else this.revisions.updateItems(this.model.transcriptItems());
  }
  private invalidateRevisionMenuContext(): void { this.revisionVisibilityRevision++; this.revisionMenuLease = undefined; }
  private captureRevisionMenuLease(message: ChatMessage): void {
    this.revisionMenuLease = undefined;
    const id = message.itemId, target = this.model.target(), generation = this.model.connectionGeneration(), scope = this.model.scope();
    if (!id || message.ownerTarget !== target || !this.active || !this.model.connected || this.model.demo || !this.model.isChat() ||
      !this.model.owns(generation, scope) || !this.revisions.isCurrent()) return;
    const fingerprint = this.revisions.fingerprint(id);
    if (!fingerprint || (!this.revisions.canFork(id) && !this.revisions.canRewind(id))) return;
    this.revisionMenuLease = new ConversationRevisionLease(this.model, generation, scope, target, this.revisionVisibilityRevision,
      id, fingerprint, message.ownerTarget);
  }
  private revisionOwnerCurrent(lease: ConversationRevisionLease, message: ChatMessage): boolean {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.model.target();
    const currentSession = this.revisions.isCurrent() && this.model.owns(lease.generation, lease.scope);
    return lease.owns(this.model, this.active, this.model.connected, this.model.demo, generation, scope, target,
      this.revisionVisibilityRevision, message.itemId, message.ownerTarget, currentSession);
  }
  private revisionLeaseCurrent(lease: ConversationRevisionLease, message: ChatMessage): boolean {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.model.target();
    const currentSession = this.revisions.isCurrent() && this.model.owns(lease.generation, lease.scope);
    return lease.matches(this.model, this.active, this.model.connected, this.model.demo, generation, scope, target,
      this.revisionVisibilityRevision, message.itemId, this.revisions.fingerprint(lease.itemId), message.ownerTarget, currentSession);
  }
  private takeRevisionMenuLease(message: ChatMessage): ConversationRevisionLease | undefined {
    const lease = this.revisionMenuLease;
    this.revisionMenuLease = undefined;
    return lease && this.revisionLeaseCurrent(lease, message) ? lease : undefined;
  }
  private openForkConfirmation(message: ChatMessage): void {
    const lease = this.takeRevisionMenuLease(message);
    if (lease) this.confirmFork(message, lease);
  }
  private openRewindConfirmation(message: ChatMessage, restore: SessionRevisionRestore): void {
    const lease = this.takeRevisionMenuLease(message);
    if (lease) this.confirmRewind(message, restore, lease);
  }
  private confirmFork(message: ChatMessage, lease: ConversationRevisionLease): void {
    const id = lease.itemId, fingerprint = lease.fingerprint;
    if (!this.revisionLeaseCurrent(lease, message) || !this.revisions.canFork(id)) return;
    this.getUIContext().showAlertDialog({ title: tr('branch_chat'), message: this.model.selected.title + '\n' + this.model.machineName(this.model.selected.machine),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('branch_chat'), action: () => {
        if (!this.revisionLeaseCurrent(lease, message) || !this.revisions.canFork(id)) return;
        this.revisions.fork(id, fingerprint).then((row: ResourceRow | undefined) => {
          if (row && this.revisionOwnerCurrent(lease, message)) this.onOpenResource(row);
        });
      } } });
  }
  private confirmRewind(message: ChatMessage, restore: SessionRevisionRestore, lease: ConversationRevisionLease): void {
    const id = lease.itemId, fingerprint = lease.fingerprint;
    if (!this.revisionLeaseCurrent(lease, message) || !this.revisions.canRewind(id)) return;
    this.getUIContext().showAlertDialog({ title: tr(restore === 'code_and_conversation' ? 'rewind_code_chat' : 'rewind_chat'),
      message: this.model.machineName(this.model.selected.machine) + '\n' + this.model.selected.path + '\n\n' + tr(restore === 'code_and_conversation' ? 'rewind_code_consequence' : 'rewind_chat_consequence') + '\n\n' + message.text,
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('confirm'), action: () => {
        if (!this.revisionLeaseCurrent(lease, message) || !this.revisions.canRewind(id)) return;
        this.revisionResult = ''; this.revisionCacheError = '';
        this.revisions.rewind(id, restore, fingerprint).then((outcome: SessionRevisionOutcome) => this.rewindFinished(outcome, lease, message));
      } } });
  }
  private async rewindFinished(outcome: SessionRevisionOutcome, lease: ConversationRevisionLease, message: ChatMessage): Promise<void> {
    if (!this.revisionOwnerCurrent(lease, message) || outcome.state !== 'done') return;
    this.revisionResult = outcome.codeRestored ? 'rewind_code_done' : 'rewind_chat_done';
    if (!outcome.inputCleared) this.revisionResult = 'rewind_input_kept';
    else if (outcome.canFillDraft) {
      await this.restoreRevisionDraft(outcome, lease, message);
      if (this.revisionOwnerCurrent(lease, message)) this.model.loadChat(this.model.selected).catch(() => {});
      return;
    }
    else if (outcome.draftUnchanged && outcome.prompt) {
      this.getUIContext().showAlertDialog({ title: tr('restore_draft'), message: tr('replace_draft'), primaryButton: { value: tr('cancel'), action: () => {
        if (this.revisionOwnerCurrent(lease, message)) this.model.loadChat(this.model.selected).catch(() => {});
      } }, secondaryButton: { value: tr('confirm'), action: () => {
        void this.finishRewindDraftConfirmation(outcome, lease, message);
      } } });
      return;
    }
    if (this.revisionOwnerCurrent(lease, message)) this.model.loadChat(this.model.selected).catch(() => {});
  }
  private async restoreRevisionDraft(outcome: SessionRevisionOutcome, lease: ConversationRevisionLease, message: ChatMessage): Promise<void> {
    await restoreAcknowledgedSessionDraft(this.model, outcome, (): boolean => this.revisionOwnerCurrent(lease, message), (): void => {
      this.revisionCacheError = 'cache_unavailable';
    });
  }
  private async finishRewindDraftConfirmation(outcome: SessionRevisionOutcome, lease: ConversationRevisionLease, message: ChatMessage): Promise<void> {
    if (this.revisionOwnerCurrent(lease, message)) await this.restoreRevisionDraft(outcome, lease, message);
    if (this.revisionOwnerCurrent(lease, message)) this.model.loadChat(this.model.selected).catch(() => {});
  }
  private async copyMessage(message: ChatMessage): Promise<void> {
    if (message.ownerTarget !== this.model.target()) return;
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.model.target();
    try { await pasteboard.getSystemPasteboard().setData(pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, message.text)); }
    catch (_) { if (this.model.owns(generation, scope) && this.model.target() === target) this.model.error = 'failed'; }
  }
  @Builder messageMenu(message: ChatMessage) {
    Menu() {
      if (message.text) MenuItem({ content: tr('copy') }).onClick(() => this.copyMessage(message))
      if (message.text) MenuItem({ content: tr('use_as_context') }).onClick(() => this.model.useMessageContext(message))
      if (message.itemId && message.ownerTarget === this.model.target() && this.revisions.canFork(message.itemId))
        MenuItem({ content: tr('branch_chat') }).onClick(() => this.openForkConfirmation(message))
      if (message.itemId && message.ownerTarget === this.model.target() && this.revisions.canRewind(message.itemId)) {
        MenuItem({ content: tr('rewind_chat') }).onClick(() => this.openRewindConfirmation(message, 'conversation'))
        MenuItem({ content: tr('rewind_code_chat') }).onClick(() => this.openRewindConfirmation(message, 'code_and_conversation'))
      }
    }.onAppear(() => this.captureRevisionMenuLease(message))
  }
  private syncTimeline(): void {
    this.imageGallery = conversationImageEntries(this.model.messages, this.model.target());
    this.imageIndices.clear();
    this.imageGallery.forEach((entry: MessageImageGalleryEntry, index: number): void => { this.imageIndices.set(entry.key, index); });
    const messages = new Map<string, ChatMessage>(), cards = new Map<string, Record<string, Object>>();
    const inputs: TimelineMessage[] = this.model.messages.map((message: ChatMessage): TimelineMessage => {
      messages.set(message.id, message); return { id: message.id, role: message.role, at: message.at };
    });
    const activities: TimelineCard[] = this.feed.items.filter((item: Record<string, Object>) => !['task', 'today', 'brief'].includes(Wire.text(item, 'kind')))
      .map((item: Record<string, Object>): TimelineCard => { const id = Wire.text(item, 'id'), stamp = Date.parse(Wire.text(item, 'at')); cards.set(id, item); return { id: id, at: Number.isFinite(stamp) ? stamp : 0 }; });
    const lines = mergeConversationTimeline(inputs, activities).map((entry) => new ConversationLine(entry.id, messages.get(entry.messageId), cards.get(entry.cardId)));
    this.model.personaQuestions.forEach((request: Decision) => lines.push(new ConversationLine('persona-question:' + request.id, undefined, undefined, request)));
    this.lines = lines;
  }
  aboutToAppear(): void {
    this.syncFeed(); this.syncAttachments(); this.syncTimeline(); this.syncRevision();
    const target = this.model.target(); this.model.loadView('work:' + target).then((value: string) => {
      if (target === this.model.target() && value) { try { this.expanded = Wire.strings(Wire.parse(value), 'expanded'); } catch (_) {} }
    });
  }
  aboutToDisappear(): void {
    if (this.feedTimer !== -1) clearTimeout(this.feedTimer); this.feedTimer = -1; this.feed.close(); this.feedKey = '';
    this.attachmentKey = ''; this.attachments.close(); this.invalidateRevisionMenuContext(); this.revisionContextKey = '';
    this.revisions.hide(); this.revisionKey = '';
  }
  private toggle(id: string): void {
    this.expanded = this.expanded.includes(id) ? this.expanded.filter((value: string) => value !== id) : this.expanded.concat([id]);
    this.model.saveView('work:' + this.model.target(), JSON.stringify(Wire.payload(['expanded'], [this.expanded]))).catch(() => this.model.error = 'failed');
  }
  private confirmChecked(id: string): void {
    this.getUIContext().showAlertDialog({ title: tr('confirm'), message: tr('checked_no_effect'), primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('confirm'), action: () => this.model.acknowledgeWrite(id) } });
  }
  private restoreDraft(message: ChatMessage): void {
    const target = this.model.target(), generation = this.model.connectionGeneration(), scope = this.model.scope();
    if (!this.model.draft && !this.model.sourceContext) { this.model.restoreMessageDraft(message.localOperation); return; }
    this.getUIContext().showAlertDialog({ title: tr('restore_draft'), message: tr('replace_draft'), primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('confirm'), action: () => { if (this.model.owns(generation, scope) && this.model.target() === target) this.model.restoreMessageDraft(message.localOperation); } } });
  }
  private imageKey(message: ChatMessage, kind: string, source: string, index: number = 0): string {
    return conversationImageKey(message.ownerTarget, message.id, kind, source, index);
  }
  private imageIndex(message: ChatMessage, kind: string, source: string, index: number = 0): number {
    return this.imageIndices.get(this.imageKey(message, kind, source, index)) ?? -1;
  }
  private editReceivedImage(image: LoadedMessageImage): void {
    const attachments = this.attachments;
    if (!this.active || this.sending || this.model.demo || image.target !== this.model.target() || this.attachmentKey !== image.target) return;
    void attachments.editReceivedImage(image.data, image.mime, image.target, image.scope, image.generation, image.hub);
  }
  @Builder message(message: ChatMessage) {
    Column({ space: Tokens.gap }) {
      if (message.work) {
        Button(tr('work_summary'), { type: ButtonType.Normal }).onClick(() => { this.toggle(message.id); if (this.expanded.includes(message.id)) this.model.expandWork(message); })
          .fontColor($r('sys.color.font_secondary')).backgroundColor(Color.Transparent).height(Tokens.touch)
        if (this.expanded.includes(message.id)) {
          Text(message.work).fontFamily('monospace').fontSize(Tokens.meta).copyOption(CopyOptions.LocalDevice)
        }
      }
      if (message.localImageCount > 0) Text(tr('photos') + ' · ' + message.localImageCount).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (message.source) Text(message.source).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (message.ownerTarget === this.model.target()) ForEach(message.imagePaths, (path: string, index: number) => {
        TranscriptImage({ model: this.model, remotePath: path, active: this.active,
          sourceKey: this.imageKey(message, 'path', path, index), galleryEntries: this.imageGallery,
          galleryIndex: this.imageIndex(message, 'path', path, index), onEdit: (image: LoadedMessageImage) => this.editReceivedImage(image) })
      }, (path: string, index: number) => this.imageKey(message, 'path', path, index))
      ForEach(message.filePaths, (path: string) => { Button(path.split('/').pop() || path, { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.onOpenFile(path)) }, (path: string) => path)
      if (message.imageId && message.ownerTarget === this.model.target()) TranscriptImage({ model: this.model, itemId: message.imageId, active: this.active,
        sourceKey: this.imageKey(message, 'item', message.imageId), galleryEntries: this.imageGallery,
        galleryIndex: this.imageIndex(message, 'item', message.imageId), onEdit: (image: LoadedMessageImage) => this.editReceivedImage(image) })
      if (message.text) {
        WithTheme({ theme: nativeMessageTheme(this.look, this.model.style, message.role === 'user') }) {
        Column() {
          if (message.role === 'assistant' && this.model.selected.kind === 'conversation') AssistantMessage({ model: this.model, feed: this.feed, message: message, active: this.active, onLink: (address: string) => this.onLink(address), onOpenResource: (row: ResourceRow) => this.onOpenResource(row) })
          else MarkdownText({ text: message.text, onLink: (address: string) => this.onLink(address) })
        }.width('100%')
          .padding(message.role === 'user' ? Tokens.inset : 0)
          .backgroundColor(message.role === 'user' ? $r('sys.color.comp_background_primary') : Color.Transparent)
        }
      }
      if (message.deliveryState && message.deliveryState !== 'done' && !message.failed) Text(tr(message.deliveryState === 'unknown' ? 'result_unknown' : message.deliveryState)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (message.failed) Text(tr('failed')).fontColor($r('app.color.persona_danger')).fontSize(Tokens.meta)
      if (message.localOperation && this.model.canCheckLocalMessage(message.localOperation)) Button(tr('checked'), { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.confirmChecked(message.localOperation))
      if (message.localOperation && this.model.canRestoreMessage(message.localOperation)) Button(tr('restore_draft'), { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.restoreDraft(message))
    }.width('100%').alignItems(HorizontalAlign.Start).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.gap })
      .bindContextMenu(this.messageMenu(message), ResponseType.LongPress)
  }
  @Builder actionsMenu() {
    Menu() {
      ForEach(this.actionItems, (item: SheetInfo) => {
        MenuItem({ content: item.title }).constraintSize({ minHeight: Tokens.touch }).onClick(() => item.action())
      }, (_item: SheetInfo, index: number) => String(this.actionsRequest) + ':' + String(index))
    }
  }
  private actionMenuOptions(request: number): MenuOptions {
    return { onDisappear: () => this.onHideActions(request) };
  }
  @Builder readingContent() {
    Column() {
      if (this.revisions.forkState === 'branching' || this.revisions.rewindState === 'rewinding') LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.revisionResult || this.revisions.forkError || this.revisions.rewindError) Text(tr(this.revisions.forkError || this.revisions.rewindError || this.revisionResult)).fontSize(Tokens.meta).padding(Tokens.inset).width('100%')
      if (this.revisionCacheError) Text(tr(this.revisionCacheError)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      if (this.showContextMenu) {
        Row() {
          Button(tr('more'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.onShowActions()).constraintSize({ minHeight: Tokens.touch })
            .bindMenu(this.actionsVisible, this.actionsMenu, this.actionMenuOptions(this.actionsRequest))
        }.justifyContent(FlexAlign.End).padding({ left: Tokens.page, right: Tokens.page }).width('100%')
      }
      if (this.model.error) {
        Row() {
          Text(tr(this.model.error)).fontSize(Tokens.meta).layoutWeight(1)
          Button(tr('retry')).onClick(() => this.model.refresh())
        }.padding(Tokens.inset)
      }
      if (this.model.unresolvedWrites.length) {
        Row({ space: Tokens.gap }) {
          Text(tr('result_unknown')).layoutWeight(1)
          Button(tr('refresh')).onClick(() => this.model.reconcileWrites())
          if (this.model.unresolvedWrites.some((record) => record.state === 'unknown' && !record.requiresReceipt))
            Button(tr('checked')).onClick(() => { const record = this.model.unresolvedWrites.find((item) => item.state === 'unknown' && !item.requiresReceipt); if (record) this.confirmChecked(record.id); })
        }.padding(Tokens.inset)
      }
      List({ scroller: this.scroller, space: Tokens.gap }) {
        if (this.feed.error && this.feed.error !== this.model.error) ListItem() {
          Row({ space: Tokens.gap }) { Text(tr(this.feed.error)).fontSize(Tokens.meta).layoutWeight(1); Button(tr('retry')).onClick(() => this.feed.refresh()) }.padding(Tokens.inset)
        }
        if (this.model.selected.kind === 'conversation') ListItem() {
          AssistantCards({ model: this.model, feed: this.feed, active: this.active && this.model.connected, onOpenResource: (row: ResourceRow) => this.onOpenResource(row), dockOnly: true, onOpenSession: (row: ResourceRow) => this.onOpenResource(row),
            onOpenTodo: (row: ResourceRow) => this.onOpenResource(row), onOpenInbox: (row: ResourceRow) => this.onOpenResource(row), onLink: (address: string) => this.onLink(address) })
        }
        if (this.model.hasMoreMessages || this.feed.nextCursor) ListItem() { Button(tr('load_more')).onClick(() => { this.atEnd = false; this.model.loadOlder(); this.feed.loadMore(); }) }
        Repeat(this.lines).each((entry: RepeatItem<ConversationLine>) => {
          ListItem() {
            if (entry.item.message) this.message(entry.item.message)
            else if (entry.item.card) AssistantCards({ model: this.model, feed: this.feed, active: this.active && this.model.connected, onOpenResource: (row: ResourceRow) => this.onOpenResource(row), item: entry.item.card, onOpenSession: (row: ResourceRow) => this.onOpenResource(row),
              onOpenTodo: (row: ResourceRow) => this.onOpenResource(row), onOpenInbox: (row: ResourceRow) => this.onOpenResource(row), onLink: (address: string) => this.onLink(address) })
            else if (entry.item.decision) InteractionDecisionForm({ model: this.model, request: entry.item.decision, onLink: (address: string) => this.onLink(address) })
          }.id('chat.line.' + entry.item.id)
        }).key((line: ConversationLine) => line.id)
      }.layoutWeight(1).width('100%').onReachEnd(() => { this.atEnd = true; this.hasNew = false; })
        .onScrollFrameBegin((offset: number) => { if (offset < 0) this.atEnd = false; return { offsetRemain: offset }; })
    }.width('100%').height('100%')
  }

  @Builder interactionContent() {
    Scroll() {
      Column() {
        if (this.model.decision) {
          ForEach([this.model.decision], (request: Decision) => {
            InteractionDecisionForm({ model: this.model, request: request, onLink: (address: string) => this.onLink(address) })
          }, (request: Decision) => request.id)
        }
      if (this.hasNew && !this.atEnd) Button(tr('latest')).onClick(() => { this.scroller.scrollEdge(Edge.Bottom); this.atEnd = true; this.hasNew = false; })
      if (this.model.sourceContext) {
        Row() {
          Text(this.model.contextTitle()).fontSize(Tokens.meta).layoutWeight(1)
          Button(tr('remove')).enabled(!this.sending).onClick(() => this.model.removeContext())
        }.padding({ left: Tokens.page, right: Tokens.page })
      }
      if (this.model.canCompose() && !this.model.decision) {
        if (this.attachments.busy && !this.sending && (this.attachments.errorKey === 'chat_attachment_storage_unavailable' || this.model.error === 'cache_unavailable')) Button(tr('retry')).onClick(() => { this.attachmentKey = ''; this.syncAttachments(); })
        AttachmentStrip({ attachments: this.attachments.attachments, errorKey: this.attachments.errorKey, actionsEnabled: !this.sending && !this.attachments.busy,
          onRemove: (id: string) => this.attachments.remove(id), onEdit: (id: string) => this.attachments.editPhoto(id), onPreview: (item: ChatAttachment) => this.preview(item) })
        GridRow({ columns: 12, gutter: { x: Tokens.gap, y: Tokens.gap } }) {
          if (!this.model.demo) GridCol({ span: this.stackedComposer() ? 12 : 3, order: this.stackedComposer() ? 2 : 0 }) {
            Button(tr('attach'), { buttonStyle: ButtonStyleMode.TEXTUAL }).constraintSize({ minHeight: Tokens.touch }).width('100%')
              .enabled(!this.sending && !this.attachments.busy && !this.model.draftLoading).bindMenu(this.attachmentMenu).id('chat.attach')
          }
          GridCol({ span: this.stackedComposer() ? 12 : this.model.demo ? 9 : 6, order: this.stackedComposer() ? 0 : 1 }) {
          TextArea({ text: this.model.draft, placeholder: tr('message') }).maxLength(100000).width('100%')
            .constraintSize({ minHeight: Tokens.input, maxHeight: Tokens.inputExpanded })
            .enabled(!this.sending && !this.model.draftLoading).onChange((value: string) => this.model.updateDraft(value)).id('chat.input')
          }
          GridCol({ span: this.stackedComposer() ? 12 : 3, order: this.stackedComposer() ? 1 : 2 }) {
          Button(tr(this.sending ? 'pending' : 'send')).enabled(this.model.canSend() && !this.sending && !this.attachments.busy && !this.model.draftLoading &&
            (this.model.draft.trim().length > 0 || this.attachments.attachments.length > 0) && this.model.operationState !== 'pending' && this.model.operationState !== 'unknown')
            .onClick(() => this.sendDraft()).constraintSize({ minHeight: Tokens.touch }).width('100%').id('chat.send')
          }
        }.alignItems(ItemAlign.Center).padding(Tokens.inset).width('100%')
      } else if (!this.model.canSend() && !this.model.decision) {
        if (this.model.canAttach()) Button(tr('attach_live')).onClick(() => this.model.attach())
        Text(tr('read_only')).fontSize(Tokens.meta).padding(Tokens.page)
      }
      }.width('100%').onAreaChange((_before: Area, area: Area): void => {
        const height = Number(area.height);
        if (Number.isFinite(height) && height > 0 && Math.abs(height - this.controlsHeight) > 1) this.controlsHeight = height;
      })
    }.width('100%').height('100%')
  }

  build() {
    Column() {
      if (this.useNativeHover) {
      FoldSplitContainer({ primary: (): void => this.readingContent(), secondary: (): void => this.interactionContent(),
        expandedLayoutOptions: { verticalSplitRatio: this.ordinarySplitRatio() },
        foldedLayoutOptions: { verticalSplitRatio: this.ordinarySplitRatio() },
        hoverModeLayoutOptions: { showExtraRegion: false }, animationOptions: null,
        onHoverStatusChange: (status: HoverModeStatus): void => {
          hilog.info(0x0000, 'PersonaLayout', 'fold=%{public}d hover=%{public}s rotation=%{public}d window=%{public}d',
            status.foldStatus, String(status.isHoverMode), status.appRotation, status.windowStatusType);
        } })
      } else {
        Column() { this.readingContent() }.width('100%').layoutWeight(1).clip(true)
        Column() { this.interactionContent() }.width('100%')
          .height(Math.max(1, Math.min(this.controlsHeight, this.availableHeight * 0.65)))
      }
    }.width('100%').height('100%').onAreaChange((_before: Area, area: Area): void => {
      const height = Number(area.height);
      const width = Number(area.width);
      if (Number.isFinite(width) && width > 0 && Math.abs(width - this.availableWidth) > 1) this.availableWidth = width;
      if (Number.isFinite(height) && height > 0 && Math.abs(height - this.availableHeight) > 1) this.availableHeight = height;
    })
  }
}
