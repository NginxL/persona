// Per-session state; policy uses only the system-prompt hook.
import { RpcError } from '../../runtime-sdk/index.js'

export const ASSISTANT_ENTRY = 'persona-assistant-state'

export function assistantState(sm) {
  return [...sm.getEntries()].reverse().find(e => e.type === 'custom' && e.customType === ASSISTANT_ENTRY)?.data
}

export function recordAssistant(sm, params, previous = assistantState(sm)) {
  if (params.assistant !== true && !previous) return undefined
  const mode = params.permission_mode || params.mode || previous?.mode || 'default'
  if (!['default', 'bypassPermissions'].includes(mode)) throw new RpcError('This pi session cannot apply that permission mode', 'readonly_unsupported')
  if (params.model || params.effort) throw new RpcError('Choose this pi session’s model before sending', 'model_unsupported')
  if (typeof sm.appendCustomEntry !== 'function') throw new RpcError('Update pi to enable assistant sessions', 'assistant_supervision_runtime')
  const next = {
    mode,
    policy: typeof params.supervision_instructions === 'string' ? params.supervision_instructions : previous?.policy || '',
    context: typeof params.context === 'string' ? params.context : previous?.context || '',
    memoryDir: typeof params.memory_dir === 'string' ? params.memory_dir : previous?.memoryDir || '',
    closed: params.closed === true,
  }
  sm.appendCustomEntry(ASSISTANT_ENTRY, next)
  return next
}

// pi's API is session-local; tools recheck the actual caller's session.
export async function assistantBeforeTurn(rt, pi, ctx, event, installed) {
  const id = ctx.sessionManager.getSessionId()
  const state = assistantState(ctx.sessionManager)
  if (!state) return undefined
  if (state.closed) throw new RpcError('This execution session is retired', 'conversation_retired')
  const tools = await rt.tools({ assistant: true, sessionId: id, signal: ctx.signal })
  for (const tool of tools) {
    const key = `${id}:${tool.name}`
    if (installed.has(key)) continue
    pi.registerTool({
      name: tool.name, label: tool.name, description: tool.description || tool.name,
      parameters: tool.input_schema || { type: 'object', properties: {} },
      async execute(_callId, args, signal, _update, actual) {
        const current = actual.sessionManager.getSessionId()
        const authority = assistantState(actual.sessionManager)
        if (current !== id || !authority || authority.closed) throw new RpcError('Tool unavailable in this session', 'conversation_retired')
        const result = await rt.callTool({ name: tool.name, args, session_id: current, assistant: true }, { signal })
        return { content: result.content || [], details: { isError: result.is_error === true } }
      },
    })
    installed.add(key)
  }
  const out = { systemPrompt: [event.systemPrompt || '', state.policy].filter(Boolean).join('\n\n') }
  if (state.context) out.message = { customType: 'persona-assistant-context', content: state.context, display: false }
  return out
}
