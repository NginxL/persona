import assert from 'node:assert/strict'
import { test } from 'node:test'
import { localMediaPath } from '../lib/media.js'

test('POSIX media paths allow ordinary colons without accepting non-file URI schemes', () => {
  assert.equal(localMediaPath('/captures/09:30.png', false), '/captures/09:30.png')
  assert.equal(localMediaPath('file:///captures/09%3a30.png', false), '/captures/09:30.png')
  assert.equal(localMediaPath('captures/09:30.png', false), 'captures/09:30.png')
  for (const uri of ['https://example.test/image.png', 'data:image/png;base64,AA==', 'file:captures/image.png']) {
    assert.equal(localMediaPath(uri, false), '', uri)
  }
})

test('Windows media paths allow local volumes and reject UNC, devices, and drive-relative paths', () => {
  for (const uri of [String.raw`C:\captures\screen one.png`, 'C:/captures/screen one.png',
    'file:///C:/captures/screen%20one.png', 'file://localhost/C:/captures/screen%20one.png']) {
    assert.equal(localMediaPath(uri, true), String.raw`C:\captures\screen one.png`, uri)
  }
  assert.equal(localMediaPath('captures/screen.png', true), String.raw`captures\screen.png`)
  for (const uri of [String.raw`\\host\share\screen.png`, String.raw`\\?\C:\screen.png`, String.raw`\\.\PhysicalDrive0`,
    'file://host/share/screen.png', 'file:////host/share/screen.png', '/screen.png', 'C:screen.png',
    'C:/screen.png:stream', 'C:/captures/NUL.png', 'captures/COM1', String.raw`..\outside.png`,
    'file:///C:/captures%5cscreen.png']) {
    assert.equal(localMediaPath(uri, true), '', uri)
  }
})
