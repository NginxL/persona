const text = { type: 'string' }
const task = { task_id: text }
const message = { message_id: text, text }

export const a2aTools = [
  tool('a2a_sessions', 'Find sessions on this Persona Manager.', { machine_id: text }),
  tool('a2a_get', 'Read an A2A task or list this session’s tasks.', { ...task, before: text }),
  tool('a2a_send', 'Send work to another session, launch an Agent, or answer a task question. Keep message_id on retries.', {
    ...task, ...message, machine_id: text, session_id: text, cwd: text, launcher: text,
    context_id: text, wake: { type: 'boolean' },
  }, ['message_id', 'text']),
  tool('a2a_report', 'Report progress, request input, submit a result, or report an A2A task failure. Keep message_id on retries.', {
    ...task, ...message, state: { type: 'string', enum: ['progress', 'input_required', 'completed', 'failed'] },
  }, ['task_id', 'message_id', 'state', 'text']),
  tool('a2a_cancel', 'Cancel an A2A task you sent when cancellation is available.', task, ['task_id']),
]

function tool(name, description, properties, required = []) {
  return { name, description, input: { type: 'object', properties, required, additionalProperties: false } }
}
