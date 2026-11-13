import { ComposeListItemV2, ContentItemV2, OperateItemV2, OperateCheckV2 } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { Decision, DecisionField, Wire } from '../models/Wire';
import {
  InteractionChoice, InteractionSchemaField, InteractionValidation,
  interactionCanSubmit, interactionElicitationDefaults, interactionElicitationMessage,
  interactionElicitationMode, interactionElicitationSupported, interactionElicitationUrl, interactionHistoryResolution,
  interactionPermissionFieldPlaceholder, interactionQuestionOptions,
  interactionSchemaFields, interactionScreenCanCancel, interactionScreenMulti, interactionScreenOptions,
  interactionScreenQuestion, interactionScreenSubmitLabel, validateInteractionElicitation
} from '../models/InteractionDecision';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';

@ComponentV2
export struct InteractionDecisionForm {
  @Require @Param model: ClientModel;
  @Require @Param request: Decision;
  @Param readOnly: boolean = false;
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Local selected: string = '';
  @Local selectedScreen: string = '';
  @Local screenOptions: string[] = [];
  @Local screenText: string = '';
  @Local openedUrl: string = '';
  @Local busy: boolean = false;
  @Local textValues: Record<string, string> = Wire.payload([], []) as Record<string, string>;
  @Local questionPicks: Record<string, string[]> = Wire.payload([], []) as Record<string, string[]>;
  @Local formValues: Record<string, Object> = Wire.payload([], []);

  aboutToAppear(): void { this.resetForm(); }
  @Monitor('request.id') requestChanged(): void { this.resetForm(); }
  @Monitor('request.revision') revisionChanged(): void {
    this.selectedScreen = '';
    this.screenOptions = [];
    this.screenText = '';
  }

  private resetForm(): void {
    this.selected = '';
    this.selectedScreen = '';
    this.screenOptions = [];
    this.screenText = '';
    this.openedUrl = '';
    this.busy = false;
    this.textValues = Wire.payload([], []) as Record<string, string>;
    this.questionPicks = Wire.payload([], []) as Record<string, string[]>;
    this.formValues = interactionElicitationDefaults(this.request);
  }

  private canRespond(): boolean {
    return !this.busy && this.model.canApprove() && interactionCanSubmit(this.request, this.readOnly);
  }

  private statusLabel(): string {
    if (this.request.status === 'open') return tr('answer');
    if (this.request.status === 'pending') return tr('pending');
    if (this.request.status === 'resolved') return tr('completed');
    if (this.request.status === 'unknown') return tr('result_unknown');
    if (this.request.status === 'expired' || this.request.status === 'cancelled' || this.request.status === 'dismissed') return tr('canceled');
    return this.request.status;
  }

  private setText(key: string, value: string): void {
    const next: Record<string, string> = Wire.payload([], []) as Record<string, string>;
    Object.keys(this.textValues).forEach((name: string) => next[name] = this.textValues[name]);
    next[key] = value;
    this.textValues = next;
  }

  private setValue(key: string, value: Object | undefined): void {
    const next: Record<string, Object> = Wire.payload([], []);
    Object.keys(this.formValues).filter((name: string) => name !== key).forEach((name: string) => next[name] = this.formValues[name]);
    if (value !== undefined && value !== null) next[key] = value;
    this.formValues = next;
  }

  private setQuestionPicks(key: string, values: string[]): void {
    const next: Record<string, string[]> = Wire.payload([], []) as Record<string, string[]>;
    Object.keys(this.questionPicks).forEach((name: string) => next[name] = this.questionPicks[name]);
    next[key] = values;
    this.questionPicks = next;
  }

  private setScreenOptions(values: string[]): void { this.screenOptions = values; }

  private questionChoices(field: DecisionField): InteractionChoice[] {
    return interactionQuestionOptions(this.request, field.key);
  }

  private pickedQuestion(field: DecisionField, value: string): boolean {
    return (this.questionPicks[field.key] || []).includes(value);
  }

  private pickQuestion(field: DecisionField, choice: InteractionChoice, checked: boolean | undefined = undefined): void {
    if (field.kind !== 'multi_choice') {
      this.setQuestionPicks(field.key, [choice.label]);
      this.setText(field.key + ':other', '');
      return;
    }
    const next = (this.questionPicks[field.key] || []).filter((item: string) => item !== choice.label);
    const picked = checked === undefined ? !this.pickedQuestion(field, choice.label) : checked;
    if (picked) next.push(choice.label);
    const ordered = this.questionChoices(field).filter((item: InteractionChoice) => next.includes(item.label)).map((item: InteractionChoice) => item.label);
    this.setQuestionPicks(field.key, ordered);
  }

  private toggleScreenOption(choice: InteractionChoice, checked: boolean | undefined = undefined): void {
    const next = this.screenOptions.filter((key: string) => key !== choice.key);
    const picked = checked === undefined ? !this.screenOptions.includes(choice.key) : checked;
    if (picked) next.push(choice.key);
    const ordered = interactionScreenOptions(this.request).filter((item: InteractionChoice) => next.includes(item.key)).map((item: InteractionChoice) => item.key);
    this.setScreenOptions(ordered);
  }

  private screenPicked(choice: InteractionChoice): boolean {
    return this.screenOptions.includes(choice.key);
  }

  private screenFreeText(): boolean {
    return interactionScreenOptions(this.request).some((choice: InteractionChoice) => choice.kind === 'type_something');
  }

  private selectedScreenChoice(): InteractionChoice | undefined {
    return interactionScreenOptions(this.request).find((choice: InteractionChoice) => choice.key === this.selectedScreen);
  }

  private schemaFields(): InteractionSchemaField[] { return interactionSchemaFields(this.request); }

  private schemaText(field: InteractionSchemaField): string {
    const value = this.formValues[field.key];
    return value === undefined || value === null ? '' : String(value);
  }

  private schemaPicked(field: InteractionSchemaField, index: number): boolean {
    const value = this.formValues[field.key], choice = field.choices[index];
    if (field.type === 'array') return Array.isArray(value) && (value as Object[]).some((item: Object) => String(item) === String(choice));
    return value !== undefined && String(value) === String(choice);
  }

  private schemaValidation(): InteractionValidation {
    return validateInteractionElicitation(this.request, this.formValues);
  }

  private inputType(field: InteractionSchemaField): InputType {
    if (field.secret) return InputType.Password;
    if (field.type === 'number' || field.type === 'integer') return InputType.Number;
    return InputType.Normal;
  }

  private canSubmit(): boolean {
    if (!this.canRespond()) return false;
    if (this.request.kind === 'plan' || this.request.kind === 'permission') {
      if (!this.selected) return false;
      if (this.selected === 'deny') return true;
      if (this.request.kind === 'permission') return !this.request.fields.some((field: DecisionField) =>
        field.required && !(this.textValues[field.key] || '').trim());
      return true;
    }
    if (this.request.kind === 'question') return this.request.fields.length > 0 && this.request.fields.every((field: DecisionField) =>
      !!(this.textValues[field.key + ':other'] || (this.questionPicks[field.key] || []).join(', ')).trim());
    if (this.request.kind === 'screen') {
      if (interactionScreenMulti(this.request)) return this.screenOptions.length > 0 || !!this.screenText.trim();
      const choice = this.selectedScreenChoice();
      return !!choice && choice.kind !== 'unknown' && (choice.kind !== 'type_something' || !!this.screenText.trim());
    }
    if (this.request.kind === 'elicitation') {
      const mode = interactionElicitationMode(this.request);
      if (mode === 'url') return !!interactionElicitationUrl(this.request) && this.openedUrl === interactionElicitationUrl(this.request);
      return interactionElicitationSupported(this.request) && this.schemaValidation().valid;
    }
    return false;
  }

  private answerValues(): Record<string, Object> {
    const values: Record<string, Object> = Wire.payload([], []);
    if (this.request.kind === 'question') {
      this.request.fields.forEach((field: DecisionField) => {
        const other = (this.textValues[field.key + ':other'] || '').trim();
        const options = this.questionChoices(field).filter((choice: InteractionChoice) => (this.questionPicks[field.key] || []).includes(choice.label))
          .map((choice: InteractionChoice) => choice.label);
        const answer = other || options.join(', ');
        if (answer) values[field.key] = answer;
      });
    } else if (this.request.kind === 'plan') {
      values['feedback'] = this.textValues['feedback'] || '';
    } else if (this.request.kind === 'permission') {
      if (this.selected !== 'deny') this.request.fields.forEach((field: DecisionField) => {
        const value = (this.textValues[field.key] || '').trim();
        if (value) values[field.key] = value;
      });
    } else if (this.request.kind === 'screen') {
      values['options'] = this.screenOptions;
      values['text'] = this.screenText;
    } else if (this.request.kind === 'elicitation') {
      Object.keys(this.formValues).forEach((key: string) => values[key] = this.formValues[key]);
      if (this.openedUrl) values['opened_url'] = this.openedUrl;
    }
    return values;
  }

  private clearSecrets(): void {
    const keys: string[] = this.request.kind === 'permission' ? this.request.fields.filter((field: DecisionField) => field.kind === 'secret').map((field: DecisionField) => field.key) :
      this.request.kind === 'elicitation' ? this.schemaFields().filter((field: InteractionSchemaField) => field.secret).map((field: InteractionSchemaField) => field.key) : [];
    const next: Record<string, string> = Wire.payload([], []) as Record<string, string>;
    Object.keys(this.textValues).filter((key: string) => !keys.includes(key)).forEach((key: string) => next[key] = this.textValues[key]);
    this.textValues = next;
    keys.forEach((key: string) => this.setValue(key, undefined));
  }

  private submit(option: string): void {
    if (!this.canRespond()) return;
    if (this.request.kind === 'question' && option === 'cancel') {
      if (!interactionScreenCanCancel(this.request)) return;
    } else if (this.request.kind === 'elicitation' && (option === 'decline' || option === 'cancel')) {
      // Decline and cancel remain available when a form is unsupported or incomplete.
    } else if (!this.canSubmit()) return;
    const values = this.request.kind === 'question' && option === 'cancel' || this.request.kind === 'elicitation' && option !== 'accept' ?
      Wire.payload([], []) : this.answerValues();
    this.busy = true;
    this.clearSecrets();
    this.model.answer(option, values, this.request).then(() => this.busy = false);
  }

  private historySummary(): string {
    const result = interactionHistoryResolution(this.request), rows: string[] = [];
    if (this.request.kind === 'question') Object.keys(result).forEach((key: string) => rows.push(key + ': ' + Wire.text(result, key)));
    else if (this.request.kind === 'plan') {
      if (result['approve'] === true) rows.push(tr('approve'));
      else if (result['approve'] === false) rows.push(tr('deny'));
      if (Wire.text(result, 'feedback')) rows.push(Wire.text(result, 'feedback'));
    } else if (this.request.kind === 'permission') {
      if (result['allow'] === true) rows.push(tr('allow'));
      else if (result['allow'] === false) rows.push(tr('deny'));
    } else if (this.request.kind === 'screen') {
      const choice = Wire.text(result, 'option_label');
      if (choice) rows.push(choice);
      const options = Wire.strings(result, 'options_labels');
      if (options.length) rows.push(options.join(', '));
      if (Wire.text(result, 'text')) rows.push(Wire.text(result, 'text'));
    } else if (this.request.kind === 'elicitation') {
      const action = Wire.text(result, 'action');
      if (action === 'accept') rows.push(tr('approve'));
      else if (action === 'decline') rows.push(tr('deny'));
      else if (action === 'cancel') rows.push(tr('canceled'));
    }
    return rows.join('\n');
  }

  @Builder actionChoice(label: string, value: string) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: label }),
      operateItemV2: new OperateItemV2({ radio: new OperateCheckV2({ isCheck: this.selected === value,
        accessibilityText: label, onChange: (checked: boolean) => { if (checked) this.selected = value; } }) }) })
      .enabled(this.canRespond())
  }

  @Builder questionChoice(field: DecisionField, choice: InteractionChoice) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: choice.label, secondaryText: choice.detail || undefined }),
      operateItemV2: new OperateItemV2({
        checkbox: field.kind === 'multi_choice' ? new OperateCheckV2({ isCheck: this.pickedQuestion(field, choice.label),
          accessibilityText: choice.label, onChange: (checked: boolean) => this.pickQuestion(field, choice, checked) }) : undefined,
        radio: field.kind !== 'multi_choice' ? new OperateCheckV2({ isCheck: this.pickedQuestion(field, choice.label),
          accessibilityText: choice.label, onChange: (checked: boolean) => { if (checked) this.pickQuestion(field, choice); } }) : undefined
      }) }).enabled(this.canRespond())
  }

  @Builder screenChoice(choice: InteractionChoice) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: choice.label, secondaryText: choice.detail || undefined }),
      operateItemV2: new OperateItemV2({
        checkbox: interactionScreenMulti(this.request) && choice.kind === 'plain' ? new OperateCheckV2({ isCheck: this.screenPicked(choice),
          accessibilityText: choice.label, onChange: (checked: boolean) => this.toggleScreenOption(choice, checked) }) : undefined,
        radio: !interactionScreenMulti(this.request) ? new OperateCheckV2({ isCheck: this.selectedScreen === choice.key,
          accessibilityText: choice.label, onChange: (checked: boolean) => { if (checked) this.selectedScreen = choice.key; } }) : undefined
      }) }).enabled(this.canRespond())
  }

  @Builder schemaChoice(field: InteractionSchemaField, index: number) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: field.choiceLabels[index] }),
      operateItemV2: new OperateItemV2({
        checkbox: field.type === 'array' ? new OperateCheckV2({ isCheck: this.schemaPicked(field, index), accessibilityText: field.choiceLabels[index],
          onChange: (checked: boolean) => this.toggleSchemaChoice(field, index, checked) }) : undefined,
        radio: field.type !== 'array' ? new OperateCheckV2({ isCheck: this.schemaPicked(field, index), accessibilityText: field.choiceLabels[index],
          onChange: (checked: boolean) => { if (checked) this.setValue(field.key, field.choices[index]); } }) : undefined
      }) }).enabled(this.canRespond())
  }

  private toggleSchemaChoice(field: InteractionSchemaField, index: number, checked: boolean): void {
    const selected: Object[] = Array.isArray(this.formValues[field.key]) ? this.formValues[field.key] as Object[] : [];
    const choice = field.choices[index], next = selected.filter((item: Object) => String(item) !== String(choice));
    if (checked) next.push(choice);
    const ordered = field.choices.filter((item: Object) => next.some((value: Object) => String(value) === String(item)));
    this.setValue(field.key, ordered.length ? ordered : undefined);
  }

  @Builder questionField(field: DecisionField) {
    Column({ space: Tokens.gap }) {
      Text(field.title).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      ForEach(this.questionChoices(field), (choice: InteractionChoice) => this.questionChoice(field, choice), (choice: InteractionChoice) => choice.label)
      TextInput({ text: this.textValues[field.key + ':other'] || '', placeholder: tr('other') })
        .enabled(this.canRespond()).onChange((value: string) => this.setText(field.key + ':other', value)).id('interaction.question.' + field.key + '.other')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder permissionField(field: DecisionField) {
    Column({ space: Tokens.gap }) {
      Text(field.title).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      TextInput({ text: this.textValues[field.key] || '', placeholder: interactionPermissionFieldPlaceholder(this.request, field.key) })
        .type(field.kind === 'secret' ? InputType.Password : InputType.Normal).showPasswordIcon(false)
        .enabled(this.canRespond()).onChange((value: string) => this.setText(field.key, value)).id('interaction.field.' + field.key)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder elicitationField(field: InteractionSchemaField) {
    Column({ space: Tokens.gap }) {
      Row({ space: Tokens.gap }) {
        Text(field.title).fontSize(Tokens.body).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (field.required) Text(tr('required')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      }.width('100%')
      if (field.description) Text(field.description).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (field.type === 'boolean') {
        Toggle({ type: ToggleType.Switch, isOn: this.formValues[field.key] === true })
          .enabled(this.canRespond()).onChange((checked: boolean) => this.setValue(field.key, checked)).id('interaction.field.' + field.key)
      } else if (field.choices.length > 0) {
        ForEach(field.choices, (_choice: Object, index: number) => this.schemaChoice(field, index), (choice: Object, index: number) => field.key + ':' + String(index) + ':' + String(choice))
      } else {
        TextInput({ text: this.schemaText(field), placeholder: field.title }).type(this.inputType(field)).showPasswordIcon(false)
          .enabled(this.canRespond()).onChange((value: string) => this.setValue(field.key, value || undefined)).id('interaction.field.' + field.key)
      }
      if (this.schemaValidation().errors[field.key]) Text(tr(this.schemaValidation().errors[field.key] === 'required' ? 'required' : 'failed')).fontSize(Tokens.meta)
        .fontColor($r('app.color.persona_danger')).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder elicitationForm() {
    if (interactionElicitationSupported(this.request)) {
      ForEach(this.schemaFields(), (field: InteractionSchemaField) => this.elicitationField(field), (field: InteractionSchemaField) => field.key)
    } else {
      Text(tr('unknown_field')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
    }
  }

  @Builder bodyContent() {
    if (this.request.kind === 'question') {
      ForEach(this.request.fields, (field: DecisionField) => this.questionField(field), (field: DecisionField) => field.key)
      if (interactionScreenCanCancel(this.request)) Button(tr('cancel')).enabled(this.canRespond()).onClick(() => this.submit('cancel'))
    } else if (this.request.kind === 'plan') {
      this.actionChoice(tr('approve'), 'approve')
      this.actionChoice(tr('deny'), 'deny')
      if (this.selected === 'deny') TextArea({ text: this.textValues['feedback'] || '', placeholder: tr('feedback') })
        .height(Tokens.inputExpanded).enabled(this.canRespond()).onChange((value: string) => this.setText('feedback', value))
    } else if (this.request.kind === 'permission') {
      ForEach(this.request.fields, (field: DecisionField) => this.permissionField(field), (field: DecisionField) => field.key)
      this.actionChoice(tr('allow'), 'approve')
      this.actionChoice(tr('deny'), 'deny')
    } else if (this.request.kind === 'screen') {
      if (interactionScreenQuestion(this.request)) Text(interactionScreenQuestion(this.request)).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      ForEach(interactionScreenOptions(this.request), (choice: InteractionChoice) => this.screenChoice(choice), (choice: InteractionChoice) => choice.key)
      if (interactionScreenMulti(this.request) && this.screenFreeText()) TextInput({ text: this.screenText, placeholder: tr('other') })
        .enabled(this.canRespond()).onChange((value: string) => this.screenText = value).id('interaction.screen.other')
      else if (!interactionScreenMulti(this.request) && this.selectedScreenChoice()?.kind === 'type_something') TextInput({ text: this.screenText, placeholder: tr('other') })
        .enabled(this.canRespond()).onChange((value: string) => this.screenText = value).id('interaction.screen.other')
    } else if (this.request.kind === 'elicitation') {
      Text(interactionElicitationMessage(this.request)).fontSize(Tokens.body).width('100%')
      if (interactionElicitationMode(this.request) === 'url' && interactionElicitationUrl(this.request)) {
        Text(interactionElicitationUrl(this.request)).fontSize(Tokens.meta).width('100%')
        Row({ space: Tokens.gap }) {
          Button(tr('open')).enabled(this.canRespond()).onClick(() => this.openUrl()).id('interaction.open')
        }.width('100%')
        Button(tr('continue')).enabled(this.canSubmit()).onClick(() => this.submit('accept')).id('interaction.continue')
      } else if (interactionElicitationMode(this.request) === 'url') {
        Text(tr('unsafe_endpoint')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
      } else {
        this.elicitationForm()
        if (interactionElicitationSupported(this.request)) Button(tr('confirm')).enabled(this.canSubmit())
          .onClick(() => this.submit('accept')).id('interaction.accept')
      }
      Row({ space: Tokens.gap }) {
        Button(tr('deny')).enabled(this.canRespond()).onClick(() => this.submit('decline')).layoutWeight(1)
        Button(tr('cancel')).enabled(this.canRespond()).onClick(() => this.submit('cancel')).layoutWeight(1)
      }.width('100%')
    }
    if (this.request.kind !== 'elicitation') Button(this.request.kind === 'screen' && interactionScreenMulti(this.request) ? interactionScreenSubmitLabel(this.request) : tr('confirm')).enabled(this.canSubmit())
      .width('100%').constraintSize({ minHeight: Tokens.touch }).onClick(() => this.submit(this.request.kind === 'question' ? 'answer' :
        this.request.kind === 'screen' && interactionScreenMulti(this.request) ? 'submit' :
          this.request.kind === 'screen' ? this.selectedScreen : this.selected)).id('interaction.submit')
  }

  private openUrl(): void {
    const address = interactionElicitationUrl(this.request);
    if (!address || !this.canRespond()) return;
    this.onLink(address);
    this.openedUrl = address;
  }

  build() {
    Column({ space: Tokens.gap }) {
      if (this.request.status !== 'open' || this.readOnly) Row({ space: Tokens.gap }) {
        Text(this.statusLabel()).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).layoutWeight(1)
        if (this.readOnly) Text(tr('read_only')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      }.width('100%')
      if (this.request.prompt && this.request.kind !== 'elicitation' && this.request.kind !== 'screen')
        Text(this.request.prompt).fontSize(Tokens.title).fontWeight(FontWeight.Medium).width('100%')
      if (this.readOnly) {
        if (this.historySummary()) Text(this.historySummary()).fontSize(Tokens.body).width('100%')
      } else {
        this.bodyContent()
      }
      if (this.model.error) Text(tr(this.model.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
    }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
  }
}
