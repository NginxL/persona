import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { it } from 'node:test'
import { AgyAcpClient } from '../lib/acp.js'

it('caches the native port for one process and discovers a replacement port', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-native-port-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const client = new AgyAcpClient()
  client.ensureStarted = async () => {}
  for (const generation of [1, 2]) {
    const server = createServer((req, res) => {
      req.resume()
      res.end(JSON.stringify({ generation }))
    })
    t.after(() => server.close())
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
    const file = join(dir, `native-${generation}.log`)
    writeFileSync(file, `random port at ${server.address().port} for HTTP\n`)
    client.proc = {}
    client.nativeLogs.set(client.proc, { file })
    assert.deepEqual(await client.nativeRequest('Read', {}), { generation })
    rmSync(file)
    assert.deepEqual(await client.nativeRequest('Read', {}), { generation })
  }
})
