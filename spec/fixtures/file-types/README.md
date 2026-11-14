# file-types

What a file whose name cannot tell is (#7974, `docs/protocol/protocol.md` "File preview"): a `.ts`, `.mts` or `.cts`
file is TypeScript as often as an MPEG transport stream, so the node and the hub's `file.read` relay type it by its
first bytes (`server/internal/proto/filetype.go`, `ScriptOrStream`). Clients switch on the `mime` and `text` they are
given, never on the name.

## Rules

- 188-byte packets each starting with the sync byte `0x47` (192-byte packets with it at byte 4: M2TS / AVCHD), at
  least two in the bytes read, and binary (a NUL): `video/mp2t`, a streamed video (`segment.ts`).
- Text (UTF-8 with no NUL; an empty file too): `text/typescript`, shown as highlighted TypeScript (`typescript.ts`).
- Anything else is sniffed like a file without a known name.

## Format

Real files, not JSON cases: the bytes are the case, and each file's name is the type its extension would wrongly
suggest.

- `typescript.ts`: a TypeScript module (types, a generic class, private fields).
- `segment.ts`: an MPEG-TS segment from ffmpeg (`-f lavfi -i testsrc=s=32x32:r=10:d=0.5 -c:v mpeg2video -g 5
  -flags +bitexact -fflags +bitexact -f mpegts`): 15 packets of 188 bytes, five MPEG-2 video frames that `ffprobe`
  reads.

## Checked by

- `server/internal/proto/filetype_test.go`: the rule itself, plus M2TS, text with `G` where packets would start, a
  character cut by the probe, one packet only, lost sync.
- `server/internal/node/filemime_test.go` (`TestTypeScriptAndTransportStreamByTheirBytes`): `file.read` with and
  without `stream`, the download's type and the Info sheet's `mime`, for `.ts`, `.mts` and `.cts`.
- `server/internal/hub/fileread_test.go`: the hub turning an older node's `video/mp2t` for TypeScript into the text
  page, and leaving the real segment a streamed video.
