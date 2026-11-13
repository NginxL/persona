import { ClientModel } from '../models/ClientModel';
import { AssistantFeedModel } from '../models/AssistantFeed';
import { AssistantViewModel } from '../models/AssistantView';
import { Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { ExtensionView } from './ExtensionView';

/** Native assistant card host. The presentation itself lives in a bounded owner-scoped cache. */
@ComponentV2
export struct AssistantViewCard {
  @Require @Param model: ClientModel;
  @Require @Param feed: AssistantFeedModel;
  @Require @Param item: Record<string, Object>;
  @Require @Param cardKey: string;
  @Param active: boolean = false;
  @Param showTitle: boolean = true;
  @Event onLink: (address: string) => void = (_address: string): void => {};
  @Event onOpenResource: (address: string) => void = (_address: string): void => {};
  @Local presentation: AssistantViewModel | undefined = undefined;
  @Local mounted: boolean = false;
  @Local visible: boolean = false;
  @Local hostVisit: number = 0;

  aboutToAppear(): void { this.hostVisit++; this.mounted = true; this.bindPresentation(); }
  aboutToDisappear(): void {
    this.hostVisit++;
    this.mounted = false;
    this.visible = false;
    this.presentation?.hide();
  }

  private effectiveActive(): boolean { return this.mounted && this.visible && this.active; }

  private updateVisibility(): void {
    if (this.presentation) this.presentation.setVisible(this.effectiveActive());
    else if (this.mounted) this.bindPresentation();
  }

  private bindPresentation(): void {
    const previous = this.presentation;
    const next = AssistantViewModel.acquire(this.model, this.feed, this.item, this.cardKey, this.effectiveActive());
    if (previous !== next) this.hostVisit++;
    if (previous && previous !== next) previous.hide();
    this.presentation = next;
  }

  @Monitor('active') activeChanged(): void {
    this.hostVisit++;
    this.updateVisibility();
  }

  private visibleAreaChanged(visible: boolean, ratio: number): void {
    const nextVisible = visible && ratio > 0;
    if (this.visible === nextVisible) return;
    this.visible = nextVisible;
    this.hostVisit++;
    this.updateVisibility();
  }

  @Monitor('item', 'cardKey', 'model.profile', 'model.connected') sourceChanged(): void {
    if (this.mounted) this.bindPresentation();
  }

  @Monitor('feed.views', 'feed.viewActions', 'feed.viewBlocks') feedChanged(): void {
    if (this.mounted) this.bindPresentation();
  }

  private hostLeaseCurrent(presentation: AssistantViewModel, client: ClientModel, cardKey: string, visit: number,
    generation: number, scope: string, target: string): boolean {
    return this.presentation === presentation && this.model === client && this.cardKey === cardKey &&
      this.hostVisit === visit && this.effectiveActive() && client.connectionGeneration() === generation &&
      client.scope() === scope && client.target() === target;
  }

  @Monitor('presentation.confirmationRevision') confirmationChanged(): void {
    const presentation = this.presentation;
    if (!presentation || !this.effectiveActive()) return;
    const selection = presentation.selectionSnapshot;
    const token = Wire.text(selection, 'token');
    if (!token) return;
    const client = this.model;
    const cardKey = this.cardKey;
    const visit = this.hostVisit;
    const generation = client.connectionGeneration();
    const scope = client.scope();
    const target = client.target();
    const lease = presentation.confirmationLease(token);
    if (!lease || !this.hostLeaseCurrent(presentation, client, cardKey, visit, generation, scope, target) ||
      !presentation.confirmationLeaseIsCurrent(lease)) return;
    const action = Wire.object(selection, 'action');
    const title = Wire.text(action, 'confirm') || tr('assistant_view_confirm_title');
    this.getUIContext().showAlertDialog({ title: title, message: Wire.text(selection, 'message'),
      cancel: () => presentation.declineConfirmation(token),
      primaryButton: { value: tr('cancel'), action: () => presentation.declineConfirmation(token) },
      secondaryButton: { value: tr('confirm'), action: () => {
        if (!this.hostLeaseCurrent(presentation, client, cardKey, visit, generation, scope, target) ||
          !presentation.confirmationLeaseIsCurrent(lease)) {
          presentation.declineConfirmation(token);
          return;
        }
        const captured = presentation.takeConfirmation(token);
        if (!captured) return;
        if (!this.hostLeaseCurrent(presentation, client, cardKey, visit, generation, scope, target) ||
          !presentation.selectionMatchesLease(captured, lease)) {
          presentation.declineConfirmation(token);
          return;
        }
        presentation.confirmSelection(captured, lease).catch(() => {});
      } } });
  }

  @Monitor('presentation.effectRevision') effectChanged(): void {
    const presentation = this.presentation;
    if (!presentation || !this.effectiveActive() || !presentation.canDispatchEffects()) {
      presentation?.clearEffect();
      return;
    }
    const effect = presentation.effect;
    const address = Wire.text(effect, 'address');
    if (address && Wire.text(effect, 'kind') === 'link') this.onLink(address);
    else if (address && Wire.text(effect, 'kind') === 'resource') this.onOpenResource(address);
    presentation.clearEffect();
  }

  private hasContent(view: Record<string, Object>): boolean {
    for (const section of Wire.rows(view, 'sections')) if (Wire.rows(section, 'items').length > 0) return true;
    return false;
  }

  build() {
    Column({ space: Tokens.gap }) {
      if (!this.presentation) {
        Text(tr('assistant_feed_view_unavailable')).fontSize(Tokens.body).fontColor($r('sys.color.font_secondary'))
      } else {
        if (this.showTitle && this.presentation.pageTitle) Row({ space: Tokens.gap }) {
          if (this.presentation.pageStack.length > 0) Button(tr('back')).buttonStyle(ButtonStyleMode.TEXTUAL)
            .height(Tokens.touch).onClick(() => this.presentation?.back())
          Text(this.presentation.pageTitle).fontSize(Tokens.body).fontWeight(FontWeight.Medium)
            .layoutWeight(1).accessibilityLevel('yes')
        }.width('100%')
        if (this.presentation.error) Text(tr(this.presentation.error)).fontSize(Tokens.meta)
          .fontColor($r('sys.color.font_secondary')).width('100%')
        if (this.presentation.selectedSummaryKey) Text(tr(this.presentation.selectedSummaryKey)).fontSize(Tokens.meta)
          .fontColor($r('sys.color.font_secondary')).width('100%')
        if (this.presentation.actionError) Text(tr('assistant_view_submit_failed')).fontSize(Tokens.meta)
          .fontColor($r('app.color.persona_danger')).width('100%')
        if (this.presentation.canRetryOriginal) Button(tr('assistant_view_retry_original')).height(Tokens.touch)
          .enabled(this.effectiveActive() && !this.presentation.busy)
          .onClick(() => this.presentation?.retryOriginal().catch(() => {}))
        if (!this.presentation.valid || !this.presentation.complete) {
          Text(this.presentation.fallbackText || tr('assistant_feed_view_unavailable')).fontSize(Tokens.body).width('100%')
        } else {
          ExtensionView({ model: this.presentation, compact: true, active: this.effectiveActive(), showTitle: false,
            onLink: (address: string) => this.onLink(address) })
          if (!this.hasContent(this.presentation.view) && this.presentation.fallbackText) {
            Text(this.presentation.fallbackText).fontSize(Tokens.body).width('100%')
          }
        }
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
      .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.gap)
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => this.visibleAreaChanged(visible, ratio))
  }
}
