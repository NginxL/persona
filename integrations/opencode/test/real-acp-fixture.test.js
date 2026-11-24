import assert from 'node:assert/strict'
import { test } from 'node:test'
import { fakeModel } from './real-acp-fixture.mjs'

test('fake model rejects port discovery and still answers native completion calls', async (t) => {
  const model = await fakeModel(t)
  const probe = await fetch(new URL('/', model.url), { headers: { 'User-Agent': 'persona-ports' } })
  await probe.text()
  assert.equal(probe.status, 404)
  assert.deepEqual(model.requests, [])

  const response = await fetch(`${model.url}/chat/completions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: 'Continue' }] }),
  })
  const body = await response.json()
  assert.equal(response.status, 200)
  assert.equal(body.object, 'chat.completion')
  assert.equal(model.requests.length, 1)
  assert.equal(model.requests[0].error, undefined)
})
