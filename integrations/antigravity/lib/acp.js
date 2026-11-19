import { assistantSearchPaths } from './compat.js'
// Persistent stdio JSON-RPC client for `agy acp`.
import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { createInterface } from 'node:readline'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { request } from 'node:http'

export class AgyAcpClient extends EventEmitter {
  constructor(options = {}) {
    super()
    this.bin = options.bin || 'agy'
    this.args = options.args || ['acp']
    this.log = options.log || (() => {})
    this.onPermissionRequest = options.onPermissionRequest || null
    this.proc = null
    this.readyPromise = null
    this.nextId = 1
    this.pending = new Map()
    this.loadedSessions = new Map() // sessionId -> { mcp: boolean, extraDirsKey: string }
    this.availableModels = null
    this.agentVersion = ''
    this.supportsAdditionalDirectories = false
    // Counts the agy processes started: a model list read from one carries its generation (#10368).
    this.generation = 0
    this.closed = false
    this.initTimeoutMs = options.initTimeoutMs ?? 60_000
    // How long a stopped agy gets to exit on SIGTERM before it is killed.
    this.killGraceMs = options.killGraceMs ?? 3_000
    // Kills in flight: each resolves once its agy is gone. stop() waits for all of them, so an agy dropped earlier (a
    // failed initialize, a stdin error) that ignores SIGTERM cannot outlive the companion.
    this.dying = new Set()
    this.nativeLogs = new Map()
  }

  async ensureStarted() {
    if (this.closed) throw stoppedError()
    if (this.proc && this.readyPromise) return this.readyPromise
    this.readyPromise = this._start()
    return this.readyPromise
  }

  async _start() {
    const home = process.env.HOME || '/root'
    const extraPath = assistantSearchPaths(home).join(':')
    const directory = mkdtempSync(join(tmpdir(), 'persona-agy-'))
    const nativeLog = join(directory, 'native.log')
    writeFileSync(nativeLog, '', { mode: 0o600 })
    this.generation += 1
    const child = spawn(this.bin, [...this.args, '--log-file', nativeLog], {
      stdio: ['pipe', 'pipe', 'pipe'],
      env: { ...process.env, PATH: `${extraPath}:${process.env.PATH || ''}` },
    })
    this.proc = child
    this.nativeLogs.set(child, { directory, file: nativeLog })
    this.loadedSessions.clear()
    // A missing binary ('exit' never comes then) or a failed kill: without a listener the companion would crash.
    child.on('error', (err) => {
      this.log(`agy acp process error: ${err.message}`)
      this._forget(child, err)
      if (!child.pid) this._removeNativeLog(child)
    })

    const rl = createInterface({ input: child.stdout, crlfDelay: Infinity })
    rl.on('line', (line) => {
      const trimmed = line.trim()
      if (!trimmed) return
      let msg
      try {
        msg = JSON.parse(trimmed)
      } catch {
        return
      }
      this._onMessage(msg, child)
    })

    child.stderr.on('data', (chunk) => {
      const s = String(chunk).trim()
      if (s) this.log(`agy acp stderr: ${s}`)
    })

    // EPIPE: agy closed its stdin or exited before 'exit' arrived. It cannot take requests any more.
    child.stdin.on('error', (err) => {
      this.log(`agy acp stdin error: ${err.message}`)
      if (this._forget(child, err)) void this._kill(child)
    })
    child.on('exit', (code, sig) => {
      this.log(`agy acp exited (code=${code}, sig=${sig})`)
      this._forget(child, new Error(`agy acp exited (${code ?? sig})`))
      this._removeNativeLog(child)
    })

    let init
    try {
      init = await this._rawCall('initialize', {
        protocolVersion: 1,
        clientCapabilities: {},
        clientInfo: { name: 'persona', version: '0.1.0' },
      }, this.initTimeoutMs)
    } catch (err) {
      // An agy that did not initialize is of no use: end it, and let the next call start a fresh one. (Already
      // forgotten: it exited, failed to spawn, or stop() or a stdin error ended it.)
      if (this._forget(child, err)) void this._kill(child)
      throw err
    }
    this.agentVersion = init?.agentInfo?.version || ''
    this.supportsAdditionalDirectories = Boolean(
      init?.agentCapabilities?.sessionCapabilities?.additionalDirectories ||
      init?.agentCapabilities?.session_capabilities?.additionalDirectories
    )
    return init
  }

  /**
   * Drop `child` when it is still the current process: later calls start a new one and every pending call fails with
   * `err`. A process that is not the current one any more (replaced, stopped) changes nothing. True when it was dropped.
   */
  _forget(child, err) {
    if (this.proc !== child) return false
    this.proc = null
    this.readyPromise = null
    this.loadedSessions.clear()
    this.availableModels = null
    this.agentVersion = ''
    this.supportsAdditionalDirectories = false
    const pending = [...this.pending.values()]
    this.pending.clear()
    for (const p of pending) p.reject(err)
    return true
  }

  /** End `child`: close its stdin, SIGTERM, and SIGKILL when it is still there after the grace period. */
  _kill(child) {
    try {
      child.stdin?.end()
    } catch {}
    // No pid: the spawn failed, there is nothing to signal.
    if (!child.pid || child.exitCode !== null || child.signalCode !== null) return Promise.resolve()
    const gone = new Promise((resolve) => {
      const timer = setTimeout(() => {
        if (child.exitCode !== null || child.signalCode !== null) return
        this.log(`agy acp did not exit ${this.killGraceMs}ms after SIGTERM, killing it`)
        try {
          child.kill('SIGKILL')
        } catch {}
      }, this.killGraceMs)
      timer.unref?.()
      child.once('exit', () => {
        clearTimeout(timer)
        resolve()
      })
      try {
        child.kill('SIGTERM')
      } catch {}
    })
    this.dying.add(gone)
    void gone.then(() => this.dying.delete(gone))
    return gone
  }

  _send(obj) {
    if (this.closed) throw stoppedError()
    if (!this.proc || !this.proc.stdin.writable) {
      throw new Error('agy acp process is not writable')
    }
    this.proc.stdin.write(`${JSON.stringify(obj)}\n`)
  }

  /**
   * Answer a request `child` made. Never throws: when that process is gone (it exited while the phone decided), the
   * answer has nobody to go to and is dropped.
   */
  _reply(child, obj) {
    try {
      if (child && child === this.proc && child.stdin.writable) {
        child.stdin.write(`${JSON.stringify(obj)}\n`)
        return
      }
      this.log(`agy acp: dropped the answer to request ${obj.id}, the agy process that asked is gone`)
    } catch (err) {
      this.log(`agy acp: could not answer request ${obj.id}: ${err?.message || err}`)
    }
  }

  _rawCall(method, params, timeoutMs = 60_000) {
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      const timer = timeoutMs > 0 ? setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`agy acp ${method} timed out after ${timeoutMs}ms`))
      }, timeoutMs) : null
      this.pending.set(id, {
        resolve: (res) => { clearTimeout(timer); resolve(res) },
        reject: (err) => { clearTimeout(timer); reject(err) },
      })
      try {
        this._send({ jsonrpc: '2.0', id, method, params })
      } catch (err) {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(err)
      }
    })
  }

  /** Send a JSON-RPC notification (no id, no answer). False when no agy process is running to take it. */
  notify(method, params) {
    if (!this.proc || !this.proc.stdin.writable) return false
    try {
      this._send({ jsonrpc: '2.0', method, params })
      return true
    } catch (err) {
      this.log(`agy acp: could not send ${method}: ${err.message}`)
      return false
    }
  }

  async call(method, params, timeoutMs = 60_000) {
    await this.ensureStarted()
    try {
      return await this._rawCall(method, params, timeoutMs)
    } catch (error) {
      if (params?.sessionId && /session[ _-]?not[ _-]?found|invalid[ _-]?session/i.test(`${error.code ?? ''} ${error.message} ${JSON.stringify(error.data ?? '')}`)) {
        this.loadedSessions.delete(params.sessionId)
        error.invalidSession = true
      }
      throw error
    }
  }

  _onMessage(msg, child) {
    if (child !== this.proc) return
    if (msg.id !== undefined && (msg.result !== undefined || msg.error !== undefined)) {
      const entry = this.pending.get(msg.id)
      if (!entry) return
      this.pending.delete(msg.id)
      if (msg.error) {
        const err = new Error(msg.error.message || `ACP error ${msg.error.code}`)
        err.code = msg.error.code
        if (msg.error.data !== undefined) err.data = msg.error.data
        entry.reject(err)
      } else {
        entry.resolve(msg.result)
      }
      return
    }

    if (msg.id !== undefined && msg.method) {
      void this._onServerRequest(msg.id, msg.method, msg.params, child)
      return
    }

    if (msg.method === 'session/update' && msg.params) {
      this.emit('update', msg.params)
    }
  }

  /** Answer a request from the agy process `child`. Never throws: the reply may find that process gone. */
  async _onServerRequest(id, method, params, child) {
    if (child !== this.proc) {
      // It came from a process already dropped (exited, stopped): nobody is left to answer, so don't ask the phone.
      this._reply(child, { jsonrpc: '2.0', id, error: { code: -32000, message: 'agy acp process is gone' } })
      return
    }
    try {
      if (method === 'session/request_permission' && this.onPermissionRequest) {
        const result = await this.onPermissionRequest(params)
        this._reply(child, { jsonrpc: '2.0', id, result })
        return
      }
      // Default fallback for unhandled permission requests: select allow_once if available
      if (method === 'session/request_permission') {
        const opts = Array.isArray(params?.options) ? params.options : []
        const allow = opts.find((o) => o.kind === 'allow_once' || o.kind === 'allow_always') || opts[0]
        this._reply(child, {
          jsonrpc: '2.0',
          id,
          result: allow ? { outcome: { outcome: 'selected', optionId: allow.optionId } } : { outcome: { outcome: 'cancelled' } },
        })
        return
      }
      this._reply(child, { jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
    } catch (err) {
      this._reply(child, { jsonrpc: '2.0', id, error: { code: -32000, message: err?.message || 'Internal error' } })
    }
  }

  async listSessions() {
    const res = await this.call('session/list', {}, 20_000)
    return Array.isArray(res?.sessions) ? res.sessions : []
  }

  _removeNativeLog(child) {
    const log = this.nativeLogs.get(child)
    this.nativeLogs.delete(child)
    try { if (log) rmSync(log.directory, { recursive: true, force: true }) } catch (error) { this.log(`agy acp log cleanup: ${error.message}`) }
  }

  /** The native service belongs to this ACP process. Its port comes only from that process's private log. */
  async nativeRequest(method, params) {
    await this.ensureStarted()
    const child = this.proc
    const state = this.nativeLogs.get(child)
    const portOf = () => {
      if (!state?.port && state?.file) {
        const port = Number(/random port at (\d+) for HTTP(?:\r?\n|$)/m.exec(readFileSync(state.file, 'utf8'))?.[1])
        if (port > 0 && port <= 65535) state.port = port
      }
      return state?.port
    }
    // Initialize alone leaves the native service unopened. Listing sessions opens it without resuming one.
    if (!portOf()) {
      if (!state) throw new Error('Antigravity did not expose its native conversation service')
      state.ready ??= this._rawCall('session/list', {}, 20_000).catch((error) => {
        delete state.ready
        throw error
      })
      await state.ready
    }
    if (child !== this.proc) throw new Error('Antigravity process changed during the conversation operation')
    const port = portOf()
    if (!port || port > 65535) throw new Error('Antigravity did not expose its native conversation service')
    const body = JSON.stringify(params)
    return new Promise((resolve, reject) => {
      const req = request({ hostname: '127.0.0.1', port,
        path: `/exa.language_server_pb.LanguageServerService/${method}`, method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', 'Content-Length': Buffer.byteLength(body) },
      }, (res) => {
        const chunks = []
        res.on('data', (chunk) => chunks.push(chunk))
        res.on('error', reject)
        res.on('end', () => {
          if (child !== this.proc) return reject(new Error('Antigravity process changed during the conversation operation'))
          let value
          try { value = JSON.parse(Buffer.concat(chunks).toString('utf8')) } catch (error) { return reject(error) }
          if (res.statusCode !== 200) return reject(new Error(value.message || `Antigravity conversation service returned ${res.statusCode}`))
          resolve(value)
        })
      })
      req.setTimeout(15_000, () => req.destroy(new Error('Antigravity conversation operation timed out')))
      req.on('error', reject)
      req.end(body)
    })
  }

  async newSession({ cwd, mcpServers = [], additionalDirectories = [] }) {
    const extraDirs = Array.isArray(additionalDirectories) ? additionalDirectories.filter(Boolean) : []
    const params = { cwd, mcpServers, ...(extraDirs.length > 0 ? { additionalDirectories: extraDirs } : {}) }
    const res = await this.call('session/new', params, 30_000)
    if (res?.sessionId) {
      this.loadedSessions.set(res.sessionId, { mcp: mcpServers.length > 0, extraDirsKey: JSON.stringify(extraDirs) })
      if (res.models?.availableModels) this.availableModels = res.models.availableModels
    }
    return res
  }

  async ensureSessionLoaded(sessionId, cwd, mcpServers = [], additionalDirectories = []) {
    await this.ensureStarted()
    const extraDirs = Array.isArray(additionalDirectories) ? additionalDirectories.filter(Boolean) : []
    const extraDirsKey = JSON.stringify(extraDirs)
    const loaded = this.loadedSessions.get(sessionId)
    if (loaded && (mcpServers.length === 0 || loaded.mcp) && loaded.extraDirsKey === extraDirsKey) {
      return
    }
    const params = {
      sessionId,
      cwd: cwd && existsSync(cwd) ? cwd : process.env.HOME || '/tmp',
      mcpServers,
      ...(extraDirs.length > 0 ? { additionalDirectories: extraDirs } : {}),
    }
    const res = await this.call('session/resume', params, 30_000)
    this.loadedSessions.set(sessionId, { mcp: mcpServers.length > 0, extraDirsKey })
    if (res?.models?.availableModels) this.availableModels = res.models.availableModels
    return res
  }

  async setModel(sessionId, modelId, timeoutMs = 15_000) {
    if (!modelId) return
    return this.call('session/set_model', { sessionId, modelId }, timeoutMs)
  }

  async setMode(sessionId, modeId) {
    if (!modeId) return
    return this.call('session/set_mode', { sessionId, modeId }, 15_000)
  }

  async setConfigOption(sessionId, configId, value) {
    if (!configId || value === undefined || value === '') return
    return this.call('session/set_config_option', { sessionId, configId, value }, 15_000)
  }

  async prompt(sessionId, promptBlocks) {
    // Turn execution can take minutes
    return this.call('session/prompt', { sessionId, prompt: promptBlocks }, 0)
  }

  // ACP's session/cancel is a notification: the agent ends the running session/prompt with stopReason 'cancelled' and
  // answers nothing else (sent as a request, an agent that follows the spec rejects it and keeps running). With no agy
  // process there is no turn to cancel.
  async cancel(sessionId) {
    this.notify('session/cancel', { sessionId })
  }

  async closeSession(sessionId) {
    this.loadedSessions.delete(sessionId)
    try {
      return await this.call('session/close', { sessionId }, 10_000)
    } catch {
      return {}
    }
  }

  /**
   * Replace the running agy: it is dropped as an agy that exited is, so the next call starts a fresh one, and ended.
   * Calls still pending on it fail with `reason`. Resolves once it is gone. The caller makes sure no turn is running.
   */
  recycle(reason = 'agy acp recycled') {
    const child = this.proc
    if (!child || !this._forget(child, new Error(reason))) return Promise.resolve()
    this.log(`agy acp: replacing the process (${reason})`)
    return this._kill(child)
  }

  /**
   * Stop for good: pending and later calls fail at once and agy is never started again. agy gets its stdin closed and
   * SIGTERM, and SIGKILL when it is still running after the grace period. Resolves once it is gone, and so is every agy
   * dropped earlier that was still inside its grace period.
   */
  stop() {
    this.closed = true
    const child = this.proc
    if (child) {
      this._forget(child, stoppedError())
      void this._kill(child)
    }
    return Promise.all([...this.dying]).then(() => {})
  }
}

function stoppedError() {
  return new Error('agy acp client stopped')
}
