import { image } from '@kit.ImageKit';
import { ClientModel } from '../models/ClientModel';
import { ComputerScreen, ComputerScreenFrame } from '../models/ComputerScreen';
import { ResourceRow, Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct ComputerScreenPage {
  @Require @Param model: ClientModel;
  @Require @Param row: ResourceRow;
  @Param active: boolean = true;
  @Event onClose: () => void = (): void => {};

  @Local screen: ComputerScreen = new ComputerScreen();
  @Local framePixels: image.PixelMap | undefined = undefined;
  @Local frameRatio: number = 1;
  @Local accountDraft: string = '';
  @Local passwordDraft: string = '';
  @Local textDraft: string = '';
  @Local textSending: boolean = false;
  @Local frameAreaWidth: number = 0;
  @Local frameAreaHeight: number = 0;

  private mounted: boolean = false;
  private pointerDown: boolean = false;

  aboutToAppear(): void { this.mounted = true; this.updateVisibility(); }

  aboutToDisappear(): void {
    this.mounted = false;
    this.deactivate();
  }

  @Monitor('active') activeChanged(): void { this.updateVisibility(); }

  private updateVisibility(): void {
    if (!this.mounted) return;
    if (this.active) this.open();
    else this.deactivate();
  }

  private isVisible(): boolean { return this.mounted && this.active; }

  private deactivate(): void {
    this.pointerDown = false;
    this.clearFields();
    this.screen.hide();
    this.clearFrame();
  }

  @Monitor('row') rowChanged(): void { this.open(); }

  @Monitor('model.profile') profileChanged(): void { this.dismissForOwnerChange(); }

  @Monitor('model.connected') connectionChanged(): void { if (!this.model.connected) this.dismissForOwnerChange(); }

  @Monitor('model.machines') machinesChanged(): void {
    const machine: ResourceRow | undefined = this.model.machines.find((candidate: ResourceRow) => candidate.key() === this.row.key());
    if (!machine || !Wire.bool(machine.data(), 'online') || !this.screen.validateOwner()) this.dismissForOwnerChange();
  }

  @Monitor('screen.frameRevision') frameChanged(): void { this.decodeFrame(); }

  @Monitor('screen.phase') phaseChanged(): void { if (!this.screen.isLive()) this.clearFrame(); if (['idle', 'closed', 'failed', 'auth_failed', 'sharing_off'].includes(this.screen.phase)) this.clearFields(); }

  @Monitor('screen.controlRevision') controlChanged(): void {
    if (!this.screen.controlAllowed()) { this.textDraft = ''; this.pointerDown = false; }
  }

  private open(): void {
    if (!this.isVisible()) return;
    this.clearFields();
    this.clearFrame();
    this.frameRatio = 1;
    this.pointerDown = false;
    this.screen.open(this.model, this.row).catch((): void => {});
  }

  private clearFields(): void {
    this.textSending = false;
    this.accountDraft = '';
    this.passwordDraft = '';
    this.textDraft = '';
  }

  private clearFrame(): void {
    this.framePixels?.release();
    this.framePixels = undefined;
  }

  private dismissForOwnerChange(): void {
    if (!this.isVisible()) return;
    this.deactivate();
    this.onClose();
  }

  private decodeFrame(): void {
    if (!this.isVisible()) { this.clearFrame(); return; }
    if (!this.screen.isLive()) { this.clearFrame(); return; }
    let snapshot: ComputerScreenFrame;
    try { snapshot = this.screen.frameSnapshot(); }
    catch (_) {
      this.clearFrame();
      return;
    }
    if (snapshot.width <= 0 || snapshot.height <= 0 || snapshot.pixels.length !== snapshot.width * snapshot.height * 4) {
      this.clearFrame();
      return;
    }
    let next: image.PixelMap | undefined;
    try {
      next = image.createPixelMapFromPixelsSync(snapshot.pixels.buffer as ArrayBuffer, {
        size: { width: snapshot.width, height: snapshot.height },
        srcPixelFormat: image.PixelMapFormat.BGRA_8888,
        pixelFormat: image.PixelMapFormat.BGRA_8888
      });
    } catch (_) { return; }
    if (!this.isVisible() || !this.screen.isLive()) { next.release(); return; }
    const previous: image.PixelMap | undefined = this.framePixels;
    this.framePixels = next;
    this.frameRatio = snapshot.width / snapshot.height;
    previous?.release();
  }

  private confirmControl(): void {
    if (!this.isVisible() || !this.screen.isLive() || this.screen.controlAllowed()) return;
    const attempt = this.screen.connectionRevision(), rowKey = this.row.key();
    const target: string = this.row.title || this.row.id;
    this.getUIContext().showAlertDialog({
      title: tr('computer_screen_control_confirm_title'),
      message: tr('computer_screen_control_confirm_message') + '\n' + target,
      primaryButton: { value: tr('cancel'), action: (): void => {} },
      secondaryButton: { value: tr('computer_screen_allow_control'), action: (): void => { if (this.isVisible() && this.row.key() === rowKey && this.screen.connectionRevision() === attempt) this.screen.grantControl(); } }
    });
  }

  private cancelCredentials(): void {
    this.clearFields();
    this.screen.cancelCredentials();
  }

  private submitCredentials(): void {
    if (this.screen.submitCredentials(this.accountDraft, this.passwordDraft)) this.clearFields();
  }

  private async submitText(): Promise<void> {
    if (this.textSending || !this.textDraft) return;
    const captured: string = this.textDraft, attempt: number = this.screen.connectionRevision();
    this.screen.releasePointer(); this.pointerDown = false;
    this.textSending = true;
    try {
      const sent: boolean = await this.screen.sendText(captured);
      if (sent && this.isVisible() && this.screen.connectionRevision() === attempt && this.textDraft === captured) this.textDraft = '';
    } catch (_) { }
    finally { if (this.screen.connectionRevision() === attempt) this.textSending = false; }
  }

  private sendKey(value: number): void { if (!this.textSending) this.screen.pressKey(value); }

  private touchFrame(event: TouchEvent): void {
    if (this.textSending || !this.screen.controlAllowed() || this.frameAreaWidth <= 0 || this.frameAreaHeight <= 0 || event.changedTouches.length === 0) return;
    const touch: TouchObject = event.changedTouches[0];
    if (touch.type === TouchType.Down) {
      this.pointerDown = this.screen.pointerFromView(touch.x, touch.y, this.frameAreaWidth, this.frameAreaHeight, 1);
    } else if (touch.type === TouchType.Move) {
      this.screen.pointerFromView(touch.x, touch.y, this.frameAreaWidth, this.frameAreaHeight, this.pointerDown ? 1 : 0);
    } else if (touch.type === TouchType.Up) {
      if (this.pointerDown && !this.screen.pointerFromView(touch.x, touch.y, this.frameAreaWidth, this.frameAreaHeight, 0)) this.screen.releasePointer();
      this.pointerDown = false;
    } else if (touch.type === TouchType.Cancel) {
      this.screen.releasePointer();
      this.pointerDown = false;
    }
  }

  @Builder credentialForm() {
    Column({ space: Tokens.gap }) {
      TextInput({ text: this.accountDraft, placeholder: tr('computer_screen_account') })
        .onChange((value: string) => this.accountDraft = value).height(Tokens.touch)
      TextInput({ text: this.passwordDraft, placeholder: tr('computer_screen_password') })
        .type(InputType.Password).showPasswordIcon(false).onChange((value: string) => this.passwordDraft = value).height(Tokens.touch)
      Row({ space: Tokens.gap }) {
        Button(tr('computer_screen_cancel_sign_in')).layoutWeight(1).onClick(() => this.cancelCredentials()).height(Tokens.touch)
        Button(tr('computer_screen_sign_in')).layoutWeight(1).enabled(!!this.accountDraft.trim() && !!this.passwordDraft)
          .onClick(() => this.submitCredentials()).height(Tokens.touch)
      }.width('100%')
    }.padding(Tokens.page).width('100%')
  }

  @Builder keyboardTools() {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Button('Esc').layoutWeight(1).onClick(() => this.sendKey(0xff1b)).height(Tokens.touch)
        Button('Tab').layoutWeight(1).onClick(() => this.sendKey(0xff09)).height(Tokens.touch)
        Button('←').layoutWeight(1).onClick(() => this.sendKey(0xff51)).height(Tokens.touch)
        Button('↑').layoutWeight(1).onClick(() => this.sendKey(0xff52)).height(Tokens.touch)
        Button('↓').layoutWeight(1).onClick(() => this.sendKey(0xff54)).height(Tokens.touch)
        Button('→').layoutWeight(1).onClick(() => this.sendKey(0xff53)).height(Tokens.touch)
        Button('↵').layoutWeight(1).onClick(() => this.sendKey(0xff0d)).height(Tokens.touch)
      }.width('100%').enabled(!this.textSending)
      Row({ space: Tokens.gap }) {
        TextInput({ text: this.textDraft, placeholder: tr('computer_screen_type_text') })
          .layoutWeight(1).onChange((value: string) => this.textDraft = value).height(Tokens.touch)
        Button(tr('computer_screen_send_text')).enabled(!!this.textDraft && this.textDraft.length <= 256 && !this.textSending)
          .onClick(() => this.submitText()).height(Tokens.touch)
      }.width('100%')
    }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.page }).width('100%')
  }

  build() {
    Column() {
      if (this.screen.phase === 'live') Row({ space: Tokens.gap }) {
        if (!this.screen.controlAllowed()) Text(tr('computer_screen_read_only')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).layoutWeight(1)
        else Blank().layoutWeight(1)
        Button(tr('computer_screen_refresh')).onClick(() => this.screen.refresh()).constraintSize({ minHeight: Tokens.touch })
        if (this.screen.controlAllowed()) Button(tr('computer_screen_release_control')).onClick(() => this.screen.releaseControl()).constraintSize({ minHeight: Tokens.touch })
        else Button(tr('computer_screen_control')).onClick(() => this.confirmControl()).constraintSize({ minHeight: Tokens.touch })
      }.padding({ left: Tokens.page, right: Tokens.page, bottom: Tokens.gap }).width('100%').alignItems(VerticalAlign.Center)

      if (this.screen.credentialsRequested) this.credentialForm()

      Stack() {
        if (this.screen.isLive() && this.framePixels) Image(this.framePixels).objectFit(ImageFit.Contain).aspectRatio(this.frameRatio)
          .width('100%').height('100%').onTouch((event: TouchEvent) => this.touchFrame(event))
        else if (this.screen.phase === 'live' || this.screen.phase === 'connecting' || this.screen.phase === 'checking')
          LoadingProgress().width(Tokens.touch).height(Tokens.touch).align(Alignment.Center)
        else if (!this.screen.credentialsRequested) Column({ space: Tokens.gap }) {
          Text(tr(this.screen.messageKey)).fontSize(Tokens.body).fontColor($r('sys.color.font_secondary')).textAlign(TextAlign.Center)
          Button(tr('computer_screen_connect')).onClick(() => this.open()).constraintSize({ minHeight: Tokens.touch })
        }.align(Alignment.Center).width('100%').padding(Tokens.page)
      }.layoutWeight(1).width('100%').backgroundColor($r('app.color.persona_canvas'))
        .onAreaChange((_oldArea: Area, area: Area) => { this.frameAreaWidth = Number(area.width); this.frameAreaHeight = Number(area.height); })

      if (this.screen.isLive() && !this.screen.credentialsRequested && this.screen.controlAllowed()) this.keyboardTools()
    }.width('100%').height('100%').backgroundColor($r('app.color.persona_canvas'))
  }
}
