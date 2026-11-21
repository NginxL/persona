// The Managed Agents REST API, as far as this runtime uses it (docs/integrations/managed-agents.md "Mapping"):
// sessions (list, get, create), a session's event log (list, send, stream). One client per profile; every request
// carries the profile's key in its auth header, and the model credentials (BYOK Direct providers) go only on event
// POSTs. Nothing here logs; errors are redacted before they leave.

const TIMEOUT_MS = 30_000

export class ApiError extends Error {
  constructor(message, status, code) {
    super(message)
    this.status = status
    this.code = code
  }
}

/** @param {object} profile a parsed profile (config.js); @param {object} secrets createSecrets(). */
export function createClient(profile, secrets, { fetch: doFetch = globalThis.fetch } = {}) {
  const url = (path, query) => {
    const u = new URL(profile.base + profile.prefix + path)
    for (const [k, v] of Object.entries(query ?? {})) {
      if (v === undefined || v === null || v === '') continue
      for (const one of Array.isArray(v) ? v : [v]) u.searchParams.append(k, String(one))
    }
    return u
  }

  async function headers(extra = {}, { model = false } = {}) {
    const key = await secrets.resolve(profile.apiKey, `${profile.id}.api_key`)
    const h = { ...profile.headers, ...extra }
    if (profile.auth === 'bearer') h.Authorization = `Bearer ${key}`
    else h['x-api-key'] = key
    if (model) {
      for (const [name, ref] of Object.entries(profile.modelHeaders)) {
        h[profile.modelHeaderPrefix + name] = await secrets.resolve(ref, `${profile.id}.model_headers.${name}`)
      }
    }
    return h
  }

  async function request(method, path, { query, body, model = false, signal } = {}) {
    const h = await headers(body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' }, { model })
    let res
    try {
      res = await doFetch(url(path, query), {
        method,
        headers: h,
        body: body === undefined ? undefined : JSON.stringify(body),
        redirect: 'error', // a redirect would carry the key to another address
        signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(TIMEOUT_MS)]) : AbortSignal.timeout(TIMEOUT_MS),
      })
    } catch (err) {
      throw new ApiError(secrets.redact(`${profile.id}: ${method} ${path}: ${err.cause?.code ?? err.name === 'TimeoutError' ? 'timed out' : err.message}`), 0, 'network')
    }
    const text = await res.text()
    let json
    try {
      json = text ? JSON.parse(text) : {}
    } catch {
      json = undefined
    }
    if (!res.ok) {
      if (res.status === 401 || res.status === 403) secrets.forget()
      const msg = json?.error?.message ?? json?.message ?? json?.ResponseMetadata?.Error?.Message ?? text.slice(0, 200)
      const code = json?.error?.code ?? json?.error?.type ?? (res.status === 401 ? 'unauthorized' : res.status === 404 ? 'not_found' : `http_${res.status}`)
      throw new ApiError(secrets.redact(`${profile.id}: ${method} ${path}: ${res.status} ${msg}`), res.status, code)
    }
    if (json === undefined) throw new ApiError(`${profile.id}: ${method} ${path}: the answer is not JSON`, res.status, 'bad_answer')
    return json
  }

  return {
    profile,
    /** One page of the newest sessions (data-plane list; the route is configurable since not every server has it). */
    async listSessions({ limit = profile.listLimit, signal } = {}) {
      const body = await request('GET', profile.listPath, { query: { limit, order: 'desc', project_name: profile.project }, signal })
      return listOf(body).map(sessionOf).filter(Boolean)
    },
    async getSession(id, { signal } = {}) {
      return sessionOf(await request('GET', `/sessions/${encodeURIComponent(id)}`, { signal }))
    },
    async createSession({ agent, environmentId, title, body }, { signal } = {}) {
      const req = { ...(body ?? {}), agent }
      if (environmentId) req.environment_id = environmentId
      if (profile.project && req.project_name === undefined) req.project_name = profile.project
      if (title) req.title = title
      return sessionOf(await request('POST', '/sessions', { body: req, signal }))
    },
    /**
     * Events after `after` (an event id, or none for the start), oldest first, following next_page. The server's
     * `after` filter is not relied on (docs: it may return earlier events), so callers dedupe by id; `maxPages` bounds it.
     */
    async listEvents(id, { after, maxPages = 50, signal } = {}) {
      const out = []
      let page
      for (let i = 0; i < maxPages; i++) {
        const body = await request('GET', `/sessions/${encodeURIComponent(id)}/events`, { query: { order: 'asc', limit: 100, after: page ? undefined : after, page }, signal })
        out.push(...listOf(body))
        page = body.next_page ?? body.NextPage
        if (!page || (body.has_more === false)) return { events: out, complete: true }
      }
      return { events: out, complete: false }
    },
    /** Sends events one per request (servers take one at a time; the order is kept, the batch is not atomic). */
    async sendEvents(id, events, { signal } = {}) {
      const sent = []
      for (const ev of events) {
        const model = ev.type !== 'user.interrupt'
        const body = await request('POST', `/sessions/${encodeURIComponent(id)}/events`, { body: { events: [ev] }, model, signal })
        sent.push(...listOf(body))
      }
      return sent
    },
    /**
     * Opens the session's event stream (server-sent events) with live deltas for replies and thinking, and calls
     * onEvent with each JSON frame until the stream ends or `signal` aborts. Resolves when it ends.
     */
    async stream(id, onEvent, { signal } = {}) {
      const h = await headers({ Accept: 'text/event-stream' })
      let res
      try {
        res = await doFetch(url(`/sessions/${encodeURIComponent(id)}/events/stream`, { event_deltas: ['agent.message', 'agent.thinking'] }), { headers: h, redirect: 'error', signal })
      } catch (err) {
        if (signal?.aborted) return
        throw new ApiError(secrets.redact(`${profile.id}: stream: ${err.cause?.code ?? err.message}`), 0, 'network')
      }
      if (!res.ok || !res.body) {
        if (res.status === 401 || res.status === 403) secrets.forget()
        throw new ApiError(`${profile.id}: stream: ${res.status}`, res.status, `http_${res.status}`)
      }
      const decoder = new TextDecoder()
      let buf = ''
      try {
        for await (const chunk of res.body) {
          buf += decoder.decode(chunk, { stream: true })
          let cut
          while ((cut = buf.search(/\r?\n\r?\n/)) >= 0) {
            const frame = buf.slice(0, cut)
            buf = buf.slice(cut).replace(/^\r?\n\r?\n/, '')
            const data = frame.split(/\r?\n/).filter((l) => l.startsWith('data:')).map((l) => l.slice(5).replace(/^ /, '')).join('\n')
            if (!data || data === '[DONE]') continue
            try {
              onEvent(JSON.parse(data))
            } catch {}
          }
        }
      } catch (err) {
        if (!signal?.aborted) throw new ApiError(secrets.redact(`${profile.id}: stream: ${err.message}`), 0, 'network')
      }
    },
  }
}

function listOf(body) {
  for (const k of ['data', 'Items', 'items', 'sessions', 'events', 'Result']) if (Array.isArray(body?.[k])) return body[k]
  return Array.isArray(body) ? body : []
}

const pick = (o, ...keys) => {
  for (const k of keys) if (o?.[k] !== undefined && o[k] !== null) return o[k]
  return undefined
}

/** A session in one shape, whichever casing the server answers in (data plane snake_case, control plane PascalCase). */
export function sessionOf(raw) {
  const s = raw?.session ?? raw?.Result ?? raw
  const id = pick(s, 'id', 'Id', 'session_id')
  if (typeof id !== 'string' || id === '') return undefined
  const agent = pick(s, 'agent', 'Agent')
  const agentObj = agent && typeof agent === 'object' ? agent : undefined
  const model = pick(agentObj, 'model', 'Model')
  return {
    id,
    title: pick(s, 'title', 'Title') ?? '',
    status: String(pick(s, 'status', 'Status') ?? '').toLowerCase(),
    agentId: (typeof agent === 'string' ? agent : pick(agentObj, 'id', 'Id')) ?? pick(s, 'agent_id', 'AgentId') ?? '',
    agentName: pick(agentObj, 'name', 'Name') ?? '',
    model: (typeof model === 'string' ? model : pick(model, 'id', 'Id')) ?? '',
    updatedAt: pick(s, 'updated_at', 'UpdateTime', 'updatedAt') ?? pick(s, 'created_at', 'CreateTime') ?? '',
    createdAt: pick(s, 'created_at', 'CreateTime') ?? '',
    archived: Boolean(pick(s, 'archived_at', 'ArchivedAt')),
  }
}
