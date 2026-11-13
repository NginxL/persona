import { ClientModel } from '../models/ClientModel';
import { ExtensionUIModel, ExtensionUINavigationEffect } from '../models/ExtensionUI';
import { ExtensionViewRecord } from '../core/ExtensionViewTree';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { ExtensionView } from './ExtensionView';

/** Full-page host for an extension contribution; `active` follows route visibility, not mount lifetime. */
@ComponentV2
export struct ExtensionPage {
  @Require @Param model: ClientModel;
  @Require @Param extensionId: string;
  @Require @Param uiId: string;
  @Param state: string = '';
  @Param context: ExtensionViewRecord = {};
  @Param initialView: ExtensionViewRecord | undefined = undefined;
  @Param compact: boolean = false;
  @Param active: boolean = true;
  @Param authorized: boolean = true;
  @Param refreshRevision: number = 0;
  @Event onTitle: (title: string) => void = (_title: string): void => {};
  @Event onNavigate: (effect: ExtensionUINavigationEffect) => void = (_effect: ExtensionUINavigationEffect): void => {};
  @Event onLink: (address: string) => void = (_address: string): void => {};
  @Event onBack: () => void = (): void => {};
  @Local extensionUI: ExtensionUIModel = new ExtensionUIModel();
  private bound: boolean = false;

  aboutToAppear(): void { this.bindPage(); }
  aboutToDisappear(): void { this.extensionUI.dispose(); this.bound = false; }
  private bindPage(): void {
    this.extensionUI.setNavigationHandler((effect: ExtensionUINavigationEffect) => this.dispatchNavigation(effect));
    this.extensionUI.bind(this.model, this.extensionId, this.uiId, this.context, this.initialView, this.compact, this.onLink, this.active && this.authorized, this.state);
    if (!this.authorized) this.extensionUI.invalidateAuthorization();
    this.extensionUI.setVisible(this.active && this.authorized); this.bound = true;
    this.onTitle(this.extensionUI.pageTitle || this.extensionId);
  }
  private dispatchNavigation(effect: ExtensionUINavigationEffect): void {
    if (effect.kind === 'pop') this.onBack(); else this.onNavigate(effect);
  }
  @Monitor('active', 'authorized') activeChanged(): void {
    if (!this.bound) return;
    if (!this.authorized) this.extensionUI.invalidateAuthorization();
    this.extensionUI.setVisible(this.active && this.authorized);
  }
  @Monitor('refreshRevision') refreshRequested(): void { if (this.bound) this.extensionUI.refresh(); }
  @Monitor('extensionUI.pageTitle') titleChanged(): void { if (this.bound) this.onTitle(this.extensionUI.pageTitle || this.extensionId); }
  @Monitor('model.connected', 'model.features', 'model.profile') connectionChanged(): void {
    if (this.bound) this.extensionUI.connectionChanged();
  }
  @Monitor('extensionUI.confirmationToken') confirmationChanged(): void {
    const token = this.extensionUI.confirmationToken;
    if (!token || !this.active) return;
    const message = this.extensionUI.confirmationText;
    this.getUIContext().showAlertDialog({ title: this.extensionUI.confirmationTitle, message: message, cancel: () => this.extensionUI.confirmAction(token, false),
      primaryButton: { value: tr('cancel'), action: () => this.extensionUI.confirmAction(token, false) },
      secondaryButton: { value: tr('confirm'), action: () => this.extensionUI.confirmAction(token, true) } });
  }
  @Monitor('context', 'extensionId', 'uiId', 'state') inputChanged(): void {
    if (this.bound) this.bindPage();
  }

  build() {
    Column() {
      if (this.extensionUI.error) Row({ space: Tokens.gap }) {
        Text(tr(this.extensionUI.error)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).layoutWeight(1)
        if (this.extensionUI.uncertainEventId) Button(tr('extension_ui_retry_action')).height(Tokens.touch).enabled(!this.extensionUI.busy)
          .onClick(() => this.extensionUI.retryUncertainEvent(this.extensionUI.uncertainEventId))
        else if (this.extensionUI.phase === 'recovering' || this.extensionUI.phase === 'failed') Button(tr('retry')).height(Tokens.touch)
          .onClick(() => this.extensionUI.retryRead())
      }.width('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.gap })
      if (this.extensionUI.notice) Text(this.extensionUI.notice).fontSize(Tokens.meta).fontColor($r('sys.color.font_emphasize'))
        .width('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.gap })
      if (this.extensionUI.loading && !Array.isArray(this.extensionUI.view['sections'])) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      Scroll() {
        ExtensionView({ model: this.extensionUI, active: this.active && this.model.connected, showTitle: false, onLink: (address: string) => this.onLink(address), compact: this.compact })
      }.layoutWeight(1).width('100%').scrollBar(BarState.Auto)
    }.width('100%').height('100%').backgroundColor($r('app.color.persona_canvas'))
  }
}
