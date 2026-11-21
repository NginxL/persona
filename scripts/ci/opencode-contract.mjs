// Real-engine contracts use a verified native executable and fake local models.
// No package lifecycle scripts, login, or provider credentials are used.
import { createHash } from 'node:crypto'
import { mkdtemp, writeFile, rm, chmod } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'
import { execFile } from 'node:child_process'

const run = promisify(execFile)
const version = '1.19.6'
const integrity = 'SUQS023u46+2+wMUej1Cm8VSFpHUd2c6cO4m2r7suRaCgrdIjR1iXr7ebl31xb/gHzaYS65xnj1fxRqHpzhU1A=='
if (process.platform !== 'linux' || process.arch !== 'x64') {
  process.stdout.write('SKIP pinned OpenCode native contract: Linux x64 only; portable runtime tests still apply\n')
  process.exit(0)
}
const directory = await mkdtemp(join(tmpdir(), 'persona-opencode-contract-'))
try {
  const response = await fetch(`https://registry.npmjs.org/opencode-linux-x64/-/opencode-linux-x64-${version}.tgz`, { signal: AbortSignal.timeout(120_000) })
  if (!response.ok) throw new Error(`OpenCode download failed: HTTP ${response.status}`)
  const bytes = Buffer.from(await response.arrayBuffer())
  if (createHash('sha512').update(bytes).digest('base64') !== integrity) throw new Error('OpenCode integrity mismatch')
  const archive = join(directory, 'engine.tgz')
  await writeFile(archive, bytes)
  await run('tar', ['-xzf', archive, '-C', directory, 'package/bin/opencode'])
  const bin = join(directory, 'package/bin/opencode')
  await chmod(bin, 0o700)
  // The test creates a separate HOME and removes all provider credentials.
  const child = await run(process.execPath, ['--test', 'integrations/opencode/test/real-acp-isolation.test.js', 'integrations/opencode/test/real-acp-policy.test.js'], {
    cwd: resolve(process.cwd()), env: { ...process.env, OPENCODE_CONTRACT_BIN: bin }, timeout: 240_000, maxBuffer: 4 * 1024 * 1024,
  })
  process.stdout.write(child.stdout)
  process.stderr.write(child.stderr)
} catch (error) {
  if (error.stdout) process.stdout.write(error.stdout)
  if (error.stderr) process.stderr.write(error.stderr)
  throw error
} finally {
  await rm(directory, { recursive: true, force: true })
}
