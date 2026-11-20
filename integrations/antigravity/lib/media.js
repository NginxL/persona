import { constants } from 'node:fs'
import { lstat, open, readlink, realpath } from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, posix, relative, sep, win32 } from 'node:path'
import { fileURLToPath } from 'node:url'

const MAX_IMAGE_BYTES = 1152 * 1024
const MAX_IMAGE_BASE64 = MAX_IMAGE_BYTES / 3 * 4
const WINDOWS = process.platform === 'win32'
const WINDOWS_DEVICE = /^(?:con(?:in\$|out\$)?|prn|aux|nul|(?:com|lpt)[1-9¹²³])[ ]*(?:\.|$)/i
const IMAGE_SIGNATURES = new Map([
  ['image/png', [[0, Buffer.from('89504e470d0a1a0a', 'hex')]]],
  ['image/jpeg', [[0, Buffer.from('ffd8ff', 'hex')]]],
  ['image/gif', [[0, Buffer.from('GIF87a'), Buffer.from('GIF89a')]]],
  ['image/webp', [[0, Buffer.from('RIFF')], [8, Buffer.from('WEBPVP8')]]],
])

export function isImageReference(media) {
  return IMAGE_SIGNATURES.has(media?.mime_type) &&
    (media.uri == null || typeof media.uri === 'string') &&
    (media.data == null || typeof media.data === 'string') && Boolean(media.uri || media.data)
}

export function localMediaPath(uri, windows = WINDOWS) {
  const paths = windows ? win32 : posix
  let path = uri
  if (/^file:\/\//i.test(uri)) {
    if (!/^file:\/\/(?:localhost)?\//i.test(uri) || /[?#\u0000-\u001f\u007f\\]/.test(uri)) return ''
    try { path = fileURLToPath(uri, { windows }) } catch { return '' }
    if (!paths.isAbsolute(path)) return ''
  }
  const drive = windows && /^[a-z]:[\\/]/i.test(path)
  if (/^[\\/]{2}/.test(path) || path.includes('\0')) return ''
  if (windows ? ((paths.isAbsolute(path) && !drive) || (drive ? path.slice(2) : path).includes(':')) :
    (!paths.isAbsolute(path) && /^[a-z][a-z\d+.-]*:/i.test(path))) return ''
  path = paths.normalize(path)
  if (escapes(path, paths.sep) || (windows && path.split(paths.sep).some((part) => WINDOWS_DEVICE.test(part)))) return ''
  return path
}

function escapes(path, separator = sep) {
  return path === '..' || path.startsWith(`..${separator}`)
}

function inside(brain, path) {
  const rest = relative(brain, path)
  return !isAbsolute(rest) && !escapes(rest)
}

function validFile(stat) {
  return stat.isFile() && stat.size > 0n && stat.size <= BigInt(MAX_IMAGE_BYTES)
}

function sameFile(before, after) {
  return before.dev === after.dev && before.ino === after.ino
}

async function readImageFile(uri, brainDir) {
  let path = localMediaPath(uri)
  if (!path || (!WINDOWS && (!constants.O_NOFOLLOW || !constants.O_NONBLOCK))) return
  const flags = WINDOWS ? constants.O_RDONLY : constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK
  let brain
  if (!isAbsolute(path)) {
    brain = join(await realpath(dirname(brainDir)), basename(brainDir))
    if (!(await lstat(brain)).isDirectory()) return
    const parent = await realpath(join(brain, dirname(path)))
    if (!inside(brain, parent)) return
    path = join(parent, basename(path))
  }
  const before = await lstat(path, { bigint: true })
  if (!validFile(before)) return
  const file = await open(path, flags)
  try {
    const after = await file.stat({ bigint: true })
    if (!sameFile(before, after) || !validFile(after)) return
    if (brain || WINDOWS) {
      const openedPath = process.platform === 'linux' ? await readlink(`/proc/self/fd/${file.fd}`) : await realpath(path)
      if ((brain && !inside(brain, openedPath)) || (WINDOWS && !isAbsolute(localMediaPath(openedPath))) ||
          !sameFile(after, await lstat(openedPath, { bigint: true }))) return
    }
    const size = Number(after.size)
    const buffer = Buffer.allocUnsafe(size + 1)
    let length = 0
    while (length < buffer.length) {
      const { bytesRead } = await file.read(buffer, length, buffer.length - length, length)
      if (!bytesRead) break
      length += bytesRead
    }
    if (length === size) return buffer.subarray(0, size)
  } finally {
    await file.close()
  }
}

export async function readMediaImage(media, brainDir) {
  const mime = media.mime_type
  let bytes, data
  if (media.uri) {
    try { bytes = await readImageFile(media.uri, brainDir) } catch { return }
  } else if (media.data.length <= MAX_IMAGE_BASE64) {
    data = media.data
    bytes = Buffer.from(data, 'base64')
    if (bytes.toString('base64') !== data) return
  }
  if (!bytes?.length || bytes.length > MAX_IMAGE_BYTES) return
  if (!IMAGE_SIGNATURES.get(mime).every(([offset, ...signatures]) => signatures.some((signature) =>
    bytes.subarray(offset, offset + signature.length).equals(signature)))) return
  return { mime, data: data ?? bytes.toString('base64') }
}
