// Assistant execution belongs to one process tree. Ordinary Agents never enter
// this pool. Admission is serialized, including stop, so a pending start or a
// retiring child cannot transiently exceed the configured process limit.
import { RpcError } from '../../runtime-sdk/index.js'

export function createAssistantWorkers({ create, busy, retiring, limit = 8, idleMs = 300_000, now = Date.now }) {
  if (!Number.isInteger(limit) || limit < 1 || limit > 64) throw new Error('assistant worker limit must be an integer between 1 and 64')
  if (!Number.isFinite(idleMs) || idleMs < 1) throw new Error('assistant worker idle timeout must be positive')
  const workers = new Set()
  const sessions = new Map()
  let tail = Promise.resolve()
  let stopped = false
  const serial = (fn) => {
    const next = tail.catch(() => {}).then(fn)
    tail = next.catch(() => {})
    return next
  }
  const available = (worker) => !worker.retiring && worker.leases === 0 && worker.session && !busy(worker.session)
  async function retire(worker) {
    if (worker.retiring) return
    worker.retiring = true
    if (worker.session && sessions.get(worker.session.id) === worker) sessions.delete(worker.session.id)
    try {
      await retiring(worker)
    } finally {
      try { await worker.acp?.stop() } finally { workers.delete(worker) }
    }
  }
  async function reserve(seed) {
    if (stopped) throw new RpcError('OpenCode companion is stopping', 'runtime_away')
    if (workers.size >= limit) {
      const oldest = [...workers].filter(available).sort((a, b) => a.usedAt - b.usedAt)[0]
      if (!oldest) throw new RpcError('All assistant execution processes are busy; try again shortly', 'busy')
      await retire(oldest)
    }
    const worker = { acp: null, session: null, leases: 1, retiring: false, usedAt: now() }
    workers.add(worker)
    try { worker.acp = await create(worker, seed) } catch (err) { workers.delete(worker); throw err }
    return worker
  }
  function bind(worker, session) {
    if (!workers.has(worker) || worker.retiring || worker.session || sessions.has(session.id)) {
      throw new RpcError('Assistant execution already has an owner', 'busy')
    }
    worker.session = session
    sessions.set(session.id, worker)
    session.worker = worker
  }
  return {
    workers,
    reserve: (seed) => serial(() => reserve(seed)),
    bind,
    acquire: (session) => serial(async () => {
      if (stopped) throw new RpcError('OpenCode companion is stopping', 'runtime_away')
      const existing = sessions.get(session.id)
      if (existing && !existing.retiring) { existing.leases++; return existing }
      const worker = await reserve(session)
      try { bind(worker, session); return worker } catch (err) { await retire(worker); throw err }
    }),
    release(worker) { worker.leases = Math.max(0, worker.leases - 1); worker.usedAt = now() },
    discard: (worker) => serial(() => retire(worker)),
    reap: () => serial(async () => {
      for (const worker of workers) if (available(worker) && now() - worker.usedAt >= idleMs) await retire(worker)
    }),
    stop() {
      stopped = true
      return serial(async () => {
        const outcomes = await Promise.allSettled([...workers].map(retire))
        const failed = outcomes.find((outcome) => outcome.status === 'rejected')
        if (failed) throw failed.reason
      })
    },
  }
}
