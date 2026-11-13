import { webview } from '@kit.ArkWeb';

@ComponentV2
export struct LicenseNotices {
  private controller: webview.WebviewController = new webview.WebviewController();

  build() {
    Web({ src: $rawfile('native-ssh-licenses.html'), controller: this.controller })
      .javaScriptAccess(false)
      .domStorageAccess(false)
      .fileAccess(false)
      .width('100%').height('100%')
  }
}
