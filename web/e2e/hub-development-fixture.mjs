// Isolated synthetic API for the real App/HubSettings renderer. Never runs an updater.
// Run with Node from the repository root; used by hub-development.mjs.
import { createServer } from "vite";
import { WebSocketServer } from "ws";
import { fileURLToPath } from "node:url";
const web = fileURLToPath(new URL("../", import.meta.url));
const port = Number(process.env.PORT || 5996);
const current = "a".repeat(40), latest = "b".repeat(40), saved = "c".repeat(40);
const backups = [{ id: "snapshot-20261112000100000000-cccccccccccc", version: "web-dev-cccccccccccc-202611120001",
  build: 202611120001, commit: saved, updated_at: "2026-11-12T10:00:00Z", created_at: "2026-11-13T10:00:00Z" }];
const assistants = [
  { id: "assistant", name: "Example assistant", online: true, builtin: true, default: true },
  { id: "studio", name: "Studio", online: true },
];
const computers = [
  { id: "desk", name: "Desk computer", manager: "assistant", kind: "link", online: true, node_version: backups[0].version,
    node_mismatch: true, deploying: true, deploy_phase: "transferring", deploy_source: "hub_package" },
  { id: "lab", name: "Lab computer", manager: "studio", kind: "link", online: true, node_version: backups[0].version,
    node_mismatch: true, deploying: true, deploy_phase: "waiting_idle", deploy_source: "local_build" },
  { id: "travel", name: "Travel computer", manager: "studio", kind: "link", online: true, node_version: backups[0].version,
    node_mismatch: true, can_deploy: false, deploy_error: "Go is unavailable on the assistant’s computer.",
    deploy_reason: "Install Go on the computer running Studio, then choose Redeploy." },
];
let status, requests, mode;
function reset(input = {}) {
  mode = input.mode || "available";
  requests = [];
  status = { version: "web-dev-aaaaaaaaaaaa-202611130001", build: 202611130001, channel: "release", source: "web-dev",
    can_update: true, state: mode, busy: [], auto: false, auto_notice: false, confirmed: true, auto_update_nodes: false,
    development: { configured: true, branch: "main", current_commit: current, current_updated_at: "2026-11-13T10:00:00Z",
      builtin_quiesce: true, backup_default_directory: "/var/lib/persona/update/development-backups" },
    latest: { version: "web-dev-bbbbbbbbbbbb-202611140001", build: 202611140001, newer: true, bad: false,
      commit: latest, updated_at: "2026-11-14T10:00:00Z", branch: "main" },
    backup: { id: backups[0].id, retained: 3, pruned: [] }, last_check: "2026-11-15T10:00:00Z" };
  if (mode === "release" || mode === "setup") {
    status.source = "release"; status.state = "idle"; status.latest = undefined;
    status.version = "0.6.2";
    status.development.configured = mode !== "setup";
  }
  if (mode === "beta") {
    status.source = "beta"; status.state = "idle"; status.version = "0.6.2";
    status.latest = { version: "0.7.0-beta.1", build: 202611171300, newer: true, bad: false };
  }
  if (mode === "multi-assistant") status.state = "available";
  if (mode === "backing_up") status.progress = "Synthetic snapshot: verifying saved programs";
  if (mode === "failed") status.error = "Update recovery is unresolved. Inspect the retained adoption journal and staged programs before retrying. (Synthetic fixture)";
  if (mode === "rolled_back") { status.latest.newer = false; status.error = "The attempted version did not start. Previous programs restored; current database and history preserved. (Synthetic fixture)"; }
}
reset();
const sockets = new Set();
function push() { for (const s of sockets) if (s.readyState === 1) s.send(JSON.stringify({ type: "hub_update", update: status })); }
function waiting() {
  status.state = "waiting_idle";
  status.busy = [{ machine_id: "fixture-computer", machine: "Example computer", session_id: "fixture-session", title: "Example running session" }];
}
const html = '<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Persona synthetic update fixture</title></head><body><div id="root" style="height:100dvh"></div><script type="module" src="/__fixture-app.js"></script></body></html>';
const module = `import React from 'react';
import { createRoot } from 'react-dom/client';
import { initI18n } from '/src/i18n/index.ts';
import { applyLook } from '/src/ui/look.ts';
import '/src/styles.css';
const { setTheme } = await import('/src/views/theme.ts');
setTheme(localStorage.getItem('persona.theme') || 'system');
applyLook(); await initI18n('en');
const api = await import('/src/lib/api.ts'); api.setToken('synthetic-fixture');
const [{ App }, { ExtensionThemeProvider }] = await Promise.all([import('/src/App.tsx'),import('/src/ui/ExtensionThemePicker.tsx')]);
createRoot(document.getElementById('root')).render(React.createElement(ExtensionThemeProvider,null,React.createElement(App)));
`;
const server = await createServer({ root: web, configFile: web + '/vite.config.ts',
  server: { host: '127.0.0.1', port, strictPort: true },
  plugins: [{ name: 'synthetic-hub-development',
    // The regular dev server proxies /api WebSocket upgrades. This fixture handles its own hello and machine list.
    config(config) { delete config.server.proxy; },
    resolveId(id) { if (id === '/__fixture-app.js') return '\0fixture-app.js'; },
    load(id) { if (id === '\0fixture-app.js') return module; },
    configureServer(vite) { vite.middlewares.use(async (req, res, next) => {
      const url = new URL(req.url, 'http://localhost');
      if (url.pathname === '/__fixture') {
        if (req.method === 'POST') { let body = ''; for await (const p of req) body += p; reset(JSON.parse(body || '{}')); }
        res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ fixture: "persona-web-development-ui", status, requests })); return;
      }
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/src/') && !url.pathname.startsWith('/@') && !url.pathname.split('/').pop().includes('.')) {
        res.setHeader('Content-Type', 'text/html'); res.end(await vite.transformIndexHtml('/fixture.html', html)); return;
      }
      if (!url.pathname.startsWith('/api/')) return next();
      let body = ''; for await (const p of req) body += p;
      const input = JSON.parse(body || '{}'), path = url.pathname.replace('/api/v1', '');
      requests.push({ path, method: req.method, input });
      let value = {};
      if (path === '/hub/update' && req.method === 'GET') value = status;
      else if (path === '/hub/update/history') value = { history: [{ from_version: 'web-dev-cccccccccccc-202611120001', to_version: status.version,
        to_build: status.build, result: mode === 'rolled_back' ? 'rolled_back' : 'updated', auto: false, source: 'web-dev', commit: current,
        started: '2026-11-13T10:00:00Z', ended: '2026-11-13T10:00:00Z' }] };
      else if (path === '/hub/update/backups') {
        if (mode === 'backup-error') { res.statusCode = 409; value = { error: 'Synthetic backup inventory unavailable' }; }
        else value = { backups };
      } else if (path.endsWith('/apply') && path.startsWith('/hub/update/backups/')) {
        if (path.split('/')[4] !== backups[0].id) { res.statusCode = 409; value = { error: 'Unknown synthetic backup' }; }
        else { status.latest = { ...backups[0], backup_id: backups[0].id, newer: true, bad: false }; status.auto = false; waiting(); value = status; }
      } else if (path === '/hub/update/check') { status.state = 'available'; value = status; }
      else if (path === '/hub/update/settings') {
        if (input.source && input.source !== status.source) { status.source = input.source; status.auto = false; }
        for (const key of ['auto', 'auto_update_nodes']) if (key in input) status[key] = input[key];
        value = status;
      } else if (path === '/hub/update' && req.method === 'POST') { waiting(); value = status; }
      else if (path === '/extensions/-/market-settings') value = { registry_mirror: '', catalog_mirror: '' };
      else if (path === '/managers') value = { default: 'assistant', managers: mode === 'multi-assistant' ? assistants : [assistants[0]] };
      else if (path === '/machines') value = { machines: mode === 'multi-assistant' ? computers : [] };
      else if (path === '/assistant') value = { configured: true, main: '', conversations: [], name: 'Example assistant' };
      else if (path === '/assistant/directions/tree') value = { directions: [] };
      else if (path === '/sessions') value = { sessions: [] };
      else if (path === '/interactions') value = { interactions: [] };
      else if (path === '/todos') value = { todos: [] };
      else if (path === '/mutes') value = { spaces: [], sessions: [], allowed: [] };
      else if (path === '/pins') value = { spaces: [], sessions: [] };
      else if (path === '/extensions' || path === '/extensions/-/themes') value = { extensions: [], themes: [] };
      else if (path === '/inbox') value = { items: [], total: 0 };
      else if (path === '/devices') value = { devices: [] };
      else if (path === '/launchers') value = { launchers: [] };
      else if (['/agent-prefs', '/sessions/preferences', '/agents/preferences'].includes(path)) value = {};
      else { res.statusCode = 404; value = { error: 'Outside synthetic fixture' }; }
      res.setHeader('Content-Type', 'application/json'); res.setHeader('Cache-Control', 'no-store'); res.end(JSON.stringify(value));
      if (path.startsWith('/hub/update') && req.method !== 'GET') push();
    }); }
  }] });
const ws = new WebSocketServer({ noServer: true });
server.httpServer.on('upgrade', (req, socket, head) => {
  if (new URL(req.url, 'http://localhost').pathname === '/api/v1/ws') ws.handleUpgrade(req, socket, head, client => ws.emit('connection', client));
});
ws.on('connection', socket => {
  sockets.add(socket); socket.on('close', () => sockets.delete(socket));
  socket.on('message', raw => {
    const msg = JSON.parse(String(raw));
    if (msg.type === 'client_hello') socket.send(JSON.stringify({ type: 'hello', server_version: status.version, hub_name: 'Example hub', features: [], resumed: false }));
    else if (msg.type === 'app_ping' || msg.type === 'ping') socket.send(JSON.stringify({ type: 'pong' }));
  });
});
await server.listen();
console.log('Synthetic Web update fixture ready');
