import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { MCP_SERVER_NAMES } from '../../runtime-sdk/lib/compat.js'
import { page } from '../../runtime-sdk/index.js'
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

  it('does not let a call that never got a result shift the results of later calls', () => {
    const at = '2026-11-05T10:00:00Z'
    const step = (step_index, type, extra = {}) => ({ step_index, type, source: type === 'USER_INPUT' ? 'USER_EXPLICIT' : 'MODEL', status: 'DONE', created_at: at, ...extra })
    const call = (name) => ({ name, args: {} })
    const { items } = stepsToItems([
      step(0, 'USER_INPUT', { content: 'go' }),
      step(1, 'PLANNER_RESPONSE', { tool_calls: [call('view_file'), call('run_command')] }),
      step(2, 'GENERIC', { content: 'first' }), // the second call was interrupted: no result
      step(3, 'USER_INPUT', { content: 'again' }),
      step(4, 'PLANNER_RESPONSE', { tool_calls: [call('view_file')] }),
      step(5, 'GENERIC', { content: 'second' }),
      step(6, 'PLANNER_RESPONSE', { thinking: 'more', tool_calls: [call('view_file'), call('grep_search')] }),
      step(7, 'SYSTEM_MESSAGE', { content: 'a background task finished' }),
      step(8, 'GENERIC', { content: 'third' }),
      step(9, 'GENERIC', { content: 'fourth' }),
    ])
    const uses = items.filter((item) => item.kind === 'tool_use')
    const results = new Map(items.filter((item) => item.kind === 'tool_result').map((item) => [item.tool_use_id, item.text]))
    assert.deepEqual(uses.map((use) => results.get(use.tool_use_id)), ['first', undefined, 'second', 'third', 'fourth'])
  })

  it('omits unresolvable step.media image items while preserving Read ViewedImages metadata and user attached_images', () => {
    const steps = [
      {
        step_index: 0,
        source: 'USER_EXPLICIT',
        type: 'USER_INPUT',
        status: 'DONE',
        created_at: '2026-11-19T10:00:00Z',
        content: '<USER_REQUEST>\nCheck this screenshot\n\n<attached_images>\n/tmp/user-upload.png\n</attached_images>\n</USER_REQUEST>',
        media: [{ mime_type: 'image/png', uri: '/tmp/user-upload.png' }],
      },
      {
        step_index: 1,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-19T10:00:01Z',
        tool_calls: [
          { name: 'read_file', args: { Path: '/work/shots/ui.png' } },
          { name: 'view_file', args: { AbsolutePath: '/work/shots/detail.jpg' } },
          { name: 'run_command', args: { CommandLine: 'capture-screen' } },
        ],
      },
      {
        step_index: 2,
        source: 'MODEL',
        type: 'GENERIC',
        status: 'DONE',
        created_at: '2026-11-19T10:00:02Z',
        content: 'Read image /work/shots/ui.png',
        media: [
          { mime_type: 'image/png', uri: '/work/shots/ui.png' },
          { mime_type: 'application/pdf', uri: '/work/spec.pdf' },
        ],
      },
      {
        step_index: 3,
        source: 'MODEL',
        type: 'GENERIC',
        status: 'DONE',
        created_at: '2026-11-19T10:00:03Z',
        content: '',
        media: [{ mime_type: 'image/jpeg', uri: '/work/shots/detail.jpg' }],
      },
      {
        step_index: 4,
        source: 'MODEL',
        type: 'GENERIC',
        status: 'DONE',
        created_at: '2026-11-19T10:00:04Z',
        content: 'captured',
        media: [{ mime_type: 'image/webp', uri: 'steps/4/capture.webp' }],
      },
      {
        step_index: 5,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-19T10:00:05Z',
        content: 'All screenshots look aligned.',
      },
    ]

    for (const full of [false, true]) {
      const { items, toSeq, lastText } = stepsToItems(steps, { full })
      assert.equal(toSeq, 6)
      assert.equal(lastText, 'All screenshots look aligned.')
      assert.equal(items.some((it) => it.kind === 'image'), false, `full=${full}: must not emit image items`)
      assert.equal(items.some((it) => 'lazy' in it || Boolean(it.image?.lazy)), false, `full=${full}: must not emit lazy image placeholders`)

      const userItems = items.filter((it) => it.role === 'user' && it.kind !== 'tool_result')
      assert.equal(userItems.length, 1)
      assert.equal(userItems[0].kind, 'text')
      assert.equal(userItems[0].text, 'Check this screenshot\n\n<attached_images>\n/tmp/user-upload.png\n</attached_images>')

      const uses = items.filter((it) => it.kind === 'tool_use')
      const results = items.filter((it) => it.kind === 'tool_result')
      assert.equal(uses.length, 3)
      assert.equal(results.length, 3)
      assert.equal(uses[0].tool_name, 'Read')
      assert.deepEqual(uses[0].tool_input, { file_path: '/work/shots/ui.png' })
      assert.equal(uses[1].tool_name, 'Read')
      assert.deepEqual(uses[1].tool_input, { file_path: '/work/shots/detail.jpg' })
      assert.equal(uses[2].tool_name, 'Bash')
      assert.deepEqual(
        results.map((r) => [r.tool_use_id, r.is_error, r.text]),
        [
          [uses[0].tool_use_id, false, 'Read image /work/shots/ui.png'],
          [uses[1].tool_use_id, false, ''],
          [uses[2].tool_use_id, false, 'captured'],
        ],
      )

      // Turn pagination must stay intact: tool results with step.media must not look like extra user turns.
      const paged = page(items, { turns: 1 }, { history: 'h', toSeq })
      assert.equal(paged.has_more, false, `full=${full}: single turn must not be split by tool media`)
      assert.equal(paged.items[0].id, userItems[0].id)
    }

    // Photo-only user uploads carried via <attached_images> still produce the user text item without lazy image items.
    const photoOnly = stepsToItems([
      {
        step_index: 0,
        source: 'USER_EXPLICIT',
        type: 'USER_INPUT',
        status: 'DONE',
        created_at: '2026-11-19T10:01:00Z',
        content: '<USER_REQUEST>\n<attached_images>\n/tmp/only-photo.png\n</attached_images>\n</USER_REQUEST>',
        media: [{ mime_type: 'image/png', uri: '/tmp/only-photo.png' }],
      },
    ])
    assert.equal(photoOnly.items.length, 1)
    assert.equal(photoOnly.items[0].kind, 'text')
    assert.equal(photoOnly.items[0].text, '<attached_images>\n/tmp/only-photo.png\n</attached_images>')
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
