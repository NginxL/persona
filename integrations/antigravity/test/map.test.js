import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { MCP_SERVER_NAMES } from '../../runtime-sdk/lib/compat.js'
import {
  agyModeToPersona,
  cleanGenericOutput,
  cleanUserText,
  mapToolCall,
  mapToolInput,
  mapToolName,
  personaModeToAgy,
  stepsToItems,
  toAgyId,
  toPersonaId,
} from '../lib/map.js'

describe('antigravity transcript and tool mapping', () => {
  it('keeps reply previews within the latest explicit user turn', () => {
    const question = { type: 'PLANNER_RESPONSE', content: 'Which configuration should I use?' }
    const prompt = { type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'What were the results?' }
    const tools = [
      { type: 'PLANNER_RESPONSE', thinking: 'Should I inspect the logs?', tool_calls: [{ name: 'run_command', args: { CommandLine: 'cat result.log' } }] },
      { type: 'GENERIC', content: 'Retry the command?' },
    ]
    const cases = [
      ['user question alone', [prompt], ''],
      ['answered assistant question', [question, prompt], ''],
      ['thinking and tools after user question', [question, prompt, ...tools], ''],
      ['new assistant question', [prompt, ...tools, question], question.content],
      ['empty explicit prompt', [question, { ...prompt, content: '' }], ''],
      ['system input preserves assistant reply', [question, { ...prompt, source: 'SYSTEM' }], question.content],
    ]
    for (const [name, steps, expected] of cases) {
      assert.equal(stepsToItems(steps).lastText, expected, name)
    }
    const { items } = stepsToItems([prompt, ...tools])
    assert.equal(items[0].role, 'user')
    assert.equal(items[0].text, prompt.content)
    assert.equal(items.at(-1).text, tools[1].content)
  })

  it('converts session ids between persona and agy', () => {
    assert.equal(toPersonaId('ad40e999-0253-4097-8ee2-a70f9259e88b'), 'agy-ad40e999-0253-4097-8ee2-a70f9259e88b')
    assert.equal(toPersonaId('agy-ad40e999-0253-4097-8ee2-a70f9259e88b'), 'agy-ad40e999-0253-4097-8ee2-a70f9259e88b')
    assert.equal(toAgyId('agy-ad40e999-0253-4097-8ee2-a70f9259e88b'), 'ad40e999-0253-4097-8ee2-a70f9259e88b')
  })

  it('extracts user request and strips persona-context', () => {
    const raw = `<USER_REQUEST>\n<persona-context>\nsnapshot\n</persona-context>\n\nHello world\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\ntime\n</ADDITIONAL_METADATA>`
    assert.equal(cleanUserText(raw), 'Hello world')
  })

  it('maps tool names and inputs to Persona conventions', () => {
    assert.equal(mapToolName('run_command'), 'Bash')
    assert.deepEqual(mapToolInput('run_command', { CommandLine: 'npm test', toolSummary: 'Run tests' }), {
      command: 'npm test',
      description: 'Run tests',
    })
    assert.equal(mapToolName('view_file'), 'Read')
    assert.deepEqual(mapToolInput('view_file', { AbsolutePath: '/tmp/a.txt' }), { file_path: '/tmp/a.txt' })
    assert.equal(mapToolName('replace_file_content'), 'Edit')
    assert.deepEqual(
      mapToolInput('replace_file_content', {
        TargetFile: '/tmp/a.txt',
        TargetContent: 'old',
        ReplacementContent: 'new',
      }),
      { file_path: '/tmp/a.txt', old_string: 'old', new_string: 'new' },
    )
  })

  it('normalizes Antigravity eager, scoped, compatibility, and proxied Persona MCP tool names', () => {
    for (const server of MCP_SERVER_NAMES) {
      for (const prefix of [`mcp_${server}`, `mcp_${server}_0c671287`, `mcp_${server}_55a1c59d`]) {
        assert.equal(mapToolName(`${prefix}_notify_user`), 'mcp__persona__notify_user')
        assert.equal(mapToolName(`${prefix}_ask_user`), 'mcp__persona__ask_user')
        assert.equal(mapToolName(`${prefix}_task_review`), 'mcp__persona__task_review')
        assert.equal(mapToolName(`${prefix}_supervision_finish`), 'mcp__persona__supervision_finish')
        assert.equal(mapToolName(`${prefix}_persona_settings`), 'mcp__persona__persona_settings')
      }
      assert.deepEqual(
        mapToolCall(`mcp_${server}_0c671287_persona_call`, {
          name: 'task_review',
          arguments: { todo_id: 'todo_1', verdict: 'accepted' },
        }),
        {
          toolName: 'mcp__persona__task_review',
          toolInput: { todo_id: 'todo_1', verdict: 'accepted' },
        },
      )
      assert.deepEqual(mapToolCall(`mcp_${server}_persona_call`, {}), {
        toolName: 'mcp__persona__persona_call',
        toolInput: {},
      })
    }
    assert.equal(mapToolName('mcp_github_create_issue'), 'mcp_github_create_issue')
    assert.equal(mapToolName('mcp__github__create_issue'), 'mcp__github__create_issue')
    assert.equal(mapToolName('mcp_persona_other_notify_user'), 'mcp__persona__other_notify_user')

    const { items } = stepsToItems([
      {
        step_index: 0,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-18T10:00:00Z',
        tool_calls: [
          { name: 'mcp_persona_0c671287_notify_user', args: { message: 'Reminder ready' } },
          {
            name: 'mcp_persona_55a1c59d_persona_call',
            args: { name: 'task_review', arguments: { todo_id: 'todo_1', verdict: 'accepted' } },
          },
        ],
      },
    ])
    assert.equal(items[0].tool_name, 'mcp__persona__notify_user')
    assert.deepEqual(items[0].tool_input, { message: 'Reminder ready' })
    assert.equal(items[1].tool_name, 'mcp__persona__task_review')
    assert.deepEqual(items[1].tool_input, { todo_id: 'todo_1', verdict: 'accepted' })
  })

  it('maps transcript steps into ordered Persona items with paired tool results', () => {
    const steps = [
      {
        step_index: 0,
        source: 'USER_EXPLICIT',
        type: 'USER_INPUT',
        status: 'DONE',
        created_at: '2026-11-05T10:00:00Z',
        content: '<USER_REQUEST>\nCheck files\n</USER_REQUEST>',
      },
      {
        step_index: 1,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-05T10:00:01Z',
        thinking: 'Listing files first',
        content: 'I will list the directory.',
        tool_calls: [{ name: 'run_command', args: { CommandLine: 'ls -la', toolSummary: 'List files' } }],
      },
      {
        step_index: 2,
        source: 'MODEL',
        type: 'GENERIC',
        status: 'DONE',
        created_at: '2026-11-05T10:00:02Z',
        content: 'Created At: 2026-11-05T10:00:02Z\nOutput:\ntotal 0\n',
      },
    ]

    const { items, toSeq, lastText } = stepsToItems(steps)
    assert.equal(toSeq, 3)
    assert.equal(lastText, 'I will list the directory.')
    assert.equal(items.length, 5)
    assert.equal(items[0].kind, 'text')
    assert.equal(items[0].role, 'user')
    assert.equal(items[0].text, 'Check files')
    assert.equal(items[1].kind, 'thinking')
    assert.equal(items[2].kind, 'text')
    assert.equal(items[3].kind, 'tool_use')
    assert.equal(items[3].tool_name, 'Bash')
    assert.equal(items[4].kind, 'tool_result')
    assert.equal(items[4].tool_use_id, items[3].tool_use_id)
    assert.equal(items[4].text.trim(), 'total 0')
  })

  it('maps modes between Persona and Antigravity ACP', () => {
    assert.equal(personaModeToAgy('plan'), 'plan')
    assert.equal(personaModeToAgy('acceptEdits'), 'accept-edits')
    assert.equal(personaModeToAgy('auto'), 'auto')
    assert.equal(agyModeToPersona('auto'), 'auto')
    assert.equal(personaModeToAgy('default'), 'default')
    assert.equal(agyModeToPersona('accept-edits'), 'acceptEdits')
    assert.equal(cleanGenericOutput('Created At: 2026\nOutput:\nhello'), 'hello')
  })
})
