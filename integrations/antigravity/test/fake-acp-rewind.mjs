import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createInterface } from 'node:readline'
import { DatabaseSync } from 'node:sqlite'

if (process.argv.includes('models')) { console.log('test-model\tTest Model'); process.exit(0) }

const directory = process.argv[2]
const log = process.argv[process.argv.indexOf('--log-file') + 1]
const calls = `${directory}/calls.jsonl`
const loaded = new Set()
const send = (message) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n')
const server = createServer(async (req, res) => {
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const params = JSON.parse(Buffer.concat(chunks).toString())
  const method = req.url.split('/').at(-1)
  appendFileSync(calls, JSON.stringify({ worker: process.pid, method, params }) + '\n')
  const snapshot = JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8'))
  if (snapshot.fail && method === 'RevertToCascadeStep') {
    res.writeHead(409).end(JSON.stringify({ message: 'Native rewind refused' }))
    return
  }
  if (method === 'GetCascadeTrajectory') {
    if (snapshot.requireLoad && params.disableRehydration && !loaded.has(params.cascadeId)) {
      res.writeHead(404).end('{"code":"not_found","message":"Cascade is not loaded"}')
      return
    }
    const hold = snapshot.holdTrajectoryOnce
    if (hold) {
      delete snapshot.holdTrajectoryOnce
      writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
      writeFileSync(`${directory}/trajectory-held`, '')
      while (!existsSync(`${directory}/release-trajectory`)) {
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
    }
    const holdAfter = snapshot.holdAfterRevertOnce && snapshot.afterRevertArmed
    if (holdAfter) {
      delete snapshot.holdAfterRevertOnce
      delete snapshot.afterRevertArmed
      writeFileSync(`${directory}/trajectory.json`, JSON.stringify(snapshot))
      writeFileSync(`${directory}/after-trajectory-held`, '')
      while (!existsSync(`${directory}/release-after-trajectory`)) {
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
    }
    if (!snapshot.trajectory.steps?.length) delete snapshot.trajectory.steps
    if (hold) {
      res.end(JSON.stringify(snapshot), () => writeFileSync(`${directory}/trajectory-response-sent`, ''))
      return
    }
    if (holdAfter) {
      res.end(JSON.stringify(snapshot))
      return
    }
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
  if (method === 'ForkConversation' && snapshot.forkId) {
    while (snapshot.holdFork && !existsSync(`${directory}/release-fork`)) {
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
    const newCascadeId = snapshot.forkId
    const forkedAtStepIndex = params.forkAtStepIndex === -1 ? snapshot.trajectory.steps.length - 1 : params.forkAtStepIndex
    const brain = `${directory}/config/brain`
    const from = `${brain}/${params.sourceCascadeId}/.system_generated/logs/transcript_full.jsonl`
    const to = `${brain}/${newCascadeId}/.system_generated/logs`
    if (!snapshot.delayForkExport) {
      mkdirSync(to, { recursive: true })
      writeFileSync(`${to}/transcript_full.jsonl`, readFileSync(from, 'utf8').trimEnd().split('\n').slice(0, forkedAtStepIndex + 1).join('\n') + '\n')
    }
    res.end(JSON.stringify({ newCascadeId, forkedAtStepIndex }))
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
    if (snapshot.holdAfterRevertOnce) snapshot.afterRevertArmed = true
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
let prompts = 0
input.on('line', (line) => {
  const req = JSON.parse(line)
  appendFileSync(calls, JSON.stringify({ worker: process.pid, ...req }) + '\n')
  if (req.id === undefined) return
  if (req.method === 'session/list') openService()
  const snapshot = JSON.parse(readFileSync(`${directory}/trajectory.json`, 'utf8'))
  if (req.method === 'session/resume' || req.method === 'session/load') loaded.add(req.params.sessionId)
  if (req.method === 'session/prompt' && snapshot.promptPlan) {
    const plan = snapshot.promptPlan[prompts++] || {}
    const sessionId = req.params.sessionId
    const update = (value) => send({ method: 'session/update', params: { sessionId, update: value } })
    if (plan.reply) update({ sessionUpdate: 'agent_message_chunk', content: { text: plan.reply } })
    const tool = { sessionUpdate: 'tool_call_update', toolCallId: 'test-tool', status: 'completed' }
    if (plan.tool) update(tool)
    if (plan.lateTool) setTimeout(() => update(tool), plan.lateTool)
    if (plan.export || plan.flushReply) {
      const transcript = `${directory}/config/brain/${sessionId}/.system_generated/logs/transcript_full.jsonl`
      let index = readFileSync(transcript, 'utf8').trim().split('\n').length
      const now = new Date().toISOString()
      const created_at = plan.wholeSeconds ? now.replace(/\.\d{3}Z$/, 'Z') : now
      const append = (type, content) => appendFileSync(transcript,
        JSON.stringify({ type, content, step_index: index++, status: 'DONE', created_at }) + '\n')
      if (plan.flushReply) {
        append('USER_INPUT', plan.flushReply.prompt)
        append('PLANNER_RESPONSE', plan.flushReply.reply)
      }
      if (plan.export) {
        append('USER_INPUT', (plan.planMode ? '/plan ' : '') + req.params.prompt[0].text)
        if (plan.undatedStep) appendFileSync(transcript,
          JSON.stringify({ type: 'SYSTEM_MESSAGE', content: 'System notice.', step_index: index++, status: 'DONE' }) + '\n')
        if (plan.error) append('ERROR_MESSAGE', plan.error)
        if (plan.reply) append('PLANNER_RESPONSE', plan.reply)
      }
    }
    if (plan.error && !plan.export) return send({ id: req.id, error: { code: -32000, message: plan.error } })
    return send({ id: req.id, result: { stopReason: 'end_turn' } })
  }
  if (req.method === 'session/prompt' && snapshot.promptError) {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, error: { code: -32000, message: snapshot.promptError } }) + '\n')
    return
  }
  if (req.method === 'session/prompt' && snapshot.promptReply) {
    send({ method: 'session/update', params: { sessionId: req.params.sessionId,
      update: { sessionUpdate: 'agent_message_chunk', content: { text: snapshot.promptReply } } } })
  }
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: {} }) + '\n')
})
input.on('close', () => server.close(() => process.exit(0)))
