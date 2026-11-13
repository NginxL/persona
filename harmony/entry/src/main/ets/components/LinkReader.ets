import { webview } from '@kit.ArkWeb';
import { readerLink, readerHost } from '../platform/ReaderLink';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct LinkReader {
  @Require @Param address: string;
  @Event onHost: (host: string) => void = () => {};
  @Local loading: boolean = true;
  @Local failed: boolean = false;
  @Local progress: number = 0;
  @Local backward: boolean = false;
  private controller: webview.WebviewController = new webview.WebviewController();
  private attached: boolean = false;
  private loadedAddress: string = '';
  private timeout: number = -1;
  @Monitor('address') addressChanged(): void { this.loadAddress(); }
  private clearTimeout(): void { if (this.timeout !== -1) clearTimeout(this.timeout); this.timeout = -1; }
  private armTimeout(): void {
    this.clearTimeout(); this.timeout = setTimeout(() => { this.timeout = -1; this.loading = false; this.failed = true; try { this.controller.stop(); } catch (_) {} }, 30000);
  }
  private loadAddress(): void {
    const address = readerLink(this.address);
    if (!this.attached || !address || address === this.loadedAddress) return;
    this.loadedAddress = address; this.loading = true; this.failed = false; this.armTimeout();
    try { this.controller.loadUrl(address); } catch (_) { this.loading = false; this.failed = true; this.clearTimeout(); }
  }
  aboutToDisappear(): void { this.clearTimeout(); this.attached = false; try { this.controller.stop(); } catch (_) {} }
  build() {
    Column() {
      if (this.loading) Progress({ value: this.progress, total: 100, type: ProgressType.Linear }).width('100%')
      if (this.backward || this.failed) {
        Row({ space: Tokens.gap }) {
          if (this.backward) Button(tr('back'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => this.controller.backward())
          if (this.failed) {
            Text(tr('connection_failed')).fontSize(Tokens.meta).layoutWeight(1)
            Button(tr('retry')).onClick(() => { this.failed = false; this.controller.refresh(); })
          }
        }.padding(Tokens.inset).width('100%')
      }
      if (readerLink(this.address)) {
      Web({ src: readerLink(this.address), controller: this.controller, incognitoMode: true })
        .javaScriptAccess(true).fileAccess(false).domStorageAccess(false)
        .onControllerAttached(() => { this.attached = true; this.loadedAddress = readerLink(this.address); })
        .onLoadIntercept((event) => !readerLink(event.data.getRequestUrl()))
        .onPermissionRequest((event) => event.request.deny())
        .onSslErrorEventReceive((event) => { event.handler.handleCancel(); this.loading = false; this.failed = true; this.clearTimeout(); })
        .onProgressChange((event) => this.progress = event.newProgress)
        .onPageBegin((event) => { this.progress = 0; this.loading = true; this.failed = false; this.armTimeout(); const host = readerHost(event.url); if (host) this.onHost(host); })
        .onPageEnd(() => { this.clearTimeout(); this.loading = false; this.backward = this.controller.accessBackward(); })
        .onErrorReceive((event) => { if (event.request.isMainFrame()) { this.clearTimeout(); this.loading = false; this.failed = true; } })
        .width('100%').layoutWeight(1)
      }
    }.width('100%').height('100%')
  }
}
