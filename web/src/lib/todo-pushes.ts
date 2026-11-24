// To-do pushes arrive in bursts. This loop loads on the first push, after the first screen: settle a short window,
// check the cheap hub-wide revision when one is available, and read the complete list only when it changed.
import type { api } from "./api";

type Hooks = [
  revision: () => string,
  refresh: () => Promise<void>,
  inFront: () => boolean,
  request: typeof api,
];

const TODO_PUSH_SETTLE = 200;
let hooks: Hooks | undefined;
let pushedRead = false;
let pushedAgain = false;
let pushedHidden = false;
let pushedTimer: ReturnType<typeof setTimeout> | undefined;
let pushedEpoch = 0;

async function refreshPushedTodos(epoch: number) {
  const h = hooks!;
  const revision = h[0]();
  if (!revision) {
    await h[1]();
    return;
  }
  let status = 0;
  try {
    await h[3]("/todos/revision", {
      headers: { "If-None-Match": revision },
      onResponse: (_headers, code) => { status = code; },
    });
    if (status === 304) return;
  } catch {
    // A manager can become old or unavailable between hello and this request. The original full read is the fallback
    // for 404/409/503 and network failures alike, and remains responsible for the page's error state.
  }
  if (epoch !== pushedEpoch) return;
  if (!h[2]()) {
    pushedHidden = true;
    return;
  }
  await h[1]();
}

function run() {
  const h = hooks!;
  if (!h[2]()) {
    pushedHidden = true;
    return;
  }
  pushedHidden = false;
  if (pushedRead) {
    pushedAgain = true;
    return;
  }
  if (pushedTimer !== undefined) return;
  const epoch = pushedEpoch;
  pushedTimer = setTimeout(() => {
    pushedTimer = undefined;
    if (epoch !== pushedEpoch) return;
    if (!h[2]()) {
      pushedHidden = true;
      return;
    }
    pushedRead = true;
    void refreshPushedTodos(epoch).finally(() => {
      if (epoch !== pushedEpoch) return;
      pushedRead = false;
      if (!pushedAgain && !pushedHidden) return;
      pushedAgain = false;
      pushedHidden = false;
      run();
    });
  }, TODO_PUSH_SETTLE);
}

export function push(h: Hooks) {
  hooks = h;
  run();
}

/** The login reset in store calls this synchronously once this lazy loop has loaded. */
export function reset() {
  pushedEpoch++;
  clearTimeout(pushedTimer);
  pushedTimer = undefined;
  pushedRead = false;
  pushedAgain = false;
  pushedHidden = false;
}

if (typeof document !== "undefined") {
  document.addEventListener("visibilitychange", () => {
    if (hooks && document.visibilityState === "visible" && pushedHidden) run();
  });
}
