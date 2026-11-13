import { ChatAttachment } from '../models/ChatAttachments';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct AttachmentStrip {
  @Require @Param attachments: ChatAttachment[];
  @Param errorKey: string = '';
  @Param actionsEnabled: boolean = true;
  @Event onRemove: (id: string) => void = (_id: string) => {};
  @Event onEdit: (id: string) => void = (_id: string) => {};
  @Builder imageMenu(item: ChatAttachment) { Menu() { MenuItem({ content: tr('edit') }).enabled(this.actionsEnabled).onClick(() => this.onEdit(item.id)) } }
  @Event onPreview: (item: ChatAttachment) => void = (_item: ChatAttachment) => {};

  @Builder item(item: ChatAttachment) {
    Column({ space: Tokens.fine }) {
      if (item.kind === 'image') {
        Image(item.previewUri).width(Tokens.attachmentPreview).height(Tokens.attachmentPreview).objectFit(ImageFit.Cover).accessibilityText(item.name)
          .onClick(() => this.onPreview(item)).bindContextMenu(this.imageMenu(item), ResponseType.LongPress);
      } else {
        Text(item.name).fontSize(Tokens.meta).maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis })
          .width(Tokens.attachmentItem).height(Tokens.attachmentPreview).textAlign(TextAlign.Center).onClick(() => this.onPreview(item));
      }
      if (item.status === 'unknown' || item.status === 'uploading')
        Text(tr(item.status === 'uploading' ? 'chat_attachment_uploading' : 'chat_attachment_upload_unknown')).fontSize(Tokens.meta).maxLines(2).width(Tokens.attachmentItem);
      Button(tr('remove'), { buttonStyle: ButtonStyleMode.TEXTUAL }).height(Tokens.touch).enabled(this.actionsEnabled)
        .onClick(() => this.onRemove(item.id));
    }.width(Tokens.attachmentItem).alignItems(HorizontalAlign.Center).padding({ left: Tokens.fine, right: Tokens.fine });
  }

  build() {
    if (this.attachments.length > 0 || this.errorKey) {
      Column({ space: Tokens.gap }) {
        if (this.errorKey && !this.attachments.some((item: ChatAttachment) => item.status === 'unknown' || item.status === 'uploading'))
          Text(tr(this.errorKey)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%');
        if (this.attachments.length > 0) {
          Scroll() {
            Row({ space: Tokens.gap }) {
              ForEach(this.attachments, (item: ChatAttachment) => this.item(item), (item: ChatAttachment) => item.id)
            }.alignItems(VerticalAlign.Top)
          }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Auto).width('100%')
        }
      }.width('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap });
    }
  }
}
