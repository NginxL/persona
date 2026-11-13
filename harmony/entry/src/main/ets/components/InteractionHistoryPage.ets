import { ComposeListItemV2, ContentItemV2 } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { InteractionHistoryEntry, InteractionHistoryModel } from '../models/InteractionHistory';
import { InteractionDecisionForm } from './InteractionDecisionForm';
import { Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct InteractionHistoryPage {
  @Require @Param model: ClientModel;
  @Param active: boolean = true;
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Local history: InteractionHistoryModel = new InteractionHistoryModel();
  @Local inspectorOpen: boolean = false;
  private contextIdentity: string = '';

  aboutToAppear(): void { this.syncHistory(); }
  aboutToDisappear(): void { this.history.hide(); this.inspectorOpen = false; this.contextIdentity = ''; }

  @Monitor('active') activeChanged(): void { this.syncHistory(); }
  @Monitor('model.profile') profileChanged(): void { this.syncHistory(); }
  @Monitor('model.selected') selectedChanged(): void { this.syncHistory(); }
  @Monitor('model.connected') connectionChanged(): void { this.syncHistory(true); }
  @Monitor('inspectorOpen') inspectorChanged(): void {
    if (!this.inspectorOpen) this.history.selectedId = '';
  }

  private syncHistory(force: boolean = false): void {
    if (!this.active) {
      this.history.hide(); this.inspectorOpen = false; this.contextIdentity = '';
      return;
    }
    const identity = this.history.identity(this.model);
    if (!identity) {
      this.history.hide(true); this.inspectorOpen = false; this.contextIdentity = '';
      return;
    }
    if (identity !== this.contextIdentity) {
      this.contextIdentity = identity; this.inspectorOpen = false; this.history.selectedId = '';
    }
    this.history.open(this.model, true, force).catch(() => undefined);
  }

  private statusLabel(entry: InteractionHistoryEntry): string {
    if (entry.status === 'pending') return tr('pending');
    if (entry.status === 'resolved') return tr('completed');
    if (entry.status === 'expired' || entry.status === 'cancelled' || entry.status === 'dismissed') return tr('canceled');
    return entry.status || tr('result_unknown');
  }

  private summary(entry: InteractionHistoryEntry): string {
    const result = entry.outcome, lines: string[] = [];
    if (entry.request.kind === 'question') {
      Object.keys(result).forEach((key: string) => {
        const answer = Wire.text(result, key);
        if (answer) lines.push(key + ': ' + answer);
      });
    } else if (entry.request.kind === 'plan') {
      if (result['approve'] === true) lines.push(tr('approve'));
      else if (result['approve'] === false) lines.push(tr('deny'));
      if (Wire.text(result, 'feedback')) lines.push(Wire.text(result, 'feedback'));
    } else if (entry.request.kind === 'permission') {
      if (result['allow'] === true) lines.push(tr('allow'));
      else if (result['allow'] === false) lines.push(tr('deny'));
    } else if (entry.request.kind === 'screen') {
      if (Wire.text(result, 'option_label')) lines.push(Wire.text(result, 'option_label'));
      if (Wire.strings(result, 'options_labels').length) lines.push(Wire.strings(result, 'options_labels').join(', '));
      if (Wire.text(result, 'text')) lines.push(Wire.text(result, 'text'));
    } else if (entry.request.kind === 'elicitation') {
      const action = Wire.text(result, 'action');
      if (action === 'accept') lines.push(tr('approve'));
      else if (action === 'decline') lines.push(tr('deny'));
      else if (action === 'cancel') lines.push(tr('canceled'));
    }
    return lines.length ? lines.join(' · ') : entry.request.prompt;
  }

  private openEntry(entry: InteractionHistoryEntry): void {
    if (!this.active || !entry.request.id || !this.history.rows.includes(entry)) return;
    this.history.selectedId = entry.request.id; this.inspectorOpen = true;
  }

  @Builder historyRow(entry: InteractionHistoryEntry) {
    ListItem() {
      ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: this.statusLabel(entry), secondaryText: this.summary(entry) }) })
    }.onClick(() => this.openEntry(entry)).id('interaction.history.' + entry.request.id)
  }

  @Builder inspector() {
    if (this.history.selectedId) {
      ForEach(this.history.rows.filter((entry: InteractionHistoryEntry) => entry.request.id === this.history.selectedId),
        (entry: InteractionHistoryEntry) => {
          InteractionDecisionForm({ model: this.model, request: entry.request, readOnly: true,
            onLink: (address: string) => this.onLink(address) })
        }, (entry: InteractionHistoryEntry) => entry.request.id)
    }
  }

  build() {
    Column() {
      if (this.history.error) {
        Row({ space: Tokens.gap }) {
          Text(tr(this.history.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).layoutWeight(1)
          Button(tr('retry')).onClick(() => this.history.refresh()).height(Tokens.touch)
        }.padding(Tokens.inset).width('100%')
      }
      List() {
        if (this.history.loading && !this.history.rows.length) ListItem() {
          LoadingProgress().height(Tokens.touch).width(Tokens.touch).padding(Tokens.page)
        }
        ForEach(this.history.rows, (entry: InteractionHistoryEntry) => this.historyRow(entry),
          (entry: InteractionHistoryEntry) => entry.request.id)
        if (!this.history.rows.length && !this.history.loading && !this.history.error) ListItem() {
          Text(tr('empty')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).padding(Tokens.page).width('100%')
        }.id('interaction.history.empty')
      }.width('100%').height('100%').padding({ bottom: Tokens.listTail })
    }.width('100%').height('100%')
      .bindSheet($$this.inspectorOpen, this.inspector(), { title: { title: tr('interaction_history') }, height: SheetSize.LARGE, showClose: true })
  }
}
