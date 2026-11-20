/** @jsxImportSource @persona/extension */
import { describe, expect, test } from "bun:test";
import {
  Button, Grid, Graph, Section, Text, View, callback, createElement, mount, useEffect, useFetch, useInterval, useState,
  type TimerAdapter, type Renderer, type UIEvent,
} from "./ui.ts";

type StateUpdate<T> = T | ((previous: T) => T);
const allow = { authorize: () => true };
const settle = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
function event(session: Renderer, key = "press", eventId: string = crypto.randomUUID(), value: unknown = {}): UIEvent {
  const item = session.current().view.sections.flatMap(section => section.items).find(item => item.key === key) as { action: { id: string } };
  return { generation: session.generation, version: session.current().version, eventId, key, actionId: item.action.id, values: value };
}
function text(session: Renderer, key = "value"): string {
  return (session.current().view.sections.flatMap(section => section.items).find(item => item.key === key) as { text: string }).text;
}
class Clock implements TimerAdapter {
  active = new Set<() => void>(); starts = 0; stops = 0;
  setInterval(tick: () => void) { this.starts++; this.active.add(tick); return tick; }
  clearInterval(handle: unknown) { this.stops++; this.active.delete(handle as () => void); }
  tick() { for (const tick of [...this.active]) tick(); }
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { resolve, reject, promise };
}

describe("server JSX reconciler", () => {
  test("coalesces state updates and preserves all page/action/section metadata", async () => {
    let set!: (update: StateUpdate<number>) => void;
    const frames: number[] = [];
    function Page() {
      const [value, update] = useState(0); set = update;
      return <View key="root" title="fixture" state="state" layout="split" refresh_after={30}
        search={{ placeholder: "fixture", action: callback(() => {}) }}
        filter={{ name: "f", value: "a", choices: [{ value: "a", title: "fixture" }], action: callback(() => {}) }}
        tabs={{ name: "t", value: "a", items: [{ value: "a", title: "fixture" }], action: callback(() => {}) }}
        review={{ count: 1, name: "r", buttons: [{ title: "fixture", action: callback(() => {}) }] }}>
        <Section key="main" title="fixture" footer="fixture" pane="side" more={{ title: "fixture", action: callback(() => {}) }}>
          <Text key="value" text={String(value)} />
          <Button key="press" title="fixture" role="destructive" action={callback(() => update(v => v + 1), { confirm: "fixture", submit: true, values: { key: "saved" } })} />
        </Section>
      </View>;
    }
    const session = mount(<Page key="page" />, { ...allow, onRender: frame => frames.push(frame.version) });
    set(v => v + 1); set(v => v + 1); set(v => v + 1);
    await settle();
    expect(text(session)).toBe("3"); expect(frames).toEqual([1, 2]);
    const view = session.current().view;
    expect(view).toMatchObject({ title: "fixture", state: "state", layout: "split", refresh_after: 30, filter: { name: "f" }, tabs: { name: "t" }, review: { count: 1 } });
    expect(view.sections[0]).toMatchObject({ key: "main", pane: "side", title: "fixture", footer: "fixture" });
    const action = (view.sections[0].items[1] as { action: unknown }).action;
    expect(action).toMatchObject({ confirm: "fixture", submit: true, values: { key: "saved" } });
    const serialized = JSON.stringify(session.current());
    expect(serialized).not.toContain("invoke"); expect(serialized).not.toContain("callbackBrand");
    await settle(); const last = session.current(); session.dispose(); set(8); await settle(); expect((last.view.sections[0].items[0] as { text: string }).text).toBe("3");
    expect(() => session.current()).toThrow("not mounted");
  });

  test("keyed reorders preserve nested state and removals clean effects and stale setters", async () => {
    const setters = new Map<string, (value: StateUpdate<number>) => void>();
    const cleanups: string[] = [];
    function Counter({ id }: { id: string }) {
      const [count, set] = useState(0); setters.set(id, set);
      useEffect(() => () => { cleanups.push(id); }, []);
      return <Text key={`text-${id}`} text={String(count)} />;
    }
    function Wrapper({ id }: { id: string }) { return <Counter key="counter" id={id} />; }
    function Page({ ids }: { ids: string[] }) { return <View key="root"><Section key="main">{ids.map(id => <Wrapper key={id} id={id} />)}</Section></View>; }
    const session = mount(<Page key="page" ids={["a", "b"]} />, allow);
    setters.get("a")!(4); await settle();
    session.render(<Page key="page" ids={["b", "a"]} />);
    expect(session.current().view.sections[0].items.map(item => item.key)).toEqual(["text-b", "text-a"]);
    expect(text(session, "text-a")).toBe("4"); expect(cleanups).toEqual([]);
    const old = setters.get("a")!;
    session.render(<Page key="page" ids={["b"]} />); expect(cleanups).toEqual(["a"]);
    const version = session.current().version; old(10); await settle(); expect(session.current().version).toBe(version);
    session.render(<Page key="page" ids={["a", "b"]} />); expect(text(session, "text-a")).toBe("0");
    session.dispose(); expect(cleanups).toEqual(["a", "b", "a"]);
  });

  test("same key with another function component resets state and cleans up", () => {
    let cleanup = 0;
    function First() { useEffect(() => () => { cleanup++; }, []); return <Text key="value" text="first" />; }
    function Second() { return <Text key="value" text="second" />; }
    const page = (second: boolean) => <View key="root"><Section key="main">{second ? <Second key="slot" /> : <First key="slot" />}</Section></View>;
    const session = mount(page(false), allow); session.render(page(true));
    expect(cleanup).toBe(1); expect(text(session)).toBe("second"); session.dispose();
  });

  test("conditional hook count and hook kind changes fail closed", () => {
    let cleanup = 0;
    function Conditional({ extra }: { extra: boolean }) {
      useEffect(() => () => { cleanup++; }, []);
      if (extra) useState(0);
      return <View key="root" />;
    }
    const session = mount(<Conditional key="page" extra={false} />, allow);
    expect(() => session.render(<Conditional key="page" extra={true} />)).toThrow("hook order");
    expect(session.disposed).toBe(true); expect(cleanup).toBe(1);
    const fewer = mount(<Conditional key="page" extra={true} />, allow);
    expect(() => fewer.render(<Conditional key="page" extra={false} />)).toThrow("hook order");
    function Changed({ effect }: { effect: boolean }) {
      if (effect) useEffect(() => {}, []); else useState(0);
      return <View key="root" />;
    }
    const changed = mount(<Changed key="page" effect={false} />, allow);
    expect(() => changed.render(<Changed key="page" effect={true} />)).toThrow("hook order");
    expect(() => useState(0)).toThrow("mounted function component");
  });

  test("duplicates, invalid nesting, text/DOM, unregistered functions in props and limits reject", () => {
    expect(() => mount(<View key="root"><Section key="root" /></View>, allow)).toThrow("UI key");
    expect(() => mount(<View key="root"><Text key="value" text="fixture" /></View>, allow)).toThrow("nesting");
    expect(() => mount(createElement("div" as never, {}), allow)).toThrow("function");
    expect(() => mount(<View key="root">{"text" as never}</View>, allow)).toThrow("only SDK elements");
    expect(() => mount(<View key="root"><Section key="main"><Button key="press" title="fixture" action={(() => {}) as never} /></Section></View>, allow)).toThrow("native JSON props");
    expect(() => mount(<View key="root"><Section key="main" /></View>, { ...allow, limits: { nodes: 1 } })).toThrow("node limit");
    expect(() => mount(<View key="root"><Section key="main" /></View>, { ...allow, limits: { depth: 1 } })).toThrow("depth");
    expect(() => mount(<View key="root" />, { ...allow, limits: { nodes: Infinity } })).toThrow("invalid renderer limit");
  });

  test("grid tiles are keyed and callbacks bind to the tile, not its container", async () => {
    let calls = 0;
    const session = mount(<View key="root"><Section key="main"><Grid key="grid" tiles={[{ key: "tile", title: "fixture", action: callback(() => { calls++; }) }]} /></Section></View>, allow);
    const tile = (session.current().view.sections[0].items[0] as { tiles: { action: { id: string } }[] }).tiles[0];
    await session.event({ generation: session.generation, version: session.current().version, eventId: "event", actionId: tile.action.id, key: "tile", values: {} });
    expect(calls).toBe(1); session.dispose();
    expect(() => mount(<View key="root"><Section key="main"><Grid key="grid" tiles={[{ title: "fixture" } as never]} /></Section></View>, allow)).toThrow("UI key");
  });

  test("graph nodes lower with their node fields and their own callbacks", async () => {
    let calls = 0;
    const session = mount(<View key="root"><Section key="main"><Graph key="graph" nodes={[{ id: "a", title: "fixture", progress: 0.5, action: callback(() => { calls++; }) }, { id: "b", title: "other" }]} edges={[{ from: "a", to: "b" }]} folded={["a"]} /></Section></View>, allow);
    const graph = session.current().view.sections[0].items[0] as { type: string; nodes: { id: string; progress?: number; action: { id: string } }[]; edges: { from: string; to: string }[]; folded: string[] };
    expect(graph.type).toBe("graph");
    expect(graph.nodes[0].progress).toBe(0.5);
    expect(graph.folded).toEqual(["a"]);
    await session.event({ generation: session.generation, version: session.current().version, eventId: "event", actionId: graph.nodes[0].action.id, key: "graph", values: {} });
    expect(calls).toBe(1); session.dispose();
  });

  test("independent mounted sessions do not share state, callbacks, generations or cleanup", async () => {
    const setters: ((update: StateUpdate<number>) => void)[] = [];
    let cleanups = 0;
    function Counter() { const [count, set] = useState(0); setters.push(set); useEffect(() => () => { cleanups++; }, []); return <View key="root"><Section key="main"><Text key="value" text={String(count)} /><Button key="press" title="fixture" action={callback(() => set(v => v + 1))} /></Section></View>; }
    const a = mount(<Counter key="counter" />, allow); const b = mount(<Counter key="counter" />, allow);
    const request = event(a);
    await expect(b.event(request)).rejects.toThrow("stale");
    setters[0](3); await settle(); expect(text(a)).toBe("3"); expect(text(b)).toBe("0");
    a.dispose(); expect(cleanups).toBe(1); await b.event(event(b)); await settle(); expect(text(b)).toBe("1"); b.dispose(); expect(cleanups).toBe(2);
  });
});

describe("server callback registry", () => {
  const page = (action = callback(() => {})) => <View key="root"><Section key="main"><Button key="press" title="fixture" action={action} /></Section></View>;
  test("rejects stale version/ID/key/generation, malformed values and disposed sessions", async () => {
    let calls = 0;
    const session = mount(page(callback(() => { calls++; })), allow);
    const old = event(session);
    expect(old.actionId).toMatch(/^cb\.[0-9a-f-]{36}$/);
    await expect(session.event({ ...old, key: "elsewhere" })).rejects.toThrow("unknown or stale callback");
    await expect(session.event({ ...old, actionId: "guess" })).rejects.toThrow("unknown or stale callback");
    await expect(session.event({ ...old, eventId: "bad-values", values: { n: 1 } })).rejects.toThrow("invalid action values");
    expect(calls).toBe(0);
    session.render(); await expect(session.event(old)).rejects.toThrow("stale");
    const current = event(session); await session.event(current); expect(calls).toBe(1);
    session.suspend(); await expect(session.event(current)).rejects.toThrow("inactive"); session.resume();
    session.dispose(); await expect(session.event(current)).rejects.toThrow("inactive");
    await expect(session.event({ ...current, generation: "old" })).rejects.toThrow("inactive");
  });

  test("events cannot approve native gates, and authorization is rechecked after awaiting", async () => {
    let calls = 0; let saved: unknown;
    const action = callback(() => { calls++; }, { confirm: "fixture", submit: true });
    const denied = mount(page(action), { authorize: (wireAction) => { saved = wireAction; return false; } });
    await expect(denied.event(event(denied))).rejects.toThrow("gate not satisfied");
    expect(saved).toMatchObject({ confirm: "fixture", submit: true }); expect(calls).toBe(0); denied.dispose();
    const gate = deferred<boolean>();
    const session = mount(page(action), { authorize: () => gate.promise });
    const pending = session.event(event(session)); await settle(); session.suspend(); gate.resolve(true);
    await expect(pending).rejects.toThrow("stale"); expect(calls).toBe(0); session.dispose();
  });

  test("deduplicates accepted events, rejects changed retries, bounded overflow and concurrent writes", async () => {
    const work = deferred<void>(); let calls = 0;
    const session = mount(page(callback(async () => { calls++; await work.promise; })), { ...allow, limits: { events: 1 } });
    const request = event(session, "press", "same");
    const pending = session.event(request);
    expect(session.event({ ...request })).toBe(pending);
    await expect(session.event({ ...request, values: { q: "changed" } })).rejects.toThrow("body mismatch");
    await settle(); expect(calls).toBe(1);
    await expect(session.event({ ...request, eventId: "another" })).rejects.toThrow("already running");
    work.resolve(); await pending;
    await expect(session.event({ ...request, eventId: "another" })).rejects.toThrow("stale");
    expect(session.event(request)).toBe(pending); session.dispose();
  });

  test("event input is cloned and custom parsers validate typed data", async () => {
    let value = 0;
    const action = callback<number>(n => { value = n; }, {}, data => { if (typeof data !== "number") throw new Error("expected number"); return data; });
    const gate = deferred<boolean>();
    const session = mount(page(action), { authorize: () => gate.promise });
    const request = event(session, "press", "number", 4);
    const result = session.event(request); request.values = 8; gate.resolve(true); await result;
    expect(value).toBe(4);
    await expect(session.event(event(session, "press", "bad", "4"))).rejects.toThrow("expected number"); session.dispose();
  });
});

describe("server hook effects and visibility", () => {
  test("dependency changes/unmount/suspend/dispose clean once, resume runs after snapshot", () => {
    const history: string[] = [];
    function Page({ n }: { n: number }) { useEffect(signal => { history.push(`start:${n}`); return () => { expect(signal.aborted).toBe(true); history.push(`stop:${n}`); }; }, [n]); return <View key="root" />; }
    const session = mount(<Page key="page" n={1} />, { ...allow, onRender: () => history.push("snapshot") });
    session.render(); expect(history).toEqual(["snapshot", "start:1", "snapshot"]);
    session.render(<Page key="page" n={2} />);
    session.suspend(); session.suspend(); session.resume(); session.dispose(); session.dispose();
    expect(history).toEqual(["snapshot", "start:1", "snapshot", "stop:1", "snapshot", "start:2", "stop:2", "snapshot", "start:2", "stop:2"]);
  });

  test("pending effect cancellation invokes late cleanup and reports async/cleanup errors", async () => {
    const work = deferred<() => void>(); const failure = deferred<void>();
    let signal!: AbortSignal; let cleaned = 0;
    function Page() { useEffect(s => { signal = s; return work.promise; }, []); useEffect(() => failure.promise, []); return <View key="root" />; }
    const errors: unknown[] = [];
    const session = mount(<Page key="page" />, { ...allow, onError: error => errors.push(error) });
    session.dispose(); expect(signal.aborted).toBe(true);
    work.resolve(() => { cleaned++; throw new Error("cleanup failed"); }); failure.reject(new Error("pending failed")); await settle();
    expect(cleaned).toBe(1); expect(errors.map(error => (error as Error).message).sort()).toEqual(["cleanup failed", "pending failed"]);
    expect(session.takeErrors()).toHaveLength(2);
  });

  test("fetch aborts old work and rejects late results after replacement, suspension and dispose", async () => {
    const requests: { url: string; signal: AbortSignal; result: ReturnType<typeof deferred<Response>> }[] = [];
    const fetch = (url: string, init: RequestInit & { signal: AbortSignal }) => { const result = deferred<Response>(); requests.push({ url, signal: init.signal, result }); return result.promise; };
    function Page({ url }: { url: string }) { const result = useFetch<{ n: number }>(url); return <View key="root"><Section key="main"><Text key="value" text={result.data ? String(result.data.n) : result.status === "loading" ? "pending" : "error"} /></Section></View>; }
    const session = mount(<Page key="page" url="https://example.com/a" />, { ...allow, fetch });
    session.render(<Page key="page" url="https://example.com/b" />); expect(requests[0].signal.aborted).toBe(true);
    requests[0].result.resolve(Response.json({ n: 1 })); requests[1].result.resolve(Response.json({ n: 2 })); await settle(); expect(text(session)).toBe("2");
    session.suspend(); const before = session.current().version; session.resume(); expect(requests).toHaveLength(3);
    session.suspend(); expect(requests[2].signal.aborted).toBe(true); requests[2].result.resolve(Response.json({ n: 3 })); await settle(); expect(text(session)).toBe("2");
    expect(session.current().version).toBe(before + 1); session.resume(); const last = session.current(); session.dispose(); requests[3].result.resolve(Response.json({ n: 4 })); await settle(); expect((last.view.sections[0].items[0] as { text: string }).text).toBe("pending");
    expect(() => session.current()).toThrow("not mounted");
  });

  test("missing fetch adapter fails closed and never calls platform fetch", async () => {
    function Page() { const result = useFetch("https://example.com/denied"); return <View key="root"><Section key="main"><Text key="value" text={result.error ? (result.error as Error).message : "pending"} /></Section></View>; }
    const session = mount(<Page key="page" />, allow); await settle(); expect(text(session)).toBe("no sanctioned fetch adapter"); session.dispose();
  });

  test("fetch failure is state and a canceled fetch rejection does not replace newer data", async () => {
    const results = [deferred<Response>(), deferred<Response>()]; let index = 0;
    function Page({ url }: { url: string }) { const result = useFetch(url); return <View key="root"><Section key="main"><Text key="value" text={result.error ? "error" : result.data ? String(result.data) : "pending"} /></Section></View>; }
    const session = mount(<Page key="page" url="https://example.com/a" />, { ...allow, fetch: () => results[index++].promise });
    session.render(<Page key="page" url="https://example.com/b" />); results[0].reject(new Error("aborted")); results[1].resolve(new Response("no", { status: 503 })); await settle(); expect(text(session)).toBe("error"); session.dispose();
  });

  test("intervals keep their latest callback, pause completely while hidden, and resume without backlog", async () => {
    const clock = new Clock();
    function Page() { const [n, set] = useState(0); useInterval(() => set(n + 1), 100); return <View key="root"><Section key="main"><Text key="value" text={String(n)} /></Section></View>; }
    const session = mount(<Page key="page" />, { ...allow, timers: clock });
    clock.tick(); await settle(); clock.tick(); await settle(); expect(text(session)).toBe("2"); expect(clock.starts).toBe(1);
    session.suspend(); expect(clock.active.size).toBe(0); clock.tick(); await settle(); expect(text(session)).toBe("2");
    session.resume(); expect(clock.starts).toBe(2); clock.tick(); await settle(); expect(text(session)).toBe("3");
    session.dispose(); expect(clock.active.size).toBe(0); expect(clock.stops).toBe(2);
  });

  test("queued state updates while hidden emit only a fresh resume snapshot", async () => {
    let set!: (value: StateUpdate<number>) => void; let frames = 0;
    function Page() { const [n, update] = useState(0); set = update; return <View key="root"><Section key="main"><Text key="value" text={String(n)} /></Section></View>; }
    const session = mount(<Page key="page" />, { ...allow, onRender: () => { frames++; } });
    set(1); session.suspend(); set(2); set(3); await settle(); expect(frames).toBe(1);
    session.resume(); await settle(); expect(frames).toBe(2); expect(text(session)).toBe("3"); session.dispose();
  });
});
