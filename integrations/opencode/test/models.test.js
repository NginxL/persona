import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  buildLauncher,
  extractEfforts,
  normalizeModels,
  parseAcpEffortOptions,
  parseAcpModelOptions,
  parseCliModels,
  qualifyModelId,
  readConfiguredModel,
  resolveModel,
  splitModelId,
  stripJsonc,
} from '../lib/models.js'

test('stripJsonc and readConfiguredModel handle comments, trailing commas, and URLs inside strings', () => {
  const dir = mkdtempSync(join(tmpdir(), 'oc-models-'))
  try {
    const raw = `{
      // line comment
      "model": "opencode-go/claude-sonnet-4-6",
      /* block comment */
      "provider": { "url": "https://example.test//keep", },
    }`
    const parsed = JSON.parse(stripJsonc(raw))
    assert.equal(parsed.model, 'opencode-go/claude-sonnet-4-6')
    assert.equal(parsed.provider.url, 'https://example.test//keep')

    writeFileSync(join(dir, 'opencode.jsonc'), raw)
    assert.equal(readConfiguredModel({ configDir: dir }), 'opencode-go/claude-sonnet-4-6')
    assert.equal(readConfiguredModel({ configDir: join(dir, 'missing') }), '')

    const withCommaBracesInStrings = `{
      "pattern": "\\",}",
      "items": "[a, b,]",
      "model": "opencode-go/claude-sonnet-4-6", // trailing comma before comment
    }`
    const parsedStrings = JSON.parse(stripJsonc(withCommaBracesInStrings))
    assert.equal(parsedStrings.pattern, '",}')
    assert.equal(parsedStrings.items, '[a, b,]')
    assert.equal(parsedStrings.model, 'opencode-go/claude-sonnet-4-6')
    writeFileSync(join(dir, 'opencode.jsonc'), withCommaBracesInStrings)
    assert.equal(readConfiguredModel({ configDir: dir }), 'opencode-go/claude-sonnet-4-6')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('parseCliModels parses both `opencode models --verbose` JSON blocks and plain lines', () => {
  const verbose = [
    'opencode-go/claude-sonnet-4-6',
    '{',
    '  "id": "claude-sonnet-4-6",',
    '  "providerID": "opencode-go",',
    '  "name": "Claude Sonnet 4.6",',
    '  "status": "active",',
    '  "limit": { "context": 200000, "output": 64000 },',
    '  "variants": { "high": {}, "low": {}, "xhigh": {}, "weird": {} }',
    '}',
    'openrouter/anthropic/claude-3.7-sonnet',
    '',
    'openai/gpt-5',
  ].join('\n')

  const raw = parseCliModels(verbose)
  assert.equal(raw.length, 3)
  assert.equal(raw[0].providerID, 'opencode-go')
  assert.equal(raw[0].id, 'claude-sonnet-4-6')
  assert.equal(raw[0].limit.context, 200000)
  assert.deepEqual(extractEfforts(raw[0].variants), ['low', 'high', 'xhigh'])
  assert.deepEqual(extractEfforts([{ id: 'MAX' }, 'medium', 'unknown']), ['medium', 'max'])
  assert.equal(raw[1].providerID, 'openrouter')
  assert.equal(raw[1].id, 'anthropic/claude-3.7-sonnet')
  assert.equal(raw[2].providerID, 'openai')
  assert.equal(raw[2].id, 'gpt-5')
})

test('parseAcpModelOptions extracts model options and current default', () => {
  const parsed = parseAcpModelOptions([
    { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build', name: 'Build' }] },
    {
      id: 'model',
      category: 'model',
      currentValue: 'opencode-go/gpt-5',
      options: [
        { value: 'opencode-go/gpt-5', name: 'opencode-go/GPT-5' },
        { value: 'anthropic/claude-sonnet-4-6', name: 'Claude Sonnet' },
      ],
    },
  ])
  assert.equal(parsed.currentModel, 'opencode-go/gpt-5')
  assert.deepEqual(parsed.models.map((m) => m.qualifiedId), ['opencode-go/gpt-5', 'anthropic/claude-sonnet-4-6'])
})

test('normalizeModels deduplicates, marks default first, puts deprecated last, builds windows, and caps at 200', () => {
  const raw = [
    { providerID: 'prov', id: 'old', name: 'Old', status: 'deprecated', limit: { context: 8000 } },
    { providerID: 'prov', id: 'disabled', name: 'Off', enabled: false },
    { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', limit: { context: 200000 }, variants: ['high', 'low'] },
    { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Duplicate' },
    { providerID: 'openrouter', id: 'deepseek/deepseek-r1', name: 'DeepSeek R1', limit: { context: 128000 } },
  ]
  const res = normalizeModels(raw, 'opencode-go/claude-sonnet-4-6')
  assert.equal(res.defaultModel, 'opencode-go/claude-sonnet-4-6')
  assert.deepEqual(res.models.map((m) => m.id), [
    'opencode-go/claude-sonnet-4-6',
    'openrouter/deepseek/deepseek-r1',
    'prov/old',
  ])
  assert.equal(res.models[0].default, true)
  assert.deepEqual(res.models[0].efforts, ['low', 'high'])
  assert.equal(res.windows.get('opencode-go\0claude-sonnet-4-6'), 200000)
  assert.equal(res.windows.get('opencode-go/claude-sonnet-4-6'), 200000)
  assert.equal(res.windows.get('claude-sonnet-4-6'), 200000)

  const big = Array.from({ length: 250 }, (_, i) => ({ providerID: 'p', id: `m${i}` }))
  assert.equal(normalizeModels(big).models.length, 200)
})

test('resolveModel handles provider/model, nested slashes, provider:model, bare matches, and rejects unknown bare IDs', () => {
  const catalog = [
    { id: 'opencode-go/claude-sonnet-4-6', name: 'opencode-go/Claude Sonnet 4.6', default: true },
    { id: 'openrouter/anthropic/claude-3.7-sonnet', name: 'openrouter/Claude 3.7' },
    { id: 'openai/gpt-5', name: 'openai/GPT-5' },
  ]
  assert.equal(resolveModel('', catalog), null)
  assert.deepEqual(resolveModel('opencode-go/claude-sonnet-4-6', catalog), {
    id: 'opencode-go/claude-sonnet-4-6',
    providerID: 'opencode-go',
    modelID: 'claude-sonnet-4-6',
  })
  assert.deepEqual(resolveModel('opencode-go:claude-sonnet-4-6', catalog), {
    id: 'opencode-go/claude-sonnet-4-6',
    providerID: 'opencode-go',
    modelID: 'claude-sonnet-4-6',
  })
  assert.deepEqual(resolveModel('openrouter/anthropic/claude-3.7-sonnet', catalog), {
    id: 'openrouter/anthropic/claude-3.7-sonnet',
    providerID: 'openrouter',
    modelID: 'anthropic/claude-3.7-sonnet',
  })
  assert.deepEqual(resolveModel('openrouter:anthropic/claude-3.7-sonnet', catalog), {
    id: 'openrouter/anthropic/claude-3.7-sonnet',
    providerID: 'openrouter',
    modelID: 'anthropic/claude-3.7-sonnet',
  })
  assert.deepEqual(resolveModel('gpt-5', catalog), {
    id: 'openai/gpt-5',
    providerID: 'openai',
    modelID: 'gpt-5',
  })
  // Explicit provider/model not in catalog is passed through.
  assert.deepEqual(resolveModel('opencode-go/new-model', catalog), {
    id: 'opencode-go/new-model',
    providerID: 'opencode-go',
    modelID: 'new-model',
  })
  assert.throws(() => resolveModel('nonexistent-bare-model', catalog), (err) => err.code === 'bad_request')
  assert.equal(qualifyModelId('p', 'p/m'), 'p/m')
  assert.deepEqual(splitModelId('p:m'), { providerID: 'p', modelID: 'm' })
})

test('buildLauncher populates name, display_name, agent, model, and models', () => {
  const models = [{ id: 'opencode-go/gpt-5', name: 'opencode-go/GPT-5', default: true }]
  assert.deepEqual(buildLauncher({ name: 'opencode', displayName: 'OpenCode', models }), {
    name: 'opencode',
    display_name: 'OpenCode',
    agent: 'opencode',
    model: 'opencode-go/gpt-5',
    models,
  })
})

test('resolveModel matches bare IDs containing colons or slashes and rejects ambiguous bare IDs across providers', () => {
  const catalog = [
    { id: 'ollama/llama3:8b', name: 'ollama/Llama 3 8B' },
    { id: 'openrouter/anthropic/claude-x', name: 'openrouter/Claude X' },
    { id: 'openai/gpt-5', name: 'openai/GPT-5', default: true },
    { id: 'openrouter/gpt-5', name: 'openrouter/GPT-5' },
  ]
  assert.deepEqual(resolveModel('llama3:8b', catalog), {
    id: 'ollama/llama3:8b',
    providerID: 'ollama',
    modelID: 'llama3:8b',
  })
  assert.deepEqual(resolveModel('anthropic/claude-x', catalog), {
    id: 'openrouter/anthropic/claude-x',
    providerID: 'openrouter',
    modelID: 'anthropic/claude-x',
  })
  assert.throws(
    () => resolveModel('gpt-5', catalog),
    (err) => err.code === 'bad_request' && err.message.includes('openai/gpt-5') && err.message.includes('openrouter/gpt-5'),
  )
  // Unknown colon-bearing string with non-provider prefix is rejected rather than turned into a bogus provider.
  assert.throws(
    () => resolveModel('unknown-tag:8b', catalog),
    (err) => err.code === 'bad_request',
  )
})

test('parseAcpEffortOptions extracts variant/reasoning_effort/thought_level options and normalizeModels separates explicitDefault', () => {
  const parsed = parseAcpEffortOptions([
    { id: 'mode', category: 'mode', currentValue: 'build', options: [] },
    {
      id: 'variant',
      category: 'reasoning',
      currentValue: 'high',
      options: [{ value: 'low' }, { value: 'high' }, { value: 'custom' }],
    },
  ])
  assert.deepEqual(parsed, { configId: 'variant', currentEffort: 'high', efforts: ['low', 'high'] })

  const noDefault = normalizeModels([{ providerID: 'p', id: 'm1' }, { providerID: 'p', id: 'm2' }])
  assert.equal(noDefault.explicitDefault, '')
  assert.equal(noDefault.defaultModel, 'p/m1')
})
