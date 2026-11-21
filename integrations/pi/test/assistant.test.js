import { test } from 'node:test'
import assert from 'node:assert/strict'
import { assistantState, recordAssistant, assistantBeforeTurn } from '../lib/assistant.js'

function manager(id) {
 const entries = []
 return { getSessionId: () => id, getEntries: () => entries, appendCustomEntry(customType, data) { entries.push({ type: 'custom', customType, data }) } }
}

test('assistant policy/context and tool authority are isolated to the actual pi session', async () => {
 const a = manager('a'), b = manager('b')
 recordAssistant(a, { assistant: true, context: 'fresh snapshot A', supervision_instructions: 'policy A', permission_mode: 'default' })
 recordAssistant(b, { assistant: true, context: 'fresh snapshot B', supervision_instructions: 'policy B', permission_mode: 'bypassPermissions' })
 const calls = [], tools = []
 const rt = { tools: async (p) => { calls.push(p); return [{ name: 'todo_decisions', description: 'Record a decision', input_schema: { type: 'object' } }] }, callTool: async (p) => { calls.push(p); return { content: [{ type: 'text', text: 'saved' }] } } }
 const pi = { registerTool(t) { tools.push(t) } }
 const ctx = { sessionManager: a }
 const result = await assistantBeforeTurn(rt, pi, ctx, { systemPrompt: 'base' }, new Set())
 assert.equal(result.systemPrompt, 'base\n\npolicy A')
 assert.deepEqual(result.message, { customType: 'persona-assistant-context', content: 'fresh snapshot A', display: false })
 assert.equal(calls[0].sessionId, 'a')
 await tools[0].execute('call', {}, undefined, undefined, ctx)
 assert.equal(calls[1].session_id, 'a')
 await assert.rejects(tools[0].execute('call', {}, undefined, undefined, { sessionManager: b }), /unavailable/)
 recordAssistant(a, { assistant: true, context: 'fresh A2', supervision_instructions: '' })
 assert.equal(assistantState(a).policy, '')
 assert.equal(assistantState(b).policy, 'policy B')
 recordAssistant(a, { assistant: true, closed: true })
 await assert.rejects(assistantBeforeTurn(rt, pi, ctx, {}, new Set()), /retired/)
 await assert.rejects(tools[0].execute('call', {}, undefined, undefined, ctx), /unavailable/)
})

test('unsupported pi modes/models fail before a prompt and absent policy support fails closed', () => {
 const sm = manager('a')
 assert.throws(() => recordAssistant(sm, { assistant: true, permission_mode: 'plan' }), /permission mode/)
 assert.throws(() => recordAssistant(sm, { assistant: true, model: 'unresolved-model' }), /model/)
 assert.throws(() => recordAssistant({ getEntries: () => [] }, { assistant: true, supervision_instructions: 'policy' }), /Update pi/)
 assert.equal(assistantState(sm), undefined)
})
