// Trusted Deno transport. Extension code exports a Program; it never supplies this adapter.
import { connect, type Program } from "../../../../sdk/ts/src/index.ts";

const nativeFetch = globalThis.fetch.bind(globalThis);
const exit = Deno.exit.bind(Deno);
const stdout = Deno.stdout;
const stdin = Deno.stdin;
const encoder = new TextEncoder();
const shutdown = new AbortController();
const proxy = Deno.args[1];
const MAX_BODY = 8 << 20;
const MAX_FRAME = 8 << 20;
let pendingFetches = 0;

async function boundedBody(stream: ReadableStream<Uint8Array> | null, signal: AbortSignal): Promise<Uint8Array> {
  signal.throwIfAborted();
  if (!stream) return new Uint8Array();
  const reader = stream.getReader();
  const abort = () => { void reader.cancel(signal.reason).catch(() => {}); };
  signal.addEventListener("abort", abort, { once: true });
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) { signal.throwIfAborted(); break; }
      signal.throwIfAborted();
      size += value.length;
      if (size > MAX_BODY) throw new Error("fetch body exceeds 8 MiB");
      chunks.push(value);
    }
  } finally {
    signal.removeEventListener("abort", abort);
    // A guest stream's cancellation hook may never settle. Release admission
    // without awaiting guest-controlled cleanup after rejection or cancellation.
    void reader.cancel().catch(() => {});
  }
  const result = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) { result.set(chunk, at); at += chunk.length; }
  return result;
}

// The check SDK marks only its own requests with the check's proxy URL. The broker address stays fixed; only its
// credential crosses this outer request, never in guest JSON or in the upstream request the broker makes.
function settingsCheckAuthorization(options?: RequestInit): string {
  const proxy = (options as (RequestInit & { proxy?: unknown }) | undefined)?.proxy;
  if (proxy == null || proxy === "") return "";
  if (typeof proxy !== "string") throw new Error("invalid settings check proxy");
  const u = new URL(proxy);
  if (u.protocol !== "http:" || u.username !== "ext" || !u.password) throw new Error("invalid settings check proxy");
  return "Basic " + btoa(`${u.username}:${u.password}`);
}

// Deno checks fetch TARGET permissions even with a proxy-aware HttpClient. Therefore
// only the registered loopback broker is permitted, and it invokes existing egress policy.
Object.defineProperty(globalThis, "fetch", { configurable: false, writable: false,
  value: async (input: RequestInfo | URL, options?: RequestInit): Promise<Response> => {
    const authorization = settingsCheckAuthorization(options);
    const brokerHeaders: Record<string, string> = { "Content-Type": "application/json" };
    if (authorization) brokerHeaders["Proxy-Authorization"] = authorization;
    if (++pendingFetches > 64) { pendingFetches--; throw new Error("too many pending fetches"); }
    try {
      let request = new Request(input, options);
      const signal = AbortSignal.any([shutdown.signal, request.signal, AbortSignal.timeout(120000)]);
      const bytes = await boundedBody(request.body, signal);
      let encoded = "";
      for (let at = 0; at < bytes.length; at += 8192) encoded += String.fromCharCode(...bytes.subarray(at, at + 8192));
      let body = btoa(encoded);
      for (let redirect = 0; redirect <= 20; redirect++) {
        const url = new URL(request.url);
        if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw new Error("invalid fetch target");
        const response = await nativeFetch(proxy + "/__persona_runtime_fetch", {
          method: "POST", signal, redirect: "manual", headers: brokerHeaders,
          body: JSON.stringify({ url: url.href, method: request.method, headers: Object.fromEntries(request.headers), body }),
        });
        if ([301,302,303,307,308].includes(response.status) && response.headers.has("location")) {
          if (request.redirect === "error") { await response.body?.cancel(); throw new Error("redirect forbidden"); }
          if (request.redirect === "follow") {
            await response.body?.cancel();
            if (redirect === 20) throw new Error("too many redirects");
            const next = new URL(response.headers.get("location")!, url);
            const headers = new Headers(request.headers);
            if (next.origin !== url.origin) { headers.delete("authorization"); headers.delete("cookie"); }
            let method = request.method;
            if (response.status === 303 && method !== "HEAD" || [301,302].includes(response.status) && method === "POST") {
              method = "GET"; body = "";
              for (const name of ["content-encoding", "content-language", "content-location", "content-type", "content-length"]) headers.delete(name);
            }
            request = new Request(next, { method, headers, redirect: request.redirect, signal });
            continue;
          }
        }
        const headers = new Headers(response.headers);
        const headLength = headers.get("x-persona-runtime-head-content-length");
        headers.delete("x-persona-runtime-head-content-length");
        if (request.method === "HEAD") {
          if (headLength !== null) headers.set("content-length", headLength);
          await response.body?.cancel();
        }
        const result = new Response(request.method === "HEAD" ? null : response.body, { status: response.status, statusText: response.statusText, headers });
        Object.defineProperties(result, { url: { value: url.href }, redirected: { value: redirect > 0 } });
        return result;
      }
      throw new Error("too many redirects");
    } finally { pendingFetches--; }
  }
});

// A blob worker could introduce code/imports absent from the signed bundle. Keep
// service and UI execution in this extension's bounded main isolate.
Object.defineProperty(globalThis, "Worker", { value: undefined, configurable: false, writable: false });

// Reserve stdout for the protocol; console output is bounded by the host's log reader.
console.log = console.info = console.debug = console.warn = console.error.bind(console);
const program = (await import(Deno.args[0])).default as Program;
if (!program || typeof program.id !== "string" || typeof program.version !== "string" || program.fetch) throw new Error("bundle must default-export a stdio Program");
let output = Promise.resolve();
let queuedBytes = 0;
const conn = connect(program, line => {
  const data = encoder.encode(line + "\n");
  queuedBytes += data.length;
  if (data.length > MAX_FRAME || queuedBytes > MAX_FRAME * 2) exit(1);
  output = output.then(async () => {
    for (let at = 0; at < data.length;) at += await stdout.write(data.subarray(at));
    queuedBytes -= data.length;
  }).catch(() => exit(1));
});
const decoder = new TextDecoder();
let text = "";
let inflight = 0;
try {
  for await (const chunk of stdin.readable) {
    text += decoder.decode(chunk, { stream: true });
    if (text.length > MAX_FRAME) throw new Error("input frame too large");
    let nl: number;
    while ((nl = text.indexOf("\n")) >= 0) {
      const line = text.slice(0, nl); text = text.slice(nl + 1);
      if (++inflight > 128) exit(1);
      conn.line(line);
      void conn.drain().finally(() => inflight--);
    }
  }
} finally {
  shutdown.abort(); conn.close();
  await output;
  // EOF cancels requests and disposes timers instead of waiting for an unbounded guest promise.
  exit(0);
}
