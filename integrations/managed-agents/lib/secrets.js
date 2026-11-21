// Where a key comes from (docs/integrations/managed-agents.md "Keys"). The config names a key by reference, never by
// value: a key typed into the config file would be copied into backups and dotfile repositories, and read by anything
// that reads the file. Values are resolved when a request needs them, kept in memory for a short while, and never
// logged; redact() takes them out of any text that leaves this process (errors, logs).

import { execFile } from 'node:child_process'
import { readFile, stat } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'

const SCHEMES = ['env', 'file', 'keychain', 'extension']
const KEEP_MS = 60_000
const MAX_VALUE = 16 * 1024

/** The keychain service a `keychain:<account>` reference without a service uses. */
export const KEYCHAIN_SERVICE = 'persona-managed-agents'

export class SecretError extends Error {
  constructor(message, code = 'secret_unavailable') {
    super(message)
    this.code = code
  }
}

/**
 * Checks a reference's form (not its value): `env:NAME`, `file:<path>`, `keychain:<account>` or
 * `keychain:<service>/<account>`, `extension:<secret name>`. Returns the parsed reference, or throws.
 */
export function parseRef(ref, what = 'key') {
  if (typeof ref !== 'string' || ref.trim() === '') throw new SecretError(`${what}: a reference is required (env:, file:, keychain: or extension:)`, 'bad_reference')
  const i = ref.indexOf(':')
  const scheme = i > 0 ? ref.slice(0, i) : ''
  const rest = i > 0 ? ref.slice(i + 1) : ''
  if (!SCHEMES.includes(scheme) || rest === '') {
    // Never echo the text back: it may be the key itself, pasted where a reference belongs.
    throw new SecretError(`${what}: not a reference; write env:NAME, file:<path>, keychain:<account> or extension:<name>, never the key itself`, 'bad_reference')
  }
  if (scheme === 'env' && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(rest)) throw new SecretError(`${what}: env: needs a variable name`, 'bad_reference')
  if (scheme === 'extension' && !/^[a-z][a-z0-9_]{0,31}$/.test(rest)) throw new SecretError(`${what}: extension: needs a secret name`, 'bad_reference')
  if (scheme === 'keychain') {
    const slash = rest.lastIndexOf('/')
    return { scheme, ref, service: slash > 0 ? rest.slice(0, slash) : KEYCHAIN_SERVICE, account: slash > 0 ? rest.slice(slash + 1) : rest }
  }
  return { scheme, ref, name: rest }
}

const expand = (p) => (p === '~' ? homedir() : p.startsWith('~/') ? join(homedir(), p.slice(2)) : p)

/**
 * Resolves references to values. `env` is the environment to read (the process's by default), `run` runs a command
 * and resolves to its stdout (the keychain's; tests replace it), `log` gets warnings without values.
 */
export function createSecrets({ env = process.env, run = runCommand, platform = process.platform, log = () => {}, now = Date.now } = {}) {
  const kept = new Map() // ref -> { value, at }
  const known = new Set() // every value resolved so far: what redact() takes out
  const warned = new Set()

  async function read(r) {
    switch (r.scheme) {
      case 'env': {
        const v = env[r.name]
        if (!v) throw new SecretError(`the environment variable ${r.name} is not set`)
        return v
      }
      case 'file': {
        const path = expand(r.name)
        let st
        try {
          st = await stat(path)
        } catch {
          throw new SecretError(`cannot read the key file ${r.name}`)
        }
        if ((st.mode & 0o077) !== 0 && !warned.has(path)) {
          warned.add(path)
          log(`the key file ${r.name} can be read by other users; chmod 600 it`)
        }
        return (await readFile(path, 'utf8')).trim()
      }
      case 'keychain': {
        if (platform !== 'darwin') throw new SecretError('keychain: references work on macOS only; use file: here')
        try {
          return (await run('/usr/bin/security', ['find-generic-password', '-s', r.service, '-a', r.account, '-w'])).trim()
        } catch {
          throw new SecretError(`no keychain item for service ${r.service}, account ${r.account}`)
        }
      }
      case 'extension': {
        let all
        try {
          all = JSON.parse(env.PERSONA_EXTENSION_SECRETS ?? '{}')
        } catch {
          throw new SecretError('PERSONA_EXTENSION_SECRETS is not JSON')
        }
        const v = all?.[r.name]
        if (typeof v !== 'string' || v === '') throw new SecretError(`the extension secret ${r.name} is not set`)
        return v
      }
    }
    throw new SecretError(`unknown reference scheme ${r.scheme}`)
  }

  return {
    /** The value a reference stands for; throws SecretError (whose text never holds a value). */
    async resolve(ref, what) {
      const r = parseRef(ref, what)
      const hit = kept.get(r.ref)
      if (hit && now() - hit.at < KEEP_MS) return hit.value
      const value = await read(r)
      if (!value) throw new SecretError(`${what ?? 'key'}: the reference resolved to nothing`)
      if (value.length > MAX_VALUE) throw new SecretError(`${what ?? 'key'}: the value is longer than 16 KB`)
      if (/[\r\n\0]/.test(value)) throw new SecretError(`${what ?? 'key'}: the value has a line break or NUL and cannot go in a header`)
      kept.set(r.ref, { value, at: now() })
      known.add(value)
      return value
    },
    /** Forget kept values (a request was refused as unauthorized: the key may have been rotated). */
    forget() {
      kept.clear()
    },
    /** Text with every resolved value and anything shaped like a bearer token replaced. */
    redact(text) {
      let out = String(text ?? '')
      for (const v of known) if (v.length >= 4) out = out.split(v).join('<redacted>')
      return out.replace(/(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}/gi, '$1<redacted>')
    },
  }
}

function runCommand(file, args) {
  return new Promise((resolve, reject) => {
    execFile(file, args, { timeout: 10_000, maxBuffer: 64 * 1024 }, (err, stdout) => (err ? reject(err) : resolve(String(stdout))))
  })
}
