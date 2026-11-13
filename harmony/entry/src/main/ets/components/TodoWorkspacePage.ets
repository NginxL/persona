import { ComposeListItemV2, ContentItemV2 } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { TodoWorkspace, TodoWorkspaceAgent, TodoWorkspaceGroup, TodoWorkspaceItem, TodoWorkspaceSection,
  TodoWorkspaceSpace, TodoWorkspaceSpaceChoice } from '../models/TodoWorkspace';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { ResourceRow, Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { MarkdownText } from './MarkdownText';

@ObservedV2
class BoundTodoSection {
  readonly identity: string;
  readonly key: string;
  @Trace value: TodoWorkspaceSection;
  @Trace rows: BoundRow<TodoWorkspaceGroup>[] = [];

  constructor(identity: string, key: string, value: TodoWorkspaceSection) {
    this.identity = identity;
    this.key = key;
    this.value = value;
  }
}

@ComponentV2
export struct TodoWorkspacePage {
  @Require @Param model: ClientModel;
  @Param row: ResourceRow | undefined = undefined;
  @Param list: boolean = true;
  @Param visible: boolean = true;
  @Event onOpen: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenResource: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Event onCreateAgent: (todo: ResourceRow) => void = (_todo: ResourceRow) => {};
  @Event onTitle: (title: string) => void = (_title: string) => {};
  @Local workspace: TodoWorkspace = new TodoWorkspace();
  @Local sectionBindings: BoundTodoSection[] = [];
  @Local lineageRowBindings: BoundRow<ResourceRow>[] = [];
  @Local relatedTodoBindings: BoundRow<TodoWorkspaceItem>[] = [];
  @Local spaceBindings: BoundRow<TodoWorkspaceSpace>[] = [];
  @Local activeAgentBindings: BoundRow<TodoWorkspaceAgent>[] = [];
  @Local previousAgentBindings: BoundRow<TodoWorkspaceAgent>[] = [];
  @Local previousAgentsOpen: boolean = false;
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  private sectionBindingSequence: number = 0;
  private pageAvailable: boolean = false;
  private lastVisible: boolean = true;
  private workspaceOpening: boolean = false;
  private workspaceInitialized: boolean = false;
  private queuedRefresh: boolean = false;
  private openSerial: number = 0;

  aboutToAppear(): void {
    this.pageAvailable = true;
    this.lastVisible = this.visible;
    this.previousAgentsOpen = false;
    this.syncRowBindings();
    this.openWorkspace();
  }
  aboutToDisappear(): void {
    this.pageAvailable = false;
    this.openSerial++;
    this.workspaceOpening = false;
    this.workspaceInitialized = false;
    this.queuedRefresh = false;
    this.workspace.close();
  }

  @Monitor('row', 'list') routeChanged(): void { this.syncRowBindings(); if (this.pageAvailable) this.openWorkspace(); }

  @Monitor('model.profile') profileChanged(): void {
    this.previousAgentsOpen = false;
    this.syncRowBindings();
    if (this.pageAvailable) this.openWorkspace();
  }
  @Monitor('model.connected') connectionChanged(): void {
    if (!this.pageAvailable) return;
    if (this.model.connected) this.refreshWhenReady(true);
    else this.workspace.error = 'offline';
  }
  @Monitor('visible') visibilityChanged(): void {
    const becameVisible = !this.lastVisible && this.visible;
    this.lastVisible = this.visible;
    if (becameVisible && this.pageAvailable) {
      this.publishTitle();
      this.refreshWhenReady();
    }
  }
  @Monitor('model.sessions') sessionsChanged(): void { this.workspace.update(this.model); }
  @Monitor('model.machines') machinesChanged(): void { this.workspace.update(this.model); }
  @Monitor('model.knowledge') projectsChanged(): void { this.workspace.update(this.model); }
  @Monitor('workspace.selected') selectedChanged(): void { this.syncRowBindings(); this.publishTitle(); }
  @Monitor('workspace.items', 'workspace.search', 'workspace.statusFilter', 'workspace.groupBy',
    'workspace.projects', 'workspace.sessions', 'workspace.machines') rowsChanged(): void { this.syncRowBindings(); }

  private openWorkspace(): void {
    const serial = ++this.openSerial;
    this.workspaceOpening = true;
    this.workspaceInitialized = false;
    this.queuedRefresh = false;
    this.workspace.open(this.model, this.row, this.list).catch(() => undefined).finally(() => {
      if (serial !== this.openSerial) return;
      this.workspaceOpening = false;
      this.workspaceInitialized = true;
      if (this.queuedRefresh && this.pageAvailable && this.visible) {
        this.queuedRefresh = false;
        this.workspace.refresh().catch(() => undefined);
      }
    });
  }

  private refreshWhenReady(retryOpening: boolean = false): void {
    if (!this.pageAvailable || !this.visible) return;
    if (this.workspaceOpening) {
      // The initial open loads the workspace and detail target. A connection retry
      // still needs a fresh read once that open has rebound its owner.
      if (this.workspaceInitialized || retryOpening) this.queuedRefresh = true;
      return;
    }
    if (this.workspaceInitialized) this.workspace.refresh().catch(() => undefined);
  }

  private publishTitle(): void {
    const row = this.row, selected = this.workspace.selected;
    if (this.list || !row || !selected.id || this.workspace.targetGone || row.id !== selected.id) return;
    const manager = row.manager || this.model.profile.manager;
    if (selected.manager && selected.manager !== manager) return;
    this.onTitle(selected.title);
  }

  private syncRowBindings(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager,
      this.row?.key() || 'list', this.list ? 'list' : 'detail']);
    const sameOwnerScope = this.rowBindingOwner === this.model && this.rowBindingScope === scope;
    const previous = sameOwnerScope ? this.sectionBindings : [];
    const available = new Map<string, BoundTodoSection[]>();
    previous.forEach((binding: BoundTodoSection) => {
      const bindings = available.get(binding.identity) || [];
      bindings.push(binding);
      available.set(binding.identity, bindings);
    });
    const occurrences = new Map<string, number>(), next: BoundTodoSection[] = [];
    this.workspace.sections().forEach((section: TodoWorkspaceSection) => {
      const occurrence = occurrences.get(section.key) || 0;
      occurrences.set(section.key, occurrence + 1);
      const identity = JSON.stringify([section.key, occurrence]);
      const matches = available.get(identity);
      const existing = matches && matches.length ? matches.shift() : undefined;
      let binding: BoundTodoSection;
      if (existing) {
        binding = existing;
        binding.value = section;
        binding.rows = reconcileBoundRows(binding.rows, section.rows, (group: TodoWorkspaceGroup) => group.key,
          this.model, this.model, scope, scope);
      } else {
        this.sectionBindingSequence++;
        binding = new BoundTodoSection(identity, 'todo-section-binding.' + String(this.sectionBindingSequence), section);
        binding.rows = reconcileBoundRows([], section.rows, (group: TodoWorkspaceGroup) => group.key,
          undefined, this.model, '', scope);
      }
      next.push(binding);
    });
    this.sectionBindings = next;
    const current = this.workspace.selected;
    this.lineageRowBindings = reconcileBoundRows(this.lineageRowBindings, current.id ? this.workspace.lineageResources(current) : [],
      (resource: ResourceRow) => resource.key(), this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.relatedTodoBindings = reconcileBoundRows(this.relatedTodoBindings, current.id ? this.workspace.relatedTodos(current) : [],
      (todo: TodoWorkspaceItem) => JSON.stringify([todo.manager, todo.id]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.spaceBindings = reconcileBoundRows(this.spaceBindings, current.id ? current.spaces : [],
      (space: TodoWorkspaceSpace) => JSON.stringify([space.machineId, space.space]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.activeAgentBindings = reconcileBoundRows(this.activeAgentBindings, current.id ? current.activeAgents() : [],
      (agent: TodoWorkspaceAgent) => JSON.stringify([agent.machineId, agent.sessionId]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.previousAgentBindings = reconcileBoundRows(this.previousAgentBindings, current.id ? current.previousAgents() : [],
      (agent: TodoWorkspaceAgent) => JSON.stringify([agent.machineId, agent.sessionId]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.rowBindingOwner = this.model;
    this.rowBindingScope = scope;
  }

  private statusOptions(): SelectOption[] {
    const options: SelectOption[] = [{ value: tr('all') }];
    ['needs_you', 'checking', 'working', 'followup', 'not_started', 'done'].forEach((state: string) => options.push({ value: tr('todo_state_' + state) }));
    return options;
  }
  private statusIndex(): number {
    const states: string[] = ['all', 'needs_you', 'checking', 'working', 'followup', 'not_started', 'done'];
    const index = states.indexOf(this.workspace.statusFilter);
    return index < 0 ? 0 : index;
  }
  private groupOptions(): SelectOption[] {
    return [{ value: tr('todo_group_status') }, { value: tr('todo_group_project') }, { value: tr('todo_group_thread') }];
  }
  private groupIndex(): number { return ['status', 'project', 'thread'].indexOf(this.workspace.groupBy); }
  private sessionKey(row: ResourceRow): string { return JSON.stringify([row.manager, row.machine, row.session || row.id]); }
  private choiceKey(choice: TodoWorkspaceSpaceChoice): string { return JSON.stringify([choice.machineId, choice.space]); }
  private selectedSession(): ResourceRow | undefined {
    return this.workspace.candidateSessions().find((row: ResourceRow) => this.sessionKey(row) === this.workspace.selectedAgentKey);
  }
  private selectedSpace(): TodoWorkspaceSpaceChoice | undefined {
    return this.workspace.spaceChoices().find((choice: TodoWorkspaceSpaceChoice) => this.choiceKey(choice) === this.workspace.selectedSpaceKey);
  }
  private selectSession(index: number): void {
    const row = this.workspace.candidateSessions()[index];
    if (row) this.workspace.selectedAgentKey = this.sessionKey(row);
  }
  private selectSpace(index: number): void {
    const choice = this.workspace.spaceChoices()[index];
    if (choice) this.workspace.selectedSpaceKey = this.choiceKey(choice);
  }
  private sessionOptions(): SelectOption[] {
    return this.workspace.candidateSessions().map((row: ResourceRow): SelectOption => ({ value: (row.title || row.id) + ' · ' + this.workspace.machineName(row.machine) }));
  }
  private spaceOptions(): SelectOption[] {
    return this.workspace.spaceChoices().map((choice: TodoWorkspaceSpaceChoice): SelectOption => ({ value: choice.label }));
  }
  private targetCurrent(todo: TodoWorkspaceItem, generation: number, scope: string): boolean {
    return this.model.owns(generation, scope) && this.workspace.isSelected(todo);
  }
  private todoSubtitle(group: TodoWorkspaceGroup): string {
    const relatedCount = group.related().length;
    const state = tr('todo_state_' + group.state);
    return relatedCount > 0 ? state + ' · ' + String(relatedCount + 1) + ' · ' + tr('todo_related') : state;
  }
  private sectionTitle(section: TodoWorkspaceSection): string {
    if (this.workspace.groupBy === 'status' || section.title === 'todo_unsorted' || section.title === 'todo_no_thread')
      return tr(section.title);
    return section.title;
  }
  private completionLabel(): string {
    return tr(this.workspace.selected.isDone() && !this.workspace.selected.startError ? 'reopen' : 'todo_complete');
  }
  private detailEmptyKey(): string {
    return this.workspace.loading ? 'loading' : this.workspace.error || 'todo_gone';
  }
  private changeCompletion(): void {
    const todo = this.workspace.selected;
    if (!this.workspace.isSelected(todo)) return;
    if (todo.isDone() && !todo.startError) this.workspace.setDone(todo, false).catch(() => undefined);
    else this.confirmDone(todo);
  }
  private openProject(): void {
    const resource = this.workspace.projectResource(this.workspace.selected);
    if (resource) this.onOpenResource(resource);
  }
  private openThread(): void {
    const resource = this.workspace.threadResource(this.workspace.selected);
    if (resource) this.onOpenResource(resource);
  }
  private openSource(): void {
    const resource = this.workspace.sourceResource(this.workspace.selected);
    if (resource) this.onOpenResource(resource);
  }

  private confirmDone(todo: TodoWorkspaceItem): void {
    const agents = todo.activeAgents().map((agent: TodoWorkspaceAgent) => (agent.title || tr('agent')) + ' · ' + this.workspace.machineName(agent.machineId));
    const message = todo.title + (agents.length ? '\n' + agents.join('\n') : '');
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = todo.id;
    this.getUIContext().showAlertDialog({ title: tr('todo_complete'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_complete'), action: () => {
        if (!this.model.owns(generation, scope) || !this.workspace.isSelected(todo) || this.workspace.selected.id !== target) return;
        this.workspace.setDone(todo, true).catch(() => undefined);
      } } });
  }

  private confirmDelete(todo: TodoWorkspaceItem): void {
    const target = todo.id, generation = this.model.connectionGeneration(), scope = this.model.scope();
    const agents = todo.activeAgents().map((agent: TodoWorkspaceAgent) => (agent.title || tr('agent')) + ' · ' + this.workspace.machineName(agent.machineId));
    const message = todo.title + (agents.length ? '\n' + agents.join('\n') : '');
    this.getUIContext().showAlertDialog({ title: tr('delete'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (!this.model.owns(generation, scope) || !this.workspace.isSelected(todo) || this.workspace.selected.id !== target) return;
        this.workspace.remove(todo).then((removed: boolean) => { if (removed) this.workspace.selected = new TodoWorkspaceItem(); }).catch(() => undefined);
      } } });
  }

  private confirmLinkAgent(todo: TodoWorkspaceItem): void {
    const session = this.selectedSession();
    if (!session) return;
    const machine = this.workspace.machineName(session.machine);
    const current = Wire.object(session.data(), 'todo');
    const previous = Wire.text(current, 'title');
    const message = todo.title + '\n' + (session.title || tr('agent')) + ' · ' + machine +
      (previous ? '\n' + tr('todo_move_from') + ': ' + previous : '');
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), key = this.sessionKey(session);
    this.getUIContext().showAlertDialog({ title: tr('todo_link_agent'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_link_agent'), action: () => {
        if (!this.targetCurrent(todo, generation, scope) || this.sessionKey(session) !== key ||
          !this.workspace.candidateSessions(todo).some((row: ResourceRow) => this.sessionKey(row) === key)) return;
        this.workspace.linkAgent(todo, session).catch(() => undefined);
      } } });
  }

  private confirmUnlinkAgent(todo: TodoWorkspaceItem, agent: TodoWorkspaceAgent): void {
    const target = todo.id, identity = JSON.stringify([agent.machineId, agent.sessionId]);
    const currentAgent = todo.agents.find((item: TodoWorkspaceAgent) => JSON.stringify([item.machineId, item.sessionId]) === identity);
    if (!currentAgent || !currentAgent.isActive()) return;
    const machine = this.workspace.machineName(currentAgent.machineId);
    const generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('todo_unlink_agent'),
      message: todo.title + '\n' + (currentAgent.title || tr('agent')) + ' · ' + machine,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_unlink_agent'), action: () => {
        const selected = this.workspace.selected;
        const latest = selected.agents.find((item: TodoWorkspaceAgent) => JSON.stringify([item.machineId, item.sessionId]) === identity);
        if (this.targetCurrent(todo, generation, scope) && selected.id === target && latest?.isActive())
          this.workspace.unlinkAgent(selected, latest).catch(() => undefined);
      } } });
  }

  private confirmLinkSpace(todo: TodoWorkspaceItem): void {
    const choice = this.selectedSpace();
    if (!choice) return;
    const target = todo.id, key = this.choiceKey(choice), generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('todo_link_project'), message: todo.title + '\n' + choice.label,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_link_project'), action: () => {
        if (!this.targetCurrent(todo, generation, scope) || this.workspace.selected.id !== target || this.choiceKey(choice) !== key ||
          !this.workspace.spaceChoices(todo).some((item: TodoWorkspaceSpaceChoice) => this.choiceKey(item) === key)) return;
        this.workspace.linkSpace(todo, choice).catch(() => undefined);
      } } });
  }

  private confirmUnlinkSpace(todo: TodoWorkspaceItem, space: TodoWorkspaceSpace): void {
    const identity = JSON.stringify([space.machineId, space.space]);
    const currentSpace = todo.spaces.find((item: TodoWorkspaceSpace) => JSON.stringify([item.machineId, item.space]) === identity);
    if (!currentSpace) return;
    const target = todo.id, generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('todo_remove_project'), message: todo.title + '\n' +
      currentSpace.space + ' · ' + this.workspace.machineName(currentSpace.machineId),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_remove_project'), action: () => {
        const selected = this.workspace.selected;
        const latest = selected.spaces.find((item: TodoWorkspaceSpace) => JSON.stringify([item.machineId, item.space]) === identity);
        if (this.targetCurrent(todo, generation, scope) && selected.id === target && latest)
          this.workspace.unlinkSpace(selected, latest).catch(() => undefined);
      } } });
  }

  @Builder advancedMenu() {
    Menu() {
      MenuItem({ content: tr('new_agent') }).enabled(!this.workspace.saving && !this.workspace.targetGone)
        .onClick(() => this.onCreateAgent(this.workspace.todoResource(this.workspace.selected)))
    }
  }
  @Builder todoMenu(todo: TodoWorkspaceItem) {
    Menu() {
      MenuItem({ content: tr('delete') }).enabled(!this.workspace.targetGone).onClick(() => this.confirmDelete(todo))
    }
  }

  @Builder todoRow(binding: BoundRow<TodoWorkspaceGroup>) {
    ListItem() {
      ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.lead.title,
        secondaryText: this.todoSubtitle(binding.value) }) })
    }.onClick(() => this.onOpenResource(this.workspace.todoResource(binding.value.lead)))
      .id('todo.' + binding.value.lead.manager + '.' + binding.value.lead.id)
  }

  @Builder sectionHeader(binding: BoundTodoSection) {
    Text(this.sectionTitle(binding.value))
      .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      .padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.fine })
  }

  @Builder listContent() {
    Column() {
      Row({ space: Tokens.gap }) {
        Search({ value: this.workspace.search, placeholder: tr('todo_search') })
          .onChange((value: string) => this.workspace.setSearch(value)).layoutWeight(1).id('todos.search')
        Button(tr('add')).onClick(() => this.workspace.startCreate()).height(Tokens.touch).id('todos.add')
      }.padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.inset, bottom: Tokens.gap })
      Row({ space: Tokens.gap }) {
        Select(this.statusOptions()).selected(this.statusIndex()).value(this.statusOptions()[this.statusIndex()].value)
          .onSelect((index: number) => this.workspace.setStatusFilter(['all', 'needs_you', 'checking', 'working', 'followup', 'not_started', 'done'][index]))
          .layoutWeight(1).accessibilityText(tr('todo_filter_status')).id('todos.filter')
        Select(this.groupOptions()).selected(Math.max(0, this.groupIndex())).value(this.groupOptions()[Math.max(0, this.groupIndex())].value)
          .onSelect((index: number) => this.workspace.setGroupBy(['status', 'project', 'thread'][index]))
          .layoutWeight(1).accessibilityText(tr('todo_group_by')).id('todos.group')
      }.padding({ left: Tokens.inset, right: Tokens.inset, bottom: Tokens.inset })
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta)
        .fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      List() {
        if (this.workspace.loading && !this.workspace.items.length) ListItem() { LoadingProgress().height(Tokens.touch).width(Tokens.touch).padding(Tokens.page) }
        ForEach(this.sectionBindings, (section: BoundTodoSection) => {
          ListItemGroup({ header: () => this.sectionHeader(section) }) {
            ForEach(section.rows, (binding: BoundRow<TodoWorkspaceGroup>) => this.todoRow(binding),
              (binding: BoundRow<TodoWorkspaceGroup>) => binding.key)
          }.id(section.key)
        }, (section: BoundTodoSection) => section.key)
        if (!this.sectionBindings.length && !this.workspace.loading) ListItem() {
          Text(tr(this.workspace.search ? 'no_results' : 'empty')).fontSize(Tokens.meta).padding(Tokens.page).width('100%')
        }
      }.width('100%').layoutWeight(1).padding({ bottom: Tokens.listTail })
    }.width('100%').height('100%')
  }

  @Builder agentRow(binding: BoundRow<TodoWorkspaceAgent>) {
    if (binding.value.isActive()) {
      ListItem() { this.agentContent(binding) }.onClick(() => this.onOpen(this.workspace.agentSession(binding.value)))
        .bindContextMenu(this.agentMenu(binding), ResponseType.LongPress)
        .id('todo.agent.' + binding.value.machineId + '.' + binding.value.sessionId)
    } else {
      ListItem() { this.agentContent(binding) }.onClick(() => this.onOpen(this.workspace.agentSession(binding.value)))
        .id('todo.agent.' + binding.value.machineId + '.' + binding.value.sessionId)
    }
  }

  @Builder agentContent(binding: BoundRow<TodoWorkspaceAgent>) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || tr('agent'),
      secondaryText: this.workspace.machineName(binding.value.machineId) + ' · ' +
        tr('todo_state_' + this.workspace.agentState(binding.value)) }) })
  }

  @Builder agentMenu(binding: BoundRow<TodoWorkspaceAgent>) {
    Menu() { MenuItem({ content: tr('todo_unlink_agent') }).enabled(!this.workspace.targetGone)
      .onClick(() => this.confirmUnlinkAgent(this.workspace.selected, binding.value)) }
  }

  @Builder spaceRow(binding: BoundRow<TodoWorkspaceSpace>) {
    ListItem() {
      ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.space,
        secondaryText: this.workspace.machineName(binding.value.machineId) }) })
    }.bindContextMenu(this.spaceMenu(binding), ResponseType.LongPress)
      .id('todo.space.' + binding.value.machineId + '.' + binding.value.space)
  }

  @Builder spaceMenu(binding: BoundRow<TodoWorkspaceSpace>) {
    Menu() { MenuItem({ content: tr('todo_remove_project') }).enabled(!this.workspace.targetGone)
      .onClick(() => this.confirmUnlinkSpace(this.workspace.selected, binding.value)) }
  }

  @Builder detailContent() {
    Column() {
      Scroll() {
        if (this.workspace.selected.id) Column({ space: Tokens.page }) {
          Text(tr(this.workspace.stateLabelKey(this.workspace.selected))).fontSize(Tokens.meta).width('100%')
            .bindContextMenu(this.todoMenu(this.workspace.selected), ResponseType.LongPress)
          if (this.workspace.selected.startError) Text(this.workspace.selected.startError).fontColor($r('app.color.persona_danger')).width('100%')
          if (this.workspace.selected.doneReason) Text(this.workspace.selected.doneReason).fontSize(Tokens.meta).width('100%')
          if (this.workspace.selected.notes) MarkdownText({ text: this.workspace.selected.notes, onLink: (address: string) => this.onLink(address) })

          if (this.workspace.selected.project) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: this.workspace.projectTitle(this.workspace.selected) }) }) }
              .onClick(() => this.openProject())
          }
          if (this.workspace.threadTitle(this.workspace.selected)) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: this.workspace.threadTitle(this.workspace.selected) }) }) }
              .onClick(() => this.openThread())
          }
          if (this.workspace.sourceResource(this.workspace.selected)) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('todo_source_conversation') }) }) }
              .onClick(() => this.openSource())
          }
          ForEach(this.lineageRowBindings, (binding: BoundRow<ResourceRow>) => {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || tr('todo_related') }) }) }
              .onClick(() => this.onOpenResource(binding.value))
          }, (binding: BoundRow<ResourceRow>) => binding.key)
          ForEach(this.relatedTodoBindings, (binding: BoundRow<TodoWorkspaceItem>) => {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title }) }) }
              .onClick(() => this.onOpenResource(this.workspace.todoResource(binding.value)))
          }, (binding: BoundRow<TodoWorkspaceItem>) => binding.key)

          if (this.spaceBindings.length) {
            Text(tr('todo_projects')).fontSize(Tokens.meta).width('100%')
            List() { ForEach(this.spaceBindings, (binding: BoundRow<TodoWorkspaceSpace>) => this.spaceRow(binding),
              (binding: BoundRow<TodoWorkspaceSpace>) => binding.key) }.height(Math.min(300, Tokens.touch * this.spaceBindings.length))
          }

          Text(tr('todo_agents')).fontSize(Tokens.meta).width('100%')
          ForEach(this.activeAgentBindings, (binding: BoundRow<TodoWorkspaceAgent>) => this.agentRow(binding),
            (binding: BoundRow<TodoWorkspaceAgent>) => binding.key)
          if (this.previousAgentBindings.length) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('todo_previously_involved') }) }) }
              .onClick(() => { this.previousAgentsOpen = !this.previousAgentsOpen; })
            if (this.previousAgentsOpen) {
              ForEach(this.previousAgentBindings, (binding: BoundRow<TodoWorkspaceAgent>) => this.agentRow(binding),
                (binding: BoundRow<TodoWorkspaceAgent>) => binding.key)
            }
          }

          if (!this.workspace.selected.isDone()) {
            if (this.workspace.candidateSessions().length) {
              Select(this.sessionOptions()).selected(Math.max(0, this.workspace.candidateSessions().findIndex((row: ResourceRow) => this.sessionKey(row) === this.workspace.selectedAgentKey)))
                .value(this.selectedSession()?.title || tr('todo_choose_agent'))
                .onSelect((index: number) => this.selectSession(index))
                .accessibilityText(tr('todo_choose_agent')).width('100%')
              Button(tr('todo_link_agent')).enabled(!!this.selectedSession() && !this.workspace.saving && !this.workspace.targetGone)
                .onClick(() => this.confirmLinkAgent(this.workspace.selected)).height(Tokens.touch).width('100%')
            }
            Button(tr('more'), { buttonStyle: ButtonStyleMode.TEXTUAL }).bindMenu(this.advancedMenu)
              .enabled(!this.workspace.saving && !this.workspace.targetGone).height(Tokens.touch)
            if (this.workspace.spaceChoices().length) {
              Select(this.spaceOptions()).selected(Math.max(0, this.workspace.spaceChoices().findIndex((item: TodoWorkspaceSpaceChoice) => this.choiceKey(item) === this.workspace.selectedSpaceKey)))
                .value(this.selectedSpace()?.label || tr('todo_choose_project'))
                .onSelect((index: number) => this.selectSpace(index))
                .accessibilityText(tr('todo_choose_project')).width('100%')
              Button(tr('todo_link_project')).enabled(!!this.selectedSpace() && !this.workspace.saving && !this.workspace.targetGone)
                .onClick(() => this.confirmLinkSpace(this.workspace.selected)).height(Tokens.touch).width('100%')
            }
          }
          if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta)
            .fontColor($r('app.color.persona_danger')).width('100%')
        }.alignItems(HorizontalAlign.Start).padding(Tokens.page).width('100%')
        else Text(tr(this.detailEmptyKey())).fontSize(Tokens.meta).padding(Tokens.page).width('100%')
      }.layoutWeight(1).width('100%')
      if (this.workspace.selected.id) Row({ space: Tokens.gap }) {
        Button(this.completionLabel()).enabled(!this.workspace.saving && !this.workspace.targetGone)
          .layoutWeight(1).height(Tokens.touch).onClick(() => this.changeCompletion())
        Button(tr('edit')).enabled(!this.workspace.saving && !this.workspace.targetGone).layoutWeight(1).height(Tokens.touch)
          .onClick(() => this.workspace.beginEdit(this.workspace.selected))
      }.padding(Tokens.inset).width('100%')
    }.width('100%').height('100%')
  }

  @Builder editor() {
    Column({ space: Tokens.page }) {
      TextInput({ text: this.workspace.titleDraft, placeholder: tr('title') }).onChange((value: string) => this.workspace.setTitleDraft(value))
        .id('todos.title')
      TextArea({ text: this.workspace.notesDraft, placeholder: tr('todo_notes') }).onChange((value: string) => this.workspace.setNotesDraft(value))
        .height(Tokens.inputExpanded).id('todos.notes')
      if (this.workspace.error) Text(tr(this.workspace.error)).fontColor($r('app.color.persona_danger')).width('100%')
      Row({ space: Tokens.gap }) {
        Button(tr('cancel')).layoutWeight(1).height(Tokens.touch).onClick(() => this.workspace.closeEditor())
        Button(tr('save')).layoutWeight(1).height(Tokens.touch)
          .enabled(!this.workspace.saving && !this.workspace.targetGone && !!this.workspace.titleDraft.trim())
          .onClick(() => this.workspace.save().catch(() => undefined)).id('todos.save')
      }.width('100%')
    }.padding(Tokens.page).width('100%')
  }

  build() {
    Column() {
      if (this.list) this.listContent()
      else this.detailContent()
    }.width('100%').height('100%')
      .bindSheet($$this.workspace.editorOpen, this.editor(), { title: { title: tr(this.workspace.selected.id ? 'edit' : 'create') }, height: SheetSize.LARGE, showClose: true })
  }
}
