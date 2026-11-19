import type * as Wire from "./wire.ts";

/** Extension actions retain native confirmation/submit/navigation metadata. */
export type ActionOptions = Omit<Wire.Action, "id" | "reply" | "open" | "pop" | "push"> & {
  push?: Omit<Wire.Push, "page">;
};
export type NativeAction = ActionOptions & Pick<Wire.Action, "id">;
export type Values = Readonly<Record<string, string>>;
export interface ActionContext { readonly signal: AbortSignal }
export const callbackTag: unique symbol = Symbol("callback");
export interface CallbackAction {
  readonly [callbackTag]: true;
  readonly options: ActionOptions;
  readonly invoke: (values: unknown, context: ActionContext) => unknown | Promise<unknown>;
}

function stringValues(value: unknown): Values {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid action values");
  const entries = Object.entries(value);
  if (entries.length > 128 || entries.some(([key, item]) => key.length > 256 || typeof item !== "string" || item.length > 16384)) {
    throw new Error("invalid action values");
  }
  return Object.freeze(Object.fromEntries(entries)) as Values;
}

/** Register server code, not approval. The host must still enforce all native write gates. */
export function callback(fn: (values: Values, context: ActionContext) => unknown | Promise<unknown>, options?: ActionOptions): CallbackAction;
export function callback<T>(fn: (values: T, context: ActionContext) => unknown | Promise<unknown>, options: ActionOptions, parse: (value: unknown) => T): CallbackAction;
export function callback<T = Values>(
  fn: (values: T, context: ActionContext) => unknown | Promise<unknown>,
  options: ActionOptions = {},
  parse: (value: unknown) => T = stringValues as (value: unknown) => T,
): CallbackAction {
  return Object.freeze({ [callbackTag]: true as const, options, invoke: (value: unknown, context: ActionContext) => fn(parse(value), context) });
}

/** Derive every native prop, including nested action slots, from generated wire types. */
export type UIProps<T> = T extends Wire.Action ? NativeAction | CallbackAction
  : T extends readonly (infer Item)[] ? UIProps<Item>[]
  : T extends object ? { [K in keyof T]: UIProps<T[K]> }
  : T;
export type NativeType = Exclude<Wire.Component["type"], "html">;
export type ComponentProps<T extends NativeType> = UIProps<Omit<Extract<Wire.Component, { type: T }>, "type" | "key" | (T extends "grid" | "carousel" ? "tiles" : never)>> & { key: string }
  & (T extends "grid" | "carousel" ? { tiles: UIProps<Wire.Tile & { key: string }>[] } : {});
export type ViewProps = UIProps<Omit<Wire.View, "sections" | "pages" | "fallback">> & { key: string; children?: Node };
export type SectionProps = UIProps<Omit<Wire.Section, "items" | "key">> & { key: string; children?: Node };

export const elementTag: unique symbol = Symbol("element");
export const nativeTag: unique symbol = Symbol("native");
export type FunctionComponent<P = any> = (props: P) => Node;
export interface Element {
  readonly [elementTag]: true;
  readonly type: FunctionComponent | typeof Fragment;
  readonly key: string | undefined;
  readonly props: Readonly<Record<string, unknown>>;
}
export type Node = Element | readonly Node[] | null | undefined | false;
export const Fragment: FunctionComponent<{ children?: Node }> = ({ children }) => children;
export type NativeComponent<P> = ((props: P) => Element) & { readonly [nativeTag]: NativeType | "view" | "section" };

export function createElement(type: Element["type"], props: Record<string, unknown> | null, key?: string): Element {
  if (typeof type !== "function" && type !== Fragment) throw new Error("only registered native or function components are supported");
  const { key: propKey, ...rest } = props ?? {};
  const resolvedKey = key ?? propKey;
  if (resolvedKey !== undefined && (typeof resolvedKey !== "string" || !resolvedKey || resolvedKey.length > 256)) {
    throw new Error("UI keys must be nonempty strings of at most 256 characters");
  }
  return Object.freeze({ [elementTag]: true as const, type, props: Object.freeze(rest), key: resolvedKey as string | undefined });
}

function native<P>(kind: NativeType | "view" | "section"): NativeComponent<P> {
  const component = ((props: P) => createElement(component, props as Record<string, unknown>)) as NativeComponent<P>;
  Object.defineProperty(component, nativeTag, { value: kind });
  return component;
}
export const View = native<ViewProps>("view");
export const Section = native<SectionProps>("section");
export const Text = native<ComponentProps<"text">>("text");
export const Metric = native<ComponentProps<"metric">>("metric");
export const Progress = native<ComponentProps<"progress">>("progress");
export const Row = native<ComponentProps<"row">>("row");
export const Button = native<ComponentProps<"button">>("button");
export const Link = native<ComponentProps<"link">>("link");
export const Field = native<ComponentProps<"field">>("field");
export const Divider = native<ComponentProps<"divider">>("divider");
export const Chart = native<ComponentProps<"chart">>("chart");
export const StatGrid = native<ComponentProps<"stat_grid">>("stat_grid");
export const Segmented = native<ComponentProps<"segmented">>("segmented");
export const Markdown = native<ComponentProps<"markdown">>("markdown");
export const Table = native<ComponentProps<"table">>("table");
export const Empty = native<ComponentProps<"empty">>("empty");
export const Loading = native<ComponentProps<"loading">>("loading");
export const Header = native<ComponentProps<"header">>("header");
export const Breadcrumb = native<ComponentProps<"breadcrumb">>("breadcrumb");
export const FileTree = native<ComponentProps<"file_tree">>("file_tree");
export const Code = native<ComponentProps<"code">>("code");
export const Diff = native<ComponentProps<"diff">>("diff");
export const CommentThread = native<ComponentProps<"comment_thread">>("comment_thread");
export const Composer = native<ComponentProps<"composer">>("composer");
export const Image = native<ComponentProps<"image">>("image");
export const Grid = native<ComponentProps<"grid">>("grid");
export const Carousel = native<ComponentProps<"carousel">>("carousel");
/** A graph's node (UI version 10). It carries an id, so UIProps would take it for an action: its fields are mapped here. */
export type GraphNodeProps = Omit<Wire.GraphNode, "action"> & { action?: NativeAction | CallbackAction };
export type GraphProps = Omit<ComponentProps<"graph">, "nodes"> & { nodes: GraphNodeProps[] };
export const Graph = native<GraphProps>("graph");
