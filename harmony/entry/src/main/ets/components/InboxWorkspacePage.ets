import { common } from '@kit.AbilityKit';
import { fileIo, picker } from '@kit.CoreFileKit';
import { image } from '@kit.ImageKit';
import { ComposeListItemV2, ContentItemV2, CapsuleSegmentButtonV2, SegmentButtonV2Items } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { InboxAttachmentDraft, InboxMailComposeSnapshot, InboxMailMessage, InboxMessage, InboxWorkspace } from '../models/InboxWorkspace';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { MarkdownText } from './MarkdownText';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

const MAX_IMAGE_PIXELS: number = 40000000;
const MAX_IMAGE_EDGE: number = 8192;
const MAX_UPLOAD_BYTES: number = 32 * 1024 * 1024;

class PickedInboxFile {
  name: string = '';
  mime: string = 'application/octet-stream';
  kind: string = 'file';
  data: ArrayBuffer = new ArrayBuffer(0);
}

function inboxMime(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() || '';
  if (ext === 'jpg' || ext === 'jpeg') return 'image/jpeg';
  if (ext === 'png') return 'image/png';
  if (ext === 'webp') return 'image/webp';
  if (ext === 'gif') return 'image/gif';
  if (ext === 'pdf') return 'application/pdf';
  if (ext === 'txt' || ext === 'md') return 'text/plain';
  if (ext === 'json') return 'application/json';
  if (ext === 'zip') return 'application/zip';
  return 'application/octet-stream';
}

function inboxBytesText(size: number): string {
  if (size < 1024) return String(size) + ' B';
  if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB';
  return (size / (1024 * 1024)).toFixed(1) + ' MB';
}

@ComponentV2
export struct InboxRemoteImage {
  @Require @Param workspace: InboxWorkspace;
  @Require @Param media: string;
  @Param name: string = 'image';
  @Param expectedMime: string = '';
  @Local pixels: image.PixelMap | undefined;
  @Local loading: boolean = false;
  @Local failed: boolean = false;
  @Local full: boolean = false;
  @Local ratio: number = 1;
  private active: boolean = true;

  private async load(): Promise<void> {
    if (this.pixels || this.loading || !this.active) return;
    const target = this.workspace.selected.key();
    this.loading = true; this.failed = false;
    let source: image.ImageSource | undefined;
    let mapped: image.PixelMap | undefined;
    try {
      const content = await this.workspace.downloadMessageMedia(this.media, this.name, this.expectedMime);
      if (!this.active || target !== this.workspace.selected.key()) return;
      source = image.createImageSource(content.data);
      const info = await source.getImageInfo();
      if (info.size.width <= 0 || info.size.height <= 0 || info.size.width > MAX_IMAGE_EDGE || info.size.height > MAX_IMAGE_EDGE ||
        info.size.width * info.size.height > MAX_IMAGE_PIXELS) throw new Error('image_limit');
      const scale = Math.min(1, 1800 / Math.max(info.size.width, info.size.height));
      mapped = await source.createPixelMap({ desiredSize: { width: Math.max(1, Math.round(info.size.width * scale)), height: Math.max(1, Math.round(info.size.height * scale)) } });
      if (!this.active || target !== this.workspace.selected.key()) { await mapped.release(); mapped = undefined; return; }
      this.ratio = info.size.width / info.size.height; this.pixels = mapped; mapped = undefined;
    } catch (_) { if (this.active && target === this.workspace.selected.key()) this.failed = true; }
    finally { await mapped?.release(); await source?.release(); this.loading = false; }
  }

  aboutToDisappear(): void { this.active = false; this.pixels?.release(); this.pixels = undefined; }

  @Builder fullView() {
    Column() {
      Row() { Text(this.name).layoutWeight(1).maxLines(1); Button(tr('close')).onClick(() => this.full = false) }
        .padding(Tokens.page).width('100%')
      if (this.pixels) Image(this.pixels).objectFit(ImageFit.Contain).width('100%').layoutWeight(1)
    }.width('100%').height('100%').backgroundColor($r('app.color.persona_canvas'))
  }

  build() {
    Column() {
      if (this.pixels) Image(this.pixels).width('100%').aspectRatio(this.ratio).objectFit(ImageFit.Contain)
        .onClick(() => this.full = true).accessibilityText(this.name)
      else if (this.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      else Button(tr(this.failed ? 'retry' : 'image')).onClick(() => this.load())
    }.width('100%').padding({ top: Tokens.gap, bottom: Tokens.gap })
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0 && !this.failed) this.load(); })
      .bindContentCover($$this.full, this.fullView())
  }
}

@ComponentV2
export struct InboxMessageParts {
  @Require @Param workspace: InboxWorkspace;
  @Require @Param parts: Record<string, Object>[];
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Event onSaveMedia: (media: string, name: string, mime: string) => void = (_media: string, _name: string, _mime: string) => {};

  private line(part: Record<string, Object>): string {
    const kind = Wire.text(part, 'kind');
    if (kind === 'emoji') return Wire.text(part, 'text', Wire.text(part, 'key'));
    if (kind === 'file') return Wire.text(part, 'name', tr('file'));
    if (kind === 'card') return Wire.text(part, 'fallback', tr('inbox_card_unsupported'));
    if (kind === 'share') {
      const fields = Wire.rows(part, 'fields').map((item: Record<string, Object>) => Wire.text(item, 'label') + ': ' + Wire.text(item, 'value'));
      return [Wire.text(part, 'title'), Wire.text(part, 'subtitle')].filter((value: string) => !!value).concat(fields).join('\n');
    }
    if (kind === 'forward') {
      const previews = Wire.rows(part, 'previews');
      const messages = Wire.rows(part, 'messages');
      const rows = previews.length ? previews : messages;
      const text = rows.map((item: Record<string, Object>) => {
        const sender = Wire.text(item, 'sender_name', Wire.text(Wire.object(item, 'sender'), 'name'));
        return (sender ? sender + ': ' : '') + Wire.text(item, 'text');
      }).filter((value: string) => !!value);
      return Wire.text(part, 'title') + (text.length ? '\n' + text.join('\n') : '');
    }
    const block = Wire.text(part, 'block'), list = Wire.object(part, 'list');
    const prefix = block === 'list_item' ? Wire.bool(list, 'ordered') ? String(Wire.number(list, 'start', 1)) + '. ' : '• ' : block === 'quote' ? '› ' : '';
    return prefix + Wire.text(part, 'text');
  }

  private partWeight(part: Record<string, Object>): FontWeight { return Wire.strings(part, 'marks').includes('bold') ? FontWeight.Bold : FontWeight.Normal; }
  private partStyle(part: Record<string, Object>): FontStyle { return Wire.strings(part, 'marks').includes('italic') ? FontStyle.Italic : FontStyle.Normal; }

  @Builder part(part: Record<string, Object>) {
    if (Wire.text(part, 'kind') === 'divider') Divider().strokeWidth(Tokens.hairline).width('100%')
    else if (Wire.text(part, 'kind') === 'image' || Wire.text(part, 'kind') === 'sticker') {
      if (Wire.text(part, 'media')) InboxRemoteImage({ workspace: this.workspace, media: Wire.text(part, 'media'), name: Wire.text(part, 'name', tr('image')), expectedMime: Wire.text(part, 'mime') })
      else if (Wire.text(part, 'text')) Text(Wire.text(part, 'text')).width('100%')
    } else if (Wire.text(part, 'kind') === 'file' || Wire.text(part, 'kind') === 'video' || Wire.text(part, 'kind') === 'audio') {
      Row({ space: Tokens.gap }) {
        Text(this.line(part)).layoutWeight(1).wordBreak(WordBreak.BREAK_WORD)
        if (Wire.text(part, 'media')) Button(tr('save')).onClick(() => this.onSaveMedia(Wire.text(part, 'media'), Wire.text(part, 'name', Wire.text(part, 'kind')), Wire.text(part, 'mime')))
      }.width('100%').alignItems(VerticalAlign.Center)
    } else if ((Wire.text(part, 'kind') === 'link' || Wire.text(part, 'kind') === 'text') && Wire.text(part, 'url')) {
      Text(this.line(part)).fontColor($r('sys.color.font_emphasize')).decoration({ type: TextDecorationType.Underline, color: $r('sys.color.font_emphasize') })
        .fontWeight(this.partWeight(part)).fontStyle(this.partStyle(part)).width('100%').onClick(() => this.onLink(Wire.text(part, 'url')))
    } else if (Wire.text(part, 'kind') === 'code') {
      Text(this.line(part)).fontFamily('monospace').fontSize(Tokens.meta).copyOption(CopyOptions.LocalDevice).width('100%')
        .padding(Tokens.inset).backgroundColor($r('sys.color.comp_background_tertiary'))
    } else if (Wire.text(part, 'kind') === 'share' && Wire.text(part, 'url')) {
      Column({ space: Tokens.gap }) {
        if (this.line(part)) Text(this.line(part)).fontSize(Tokens.body).width('100%').copyOption(CopyOptions.LocalDevice)
        Button(tr('open'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.onLink(Wire.text(part, 'url')))
      }.width('100%').alignItems(HorizontalAlign.Start)
    } else if (Wire.text(part, 'kind') === 'system') {
      Text(this.line(part)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%').textAlign(TextAlign.Center)
    } else if (this.line(part)) {
      Text(this.line(part)).fontSize(Tokens.body).fontColor($r('sys.color.font_primary')).wordBreak(WordBreak.BREAK_WORD)
        .fontWeight(this.partWeight(part)).fontStyle(this.partStyle(part)).copyOption(CopyOptions.LocalDevice).width('100%')
    }
  }

  build() {
    Column({ space: Tokens.gap }) {
      ForEach(this.parts, (part: Record<string, Object>) => this.part(part), (part: Record<string, Object>, index: number) =>
        String(index) + ':' + Wire.text(part, 'kind') + ':' + Wire.text(part, 'block_id'))
    }.width('100%').alignItems(HorizontalAlign.Start)
  }
}

@ComponentV2
export struct InboxWorkspacePage {
  @Require @Param model: ClientModel;
  @Param row: ResourceRow | undefined = undefined;
  @Param list: boolean = true;
  @Param visible: boolean = true;
  @Param refreshRevision: number = 0;
  @Event onChanged: () => void = (): void => {};
  @Event onOpen: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onHandoff: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Local workspace: InboxWorkspace = new InboxWorkspace();
  @Local itemRowBindings: BoundRow<ResourceRow>[] = [];
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  @Local detailOpen: boolean = false;
  @Local sheetOpen: boolean = false;
  @Local activeSheet: string = '';
  @Local handoffText: string = '';
  @Local pickerBusy: boolean = false;
  @Local mediaBusy: boolean = false;
  @Local error: string = '';
  private active: boolean = false;

  aboutToAppear(): void {
    this.active = true;
    this.detailOpen = !this.list && !!this.row;
    this.syncItemRows();
    this.workspace.open(this.model, this.row).then(() => {
      if (this.active && this.row && this.list === false) this.detailOpen = true;
      this.syncItemRows();
    });
  }

  aboutToDisappear(): void { this.active = false; this.workspace.close(); }

  @Monitor('model.connected') connectionChanged(): void {
    if (!this.active) return;
    if (!this.model.connected) this.workspace.onDisconnected();
    else this.workspace.reconnect(this.row);
  }

  @Monitor('model.profile') profileChanged(): void {
    if (!this.active) return;
    this.syncItemRows();
    this.workspace.close().then(() => {
      if (this.active) this.workspace.open(this.model, this.row).then(() => this.syncItemRows());
    });
  }

  @Monitor('model.inbox', 'refreshRevision', 'visible') listRefreshRequested(): void {
    if (this.active && this.visible && this.list) this.workspace.refresh().catch(() => undefined);
  }
  private acknowledged(changed: boolean): void { if (changed && this.active && this.visible) this.onChanged(); }

  @Monitor('workspace.items', 'workspace.search', 'workspace.sourceFilter', 'workspace.accountFilter', 'workspace.statusFilter') itemRowsChanged(): void {
    this.syncItemRows();
  }

  private syncItemRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager]);
    this.itemRowBindings = reconcileBoundRows(this.itemRowBindings, this.workspace.filteredItems, (row: ResourceRow) => row.key(),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.rowBindingOwner = this.model; this.rowBindingScope = scope;
  }

  @Monitor('sheetOpen') sheetChanged(): void {
    if (this.sheetOpen) return;
    if (this.activeSheet === 'mail') this.workspace.closeMailCompose();
    this.activeSheet = '';
  }

  private uiContext(): common.UIAbilityContext { return this.getUIContext().getHostContext() as common.UIAbilityContext; }
  private optionList(values: string[]): SelectOption[] { return values.map((value: string): SelectOption => ({ value: value })); }
  private sourceIndex(): number { return Math.max(0, this.workspace.sourceOptions.indexOf(this.workspace.sourceFilter)); }
  private accountIndex(): number { return Math.max(0, this.workspace.accountOptions.indexOf(this.workspace.accountFilter)); }
  private mailAccountIndex(): number { return Math.max(0, this.workspace.mailAccounts.findIndex((item: Record<string, Object>) => Wire.text(item, 'id') === this.workspace.mailComposeAccount)); }
  private sourceLabel(value: string): string { return tr(this.workspace.sourceName(value)); }
  private doneLabel(row: ResourceRow): string { return this.workspace.isHandled(row) ? tr('done') : tr('inbox_open'); }
  private rowSource(row: ResourceRow): string {
    const data = row.data();
    return this.sourceLabel(Wire.text(data, 'source')) + ' · ' + Wire.text(data, 'account_name', Wire.text(data, 'account'));
  }
  private openRow(row: ResourceRow): void {
    this.error = '';
    this.onOpen(row);
  }
  private async refreshList(): Promise<void> { await this.workspace.refresh(); }
  private openMailCompose(): void { this.activeSheet = 'mail'; this.sheetOpen = true; this.workspace.openMailCompose(); }
  private openHandoff(): void { this.handoffText = this.workspace.handoffDraft || tr('inbox_handoff_preset_summary'); this.activeSheet = 'handoff'; this.sheetOpen = true; }
  private chain(): Record<string, Object>[] { return Wire.rows(this.workspace.detail, 'chain'); }
  private handling(): Record<string, Object> { return Wire.object(this.workspace.detail, 'handling'); }
  private suggestion(): Record<string, Object> { return Wire.object(this.workspace.detail, 'suggestion'); }
  private connector(): Record<string, Object> { return Wire.object(this.workspace.detail, 'connector'); }
  private quote(message: InboxMessage): Record<string, Object> { return Wire.object(message.data(), 'reply_to'); }
  private hasQuote(message: InboxMessage): boolean { return Object.keys(this.quote(message)).length > 0; }
  private messageParts(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'parts'); }
  private messageDecorations(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'decorations'); }
  private messageReactions(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'reactions'); }
  private messageThread(message: InboxMessage): Record<string, Object> { return Wire.object(message.data(), 'thread'); }
  private messageHistory(message: InboxMessage): Record<string, Object>[] { return Wire.rows(message.data(), 'edit_history'); }
  private canOpenMessageThread(message: InboxMessage): boolean {
    return this.workspace.isTopicList ? this.workspace.capability('topics') || this.workspace.isLark() :
      this.workspace.capability('threads') || this.workspace.isLark();
  }

  private confirmReply(): void {
    const value = this.workspace.replyDraft.trim();
    if (!value || !this.workspace.canReplyToItem()) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot(value);
    const message = this.workspace.replyAccountLabel() + '\n' + this.workspace.replyRecipient() + '\n\n' + value;
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint === this.workspace.replyConfirmationSnapshot(value)) this.workspace.replyInbox(value, fingerprint).then((changed: boolean) => this.acknowledged(changed));
      } } });
  }

  private confirmConversationSend(): void {
    const value = this.workspace.replyDraft.trim();
    if (!value || !this.workspace.canSendGeneric()) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot(value);
    const destination = this.workspace.replyAccountLabel() + '\n' + this.workspace.replyRecipient() + '\n\n' + value;
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: destination,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint === this.workspace.replyConfirmationSnapshot(value))
          this.workspace.sendConversation(value, this.workspace.threadReplyTo, this.workspace.threadReply).then((changed: boolean) => this.acknowledged(changed));
      } } });
  }

  private confirmAttachmentSend(): void {
    const item = this.workspace.attachment;
    if (!item || !this.workspace.canSendGeneric()) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot('attachment:' + item.name + ':' + item.mime + ':' + String(item.size));
    const message = this.workspace.replyAccountLabel() + '\n' + this.workspace.replyRecipient() + '\n' + item.name + ' · ' + inboxBytesText(item.size);
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint === this.workspace.replyConfirmationSnapshot('attachment:' + item.name + ':' + item.mime + ':' + String(item.size)) &&
          this.workspace.attachment === item) this.workspace.sendAttachment().then((changed: boolean) => this.acknowledged(changed));
      } } });
  }

  private confirmHandoff(): void {
    const text = this.handoffText.trim(), row = this.workspace.selected;
    if (!text || !row.id) return;
    const fingerprint = this.workspace.replyConfirmationSnapshot('handoff:' + text);
    this.getUIContext().showAlertDialog({ title: tr('inbox_handoff_title'), message: row.title + '\n' + row.manager + '\n\n' + text,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        if (fingerprint !== this.workspace.replyConfirmationSnapshot('handoff:' + text)) return;
        this.workspace.handoff(text).then((target: ResourceRow | undefined) => {
          if (this.active && row.key() === this.workspace.selected.key() && this.workspace.writeState === 'handed_off') {
            this.sheetOpen = false; this.acknowledged(true);
            if (target) this.onHandoff(target);
          }
        });
      } } });
  }

  private confirmCompose(): void {
    const snapshot = this.workspace.mailComposeSnapshot();
    if (!this.workspace.mailComposeValid(snapshot)) return;
    const confirmation = this.workspace.mailComposeConfirmation(snapshot);
    const message = this.workspace.mailAccountName(snapshot.accountId) + '\n' +
      tr('to') + ': ' + this.workspace.mailComposeRecipients(snapshot) +
      (snapshot.cc ? '\n' + tr('cc') + ': ' + this.workspace.mailComposeCcRecipients(snapshot) : '') +
      (snapshot.bcc ? '\n' + tr('bcc') + ': ' + this.workspace.mailComposeBccRecipients(snapshot) : '') +
      '\n' + tr('subject') + ': ' + snapshot.subject + '\n\n' + snapshot.text;
    this.getUIContext().showAlertDialog({ title: tr('confirm_send'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('send'), action: () => {
        this.workspace.sendMailCompose(snapshot, confirmation).then((sent: boolean) => { if (sent) { this.sheetOpen = false; this.acknowledged(true); } });
      } } });
  }

  private async pickConversationAttachment(): Promise<void> {
    this.pickerBusy = true; this.error = '';
    const scope = this.model.scope(), target = this.workspace.selected.key();
    let source: image.ImageSource | undefined;
    try {
      const options = new picker.DocumentSelectOptions(); options.maxSelectNumber = 1;
      const uris = await new picker.DocumentViewPicker(this.uiContext()).select(options);
      if (!uris.length) return;
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const uri = uris[0], info = fileIo.statSync(uri);
      if (!info.size || info.size > MAX_UPLOAD_BYTES) throw new Error('attachment_limit');
      const file = fileIo.openSync(uri, fileIo.OpenMode.READ_ONLY);
      let data = new ArrayBuffer(0);
      try { data = new ArrayBuffer(info.size); if (fileIo.readSync(file.fd, data) !== info.size) throw new Error('file_read_failed'); }
      finally { fileIo.closeSync(file.fd); }
      const leaf = uri.substring(uri.lastIndexOf('/') + 1);
      const picked = new PickedInboxFile();
      try { picked.name = decodeURIComponent(leaf); } catch (_) { picked.name = leaf; }
      if (!picked.name) picked.name = 'attachment';
      picked.mime = inboxMime(picked.name); picked.kind = picked.mime.startsWith('image/') ? 'image' : 'file'; picked.data = data;
      if (picked.kind === 'image') {
        if (!/^image\/(jpeg|png|webp|gif)$/.test(picked.mime) || data.byteLength > 7.5 * 1024 * 1024) throw new Error('attachment_limit');
        source = image.createImageSource(data);
        const dimensions = await source.getImageInfo();
        if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
        if (dimensions.size.width <= 0 || dimensions.size.height <= 0 || dimensions.size.width > MAX_IMAGE_EDGE || dimensions.size.height > MAX_IMAGE_EDGE ||
          dimensions.size.width * dimensions.size.height > MAX_IMAGE_PIXELS) throw new Error('attachment_limit');
      }
      this.workspace.setAttachment(new InboxAttachmentDraft(picked.name, picked.mime, picked.kind, picked.data));
    } catch (failure) { this.error = (failure as Error).message || 'failed'; }
    finally { await source?.release(); this.pickerBusy = false; }
  }

  private async saveMailAttachment(message: InboxMailMessage, attachment: Record<string, Object>): Promise<void> {
    this.mediaBusy = true; this.error = '';
    const scope = this.model.scope(), target = this.workspace.selected.key();
    try {
      const downloaded = await this.workspace.downloadMailAttachment(message, attachment);
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const options = new picker.DocumentSaveOptions(); options.newFileNames = [downloaded.name];
      const uris = await new picker.DocumentViewPicker(this.uiContext()).save(options);
      if (!uris.length) return;
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const output = fileIo.openSync(uris[0], fileIo.OpenMode.READ_WRITE);
      try { if (fileIo.writeSync(output.fd, downloaded.data) !== downloaded.data.byteLength) throw new Error('file_write_failed'); }
      finally { fileIo.closeSync(output.fd); }
    } catch (failure) { this.error = (failure as Error).message || 'failed'; }
    finally { this.mediaBusy = false; }
  }

  private async saveMessageMedia(media: string, name: string, mime: string): Promise<void> {
    this.mediaBusy = true; this.error = '';
    const scope = this.model.scope(), target = this.workspace.selected.key();
    try {
      const downloaded = await this.workspace.downloadMessageMedia(media, name, mime);
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const options = new picker.DocumentSaveOptions(); options.newFileNames = [downloaded.name];
      const uris = await new picker.DocumentViewPicker(this.uiContext()).save(options);
      if (!uris.length) return;
      if (scope !== this.model.scope() || target !== this.workspace.selected.key()) throw new Error('request_updated');
      const output = fileIo.openSync(uris[0], fileIo.OpenMode.READ_WRITE);
      try { if (fileIo.writeSync(output.fd, downloaded.data) !== downloaded.data.byteLength) throw new Error('file_write_failed'); }
      finally { fileIo.closeSync(output.fd); }
    } catch (failure) { this.error = (failure as Error).message || 'failed'; }
    finally { this.mediaBusy = false; }
  }

  @Builder inboxList() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Search({ value: this.workspace.search, placeholder: tr('search') })
          .onChange((value: string) => this.workspace.search = value).layoutWeight(1)
        Button(tr('refresh'), { buttonStyle: ButtonStyleMode.TEXTUAL }).enabled(!this.workspace.loading)
          .onClick(() => this.refreshList()).height(Tokens.touch)
        Button(tr('compose'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.openMailCompose()).height(Tokens.touch)
      }.padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.gap })
      Row({ space: Tokens.gap }) {
        Select(this.optionList(this.workspace.sourceOptions.map((value: string) => this.sourceLabel(value))))
          .selected(this.sourceIndex()).onSelect((index: number) => this.workspace.sourceFilter = this.workspace.sourceOptions[index]).layoutWeight(1)
        Select(this.optionList(this.workspace.accountOptions.map((value: string) => this.workspace.accountName(value))))
          .selected(this.accountIndex()).onSelect((index: number) => this.workspace.accountFilter = this.workspace.accountOptions[index]).layoutWeight(1)
      }.padding({ left: Tokens.inset, right: Tokens.inset })
      CapsuleSegmentButtonV2({ items: new SegmentButtonV2Items([{ text: tr('inbox_open') }, { text: tr('done') }, { text: tr('all') }]),
        selectedIndex: this.workspace.statusFilter === 'open' ? 0 : this.workspace.statusFilter === 'done' ? 1 : 2,
        $selectedIndex: (index: number) => this.workspace.statusFilter = index === 0 ? 'open' : index === 1 ? 'done' : 'all' })
        .padding({ left: Tokens.page, right: Tokens.page })
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding({ left: Tokens.page, right: Tokens.page })
      if (this.workspace.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      List() {
        ForEach(this.itemRowBindings, (binding: BoundRow<ResourceRow>) => {
          ListItem() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || binding.value.id,
              secondaryText: [binding.value.detail || '', this.rowSource(binding.value), this.doneLabel(binding.value)].filter((value: string) => !!value).join(' · ') }) })
          }.onClick(() => this.openRow(binding.value)).id('inbox.' + binding.value.key())
        }, (binding: BoundRow<ResourceRow>) => binding.key)
        if (!this.itemRowBindings.length && !this.workspace.loading)
          ListItem() { Text(tr(this.workspace.search ? 'no_results' : 'empty')).padding(Tokens.page).width('100%') }
      }.layoutWeight(1).width('100%')
    }.width('100%').height('100%')
  }

  @Builder header() {
    Row({ space: Tokens.gap }) {
      Text(this.workspace.isReference() ? this.workspace.referenceSourceLabel() + (this.workspace.replyAccountLabel() ? ' · ' + this.workspace.replyAccountLabel() : '')
        : this.rowSource(this.workspace.selected)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).maxLines(1).layoutWeight(1)
      Button(tr('refresh'), { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).enabled(!this.workspace.detailLoading)
        .onClick(() => this.workspace.refreshDetail())
    }.width('100%').padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.gap })
  }

  @Builder rowStatus() {
    Row({ space: Tokens.gap }) {
      Text(this.workspace.isHandled() ? tr('done') : tr('inbox_open')).fontSize(Tokens.meta)
        .fontColor($r('sys.color.font_secondary'))
      if (Wire.bool(this.workspace.detail, 'urgent')) Text(tr('inbox_urgent')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (Wire.text(this.workspace.detail, 'tier')) Text(tr('inbox_tier_' + Wire.text(this.workspace.detail, 'tier'))).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(this.workspace.detail, 'folder')) Text(Wire.text(this.workspace.detail, 'folder')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
    }.width('100%')
  }

  @Builder summary() {
    Column({ space: Tokens.gap }) {
      this.rowStatus()
      if (Wire.text(this.workspace.detail, 'sender')) Text(Wire.text(this.workspace.detail, 'sender')).fontSize(Tokens.body).width('100%')
      if (Wire.text(this.workspace.detail, 'summary')) MarkdownText({ text: Wire.text(this.workspace.detail, 'summary'), onLink: (address: string) => this.onLink(address) })
      if (Wire.text(this.workspace.detail, 'preview')) MarkdownText({ text: Wire.text(this.workspace.detail, 'preview'), onLink: (address: string) => this.onLink(address) })
      if (Wire.text(this.workspace.detail, 'reason')) Text(Wire.text(this.workspace.detail, 'reason')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(this.workspace.detail, 'recap')) MarkdownText({ text: Wire.text(this.workspace.detail, 'recap'), onLink: (address: string) => this.onLink(address) })
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder trail() {
    if (this.chain().length || Object.keys(this.handling()).length || Object.keys(this.suggestion()).length > 0 || Wire.rows(this.workspace.detail, 'handoffs').length) {
      Column({ space: Tokens.gap }) {
        Text(tr('inbox_processing')).fontSize(Tokens.title).fontWeight(FontWeight.Medium)
        ForEach(this.chain(), (entry: Record<string, Object>) => {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(entry, 'text', Wire.text(entry, 'kind')),
            secondaryText: [Wire.text(entry, 'detail'), Wire.text(entry, 'at')].filter((value: string) => !!value).join(' · ') || undefined }) })
        }, (entry: Record<string, Object>) => Wire.text(entry, 'id'))
        if (Object.keys(this.handling()).length) ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox_handling_' + Wire.text(this.handling(), 'state')),
          secondaryText: [Wire.text(this.handling(), 'owner'), Wire.text(this.handling(), 'error')].filter((value: string) => !!value).join(' · ') || undefined }) })
        if (Object.keys(this.suggestion()).length) {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox_suggestion_' + Wire.text(this.suggestion(), 'kind')),
            secondaryText: Wire.text(this.suggestion(), 'note') || undefined }) })
          if (Wire.text(this.suggestion(), 'text')) MarkdownText({ text: Wire.text(this.suggestion(), 'text'), onLink: (address: string) => this.onLink(address) })
        }
        ForEach(Wire.rows(this.workspace.detail, 'handoffs'), (entry: Record<string, Object>) => {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox_handed_to'),
            secondaryText: Wire.text(entry, 'machine_id') + ' · ' + Wire.text(entry, 'session_id') + ' · ' + Wire.text(entry, 'at') }) })
        }, (entry: Record<string, Object>, index: number) => String(index) + ':' + Wire.text(entry, 'session_id'))
        ForEach(Wire.rows(this.workspace.detail, 'related'), (entry: Record<string, Object>) => {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(entry, 'title'), secondaryText: Wire.text(entry, 'source') }) })
        }, (entry: Record<string, Object>) => Wire.text(entry, 'id'))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  @Builder messageCard(message: InboxMessage) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(message.sender || (message.mine ? tr('you') : tr('message'))).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (message.state) Text(tr('inbox_message_' + message.state)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        if (Wire.bool(message.data(), 'recalled')) Text(tr('inbox_recalled')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      }.width('100%')
      if (this.hasQuote(message)) {
        Text(Wire.text(this.quote(message), 'sender_name') + ': ' + Wire.text(this.quote(message), 'summary')).fontSize(Tokens.meta)
          .fontColor($r('sys.color.font_secondary')).width('100%')
      }
      if (this.messageParts(message).length) InboxMessageParts({ workspace: this.workspace, parts: this.messageParts(message),
        onLink: (address: string) => this.onLink(address), onSaveMedia: (media: string, name: string, mime: string) => this.saveMessageMedia(media, name, mime) })
      else if (message.text) MarkdownText({ text: message.text, onLink: (address: string) => this.onLink(address) })
      ForEach(this.messageDecorations(message), (item: Record<string, Object>) => {
        Text(Wire.text(item, 'text')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }, (item: Record<string, Object>, index: number) => String(index) + Wire.text(item, 'text'))
      if (Wire.bool(message.data(), 'edited')) Text(tr('inbox_edited')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      ForEach(this.messageReactions(message), (reaction: Record<string, Object>) => {
        if (this.workspace.canReact(message)) Button(Wire.text(reaction, 'key') + ' · ' + String(Wire.number(reaction, 'count')),
          { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.workspace.react(message, Wire.text(reaction, 'key')))
        else Text(Wire.text(reaction, 'key') + ' · ' + String(Wire.number(reaction, 'count'))).fontSize(Tokens.meta)
      }, (reaction: Record<string, Object>) => Wire.text(reaction, 'key'))
      if (this.canOpenMessageThread(message) && (this.workspace.isTopicList || Object.keys(this.messageThread(message)).length)) {
        Button(tr('inbox_open_thread') + (Wire.number(this.messageThread(message), 'count') ? ' · ' + String(Wire.number(this.messageThread(message), 'count')) : ''),
          { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.workspace.openThread(message))
      }
      if (!this.workspace.isThreadReference() && this.workspace.canReplyToMessage(message)) Button(tr('reply'), { buttonStyle: ButtonStyleMode.TEXTUAL })
        .height(Tokens.touch).onClick(() => this.workspace.setThreadTarget(message.id, false))
      if (this.workspace.canReact(message) && this.workspace.quickReactions.length) {
        Row({ space: Tokens.fine }) {
          ForEach(this.workspace.quickReactions, (key: string) => {
            Button(key, { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).onClick(() => this.workspace.react(message, key))
          }, (key: string) => key)
        }.width('100%')
      }
      if (this.workspace.capability('edit_history') && this.messageHistory(message).length) {
        ForEach(this.messageHistory(message), (entry: Record<string, Object>) => {
          Text(Wire.text(entry, 'text', new InboxMessage(entry).text)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
        }, (entry: Record<string, Object>, index: number) => String(index) + Wire.text(entry, 'edited_at'))
      }
    }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
      .backgroundColor($r('sys.color.comp_background_tertiary')).border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.fine })
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0) this.workspace.markMessageVisible(message); })
  }

  @Builder conversation() {
    if (this.workspace.hasConversation()) {
      Column({ space: Tokens.gap }) {
        Row({ space: Tokens.gap }) {
          Text(tr(this.workspace.isTopicList ? 'inbox_topics' : 'inbox_conversation')).fontSize(Tokens.title).fontWeight(FontWeight.Medium).layoutWeight(1)
          if (this.workspace.threadRoot && !this.workspace.isThreadReference()) Button(tr('back'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.workspace.closeThread())
          Button(tr('refresh'), { buttonStyle: ButtonStyleMode.TEXTUAL }).enabled(!this.workspace.feedLoading).onClick(() => this.workspace.refreshConversation())
        }.width('100%')
        if (this.workspace.feedHasOlder) Button(tr(this.workspace.feedLoading ? 'loading' : 'inbox_older')).enabled(!this.workspace.feedLoading)
          .onClick(() => this.workspace.loadOlder()).width('100%')
        ForEach(this.workspace.feedMessages, (message: InboxMessage) => this.messageCard(message), (message: InboxMessage) => message.id)
        if (this.workspace.feedLoading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  @Builder threadReferenceContent() {
    if (this.workspace.isThreadReference() && !this.workspace.hasConversation()) {
      Column({ space: Tokens.gap }) {
        Text(this.workspace.workspaceThreadTitle() || this.workspace.referenceThreadId).fontSize(Tokens.title).fontWeight(FontWeight.Medium).width('100%')
        if (this.workspace.referenceThreadId) Text(this.workspace.referenceThreadId).fontSize(Tokens.meta)
          .fontColor($r('sys.color.font_secondary')).copyOption(CopyOptions.LocalDevice).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary'))
    }
  }

  private shownMailSubject(message: InboxMailMessage): string {
    if (!this.workspace.showOriginal && this.workspace.translatedMailUid === message.uid)
      return Wire.text(this.workspace.mailTranslation, 'subject', message.subject);
    return message.subject;
  }
  private shownMailText(message: InboxMailMessage): string {
    if (!this.workspace.showOriginal && this.workspace.translatedMailUid === message.uid && Wire.text(this.workspace.mailTranslation, 'text'))
      return Wire.text(this.workspace.mailTranslation, 'text');
    return message.bodyText();
  }
  private hasMailTranslation(message: InboxMailMessage): boolean { return this.workspace.translatedMailUid === message.uid; }

  @Builder mailMessage(message: InboxMailMessage) {
    Column({ space: Tokens.gap }) {
      Text(this.shownMailSubject(message)).fontSize(Tokens.title).fontWeight(FontWeight.Medium).width('100%')
      Text(message.from + (message.to ? ' · ' + message.to : '')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (message.date) Text(message.date).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (this.workspace.showOriginal || !Wire.text(this.workspace.mailTranslation, 'text'))
        MarkdownText({ text: this.shownMailText(message), onLink: (address: string) => this.onLink(address) })
      else Text(this.shownMailText(message)).fontSize(Tokens.body).width('100%').copyOption(CopyOptions.LocalDevice)
      if (Wire.rows(message.data(), 'attachments').length) {
        ForEach(Wire.rows(message.data(), 'attachments'), (attachment: Record<string, Object>) => {
          Row({ space: Tokens.gap }) {
            Text(Wire.text(attachment, 'filename', tr('attachment')) + ' · ' + inboxBytesText(Wire.number(attachment, 'size'))).layoutWeight(1).maxLines(2)
            Button(tr(this.mediaBusy ? 'loading' : 'save')).enabled(!this.mediaBusy).onClick(() => this.saveMailAttachment(message, attachment))
          }.width('100%')
        }, (attachment: Record<string, Object>) => Wire.text(attachment, 'id'))
      }
      Row({ space: Tokens.gap }) {
        Select(this.optionList(['zh-Hans', 'en', 'ja']))
          .selected(this.workspace.translationTarget === 'zh-Hans' ? 0 : this.workspace.translationTarget === 'en' ? 1 : 2)
          .onSelect((index: number) => this.workspace.translationTarget = ['zh-Hans', 'en', 'ja'][index]).layoutWeight(1)
        Button(tr(this.workspace.translationBusy ? 'loading' : this.workspace.translationError ? 'retry' : 'translation'),
          { buttonStyle: ButtonStyleMode.TEXTUAL }).enabled(!this.workspace.translationBusy).onClick(() => this.workspace.translateMail(message))
        if (this.hasMailTranslation(message)) Button(tr(this.workspace.showOriginal ? 'inbox_show_translation' : 'inbox_show_original'),
          { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.workspace.toggleOriginal())
      }.width('100%')
      if (this.workspace.translationError) Text(tr(this.workspace.translationError)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
      if (this.hasMailTranslation(message) && Wire.bool(this.workspace.mailTranslation, 'clipped')) Text(tr('inbox_translation_clipped')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
    }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
      .backgroundColor($r('sys.color.comp_background_tertiary')).border({ width: Tokens.hairline, color: $r('sys.color.font_secondary'), radius: Tokens.fine })
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { if (visible && ratio > 0) this.workspace.markMailVisible(message.uid); })
  }

  @Builder mailContent() {
    if (this.workspace.isMail()) {
      Column({ space: Tokens.gap }) {
        ForEach(this.workspace.mailMessages, (message: InboxMailMessage) => this.mailMessage(message), (message: InboxMailMessage) => String(message.uid))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  @Builder connectorContent() {
    if (Object.keys(this.connector()).length) {
      Column({ space: Tokens.gap }) {
        if (Wire.text(this.connector(), 'label')) Text(Wire.text(this.connector(), 'label')).fontSize(Tokens.title).width('100%')
        if (Wire.text(this.connector(), 'reply_to')) Text(tr('inbox_reply_to') + ': ' + Wire.text(this.connector(), 'reply_to')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
        if (Wire.text(this.connector(), 'url')) Button(tr('open'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.onLink(Wire.text(this.connector(), 'url')))
      }.width('100%').alignItems(HorizontalAlign.Start)
    }
  }

  @Builder replyBox() {
    if ((this.workspace.isReference() ? this.workspace.isGeneric() && this.workspace.hasConversation() && this.workspace.canReplyReferenceThread()
      : this.workspace.canReplyToItem() || this.workspace.isGeneric()) && !this.workspace.isHandled() &&
      !(this.workspace.isGeneric() && this.workspace.threadRoot && !this.workspace.threadReplyTo)) {
      Column({ space: Tokens.gap }) {
        if (this.workspace.replyTargetChanged) Text(tr('inbox_reply_target_changed')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
        if (this.workspace.threadReplyTo) Row({ space: Tokens.gap }) {
          Text(tr(this.workspace.threadReply ? 'inbox_thread_reply' : 'inbox_message_reply') + ' · ' + this.workspace.threadReplyTo)
            .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).layoutWeight(1)
          Button(tr('close'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.workspace.setThreadTarget('', false))
        }.width('100%')
        TextArea({ text: this.workspace.replyDraft, placeholder: tr('reply') }).height(Tokens.inputExpanded)
          .enabled(this.workspace.draftReady && !this.workspace.draftLoading && !['sending', 'pending', 'unknown'].includes(this.workspace.writeState))
          .onChange((value: string) => this.workspace.setReplyDraft(value)).id('inbox.reply')
        if (this.workspace.isGeneric()) {
          if (this.workspace.attachment) Row({ space: Tokens.gap }) {
            Text(this.workspace.attachment.name + ' · ' + inboxBytesText(this.workspace.attachment.size)).layoutWeight(1)
            Button(tr('remove'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.workspace.removeAttachment())
          }.width('100%')
          Row({ space: Tokens.gap }) {
            if (this.workspace.canSendGeneric() && this.workspace.capability('attachments')) {
              Button(tr('attachment'), { buttonStyle: ButtonStyleMode.TEXTUAL }).enabled(!this.pickerBusy && !this.workspace.attachmentBusy)
                .onClick(() => this.pickConversationAttachment())
            }
            if (this.workspace.attachment) Button(tr(this.workspace.attachmentBusy ? 'sending' : 'inbox_send_attachment'))
              .enabled(!this.workspace.attachmentBusy && this.workspace.canSendGeneric()).onClick(() => this.confirmAttachmentSend())
            Button(tr(this.workspace.writeState === 'pending' || this.workspace.writeState === 'unknown' ? 'pending' : 'send'))
              .enabled(this.workspace.replyDraft.trim().length > 0 && this.workspace.canSendGeneric() && !['sending', 'pending', 'unknown'].includes(this.workspace.writeState))
              .onClick(() => this.confirmConversationSend())
          }.width('100%')
        } else {
          Button(tr(this.workspace.writeState === 'pending' || this.workspace.writeState === 'unknown' ? 'pending' : 'send'))
            .enabled(this.workspace.replyDraft.trim().length > 0 && this.workspace.draftReady && !['sending', 'pending', 'unknown'].includes(this.workspace.writeState))
            .onClick(() => this.confirmReply()).width('100%')
        }
        if (this.workspace.writeState) Text(tr('inbox_write_' + this.workspace.writeState)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').padding(Tokens.inset).alignItems(HorizontalAlign.Start)
    }
  }

  @Builder detailContent() {
    Column() {
      this.header()
      if (this.workspace.detailLoading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.error) Text(tr(this.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      Scroll() {
        Column({ space: Tokens.section }) {
          if (this.workspace.isReference()) {
            this.threadReferenceContent()
            this.conversation()
          } else {
            this.summary()
            this.connectorContent()
            this.mailContent()
            this.conversation()
            this.trail()
          }
        }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
      }.layoutWeight(1).width('100%')
      if (!this.workspace.isReference()) Row({ space: Tokens.gap }) {
        Button(tr(this.workspace.isHandled() ? 'reopen' : 'process')).enabled(!this.workspace.writeState || !['saving', 'sending'].includes(this.workspace.writeState))
          .onClick(() => this.workspace.processInbox().then((changed: boolean) => this.acknowledged(changed))).layoutWeight(1)
        Button(tr('handoff')).enabled(!this.workspace.handoffBusy).onClick(() => this.openHandoff()).layoutWeight(1)
      }.width('100%').padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.inset })
      this.replyBox()
    }.width('100%').height('100%')
  }

  @Builder sheetContent() {
    if (this.activeSheet === 'mail') this.mailComposer()
    else if (this.activeSheet === 'handoff') this.handoffComposer()
  }

  @Builder mailComposer() {
    Column({ space: Tokens.gap }) {
      if (this.workspace.mailAccounts.length > 1) Select(this.optionList(this.workspace.mailAccounts.map((item: Record<string, Object>) => this.workspace.mailAccountName(Wire.text(item, 'id')))))
        .selected(this.mailAccountIndex()).onSelect((index: number) => this.workspace.selectMailComposeAccount(Wire.text(this.workspace.mailAccounts[index], 'id')))
      TextInput({ text: this.workspace.mailComposeTo, placeholder: tr('to') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('to', value))
      TextInput({ text: this.workspace.mailComposeCc, placeholder: tr('cc') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('cc', value))
      TextInput({ text: this.workspace.mailComposeBcc, placeholder: tr('bcc') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('bcc', value))
      TextInput({ text: this.workspace.mailComposeSubject, placeholder: tr('subject') }).enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked)
        .onChange((value: string) => this.workspace.setMailComposeField('subject', value))
      TextArea({ text: this.workspace.mailComposeText, placeholder: tr('message') }).height(Tokens.inputExpanded)
        .enabled(this.workspace.mailComposeReady && !this.workspace.mailComposeBlocked).onChange((value: string) => this.workspace.setMailComposeField('text', value))
      if (this.workspace.mailComposeError) Text(tr(this.workspace.mailComposeError)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      if (this.workspace.mailComposeBlocked) Text(tr('inbox_write_' + this.workspace.mailComposeState)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      Button(tr(this.workspace.mailComposeBusy ? 'sending' : 'send')).enabled(this.workspace.mailComposeValid() && !this.workspace.mailComposeBusy)
        .width('100%').height(Tokens.touch).onClick(() => this.confirmCompose())
    }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
  }

  @Builder handoffComposer() {
    Column({ space: Tokens.gap }) {
      TextArea({ text: this.handoffText, placeholder: tr('inbox_handoff_instruction') }).height(Tokens.inputExpanded)
        .enabled(!['pending', 'unknown'].includes(this.workspace.writeState))
        .onChange((value: string) => { this.handoffText = value; this.workspace.handoffDraft = value; })
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
      Button(tr(this.workspace.handoffBusy ? 'sending' : ['pending', 'unknown'].includes(this.workspace.writeState) ? 'pending' : 'inbox_handoff_send'))
        .enabled(!!this.handoffText.trim() && !this.workspace.handoffBusy && !['pending', 'unknown'].includes(this.workspace.writeState))
        .width('100%').height(Tokens.touch).onClick(() => this.confirmHandoff())
    }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
  }

  build() {
    Column() {
      if (this.detailOpen) this.detailContent()
      else this.inboxList()
    }.width('100%').height('100%')
      .bindSheet($$this.sheetOpen, this.sheetContent(), { title: { title: tr(this.activeSheet === 'mail' ? 'mail_compose' : 'inbox_handoff_title') }, height: SheetSize.LARGE, showClose: true })
  }
}
