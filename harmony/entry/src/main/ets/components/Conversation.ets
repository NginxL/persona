import { IconActions } from '../design/IconActions';
import { Theme, SymbolGlyphModifier } from '@kit.ArkUI';
import { ComposerOverlayWorkspace, ComposerOverlaySession } from '../models/ComposerOverlayWorkspace';
import { composerPopupLayout, ComposerPopupLayout } from '../core/ComposerPopupLayout';
import { util } from '@kit.ArkTS';
import { FoldSplitContainer, HoverModeStatus, FrameCallback, LengthMetrics } from '@kit.ArkUI';
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
import { QuestionWorkspace, QuestionSheet, questionPrompt } from '../models/QuestionWorkspace';
import { InteractionDecisionForm } from './InteractionDecisionForm';
import { AssistantFeedModel } from '../models/AssistantFeed';
import { ChatAttachments, ChatAttachment } from '../models/ChatAttachments';
import { AttachmentStrip } from './AttachmentStrip';
import { common } from '@kit.AbilityKit';
import { previewAttachment } from '../platform/NativePreview';
import { mergeConversationTimeline, reconcileTimelineRows, TimelineMessage, TimelineCard } from '../core/ConversationTimeline';
import { ClientModel } from '../models/ClientModel';
import { ChatMessage, Decision, ResourceRow, Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Colors } from '../design/Colors';
import { Symbols } from '../design/Symbols';
import { ButtonStyles } from '../design/Controls';
import { nativeSheetInset } from '../platform/NativeInsets';
import { ContextPicker } from './ContextPicker';
import type { ClientViewLease } from '../models/ClientModel';
import { parseReadingAnchor, readingAnchorIndex } from '../core/ConversationReading';
import type { ReadingAnchor } from '../core/ConversationReading';

class ConversationFrame extends FrameCallback {
  private action: () => void;
  constructor(action: () => void) { super(); this.action = action; }
  onFrame(_time: number): void { this.action(); }
}

class ComposerChoice implements AttributeModifier<ListItemAttribute> {
  readonly session: ComposerOverlaySession; readonly key: string; readonly title: ResourceStr;
  readonly symbol: Resource | undefined; readonly action: (() => void) | undefined;
  constructor(session: ComposerOverlaySession, key: string, title: ResourceStr, symbol?: Resource, action?: () => void) {
    this.session = session; this.key = key; this.title = title; this.symbol = symbol; this.action = action;
  }
  applyNormalAttribute(node: ListItemAttribute): void {
    node.accessibilityGroup(true);
    if (typeof this.title === 'string') node.accessibilityText(this.title);
    else node.accessibilityText(this.title);
  }
}
class ComposerActionIntent {
  readonly owner: string; readonly preserveInput: boolean; readonly previous: number;
  request: number = -1;
  constructor(owner: string, preserveInput: boolean, previous: number) { this.owner = owner; this.preserveInput = preserveInput; this.previous = previous; }
}

@ComponentV2
struct MessageSurface {
  @Param own: boolean = false;
  @Param maximum: number = Tokens.readingMax;
  @BuilderParam content: () => void;
  @Local fill: ResourceColor = Color.Transparent;
  onWillApplyTheme(theme: Theme): void { this.fill = theme.colors.compBackgroundPrimary; }
  build() {
    Column() { this.content() }.width(this.own ? undefined : '100%').constraintSize({ maxWidth: this.maximum })
      .padding(this.own ? Tokens.inset : 0).backgroundColor(this.own ? this.fill : Color.Transparent)
      .borderRadius(this.own ? Tokens.bubble : 0)
  }
}

class ConversationLine {
  id: string;
  readingId: string;
  message: ChatMessage | undefined;
  card: Record<string, Object> | undefined;
  decision: Decision | undefined;
  constructor(id: string, message?: ChatMessage, card?: Record<string, Object>, decision?: Decision) {
    this.id = id; this.readingId = message ? 'message:' + (message.itemId || message.id) : id; this.message = message; this.card = card; this.decision = decision;
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
  @Event admission: () => boolean = (): boolean => true;
  @Event onOpenCard: (itemId: string, kind: string) => void = () => {};
  @Event onPrimaryStop: (shown: boolean) => void = () => {};
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
  private mounted: boolean = false;
  @Local lines: ConversationLine[] = [];
  @Local imageGallery: MessageImageGalleryEntry[] = [];
  private imageIndices: Map<string, number> = new Map();
  private feedKey: string = '';
  private feedTimer: number = -1;
  @Local expanded: string[] = [];
  private scroller: Scroller = new Scroller();
  private readingLease: ClientViewLease | undefined;
  private readingKey: string = '';
  private readingEpoch: number = 0;
  private readingRestoring: boolean = true;
  private readingAnchorReady: boolean = false;
  private readingFetching: boolean = false;
  @Local readingLoading: boolean = false;
  private readingMotion: number = 0;
  private readingCursors: Set<string> = new Set();
  @Local readingFailed: boolean = false;
  private firstVisibleIndex: number = 0;
  private anchor: ReadingAnchor = { id: '', offset: 0, atEnd: true };
  private readingPrefix(): number {
    return (this.readingFailed ? 1 : 0) + (this.feed.error && this.feed.error !== this.model.error ? 1 : 0) + (this.model.selected.kind === 'conversation' ? 1 : 0) + (this.model.hasMoreMessages || this.feed.nextCursor ? 1 : 0);
  }
  private saveReading(): void {
    if (!this.readingRestoring && this.readingLease?.valid()) this.readingLease.write(JSON.stringify(this.anchor)).catch(() => {});
  }
  private syncReading(): void {
    const key = JSON.stringify([this.model.scope(), this.model.target()]);
    if (this.readingKey === key) return;
    this.saveReading(); this.readingKey = key;
    const epoch = ++this.readingEpoch, lease = this.model.captureView('reading:' + this.model.target());
    this.readingLease = lease; this.readingCursors.clear(); this.readingFetching = false; this.readingRestoring = true; this.readingAnchorReady = false; this.readingFailed = false;
    lease.read().then((value: string) => {
      if (epoch !== this.readingEpoch || key !== this.readingKey || !lease.valid()) return;
      this.anchor = parseReadingAnchor(value); this.atEnd = this.anchor.atEnd; this.readingAnchorReady = true;
      this.scheduleReading();
    }).catch(() => { if (epoch === this.readingEpoch) { this.readingFailed = true; } });
  }
  private scheduleReading(): void {
    const epoch = this.readingEpoch;
    this.getUIContext().postFrameCallback(new ConversationFrame(() => { if (epoch === this.readingEpoch && this.active) this.restoreReading(); }));
  }
  private async restoreReading(): Promise<void> {
    if (!this.active || !this.readingRestoring || !this.readingAnchorReady || this.readingFetching || this.readingFailed) return;
    if (this.model.selected.kind === 'conversation' && this.feedKey && !this.feed.hasLoaded) return;
    if (this.anchor.atEnd || !this.anchor.id) { this.scroller.scrollEdge(Edge.Bottom); this.readingRestoring = false; return; }
    const index = readingAnchorIndex(this.lines.map((line: ConversationLine) => line.readingId), this.anchor);
    if (index >= 0) {
      this.scroller.scrollToIndex(index + this.readingPrefix(), false, ScrollAlign.START, { extraOffset: LengthMetrics.vp(-this.anchor.offset) });
      this.atEnd = false; this.readingRestoring = false; return;
    }
    if (!this.model.hasMoreMessages && !this.feed.nextCursor) {
      if (!this.model.transcriptHistoryReady || !this.model.connected || this.feed.error) return;
      if (this.lines.length > 0) { this.scroller.scrollToIndex(this.readingPrefix(), false, ScrollAlign.START); this.readingRestoring = false; this.atEnd = false; }
      return;
    }
    if (this.feed.nextCursor) {
      if (this.readingCursors.has(this.feed.nextCursor)) { this.readingFailed = true; return; }
      this.readingCursors.add(this.feed.nextCursor);
    }
    const epoch = this.readingEpoch, before = this.readingProgress();
    this.readingFetching = true;
    try {
      const results = await Promise.all([this.model.hasMoreMessages ? this.model.loadOlder() : Promise.resolve(true), this.feed.nextCursor ? this.feed.loadMore() : Promise.resolve(true)]);
      if (epoch !== this.readingEpoch || !this.active) return;
      const after = this.readingProgress();
      if (before === after || results.some((ok: boolean) => ok === false)) this.readingFailed = true;
    } finally { if (epoch === this.readingEpoch) { this.readingFetching = false; if (!this.readingFailed) this.scheduleReading(); } }
  }
  private readingProgress(): string { return JSON.stringify([this.model.transcriptItems().map((item) => item.id), this.model.hasMoreMessages, this.feed.nextCursor]); }
  private visibleReadingAnchor(): ReadingAnchor | undefined {
    const prefix = this.readingPrefix(), index = Math.max(prefix, this.firstVisibleIndex), line = this.lines[index - prefix];
    if (!line) return undefined;
    try {
      const rect = this.scroller.getItemRect(index);
      if (Number.isFinite(rect.y) && Number.isFinite(rect.height) && rect.height > 0) return { id: line.readingId, offset: rect.y, atEnd: false };
    } catch (_) {}
    return undefined;
  }
  private async loadEarlier(): Promise<void> {
    if (!this.active || this.readingLoading || this.readingRestoring) return;
    const anchor = this.visibleReadingAnchor(), owner = this.model, generation = owner.connectionGeneration(), scope = owner.scope(), target = owner.target();
    const epoch = this.readingEpoch, key = this.readingKey, motion = this.readingMotion;
    const valid = (): boolean => this.active && this.model === owner && owner.owns(generation, scope) && owner.target() === target && this.readingEpoch === epoch && this.readingKey === key && this.readingMotion === motion;
    this.atEnd = false; this.readingLoading = true;
    try {
      await Promise.all([owner.hasMoreMessages ? owner.loadOlder() : Promise.resolve(true), this.feed.nextCursor ? this.feed.loadMore() : Promise.resolve(true)]);
      if (anchor && valid()) this.getUIContext().postFrameCallback(new ConversationFrame(() => {
        if (!valid()) return;
        const index = readingAnchorIndex(this.lines.map((line: ConversationLine) => line.readingId), anchor);
        if (index < 0) return;
        this.scroller.scrollToIndex(index + this.readingPrefix(), false, ScrollAlign.START, { extraOffset: LengthMetrics.vp(-anchor.offset) });
        this.anchor = anchor; this.atEnd = false;
      }));
    } finally { this.readingLoading = false; }
  }
  private retryReading(): void {
    if (!this.readingAnchorReady) { this.readingKey = ''; this.syncReading(); }
    else { this.readingCursors.clear(); this.readingFailed = false; this.scheduleReading(); }
  }
  private recordReading(start: number): void {
    if (!this.active || this.readingRestoring) return;
    this.atEnd = this.scroller.isAtEnd();
    const prefix = this.readingPrefix(), index = Math.max(prefix, start), line = this.lines[index - prefix];
    if (!line) return;
    try { const rectangle = this.scroller.getItemRect(index); this.anchor = { id: line.readingId, offset: rectangle.y, atEnd: this.atEnd }; } catch (_) {}
  }
  @Local atEnd: boolean = true;
  @Local hasNew: boolean = false;
  @Local availableHeight: number = 800;
  @Local availableWidth: number = 360;
  @Local environment: NativeEnvironment = nativeEnvironment;
  // Keep native input, IME composition and transcript nodes stable for this view.
  // Re-entering the conversation may sample a newly available native capability.
  private useNativeHover: boolean = nativeEnvironment.nativeHoverAvailable;
  @Local controlsHeight: number = 80;
  @Local fieldFocused: boolean = false;
  @Local composerWidth: number = 360;
  @Local questions: QuestionWorkspace = new QuestionWorkspace();
  @Local questionInset: number = 0;
  private questionNodeId: string = 'chat.question.' + util.generateRandomUUID();
  private questionLayoutReady: boolean = false;
  private presentedQuestion: QuestionSheet | undefined;
  @Local chooser: ComposerOverlayWorkspace = new ComposerOverlayWorkspace();
  @Local chooserItems: ComposerChoice[] = [];
  @Local chooserHeights: number[] = [];
  private actionIntent: ComposerActionIntent | undefined;
  @Local editorLeft: number = 0;
  @Local editorTop: number = 0;
  @Local editorWidth: number = 0;
  @Local editorHeight: number = 0;
  @Local contentTop: number = 0;
  private editorAnchorId: string = 'chat.editor.' + util.generateRandomUUID();
  @Local attachmentSheet: boolean = false;
  @Local contextKind: string = '';
  @Local attachmentInset: number = 0;
  private attachmentNodeId: string = 'chat.attachment.' + util.generateRandomUUID();
  private attachmentLayoutReady: boolean = false;
  private leftActionWidth(): number { const count = (this.model.demo ? 0 : 1) + (this.showContextMenu ? 1 : 0); return count * Tokens.touch + Math.max(0, count - 1) * Tokens.gap; }
  private expandedComposer(): boolean { return this.fieldFocused || !!this.model.draft || this.attachments.attachments.length > 0 || this.environment.fontScale >= 1.5 || this.composerWidth < Tokens.compactPane || this.composerWidth - this.leftActionWidth() - Tokens.touch - (this.leftActionWidth() ? 2 : 1) * Tokens.gap < Tokens.minimumInput; }
  private verticalActions(): boolean { return this.environment.fontScale >= 1.5 || this.composerWidth < Tokens.composerNarrowActions; }
  // Native ordering keeps the same editor mounted. Compact slots use pixel precision, not twelfths of the row.
  private composerColumns(): number { return this.expandedComposer() ? 12 : Math.max(1, Math.round(this.getUIContext().vp2px(this.composerWidth))); }
  private sendColumns(): number { const width = Tokens.touch + (this.expandedComposer() ? 0 : Tokens.gap); return Math.ceil(width * this.composerColumns() / this.composerWidth); }
  private leftColumns(): number { const width = this.leftActionWidth(); return Math.ceil((width + (width && !this.expandedComposer() ? Tokens.gap : 0)) * this.composerColumns() / this.composerWidth); }
  private primaryStop(): boolean { return !this.model.decision && this.model.canCompose() && this.model.canStop() && !this.model.draft.trim() && !this.attachments.attachments.length; }
  @Monitor('active', 'model.selected', 'model.sessions', 'model.connected', 'model.draft', 'model.decision', 'attachments.attachments', 'model.interruptPendingKey')
  primaryStopChanged(): void { this.onPrimaryStop(this.active && this.primaryStop()); }
  private maxControlsHeight(): number { return this.model.decision ? this.availableHeight * (this.environment.keyboardHeightPx > 0 ? 1 : Tokens.decisionFraction) : this.availableHeight * 0.8; }
  private measureAttachment(): void {
    if (!this.attachmentLayoutReady) return;
    const inset = nativeSheetInset(this.getUIContext(), this.attachmentNodeId, this.environment.safeBottomPx);
    if (Math.abs(inset - this.attachmentInset) > 0.1) this.attachmentInset = inset;
  }
  @Monitor('environment.safeBottomPx') safeBottomChanged(): void { this.measureAttachment(); this.measureQuestion(); }
  private async selectContext(row: ResourceRow): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = this.model.target();
    if (this.sending || !this.attachmentSheet || !this.active) return;
    this.sending = true;
    try {
      if (await this.model.addDraftContext(row, target, generation, scope) && this.active && this.model.owns(generation, scope) && this.model.target() === target) this.attachmentSheet = false;
    } finally { this.sending = false; }
  }
  private chooserLayout(): ComposerPopupLayout {
    const heights = this.chooserHeights.length ? this.chooserHeights : [Tokens.input], context = this.getUIContext();
    const naturalHeight = context.px2vp(heights.reduce((sum: number, value: number) => sum + Math.round(context.vp2px(value)), 0) + Math.max(0, heights.length - 1) * Math.round(context.vp2px(Tokens.divider)));
    return composerPopupLayout({ left: this.editorLeft, top: this.editorTop, width: this.editorWidth, height: this.editorHeight },
      { width: this.getUIContext().px2vp(this.environment.windowWidthPx) || this.availableWidth,
        top: this.getUIContext().px2vp(this.environment.safeTopPx), bottom: this.contentTop + this.availableHeight },
      Math.max(...heights), naturalHeight, this.environment.fontScale,
      { ordinaryWidth: Tokens.popupWidth, maximumWidth: Tokens.readingMax, margin: Tokens.page });
  }
  private preserveInput(event: ClickEvent): boolean { return this.fieldFocused && event.source === SourceType.TouchScreen; }
  private assignChooser(session: ComposerOverlaySession): void {
    this.chooserItems = session.kind === 'attachments' ? [
      new ComposerChoice(session, 'photos', tr('photos'), Symbols.apps),
      new ComposerChoice(session, 'files', tr('files'), Symbols.document),
      new ComposerChoice(session, 'knowledge', tr('knowledge'), Symbols.document),
      new ComposerChoice(session, 'todo', tr('todos'), Symbols.calendar),
      new ComposerChoice(session, 'inbox', tr('inbox'), Symbols.inbox)
    ] : this.actionItems.map((item: SheetInfo, index: number) => new ComposerChoice(session, String(index), item.title, undefined, item.action));
    this.chooserHeights = this.chooserItems.map(() => Tokens.input * Math.max(1, this.environment.fontScale));
  }
  private openAttachmentChooser(event: ClickEvent): void {
    if (!this.composerAvailable() || this.sending || this.attachments.busy || this.model.draftLoading) return;
    this.actionIntent = undefined;
    this.contextKind = ''; this.attachmentSheet = false;
    const session = this.chooser.open(this.model, this.active, 'attachments', 0, this.preserveInput(event));
    if (session) this.assignChooser(session);
  }
  private composerOwnerKey(): string { return JSON.stringify([this.model.scope(), this.model.connectionGeneration(), this.model.target()]); }
  private composerAvailable(): boolean { return this.active && !this.model.decision && this.model.canCompose(); }
  private openComposerActions(event: ClickEvent): void {
    this.requestComposerActions(this.preserveInput(event));
  }
  private requestComposerActions(preserveInput: boolean): void {
    if (!this.composerAvailable() || !this.showContextMenu || this.nativeChooser('actions')) return;
    this.actionIntent = new ComposerActionIntent(this.composerOwnerKey(), preserveInput, this.actionsRequest);
    this.onShowActions();
  }
  private openFallbackActions(): void { if (!this.active || !this.showContextMenu || this.composerAvailable()) return; this.actionIntent = undefined; this.chooser.retire(); this.onShowActions(); }
  @Monitor('active', 'model.profile', 'model.selected', 'model.selected.raw', 'model.sessions', 'model.connected', 'model.decision', 'actionsVisible', 'actionsRequest', 'showContextMenu')
  chooserContextChanged(): void { this.syncChooser(); }
  private syncChooser(): void {
    if (!this.composerAvailable()) {
      const previous = this.chooser.session, intent = this.actionIntent; this.actionIntent = undefined; this.chooser.retire();
      this.attachmentSheet = false; this.contextKind = '';
      if (previous?.kind === 'actions') this.onHideActions(previous.request);
      if (intent && intent.owner === this.composerOwnerKey() && this.actionsVisible) this.onHideActions(this.actionsRequest);
      return;
    }
    if (!this.showContextMenu) {
      const previous = this.chooser.session, intent = this.actionIntent; this.actionIntent = undefined;
      if (previous?.kind === 'actions') { this.chooser.retire(); this.onHideActions(previous.request); }
      if (intent && intent.owner === this.composerOwnerKey() && this.actionsVisible) this.onHideActions(this.actionsRequest);
    }
    this.chooser.sync(this.model, true);
    const current = this.chooser.session;
    if (current?.kind === 'actions' && (!this.actionsVisible || current.request !== this.actionsRequest)) this.closeChooser(current);
    const intent = this.actionIntent;
    if (!intent) return;
    if (!this.actionsVisible || intent.owner !== this.composerOwnerKey()) { this.actionIntent = undefined; return; }
    if (intent.request < 0) { if (intent.previous === this.actionsRequest) return; intent.request = this.actionsRequest; }
    if (intent.request !== this.actionsRequest) { this.actionIntent = undefined; return; }
    if (this.chooser.shown && this.chooser.session) { this.closeChooser(this.chooser.session); }
    if (this.chooser.presenting) return;
    const session = this.chooser.open(this.model, true, 'actions', intent.request, intent.preserveInput);
    if (session) { this.assignChooser(session); this.actionIntent = undefined; }
  }
  private closeChooser(session: ComposerOverlaySession): void {
    if (this.chooser.close(session) && session.kind === 'actions') this.onHideActions(session.request);
  }
  private finishChooser(session: ComposerOverlaySession): void {
    if (this.chooser.finish(session) && session.kind === 'actions') this.onHideActions(session.request);
    this.syncChooser();
  }
  private chooseItem(choice: ComposerChoice): void {
    const session = choice.session;
    if (!this.composerAvailable()) { this.chooser.retire(); return; }
    this.chooser.sync(this.model, true);
    if (session.owner !== this.model || !this.chooser.current(session) || session.kind === 'actions' && (!this.actionsVisible || session.request !== this.actionsRequest)) return;
    if (session.kind === 'actions') { this.closeChooser(session); choice.action?.(); return; }
    const local = choice.key === 'photos' || choice.key === 'files';
    if (!local && !this.chooser.current(session, true)) return;
    this.closeChooser(session);
    if (choice.key === 'photos') this.attachments.choosePhotos();
    else if (choice.key === 'files') this.attachments.chooseFiles();
    else { this.contextKind = choice.key; this.attachmentSheet = true; }
  }
  private itemEnabled(choice: ComposerChoice): boolean {
    return this.composerAvailable() && choice.session.owner === this.model && this.chooser.current(choice.session, choice.session.kind === 'attachments' && !['photos', 'files'].includes(choice.key));
  }
  private nativeChooser(kind: string): boolean { const session = this.chooser.session; return this.chooser.shown && session?.kind === kind && !session.preserveInput; }
  private recordChooserHeight(choice: ComposerChoice, index: number, height: number): void {
    if (!this.chooser.current(choice.session) || this.chooserItems[index] !== choice || !Number.isFinite(height) || height <= 0 || Math.abs(height - this.chooserHeights[index]) <= 0.5) return;
    const next = this.chooserHeights.slice(); next[index] = height; this.chooserHeights = next;
  }
  private attachmentMenuOptions(): MenuOptions {
    const session = this.chooser.session;
    return { onWillAppear: () => { if (session) this.chooser.begin(session); }, onDidDisappear: () => { if (session) this.finishChooser(session); } };
  }
  private actionContextMenuOptions(): ContextMenuOptions {
    const session = this.chooser.session;
    return { preview: MenuPreviewMode.IMAGE,
      onWillAppear: () => { if (session) this.chooser.begin(session); },
      onDidDisappear: () => { if (session) this.finishChooser(session); } };
  }
  private chooserPopupOptions(): CustomPopupOptions {
    const session = this.chooser.session, layout = this.chooserLayout();
    return { builder: (): void => this.chooserContent(), focusable: false, mask: true, autoCancel: true,
      placement: layout.side === 'left' ? Placement.LeftTop : layout.side === 'right' ? Placement.RightTop : layout.side === 'below' ? Placement.BottomLeft : Placement.TopLeft,
      keyboardAvoidMode: KeyboardAvoidMode.DEFAULT, avoidTarget: AvoidanceMode.AVOID_AROUND_TARGET,
      onStateChange: (event: PopupStateChangeParam): void => { if (!session) return; if (event.isVisible) this.chooser.begin(session); else this.finishChooser(session); },
      onWillDismiss: (action: DismissPopupAction): void => { if (session) this.closeChooser(session); action.dismiss(); } };
  }
  @Builder chooserContent() {
    List() {
      ForEach(this.chooserItems, (choice: ComposerChoice, index: number) => {
        ListItem() {
          Row({ space: Tokens.inset }) {
            if (choice.symbol) SymbolGlyph(choice.symbol).fontSize(Tokens.symbolFont).fontColor([Colors.link]).width(Tokens.symbol)
            Text(choice.title).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
          }.constraintSize({ minHeight: Tokens.input }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.gap }).width('100%')
            .onAreaChange((_old: Area, area: Area) => this.recordChooserHeight(choice, index, Number(area.height)))
        }.enabled(this.itemEnabled(choice)).onClick(() => this.chooseItem(choice)).attributeModifier(choice)
          .id('chat.' + (choice.session.kind === 'attachments' ? 'attachment.choice.' : 'action.choice.') + choice.key)
      }, (choice: ComposerChoice) => choice.session.id + ':' + choice.key)
    }.width(this.chooserLayout().width).height(this.chooserLayout().maximumHeight).scrollBar(BarState.Auto)
      .divider({ strokeWidth: Tokens.divider, startMargin: Tokens.page, endMargin: Tokens.page, color: Colors.divider })
  }
  @Builder attachmentNativeMenu() {
    Menu() {
      ForEach(this.chooserItems, (choice: ComposerChoice) => {
        MenuItem({ content: choice.title, symbolStartIcon: choice.symbol ? new SymbolGlyphModifier(choice.symbol) : undefined })
          .constraintSize({ minHeight: Tokens.touch }).enabled(this.itemEnabled(choice)).onClick(() => this.chooseItem(choice))
          .id('chat.' + (choice.session.kind === 'attachments' ? 'attachment.choice.' : 'action.choice.') + choice.key)
      }, (choice: ComposerChoice) => choice.session.id + ':' + choice.key)
    }
  }
  @Builder attachmentContent() {
    Column() {
      if (this.contextKind) ContextPicker({ model: this.model, kind: this.contextKind, active: this.active && this.attachmentSheet,
        onSelect: (row: ResourceRow) => this.selectContext(row), onRetired: () => this.attachmentSheet = false })
    }.width('100%').height('100%').padding({ bottom: this.attachmentInset + Tokens.page }).id(this.attachmentNodeId)
      .onAreaChange(() => this.measureAttachment())
  }

  @Local foldContainerHeight: number = 0;
  private ordinarySplitRatio(): number {
    const secondary = this.model.decision ? this.maxControlsHeight() : Math.max(1, Math.min(this.controlsHeight, this.maxControlsHeight()));
    return Math.max(1, (this.foldContainerHeight || this.availableHeight) - secondary) / secondary;
  }
  @Monitor('model.messages', 'model.transcriptHistoryReady') messagesChanged(): void {
    this.syncTimeline(); this.refreshFeed(); this.syncRevision();
    if (this.readingRestoring) { this.scheduleReading(); }
    else if (this.atEnd) this.getUIContext().postFrameCallback(new ConversationFrame(() => { if (this.active && this.atEnd && !this.readingRestoring) this.scroller.scrollEdge(Edge.Bottom); })); else this.hasNew = true;
  }
  @Monitor('feed.items', 'feed.hasLoaded') feedChanged(): void { this.syncTimeline(); if (this.readingRestoring) this.scheduleReading(); else if (this.atEnd) this.getUIContext().postFrameCallback(new ConversationFrame(() => { if (this.active && this.atEnd && !this.readingRestoring) this.scroller.scrollEdge(Edge.Bottom); })); else this.hasNew = true; }
  @Monitor('active', 'model.selected', 'model.connected') feedContextChanged(): void { if (!this.active) { this.readingMotion++; this.saveReading(); } else this.syncReading(); this.syncFeed(); this.syncAttachments(); this.syncRevision(); }
  @Monitor('model.decision') decisionChanged(): void { this.refreshFeed(); }
  @Monitor('active', 'model.selected', 'model.profile', 'model.connected', 'model.personaQuestions')
  questionsContextChanged(): void { this.questions.sync(this.model, this.active); }
  @Monitor('model.personaQuestions') personaQuestionsChanged(): void {
    this.syncTimeline(); this.refreshFeed();
    if (this.readingRestoring) { this.scheduleReading(); }
    else if (this.atEnd) this.getUIContext().postFrameCallback(new ConversationFrame(() => { if (this.active && this.atEnd && !this.readingRestoring) this.scroller.scrollEdge(Edge.Bottom); })); else this.hasNew = true;
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
    if (!this.active) this.attachmentSheet = false;
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
  private primaryActionKey(): string {
    if (this.primaryStop()) return 'stop';
    if (this.model.stopPending() || this.sending || this.model.operationState === 'pending') return 'pending';
    return this.model.operationState === 'unknown' ? 'result_unknown' : 'send';
  }
  private sendCurrent(): boolean { return this.mounted && this.active && this.admission(); }
  private async sendDraft(): Promise<void> {
    if (!this.sendCurrent() || this.sending || this.attachments.busy || !this.model.canSend()) return;
    const owner = this.model, generation = owner.connectionGeneration(), scope = owner.scope(), target = owner.target(), draft = owner.draft, context = owner.sourceContext;
    const current = (): boolean => this.sendCurrent() && this.model === owner && owner.owns(generation, scope) && owner.target() === target;
    if (!this.attachments.attachments.length) { await owner.send(current); return; }
    this.sending = true;
    try {
      const prepared = await this.attachments.prepareSend(owner.prepareMessageText(draft), current);
      await owner.sendPrepared(prepared, draft, context, current);
    } catch (error) { if (current()) this.attachments.errorKey = error instanceof Error && error.message.startsWith('chat_attachment_') ? error.message : 'chat_attachment_send_failed'; }
    finally { this.sending = false; }
  }
  private async preview(item: ChatAttachment): Promise<void> {
    const host = this.getUIContext().getHostContext(), key = this.attachmentKey; if (!host) return;
    try { await previewAttachment(host as common.UIAbilityContext, item.localPath, item.name, item.mime); }
    catch (_) { if (key === this.attachmentKey) this.attachments.errorKey = 'preview_unavailable'; }
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
  private messageMenuBuilder(entry: RepeatItem<ConversationLine>): CustomBuilder {
    return (): void => { const message = entry.item.message; if (message) this.messageMenu(message); };
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
    reconcileTimelineRows(this.lines, lines);
  }
  aboutToAppear(): void {
    this.mounted = true;
    this.questions.sync(this.model, this.active); this.chooser.sync(this.model, this.active);
    this.syncReading(); this.syncFeed(); this.syncAttachments(); this.syncTimeline(); this.syncRevision(); this.primaryStopChanged();
    const target = this.model.target(); this.model.loadView('work:' + target).then((value: string) => {
      if (target === this.model.target() && value) { try { this.expanded = Wire.strings(Wire.parse(value), 'expanded'); } catch (_) {} }
    });
  }
  aboutToDisappear(): void {
    this.mounted = false;
    this.questions.retire(); this.actionIntent = undefined; this.chooser.retire();
    this.onPrimaryStop(false); this.saveReading(); this.readingEpoch++;
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
  @Builder message(entry: RepeatItem<ConversationLine>) {
    Column({ space: Tokens.gap }) {
      if (entry.item.message!.work) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph((this.expanded.includes(entry.item.message!.id) ? Symbols.up : Symbols.down)).fontSize(Tokens.symbolFont).fontColor([IconActions.color('work_summary', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('work_summary')).bindTips(tr('work_summary')).onClick(() => { this.toggle(entry.item.message!.id); if (this.expanded.includes(entry.item.message!.id)) this.model.expandWork(entry.item.message!); }).backgroundColor(Color.Transparent)
        if (this.expanded.includes(entry.item.message!.id)) {
          Text(entry.item.message!.work).fontFamily('monospace').fontSize(Tokens.meta).copyOption(CopyOptions.LocalDevice)
        }
      }
      if (entry.item.message!.localImageCount > 0) Text(tr('photos') + ' · ' + entry.item.message!.localImageCount).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (entry.item.message!.source) Text(entry.item.message!.source).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (entry.item.message!.ownerTarget === this.model.target()) ForEach(entry.item.message!.imagePaths, (path: string, index: number) => {
        TranscriptImage({ model: this.model, remotePath: path, active: this.active,
          sourceKey: this.imageKey(entry.item.message!, 'path', path, index), galleryEntries: this.imageGallery,
          galleryIndex: this.imageIndex(entry.item.message!, 'path', path, index), onEdit: (image: LoadedMessageImage) => this.editReceivedImage(image) })
      }, (path: string, index: number) => this.imageKey(entry.item.message!, 'path', path, index))
      ForEach(entry.item.message!.filePaths, (path: string) => { Button(path.split('/').pop() || path, { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.onOpenFile(path)) }, (path: string) => path)
      if (entry.item.message!.imageId && entry.item.message!.ownerTarget === this.model.target()) TranscriptImage({ model: this.model, itemId: entry.item.message!.imageId, active: this.active,
        sourceKey: this.imageKey(entry.item.message!, 'item', entry.item.message!.imageId), galleryEntries: this.imageGallery,
        galleryIndex: this.imageIndex(entry.item.message!, 'item', entry.item.message!.imageId), onEdit: (image: LoadedMessageImage) => this.editReceivedImage(image) })
      if (entry.item.message!.text) {
        WithTheme({ colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT, theme: nativeMessageTheme(this.look, this.model.style, entry.item.message!.role === 'user', this.environment.dark) }) {
        MessageSurface({ own: entry.item.message!.role === 'user', maximum: entry.item.message!.role === 'user' ? Math.max(Tokens.touch, Math.min(this.availableWidth, Tokens.readingMax) - Tokens.page * 2 - Tokens.touch) : Tokens.readingMax }) {
          if (entry.item.message!.role === 'assistant' && this.model.selected.kind === 'conversation') AssistantMessage({ model: this.model, feed: this.feed, message: entry.item.message!, active: this.active, onLink: (address: string) => this.onLink(address), onOpenResource: (row: ResourceRow) => this.onOpenResource(row) })
          else MarkdownText({ text: entry.item.message!.text, fitContent: entry.item.message!.role === 'user', onLink: (address: string) => this.onLink(address) })
        }
        }
      }
      if (entry.item.message!.held) this.heldStatus(entry.item.message!)
      if (entry.item.message!.deliveryState && entry.item.message!.deliveryState !== 'done' && !entry.item.message!.failed) Text(tr(entry.item.message!.deliveryState === 'unknown' ? 'result_unknown' : entry.item.message!.deliveryState)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (entry.item.message!.failed) Text(tr('failed')).fontColor($r('app.color.persona_danger')).fontSize(Tokens.meta)
      if (entry.item.message!.localOperation && this.model.canCheckLocalMessage(entry.item.message!.localOperation)) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('checked')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('checked', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('checked')).bindTips(tr('checked')).onClick(() => this.confirmChecked(entry.item.message!.localOperation))
      if (entry.item.message!.localOperation && this.model.canRestoreMessage(entry.item.message!.localOperation)) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('restore_draft')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('restore_draft', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('restore_draft')).bindTips(tr('restore_draft')).onClick(() => this.restoreDraft(entry.item.message!))
    }.width('100%').alignItems(entry.item.message!.role === 'user' ? HorizontalAlign.End : HorizontalAlign.Start).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.gap })
      .bindContextMenu(this.messageMenuBuilder(entry), ResponseType.LongPress, { preview: MenuPreviewMode.IMAGE })
  }
  @Builder actionsMenu() {
    Menu() {
      ForEach(this.actionItems, (item: SheetInfo) => {
        MenuItem({ content: item.title }).constraintSize({ minHeight: Tokens.touch }).onClick(() => item.action())
      }, (_item: SheetInfo, index: number) => String(this.actionsRequest) + ':' + String(index))
    }
  }
  private confirmHeldDelete(message: ChatMessage): void {
    const model = this.model, generation = model.connectionGeneration(), scope = model.scope(), visibility = this.revisionVisibilityRevision;
    const current = (): boolean => this.sendCurrent() && this.model === model && model.owns(generation, scope) && this.revisionVisibilityRevision === visibility;
    if (!current() || !model.canActOnHeld(message, false)) return;
    const photos = message.localImageCount > 0 ? tr('photos') + ' · ' + message.localImageCount : '';
    const body = [message.text, photos].filter((part: string) => !!part).join('\n');
    this.getUIContext().showAlertDialog({ title: tr('agent_delete_held_title'), message: body,
      primaryButton: { value: tr('cancel'), defaultFocus: true, action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => model.actOnHeld(message, false, current) } });
  }
  @Builder heldStatus(message: ChatMessage) {
    Column({ space: Tokens.fine }) {
      Text(message.held!.line || tr(message.held!.state === 'failed' ? 'agent_delivery_failed' : 'agent_delivery_waiting').replace('{computer}', () => message.held!.computer))
        .fontSize(Tokens.meta).fontColor(message.held!.state === 'failed' ? Colors.danger : Colors.working)
        .constraintSize({ maxWidth: '100%' }).id('chat.held.status.' + message.held!.id)
      if (this.model.heldRetryPending(message)) Text(tr('agent_delivery_updating'))
        .fontSize(Tokens.meta).fontColor(Colors.secondary).constraintSize({ maxWidth: '100%' }).id('chat.held.update.' + message.held!.id)
      Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.End }) {
        if (this.model.offersHeldAction(message, true)) Button(tr('retry')).buttonStyle(ButtonStyleMode.TEXTUAL)
          .constraintSize({ minHeight: Tokens.touch, minWidth: Tokens.touch }).enabled(this.model.canActOnHeld(message, true))
          .onClick(() => this.model.actOnHeld(message, true, () => this.sendCurrent())).id('chat.held.retry.' + message.held!.id)
        if (this.model.offersHeldAction(message, false)) Button(tr('cancel')).buttonStyle(ButtonStyleMode.TEXTUAL)
          .constraintSize({ minHeight: Tokens.touch, minWidth: Tokens.touch }).enabled(this.model.canActOnHeld(message, false))
          .onClick(() => this.confirmHeldDelete(message)).id('chat.held.cancel.' + message.held!.id)
      }.width('100%')
    }.alignItems(HorizontalAlign.End).width('100%')
  }
  private agentNotice(): string {
    return tr(this.model.agentInputNotice()).replace('{computer}', () => this.model.machineName(this.model.selected.machine));
  }
  private actionMenuOptions(request: number): MenuOptions {
    return { onDisappear: () => this.onHideActions(request) };
  }
  @Builder readingContent() {
    Column() {
      if (this.revisions.forkState === 'branching' || this.revisions.rewindState === 'rewinding') LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.revisionResult || this.revisions.forkError || this.revisions.rewindError) Text(tr(this.revisions.forkError || this.revisions.rewindError || this.revisionResult)).fontSize(Tokens.meta).padding(Tokens.inset).width('100%')
      if (this.revisionCacheError) Text(tr(this.revisionCacheError)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      if (this.model.error) {
        Row() {
          Text(tr(this.model.error)).fontSize(Tokens.meta).layoutWeight(1)
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.model.refresh())
        }.padding(Tokens.inset)
      }
      if (this.model.unresolvedWrites.length) {
        Row({ space: Tokens.gap }) {
          Text(tr('result_unknown')).layoutWeight(1)
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).onClick(() => this.model.reconcileWrites())
          if (this.model.unresolvedWrites.some((record) => record.state === 'unknown' && !record.requiresReceipt))
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('checked')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('checked', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('checked')).bindTips(tr('checked')).onClick(() => { const record = this.model.unresolvedWrites.find((item) => item.state === 'unknown' && !item.requiresReceipt); if (record) this.confirmChecked(record.id); })
        }.padding(Tokens.inset)
      }
      List({ scroller: this.scroller, space: Tokens.gap }) {
        if (this.readingFailed) ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.retryReading()) }
        if (this.feed.error && this.feed.error !== this.model.error) ListItem() {
          Row({ space: Tokens.gap }) { Text(tr(this.feed.error)).fontSize(Tokens.meta).layoutWeight(1); Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.feed.refresh()) }.padding(Tokens.inset)
        }
        if (this.model.selected.kind === 'conversation') ListItem() {
          AssistantCards({ model: this.model, feed: this.feed, active: this.active && this.model.connected, onOpenResource: (row: ResourceRow) => this.onOpenResource(row), dockOnly: true, onOpenCard: (itemId: string, kind: string) => this.onOpenCard(itemId, kind), onOpenSession: (row: ResourceRow) => this.onOpenResource(row),
            onOpenTodo: (row: ResourceRow) => this.onOpenResource(row), onOpenInbox: (row: ResourceRow) => this.onOpenResource(row), onLink: (address: string) => this.onLink(address) })
        }
        if (this.model.hasMoreMessages || this.feed.nextCursor) ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('load_more')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('load_more', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('load_more')).bindTips(tr('load_more')).enabled(!this.readingLoading).onClick(() => this.loadEarlier()) }
        Repeat(this.lines).each((entry: RepeatItem<ConversationLine>) => {
          ListItem() {
            if (entry.item.message) this.message(entry)
            else if (entry.item.card) AssistantCards({ model: this.model, feed: this.feed, active: this.active && this.model.connected, onOpenResource: (row: ResourceRow) => this.onOpenResource(row), item: entry.item.card, onOpenCard: (itemId: string, kind: string) => this.onOpenCard(itemId, kind), onOpenSession: (row: ResourceRow) => this.onOpenResource(row),
              onOpenTodo: (row: ResourceRow) => this.onOpenResource(row), onOpenInbox: (row: ResourceRow) => this.onOpenResource(row), onLink: (address: string) => this.onLink(address) })
            else if (entry.item.decision) this.questionCard(entry)
          }.padding({ left: entry.item.message ? 0 : Tokens.page, right: entry.item.message ? 0 : Tokens.page }).id('chat.line.' + entry.item.id)
        }).key((line: ConversationLine) => line.id).virtualScroll({ totalCount: this.lines.length, reusable: false })
      }.layoutWeight(1).width('100%').id('chat.transcript')
        .bindSheet(this.questions.shown, this.questionContent(), { title: { title: tr('interaction_question_reopen') }, height: SheetSize.LARGE, keyboardAvoidMode: SheetKeyboardAvoidMode.RESIZE_ONLY, showClose: true, onWillAppear: () => this.presentedQuestion = this.questions.beginPresentation(), onAppear: () => { this.questionLayoutReady = true; this.measureQuestion(); }, onWillDismiss: (action: DismissSheetAction) => this.questions.dismiss(this.presentedQuestion, () => action.dismiss()), onDisappear: () => { this.questionLayoutReady = false; this.questions.endPresentation(this.presentedQuestion); this.presentedQuestion = undefined; } })
        .maintainVisibleContentPosition(true).onScrollStart(() => this.readingMotion++).onScrollIndex((start: number) => { this.firstVisibleIndex = start; this.recordReading(start); }).onDidScroll(() => this.recordReading(this.firstVisibleIndex)).onReachEnd(() => { if (this.active && !this.readingRestoring) { this.atEnd = true; this.anchor.atEnd = true; this.hasNew = false; } })
        .onScrollFrameBegin((offset: number) => { if (offset < 0 && !this.readingRestoring) { this.atEnd = false; this.anchor.atEnd = false; } return { offsetRemain: offset }; })
    }.width('100%').constraintSize({ maxWidth: Tokens.readingMax }).height('100%')
  }

  private openQuestion(request: Decision): void {
    if (!this.active) return;
    if (this.chooser.session) this.closeChooser(this.chooser.session);
    this.attachmentSheet = false; this.contextKind = '';
    this.questions.open(this.model, this.active, request);
  }
  private measureQuestion(): void { if (this.questionLayoutReady) this.questionInset = nativeSheetInset(this.getUIContext(), this.questionNodeId, this.environment.safeBottomPx); }
  private questionLink(sheet: QuestionSheet): (address: string) => void {
    const valid = this.questions.ticket(sheet); return (address: string): void => { if (valid()) this.onLink(address); };
  }
  @Builder questionCard(entry: RepeatItem<ConversationLine>) {
    if (['open', 'pending', 'unknown'].includes(entry.item.decision!.status)) Row({ space: Tokens.inset }) {
      Text(questionPrompt(entry.item.decision!) || tr('question')).fontSize(Tokens.title).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(3).textOverflow({ overflow: TextOverflow.Ellipsis }).layoutWeight(1)
      Button({ type: ButtonType.Circle }) { if (IconActions.busy(entry.item.decision!.status === 'open' ? 'interaction_question_reopen' : entry.item.decision!.status === 'unknown' ? 'result_unknown' : 'pending')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(entry.item.decision!.status === 'open' ? 'interaction_question_reopen' : entry.item.decision!.status === 'unknown' ? 'result_unknown' : 'pending')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(entry.item.decision!.status === 'open' ? 'interaction_question_reopen' : entry.item.decision!.status === 'unknown' ? 'result_unknown' : 'pending', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(entry.item.decision!.status === 'open' ? 'interaction_question_reopen' : entry.item.decision!.status === 'unknown' ? 'result_unknown' : 'pending')).bindTips(tr(entry.item.decision!.status === 'open' ? 'interaction_question_reopen' : entry.item.decision!.status === 'unknown' ? 'result_unknown' : 'pending'))
        .enabled(this.active && this.model.canApprove() && entry.item.decision!.status === 'open').onClick(() => this.openQuestion(entry.item.decision!)).id('interaction.open.' + entry.item.decision!.id)
    }.width('100%').padding(Tokens.page).alignItems(VerticalAlign.Center).backgroundColor(Colors.question).borderRadius(Tokens.card)
    else InteractionDecisionForm({ model: this.model, request: entry.item.decision!, readOnly: true, onLink: (address: string) => this.onLink(address) })
  }
  @Builder questionContent() {
    Column() {
      if (this.questions.sheet) ForEach([this.questions.sheet], (sheet: QuestionSheet) => {
        InteractionDecisionForm({ model: this.model, request: sheet.request, pinFooter: true, draft: sheet.draft, draftSession: sheet.id, admission: this.questions.ticket(sheet),
          onLater: () => this.questions.close(sheet), onResolved: () => this.questions.close(sheet), onLink: this.questionLink(sheet) })
      }, (sheet: QuestionSheet) => sheet.id)
    }.width('100%').height('100%').padding({ bottom: this.questionInset + Tokens.page }).id(this.questionNodeId)
      .onAreaChange(() => this.measureQuestion())
  }

  @Builder composer() {
    Column({ space: Tokens.gap }) {
      AttachmentStrip({ attachments: this.attachments.attachments, errorKey: this.attachments.errorKey, actionsEnabled: !this.sending && !this.attachments.busy,
          onRemove: (id: string) => this.attachments.remove(id), onEdit: (id: string) => this.attachments.editPhoto(id), onPreview: (item: ChatAttachment) => this.preview(item) })
      if (this.model.sourceContext) Row({ space: Tokens.gap }) {
        Text(this.model.contextTitle()).fontSize(Tokens.meta).fontColor(Colors.ink).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis }).layoutWeight(1)
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('remove')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('remove', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('remove')).bindTips(tr('remove')).enabled(!this.sending).onClick(() => this.model.removeContext())
      }.width('100%')
      GridRow({ columns: this.composerColumns(), gutter: { x: 0, y: Tokens.gap } }) {
        GridCol({ span: this.expandedComposer() ? this.composerColumns() : this.composerColumns() - this.leftColumns() - this.sendColumns(), order: this.expandedComposer() ? 0 : 1 }) {
          TextArea({ text: this.model.draft, placeholder: this.model.agentInputNotice() === 'agent_computer_holds' ? this.agentNotice() : tr('message') }).maxLength(Tokens.maxMessage).width('100%')
            .fontSize(Tokens.body).placeholderFont({ size: Tokens.body }).fontColor(Colors.ink).placeholderColor(Colors.secondary)
            .lineHeight(String(Tokens.lineHeight) + 'fp').caretColor(Colors.action).backgroundColor(Color.Transparent)
            .padding({ left: Tokens.gap, right: Tokens.gap, top: Tokens.inset, bottom: Tokens.inset })
            .constraintSize({ minHeight: Tokens.touch, maxHeight: Math.max(Tokens.inputLines, Tokens.lineHeight * this.environment.fontScale + Tokens.section) })
            .height(this.expandedComposer() ? undefined : Tokens.touch)
            .enabled(!this.sending && !this.model.draftLoading).onFocus(() => this.fieldFocused = true).onBlur(() => this.fieldFocused = false)
            .onChange((value: string) => this.model.updateDraft(value)).id('chat.input')
        }.id(this.editorAnchorId)
          .onAreaChange((_old: Area, area: Area) => { this.editorLeft = Number(area.globalPosition.x); this.editorTop = Number(area.globalPosition.y); this.editorWidth = Number(area.width); this.editorHeight = Number(area.height); })
          .bindPopup(this.chooser.shown && this.chooser.session?.preserveInput === true, this.chooserPopupOptions())
        if (!this.model.demo || this.showContextMenu) GridCol({ span: this.verticalActions() ? this.composerColumns() : this.leftColumns(), order: this.expandedComposer() ? this.verticalActions() ? 2 : 1 : 0 }) {
          Row({ space: Tokens.gap }) {
            if (!this.model.demo) Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.plus).fontSize(Tokens.symbolFont).fontColor([Colors.ink]) }
              .attributeModifier(ButtonStyles.quiet).width(Tokens.touch).height(Tokens.touch).padding(0).accessibilityText(tr('attach'))
              .enabled(!this.sending && !this.attachments.busy && !this.model.draftLoading).onClick((event: ClickEvent) => this.openAttachmentChooser(event)).id('chat.attach')
              .bindMenu(this.nativeChooser('attachments'), this.attachmentNativeMenu, this.attachmentMenuOptions())
            if (this.showContextMenu) Button({ type: ButtonType.Circle }) { Image(Symbols.more).width(Tokens.symbol).height(Tokens.symbol).fillColor(Colors.ink) }
              .attributeModifier(ButtonStyles.quiet).width(Tokens.touch).height(Tokens.touch).padding(0).accessibilityText(tr('more')).onClick((event: ClickEvent) => this.openComposerActions(event))
              .parallelGesture(LongPressGesture().onAction(() => this.requestComposerActions(false)))
              .bindContextMenu(this.nativeChooser('actions'), this.attachmentNativeMenu, this.actionContextMenuOptions()).id('chat.more')
          }.width('100%').constraintSize({ minHeight: Tokens.touch }).alignItems(VerticalAlign.Center)
        }
        if (this.expandedComposer() && !this.verticalActions()) GridCol({ span: this.composerColumns() - this.leftColumns() - this.sendColumns(), order: 3 }) {
          Row() {
            Blank().layoutWeight(1)

          }.width('100%').constraintSize({ minHeight: Tokens.touch })
        }
        GridCol({ span: this.verticalActions() ? this.composerColumns() : this.sendColumns(), order: this.expandedComposer() ? this.verticalActions() ? 1 : 4 : 2 }) {
          Row() {
          Button({ type: ButtonType.Circle }) {
            if (IconActions.busy(this.primaryActionKey())) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction)
            else SymbolGlyph(this.primaryActionKey() === 'stop' ? Symbols.stop : IconActions.symbol(this.primaryActionKey())).fontSize(Tokens.symbolFont).fontColor([Colors.onAction])
          }.accessibilityText(tr(this.primaryActionKey())).attributeModifier(ButtonStyles.primary).padding(0).width(Tokens.touch).height(Tokens.touch)
            .enabled(this.sendCurrent() && (this.primaryStop() || this.model.canSend() && !this.model.stopPending() && !this.sending && !this.attachments.busy && !this.model.draftLoading &&
              (this.model.draft.trim().length > 0 || this.attachments.attachments.length > 0) && this.model.operationState !== 'pending' && this.model.operationState !== 'unknown'))
            .onClick(() => { if (!this.sendCurrent()) return; if (this.primaryStop()) this.model.stop(() => this.sendCurrent()); else this.sendDraft(); }).id('chat.send')
          }.width('100%').justifyContent(FlexAlign.End)
        }
      }.width('100%').onAreaChange((_old: Area, area: Area) => { const width = Number(area.width); if (Number.isFinite(width) && width > 0) this.composerWidth = width; })
    }.padding(Tokens.gap).backgroundColor(Colors.subtle).borderRadius(Tokens.capsule).width('100%').id('chat.composer')
  }

  @Builder blockingContent() {
    Column({ space: Tokens.gap }) {
      Column() {
        ForEach(this.model.decision ? [this.model.decision] : [], (request: Decision) => {
          InteractionDecisionForm({ model: this.model, request: request, fillAvailable: true, onLink: (address: string) => this.onLink(address) })
        }, (request: Decision) => request.id)
      }.width('100%').layoutWeight(1)
      if (this.showContextMenu) Button({ type: ButtonType.Circle }) { Image(Symbols.more).width(Tokens.symbol).height(Tokens.symbol).fillColor(Colors.ink) }
        .attributeModifier(ButtonStyles.quiet).width(Tokens.touch).height(Tokens.touch).padding(0).accessibilityText(tr('more')).onClick(() => this.openFallbackActions())
        .bindMenu(this.actionsVisible, this.actionsMenu, this.actionMenuOptions(this.actionsRequest)).id('chat.more.fallback')
      if (this.hasNew && !this.atEnd) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('latest')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('latest', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('latest')).bindTips(tr('latest')).onClick(() => { this.scroller.scrollEdge(Edge.Bottom); this.atEnd = true; this.anchor.atEnd = true; this.hasNew = false; }).id('chat.latest')
    }.width('100%').constraintSize({ maxWidth: Tokens.readingMax }).height('100%')
      .padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.footer, top: Tokens.gap })
  }
  @Builder interactionContent() {
    if (this.model.decision) this.blockingContent()
    else Scroll() {
      Column({ space: Tokens.gap }) {
      if (this.showContextMenu && (this.model.decision || !this.model.canCompose())) Button({ type: ButtonType.Circle }) { Image(Symbols.more).width(Tokens.symbol).height(Tokens.symbol).fillColor(Colors.ink) }
        .attributeModifier(ButtonStyles.quiet).width(Tokens.touch).height(Tokens.touch).padding(0).accessibilityText(tr('more')).onClick(() => this.openFallbackActions())
        .bindMenu(this.actionsVisible, this.actionsMenu, this.actionMenuOptions(this.actionsRequest)).id('chat.more.fallback')
      if (this.hasNew && !this.atEnd) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('latest')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('latest', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('latest')).bindTips(tr('latest')).onClick(() => { this.scroller.scrollEdge(Edge.Bottom); this.atEnd = true; this.anchor.atEnd = true; this.hasNew = false; }).id('chat.latest')
      if (this.model.canCompose() && !this.model.decision) {
        if (this.attachments.busy && !this.sending && (this.attachments.errorKey === 'chat_attachment_storage_unavailable' || this.model.error === 'cache_unavailable')) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => { this.attachmentKey = ''; this.syncAttachments(); })
        this.composer()
      } else if (!this.model.canSend() && !this.model.decision) {
        if (this.model.canAttach()) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('attach_live')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('attach_live', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('attach_live')).bindTips(tr('attach_live')).onClick(() => this.model.attach())
        Text(this.model.agentInputNotice() ? this.agentNotice() : tr('read_only')).fontSize(Tokens.meta).padding(Tokens.page)
      }
      }.width('100%').constraintSize({ maxWidth: Tokens.readingMax }).padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.footer, top: Tokens.gap }).onAreaChange((_before: Area, area: Area): void => {
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
        } }).onAreaChange((_old: Area, area: Area): void => { const height = Number(area.height); if (Number.isFinite(height) && height > 0 && Math.abs(height - this.foldContainerHeight) > 0.1) this.foldContainerHeight = height; })
      } else {
        Column() { this.readingContent() }.width('100%').layoutWeight(1).clip(true)
        Column() { this.interactionContent() }.width('100%')
          .height(this.model.decision ? this.maxControlsHeight() : Math.max(1, Math.min(this.controlsHeight, this.maxControlsHeight())))
      }
    }.width('100%').height('100%').backgroundColor(Colors.canvas).expandSafeArea([SafeAreaType.SYSTEM], [SafeAreaEdge.BOTTOM])
      .bindSheet($$this.attachmentSheet, this.attachmentContent(), { title: { title: tr(this.contextKind === 'knowledge' ? 'knowledge' : this.contextKind === 'todo' ? 'todos' : this.contextKind === 'inbox' ? 'inbox' : 'attach') }, height: SheetSize.LARGE, showClose: true, onAppear: () => { this.attachmentLayoutReady = true; this.measureAttachment(); }, onDisappear: () => this.attachmentLayoutReady = false })
      .onAreaChange((_before: Area, area: Area): void => {
      const height = Number(area.height);
      const width = Number(area.width);
      this.contentTop = Number(area.globalPosition.y);
      if (Number.isFinite(width) && width > 0 && Math.abs(width - this.availableWidth) > 1) this.availableWidth = width;
      if (Number.isFinite(height) && height > 0 && Math.abs(height - this.availableHeight) > 1) this.availableHeight = height;
    })
  }
}
