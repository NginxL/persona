#!/usr/bin/env node
// agy PreToolUse hook (lib/media-guard.js): reads the hook payload on stdin, answers one JSON decision on stdout.
import { readFileSync } from 'node:fs'
import { parseArgs } from 'node:util'
import { decide } from './lib/media-guard.js'

const { values } = parseArgs({ options: { state: { type: 'string' } }, strict: false })
let payload
try { payload = JSON.parse(readFileSync(0, 'utf8')) } catch {}
process.stdout.write(JSON.stringify(decide(payload, values.state)) + '\n')
