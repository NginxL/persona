import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createInterface } from 'node:readline'
import { DatabaseSync } from 'node:sqlite'

if (process.argv.includes('models')) { console.log('test-model\tTest Model'); process.exit(0) }

const directory = process.argv[2]
const log = process.argv[process.argv.indexOf('--log-file') + 1]
const calls = `${directory}/calls.jsonl`
const send = (message) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n')
const server = createServer(async (req, res) => {
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const params = JSON.parse(Buffer.concat(chunks).toString())
  const method = req.url.split('/').at(-1)
  appendFileSync(calls, JSON.stringify({ method, params }) + '\n')
  const snapshot = JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8'))
  if (snapshot.fail && method === 'RevertToCascadeStep') {
    res.writeHead(409).end(JSON.stringify({ message: 'Native rewind refused' }))
    return
  }
  if (method === 'GetCascadeTrajectory') {
    if (!snapshot.trajectory.steps?.length) delete snapshot.trajectory.steps
    res.end(JSON.stringify(snapshot))
    return
  }
  if (method === 'GetCascadeTrajectorySteps') {
    while (JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8')).holdErrors) {
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
    res.end(JSON.stringify({ steps: (snapshot.trajectory.steps || []).slice(params.stepOffset) }))
    return
  }
  if (method === 'RevertToCascadeStep') {
    if (snapshot.holdRevert) {
      for (const update of [
        { sessionUpdate: 'agent_message_chunk', content: { text: 'Stale reply.' } },
        { sessionUpdate: 'tool_call', toolCallId: 'stale-tool', name: 'read_file' },
        { sessionUpdate: 'session_info_update', title: 'Rewind in progress' },
      ]) send({ method: 'session/update', params: { sessionId: params.cascadeId, update } })
      while (!existsSync(`${directory}/release-revert`)) {
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
      delete snapshot.holdRevert
    }
    snapshot.trajectory.steps = (snapshot.trajectory.steps || []).slice(0, params.stepIndex + 1)
    writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
    const db = new DatabaseSync(`${directory}/config/conversation_summaries.db`)
    db.prepare('UPDATE conversation_summaries SET step_count = ?, last_modified_time = ? WHERE conversation_id = ?')
      .run(params.stepIndex + 1, new Date().toISOString(), params.cascadeId)
    db.close()
    res.end('{"metadata":{}}')
    return
  }
  res.writeHead(404).end('{"message":"Unknown native method"}')
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
const openService = () => writeFileSync(log, `Language server listening on random port at ${server.address().port} for HTTP\n`)
if (!JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8')).lazyService) openService()
const input = createInterface({ input: process.stdin })
input.on('line', (line) => {
  const req = JSON.parse(line)
  appendFileSync(calls, JSON.stringify(req) + '\n')
  if (req.id === undefined) return
  if (req.method === 'session/list') openService()
  const snapshot = JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8'))
  if (req.method === 'session/prompt' && snapshot.promptError) {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, error: { code: -32000, message: snapshot.promptError } }) + '\n')
    return
  }
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: {} }) + '\n')
})
input.on('close', () => server.close(() => process.exit(0)))
