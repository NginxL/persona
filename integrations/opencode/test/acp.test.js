import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { PassThrough } from 'node:stream'
import { OpencodeAcpClient } from '../lib/acp.js'

function makeFakeChild() {
  const child = new EventEmitter()
  child.pid = Math.floor(Math.random() * 10000) + 1000
  child.exitCode = null
  child.killed = false
  child.stdin = new PassThrough()
  child.stdout = new PassThrough()
  child.stderr = new PassThrough()
  child.written = []
  const origWrite = child.stdin.write.bind(child.stdin)
  child.stdin.write = (chunk, ...rest) => {
    child.written.push(String(chunk))
    return origWrite(chunk, ...rest)
  }
  child.kills = []
  child.kill = (sig) => {
    child.kills.push(sig)
    child.killed = true
    if (child.exitCode === null) {
      child.exitCode = 1
      queueMicrotask(() => child.emit('exit', 1, sig || null))
    }
    return true
  }
  return child
}

test('OpencodeAcpClient drops stale permission replies across process exit/restart without unhandled rejection', async () => {
  const children = []
  let resolvePerm
  const client = new OpencodeAcpClient({
    spawn: () => {
      const c = makeFakeChild()
      children.push(c)
      // Auto-answer initialize
      c.stdin.on('data', (buf) => {
        for (const line of String(buf).split('\n').filter(Boolean)) {
          const msg = JSON.parse(line)
          if (msg.method === 'initialize') {
            c.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: msg.id, result: { agentCapabilities: {} } })}\n`)
          }
        }
      })
      return c
    },
    onPermissionRequest: () => new Promise((r) => { resolvePerm = r }),
  })

  await client.ensureStarted()
  const child1 = children[0]

  // Child 1 sends a permission request; while pending, child 1 exits and child 2 starts.
  child1.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: 99, method: 'session/request_permission', params: { sessionId: 's1' } })}\n`)
  await new Promise((r) => setTimeout(r, 10))
  assert.ok(resolvePerm, 'permission handler called')

  child1.exitCode = 1
  child1.stdin.destroy()
  child1.emit('exit', 1, null)

  await client.ensureStarted()
  const child2 = children[1]
  const child2WritesBefore = child2.written.length
  // A late exit/error/stdout from the old generation cannot clear or poison
  // the replacement, including an in-flight request on that replacement.
  const pending = client._callRaw('session/new', { cwd: '/w' }, 500)
  const request = JSON.parse(child2.written.at(-1))
  child1.emit('exit', 1, null)
  child1.emit('error', new Error('late old error'))
  child1.stdout.write(JSON.stringify({ id: request.id, result: { sessionId: 'forged' } }) + '\n')
  assert.equal(client.proc, child2)
  child2.stdout.write(JSON.stringify({ id: request.id, result: { sessionId: 'new-owner' } }) + '\n')
  assert.equal((await pending).sessionId, 'new-owner')

  // Resolving the old permission request must NOT throw unhandled rejection and must NOT write to child2!
  resolvePerm({ outcome: { outcome: 'selected', optionId: 'once' } })
  await new Promise((r) => setTimeout(r, 20))
  assert.equal(child2.written.length, child2WritesBefore + 1, 'only the replacement request is written, never the stale permission reply')

  await client.stop()
})

test('OpencodeAcpClient handles stdin EPIPE error without crashing and cleans up orphan child on initialize timeout', async () => {
  const children = []
  let answerInit = true
  const client = new OpencodeAcpClient({
    spawn: () => {
      const c = makeFakeChild()
      children.push(c)
      c.stdin.on('data', (buf) => {
        for (const line of String(buf).split('\n').filter(Boolean)) {
          const msg = JSON.parse(line)
          if (msg.method === 'initialize' && answerInit) {
            c.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: msg.id, result: { agentCapabilities: {} } })}\n`)
          }
        }
      })
      return c
    },
  })

  // 1. Initialize timeout must terminate child and clear this.proc.
  answerInit = false
  await assert.rejects(client.ensureStarted(50), /timed out/)
  assert.equal(children.length, 1)
  assert.ok(children[0].kills.includes('SIGTERM'), 'orphan child killed on initialize failure')
  assert.equal(client.proc, null)

  // 2. Subsequent start succeeds; stdin EPIPE fails pending RPC without uncaught error.
  answerInit = true
  await client.ensureStarted(500)
  const child2 = children[1]
  const pendingRpc = client.call('session/new', { cwd: '/w' }, 5000)
  await new Promise((r) => setTimeout(r, 10))
  child2.stdin.emit('error', Object.assign(new Error('write EPIPE'), { code: 'EPIPE' }))
  await assert.rejects(pendingRpc, /stdin error.*EPIPE/)
  assert.equal(client.proc, null)
  assert.ok(child2.kills.includes('SIGTERM'))

  await client.stop()
})
