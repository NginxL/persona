import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { describe, it } from 'node:test'
import { MCP_SERVER_NAMES } from '../../runtime-sdk/lib/compat.js'
import { page } from '../../runtime-sdk/index.js'
import {
  agyModeToPersona,
  cleanAssistantText,
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

  it('projects native context resources without hiding ordinary user labels', () => {
    const user = 'Please reply OK.\nKeep this second line.'
    const label = 'Content of persona-context:///snapshot.txt:\n'
    const context = '<persona-context>Attached context with /compact</persona-context>'
    const cases = [
      ['native first request', `<USER_REQUEST>\n${label}${context}${user}\n</USER_REQUEST>\n<ADDITIONAL_METADATA>clock</ADDITIONAL_METADATA>`, user],
      ['unwrapped resource', label + context + user, user],
      ['another Persona URI', 'Content of persona-context:///other.txt:\r\n' + context + user, user],
      ['successive resources', label + context + '\n' + label + context + user, user],
      ['no prefix', user, user],
      ['literal label', label + user, label + user],
      ['other URI', 'Content of file:///snapshot.txt:\n' + context + user, 'Content of file:///snapshot.txt:\n' + user],
      ['similar scheme', 'Content of persona-context-extra:///snapshot.txt:\n' + context + user, 'Content of persona-context-extra:///snapshot.txt:\n' + user],
      ['quoted label', 'Example:\n' + label + user, 'Example:\n' + label + user],
      ['incomplete envelope', label + '<persona-context>unfinished', label + '<persona-context>unfinished'],
    ]
    for (const [name, content, expected] of cases) {
      const steps = [{ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content }]
      const { items } = stepsToItems(steps)
      assert.equal(items[0].text, expected, name)
      assert.equal(items[0].role, 'user', name)
      assert.equal(steps[0].content, content, 'native content stays unchanged')
    }
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

  it('uses native indices for files and client messages after projected steps move', () => {
    const brainDir = mkdtempSync(join(tmpdir(), 'agy-native-index-'))
    try {
      const output = join(brainDir, '.system_generated', 'steps', '8', 'output.txt')
      mkdirSync(dirname(output), { recursive: true })
      writeFileSync(output, 'complete native output')
      const { items } = stepsToItems([
        { step_index: 8, native_step_index: 7, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'hello' },
        { step_index: 9, native_step_index: 8, type: 'GENERIC', content: 'truncated', truncated_fields: ['content'] },
      ], { full: true, brainDir, clientMsgIdFor: (index) => `client-${index}` })
      assert.equal(items[0].client_msg_id, 'client-7')
      assert.equal(items[1].text, 'complete native output')
    } finally {
      rmSync(brainDir, { recursive: true, force: true })
    }
  })

  it('keeps a delivered reply and attachment outside a later native wake on pages and catch-up', () => {
    const steps = readFileSync(new URL('./fixtures/autonomous-wake.jsonl', import.meta.url), 'utf8').trim().split('\n').map((line) => JSON.parse(line))
    const prior = stepsToItems(steps.slice(0, 3), { full: true })
    const pending = stepsToItems(steps.slice(0, 5), { full: true })
    const all = stepsToItems(steps, { full: true })
    assert.deepEqual(pending.items, prior.items, 'a notification alone is not a visible wake')
    assert.equal(pending.toSeq, 5)
    assert.deepEqual(all.items.slice(0, 2), prior.items)
    assert.match(prior.items[1].text, /file: \/synthetic\/chart\.png/)
    const catchUp = page(all.items, { after_seq: pending.toSeq }, { toSeq: all.toSeq })
    assert.deepEqual(catchUp.items.map((item) => [item.seq, item.kind, item.wake]), [
      [6, 'notice', true], [6, 'text', undefined], [8, 'text', undefined], [9, 'text', undefined],
    ])
    assert.equal(catchUp.items[0].id, prior.items[1].id.replace(/#\d+$/, '#-1'))
    assert.deepEqual(stepsToItems(steps).items.map((item) => item.id), all.items.map((item) => item.id))
    assert.doesNotMatch(JSON.stringify(all.items), /Synthetic|housekeeping|second notification/)
    const checkpoint = { step_index: 4, type: 'CHECKPOINT', created_at: steps[4].created_at }
    const compacted = [...steps.slice(0, 4), checkpoint]
    assert.deepEqual(stepsToItems(compacted).items.slice(-1).map(item => [item.kind, item.text, item.wake]), [['notice', '上下文已压缩', undefined]])
    const afterCheckpoint = stepsToItems([...compacted, steps[5]])
    assert.deepEqual(afterCheckpoint.items.slice(-2).map(item => item.id), catchUp.items.slice(0, 2).map(item => item.id))
    const emptyPlanner = { step_index: 5, type: 'PLANNER_RESPONSE', status: 'RUNNING' }
    assert.deepEqual(stepsToItems([...compacted, emptyPlanner, ...steps.slice(7)]).items.filter(item => item.wake), [])
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

  it('opts into lazy tool media without changing Read metadata, user attachments, or turn pagination', () => {
    const inline = { mime_type: 'image/gif', data: 'R0lGODlh' }
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
          inline,
          { mime_type: 'image/png' },
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

      const mapped = stepsToItems(steps, { full, includeMedia: true })
      const images = mapped.items.filter((item) => item.kind === 'image')
      const expected = [[0, 0, 'png'], [0, 2, 'gif'], [1, 0, 'jpeg'], [2, 0, 'webp']]
      assert.deepEqual(images, expected.map(([resultIndex, mediaIndex, format]) => {
        const { id, seq, tool_use_id, ts } = results[resultIndex]
        return { id: id.replace(/#0$/, `#media${mediaIndex}`), seq, role: 'assistant', kind: 'image', tool_use_id, ts,
          image: { mime: `image/${format}`, data: '', lazy: true } }
      }))
      assert.equal(mapped.media, undefined)
      assert.equal(stepsToItems(steps, { includeMedia: true, mediaItemId: images[1].id }).media, inline)
      assert.equal(stepsToItems(steps, { mediaItemId: images[1].id }).media, undefined)
      const withMedia = page(mapped.items, { turns: 1 }, { history: 'h', toSeq: mapped.toSeq })
      assert.deepEqual({ ...withMedia, items: withMedia.items.filter((item) => item.kind !== 'image') }, paged)
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
    ], { includeMedia: true })
    assert.equal(photoOnly.items.length, 1)
    assert.equal(photoOnly.items[0].kind, 'text')
    assert.equal(photoOnly.items[0].text, '<attached_images>\n/tmp/only-photo.png\n</attached_images>')
  })

  it('normalizes local file:// Markdown links and images in assistant replies only', () => {
    const cases = [
      ['unix file link', 'See [notes.md](file:///work/repo/notes.md).', 'See [notes.md](/work/repo/notes.md).'],
      ['line fragment', 'Check [app.go](file:///work/repo/app.go#L10-L20).', 'Check [app.go](/work/repo/app.go#L10-L20).'],
      ['localhost authority', '[doc](file://localhost/work/repo/doc.md)', '[doc](/work/repo/doc.md)'],
      ['uppercase scheme and percent spaces', '[a](FILE:///work/My%20Notes/a.md)', '[a](/work/My%20Notes/a.md)'],
      ['angle-bracket destination', '[a](<file:///work/My Notes/a.md> "title")', '[a](</work/My Notes/a.md> "title")'],
      ['balanced parens and title', '[p](file:///work/photo(1).png "shot")', '[p](/work/photo(1).png "shot")'],
      ['image destination', '![chart](file:///work/repo/chart.png)', '![chart](/work/repo/chart.png)'],
      ['windows drive', '[win](file:///C:/work/notes.md#L5)', '[win](C:/work/notes.md#L5)'],
      ['windows encoded colon', '[win](file:///c%3A/work/notes.md)', '[win](c:/work/notes.md)'],
      ['reference definition', '[ref]: file:///work/repo/notes.txt "Ref"', '[ref]: /work/repo/notes.txt "Ref"'],
      ['list continuation', '- item\n    [notes](file:///work/notes.md)', '- item\n    [notes](/work/notes.md)'],
      ['inline code untouched', 'Use `[notes](file:///work/notes.md)` literally.', 'Use `[notes](file:///work/notes.md)` literally.'],
      ['fenced code untouched', '```md\n[notes](file:///work/notes.md)\n```\n[live](file:///work/live.md)', '```md\n[notes](file:///work/notes.md)\n```\n[live](/work/live.md)'],
      ['indented code untouched', 'Example:\n\n    [notes](file:///work/notes.md)\n\n[live](file:///work/live.md)', 'Example:\n\n    [notes](file:///work/notes.md)\n\n[live](/work/live.md)'],
      ['remote host untouched', '[r](file://other-host/share/notes.md)', '[r](file://other-host/share/notes.md)'],
      ['unc double slash untouched', '[u](file:////server/share/notes.md)', '[u](file:////server/share/notes.md)'],
      ['percent-encoded double slash untouched', '[u](file:///%2fserver/share/notes.md)', '[u](file:///%2fserver/share/notes.md)'],
      ['percent-encoded double slash with invalid utf8 untouched', '[u](file:///%2f%ffserver/share/notes.md)', '[u](file:///%2f%ffserver/share/notes.md)'],
      ['hub route untouched', '[api](file:///api/v1/sessions)', '[api](file:///api/v1/sessions)'],
      ['dot-segment hub route untouched', '[api](file:///./api/v1/sessions)', '[api](file:///./api/v1/sessions)'],
      ['parent-segment hub route untouched', '[api](file:///work/../api/v1/sessions)', '[api](file:///work/../api/v1/sessions)'],
      ['duplicate-slash hub route untouched', '[api](file:///api//v1/sessions)', '[api](file:///api//v1/sessions)'],
      ['percent-encoded hub route with bad percent untouched', '[api](file:///%61pi/v1/sessions%ZZ)', '[api](file:///%61pi/v1/sessions%ZZ)'],
      ['query string untouched', '[q](file:///work/notes.md?x=1)', '[q](file:///work/notes.md?x=1)'],
      ['nul byte untouched', '[n](file:///work/notes%00.md)', '[n](file:///work/notes%00.md)'],
      ['percent-encoded control char untouched', '[c](file:///work/notes%0a.md)', '[c](file:///work/notes%0a.md)'],
      ['percent literal in filename', '[p](file:///work/100%25_done.md)', '[p](/work/100%25_done.md)'],
      ['percent literal before 00 in filename', '[p](file:///work/%2500_note.md)', '[p](/work/%2500_note.md)'],
      ['percent literal before 2f in leading segment', '[p](file:///%252fdir/note.md)', '[p](/%252fdir/note.md)'],
      ['relative file scheme untouched', '[rel](file:notes.md)', '[rel](file:notes.md)'],
      ['escaped bracket untouched', 'text \\](file:///work/notes.md)', 'text \\](file:///work/notes.md)'],
      ['partial streaming link untouched', 'See [notes](file:///work/no', 'See [notes](file:///work/no'],
    ]
    for (const [name, input, expected] of cases) {
      assert.equal(cleanAssistantText(input), expected, name)
    }

    const { items, lastText } = stepsToItems([
      { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', content: 'Open [u](file:///work/u.md)' },
      {
        step_index: 1,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        thinking: 'Checking [t](file:///work/t.md)',
        content: 'Opened [notes.md](file:///work/notes.md#L12) and ![img](file:///work/img.png).',
        tool_calls: [{ name: 'view_file', args: { AbsolutePath: '/work/notes.md' } }],
      },
      { step_index: 2, source: 'MODEL', type: 'GENERIC', status: 'DONE', content: '[out](file:///work/out.md)' },
    ])
    assert.equal(items[0].text, 'Open [u](file:///work/u.md)')
    assert.equal(items[1].text, 'Checking [t](file:///work/t.md)')
    assert.equal(items[2].text, 'Opened [notes.md](/work/notes.md#L12) and ![img](/work/img.png).')
    assert.equal(items[4].text, '[out](file:///work/out.md)')
    assert.equal(lastText, 'Opened [notes.md](/work/notes.md#L12) and ![img](/work/img.png).')
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

  it('attaches per-step token usage and model in projected order and preserves usage across checkpoints and prefix cache reuse', () => {
    const at = '2026-11-21T10:00:00Z'
    const steps = [
      { step_index: 0, type: 'PERSONA_MODE_NOTICE', created_at: at, content: 'mode fallback' },
      { step_index: 1, native_step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: at, content: 'Inspect' },
      {
        step_index: 2, native_step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: at,
        model: 'claude-opus-5-5-high', thinking: 'Reading', content: 'Checking',
        input_tokens: 1200, cache_read_tokens: 48800, cache_write_tokens: 300, cache_write_1h_tokens: 100, output_tokens: 180,
        tool_calls: [{ name: 'run_command', args: { CommandLine: 'ls' } }],
      },
      { step_index: 3, native_step_index: 2, source: 'MODEL', type: 'GENERIC', status: 'DONE', created_at: at, content: 'ok' },
      { step_index: 4, native_step_index: 3, source: 'SYSTEM', type: 'CHECKPOINT', status: 'DONE', created_at: at, input_tokens: 3000, cache_read_tokens: 500000, output_tokens: 4000 },
      {
        step_index: 5, native_step_index: 4, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: at,
        model: '<synthetic>', content: 'Synthetic note', input_tokens: -5, output_tokens: -2,
      },
    ]

    for (const full of [false, true]) {
      const mapped = stepsToItems(steps, { full, cacheKey: `usage-prefix-${full}`, immutablePrefix: steps.length })
      assert.deepEqual(mapped.usage, { contextTokens: 0, contextWindow: 0, outputTokens: 180, model: 'claude-opus-5-5-high' })
      // Shifted seq 3 emitted 3 assistant items (thinking, text, tool_use): all get model, only the first gets usage.
      const seq3 = mapped.items.filter((item) => item.seq === 3)
      assert.equal(seq3.length, 3)
      assert.deepEqual(seq3.map((item) => item.model), ['claude-opus-5-5-high', 'claude-opus-5-5-high', 'claude-opus-5-5-high'])
      assert.deepEqual(seq3[0].usage, [{
        model: 'claude-opus-5-5-high', in: 1200, out: 180, cache_read: 48800, cache_write: 300, cache_write_1h: 100,
      }])
      assert.equal(seq3[1].usage, undefined)
      assert.equal(seq3[2].usage, undefined)
      // Mode notice and synthetic/negative step stay clean.
      assert.equal(mapped.items[0].usage, undefined)
      assert.equal(mapped.items[0].model, undefined)
      const seq6 = mapped.items.find((item) => item.seq === 6)
      assert.equal(seq6.model, undefined)
      assert.equal(seq6.usage, undefined)

      // Reusing the cached prefix with a post-checkpoint reply preserves cumulative outputTokens and updates contextTokens/contextWindow.
      const extended = [
        ...steps,
        {
          step_index: 6, native_step_index: 5, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: at,
          model: 'gemini-4-neon-high', content: 'After compaction',
          usage: { input_tokens: 12000, cache_read_input_tokens: 3000, cache_creation_input_tokens: 500, output_tokens: 220, model_context_window: 1048576 },
        },
      ]
      const reused = stepsToItems(extended, { full, cacheKey: `usage-prefix-${full}`, immutablePrefix: extended.length })
      assert.deepEqual(reused.usage, { contextTokens: 15500, contextWindow: 1048576, outputTokens: 400, model: 'gemini-4-neon-high' })
      const seq7 = reused.items.find((item) => item.seq === 7)
      assert.equal(seq7.model, 'gemini-4-neon-high')
      assert.deepEqual(seq7.usage, [{
        model: 'gemini-4-neon-high', in: 12000, out: 220, cache_read: 3000, cache_write: 500,
      }])
      const switched = [...extended, { step_index: 7, type: 'PLANNER_RESPONSE', model: 'custom', input_tokens: 8000 }]
      const next = stepsToItems(switched, { full, cacheKey: `usage-prefix-${full}`, immutablePrefix: switched.length })
      assert.equal(next.usage.contextTokens, 8000)
      assert.equal(next.usage.contextWindow, 0)
    }
  })
})
