import { ExtensionNavigation, ExtensionNavigationEntry } from '../models/ExtensionNavigation';
import { extensionIcon } from '../design/ExtensionIcons';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct ExtensionNavigationPage {
  @Require @Param directory: ExtensionNavigation;
  @Event onOpen: (entry: ExtensionNavigationEntry) => void = (_entry: ExtensionNavigationEntry): void => {};
  build() {
    Column() {
      if (this.directory.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
      if (this.directory.error) Row({ space: Tokens.gap }) {
        Text(tr(this.directory.error)).fontSize(Tokens.meta).layoutWeight(1)
        Button(tr('retry')).height(Tokens.touch).enabled(!this.directory.loading).onClick(() => this.directory.refresh())
      }.padding(Tokens.page).width('100%')
      if (this.directory.ready && !this.directory.entries.length) Text(tr('no_applications')).fontSize(Tokens.body).padding(Tokens.page)
      List() {
        Repeat<ExtensionNavigationEntry>(this.directory.entries).each((row: RepeatItem<ExtensionNavigationEntry>) => {
          ListItem() {
            Row({ space: Tokens.page }) {
              SymbolGlyph(extensionIcon(row.item.icon)).fontSize(Tokens.title).fontColor([$r('sys.color.font_emphasize')])
              Text(row.item.title).fontSize(Tokens.body).layoutWeight(1)
              if (row.item.badge) Badge({ value: row.item.badge, style: { badgeSize: Tokens.title, fontSize: Tokens.meta,
                badgeColor: $r('sys.color.comp_background_tertiary'), color: $r('sys.color.font_primary') } }) { Column() {}.width(Tokens.title).height(Tokens.title) }
            }.width('100%').padding(Tokens.page)
          }.enabled(this.directory.accepts(row.item)).onClick(() => { if (this.directory.accepts(row.item)) this.onOpen(row.item); })
            .accessibilityGroup(true).accessibilityText(row.item.title + (row.item.badge ? ', ' + row.item.badge : ''))
        }).key((entry: ExtensionNavigationEntry): string => entry.key).virtualScroll({ totalCount: this.directory.entries.length })
      }.layoutWeight(1).width('100%').scrollBar(BarState.Auto)
    }.width('100%').height('100%').backgroundColor($r('app.color.persona_canvas'))
  }
}
