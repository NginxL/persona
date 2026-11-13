import { ResourceRow, Wire } from './Wire';

/** A destination retains the target shown when it was opened, independently of the current tab selection. */
export class NavigationContext {
  page: string = '';
  row: ResourceRow = new ResourceRow();
  scoped: boolean = false;
  static rowFrom(value: string): ResourceRow {
    const data = Wire.parse(value), row = new ResourceRow(Wire.text(data, 'id'), Wire.text(data, 'title'), Wire.text(data, 'kind'));
    row.group = Wire.text(data, 'group'); row.manager = Wire.text(data, 'manager'); row.machine = Wire.text(data, 'machine'); row.session = Wire.text(data, 'session');
    row.state = Wire.text(data, 'state'); row.raw = Wire.text(data, 'raw', '{}'); row.path = Wire.text(data, 'path'); row.detail = Wire.text(data, 'detail'); return row;
  }
  static from(name: string, param: string): NavigationContext {
    const result = new NavigationContext();
    if (name === 'settings') {
      if (param.startsWith('{')) {
        const data = Wire.parse(param); result.page = Wire.text(data, 'page');
        const target = Wire.text(data, 'target'); if (target) { result.row = NavigationContext.rowFrom(target); result.scoped = !!result.row.id; }
      } else result.page = param;
    } else if (['session', 'conversation', 'resource', 'tools', 'history', 'inbox', 'todo', 'knowledge', 'file', 'todo_source', 'todo_thread', 'computer', 'extension'].includes(name)) {
      result.row = NavigationContext.rowFrom(param); result.scoped = !!result.row.id;
    }
    return result;
  }
  static settings(page: string, row?: ResourceRow): string {
    return row ? JSON.stringify(Wire.payload(['page', 'target'], [page, JSON.stringify(row)])) : page;
  }
  static destination(row: ResourceRow): string {
    return ['session', 'conversation', 'inbox', 'todo', 'knowledge', 'file', 'todo_source', 'todo_thread'].includes(row.kind) ? row.kind : 'resource';
  }
}
