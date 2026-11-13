import { common } from '@kit.AbilityKit';
import { ClientModel } from '../models/ClientModel';
import { RemoteDocument, loadRemoteDocument } from '../models/RemoteDocument';
import { ResourceRow } from '../models/Wire';
import { previewDocument } from '../platform/NativePreview';
import { MarkdownText } from './MarkdownText';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct RemoteDocumentPage {
  @Require @Param model: ClientModel;
  @Require @Param row: ResourceRow;
  @Param active: boolean = true;
  @Event onLink: (address: string) => void = () => {};
  @Local document: RemoteDocument | undefined = undefined;
  @Local loading: boolean = true;
  @Local error: string = '';
  @Local opening: boolean = false;
  private revision: number = 0;
  private visible: boolean = false;
  aboutToAppear(): void { this.visible = true; this.load(); }
  aboutToDisappear(): void { this.visible = false; this.revision++; this.document = undefined; }
  @Monitor('model.connected', 'active') changed(): void { if (this.active && this.visible && !this.document) this.load(); }
  private async load(): Promise<void> {
    const revision = ++this.revision, key = this.row.key(), generation = this.model.connectionGeneration(), scope = this.model.scope(); this.loading = true; this.error = '';
    const valid = (): boolean => this.visible && this.active && revision === this.revision && this.row.key() === key && this.model.owns(generation, scope);
    try { const document = await loadRemoteDocument(this.model, this.row, valid); if (valid()) this.document = document; }
    catch (error) { if (valid()) this.error = error instanceof Error && error.message === 'request_updated' ? 'request_updated' : 'preview_unavailable'; }
    finally { if (valid()) this.loading = false; }
  }
  private async preview(): Promise<void> {
    const document = this.document, host = this.getUIContext().getHostContext(); if (!document || !host || this.opening) return;
    const revision = this.revision, generation = this.model.connectionGeneration(), scope = this.model.scope(); this.opening = true;
    try { await previewDocument(host as common.UIAbilityContext, this.model.profile.hubId || this.model.profile.id, document.data, document.name, document.mime,
      (): boolean => this.visible && this.active && revision === this.revision && this.model.owns(generation, scope)); }
    catch (_) { if (this.visible && revision === this.revision) this.error = 'preview_unavailable'; }
    finally { if (revision === this.revision) this.opening = false; }
  }
  build() {
    Column({ space: Tokens.gap }) {
      if (this.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.error) { Text(tr(this.error)).fontSize(Tokens.meta); Button(tr('retry')).onClick(() => this.load()) }
      if (this.document) {
        if (this.document.text) Scroll() { MarkdownText({ text: this.document.text, onLink: (address: string) => this.onLink(address) }) }.layoutWeight(1).width('100%')
        Button(tr('session_tools_preview_file')).enabled(!this.opening).onClick(() => this.preview()).id('document.preview')
      }
    }.padding(Tokens.page).width('100%').height('100%')
  }
}
