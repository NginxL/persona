import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const run = promisify(execFile)

// The CLI prints tab-separated IDs and names, with optional progress lines before the list. ACP offers the same
// catalog as modelId/name objects. Neither source requires a prompt or a billable request.
export function mapModels(entries, configured = '') {
  const seen = new Set()
  const models = entries.flatMap((entry) => {
    const id = entry?.modelId || entry?.id
    if (typeof id !== 'string' || !id || seen.has(id)) return []
    seen.add(id)
    return [{ id, name: entry.name || id }]
  })
  return models.map((model) => {
    const effort = model.id.match(/-(low|medium|high)$/)?.[1]
    const base = effort ? model.id.slice(0, -effort.length - 1) : model.id
    const efforts = ['low', 'medium', 'high'].filter((level) => seen.has(`${base}-${level}`))
    return {
      ...model,
      default: model.id === configured || model.name === configured,
      efforts,
      ...(effort ? { default_effort: effort } : {}),
    }
  })
}

// A CLI whose stored sign-in expired long ago does not fail `agy models`: it hangs until the timeout kills it (#11106).
// That is reported as a sign-in problem the owner can act on, with code SIGNIN_TIMEOUT so callers can back off.
export const SIGNIN_TIMEOUT = 'agy_signin_timeout'

export async function discoverModels(bin, { env = process.env, timeout = 25_000, signal } = {}) {
  let stdout
  try {
    ({ stdout } = await run(bin, ['models'], { env, timeout, signal, maxBuffer: 1024 * 1024 }))
  } catch (err) {
    if (err?.killed && !signal?.aborted) {
      const timedOut = new Error(`agy models did not answer within ${Math.round(timeout / 1000)} s: the Antigravity CLI ` +
        'sign-in may have expired. Run agy once on this computer to sign in again.')
      timedOut.code = SIGNIN_TIMEOUT
      throw timedOut
    }
    throw err
  }
  const entries = stdout.split(/\r?\n/).flatMap((line) => {
    const fields = line.trim().split(/\t+/)
    return fields.length >= 2 && /^\S+$/.test(fields[0]) ? [{ id: fields[0], name: fields[1] }] : []
  })
  if (!entries.length) throw new Error('agy models returned no models')
  return mapModels(entries)
}
