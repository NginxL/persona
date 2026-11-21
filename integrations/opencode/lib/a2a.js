import { a2aTools } from '../../runtime-sdk/lib/a2a.js'

export async function registerA2A(ctx, runtime) {
  if (typeof ctx.tool?.transform !== 'function') return
  await ctx.tool.transform((editor) => {
    for (const tool of a2aTools) {
      editor.add({
        ...tool,
        options: { codemode: false },
        async execute(args, { sessionID, signal }) {
          if (!sessionID) throw new Error('Persona A2A requires an OpenCode session')
          const result = await runtime.callTool({ name: tool.name, args, session_id: sessionID }, { signal })
          const content = (result.content ?? []).filter((part) => part.type === 'text').map((part) => part.text).join('\n')
          if (result.is_error) throw new Error(content || 'Persona A2A is unavailable')
          return { content }
        },
      })
    }
  })
}
