#!/usr/bin/env python3
"""Reuse the approved Persona Paper artwork for Harmony's native icon layers.

Only standard-library PNG inspection, lossless copying and a solid background export;
this does not redraw, mask or add padding to the approved brand.
"""
from pathlib import Path
import hashlib
import json
import shutil
import struct
import zlib

ROOT = Path(__file__).resolve().parents[2]
FLAT = ROOT / 'apple/Persona/Resources/Assets.xcassets/AppIcon.appiconset/icon.png'
FRONT = ROOT / 'apple/Persona/Resources/Assets.xcassets/AppIcon-Vision.solidimagestack/Front.solidimagestacklayer/Content.imageset/front.png'


def png_info(path):
    raw = path.read_bytes()
    if raw[:8] != b'\x89PNG\r\n\x1a\n':
        raise ValueError('Expected PNG artwork')
    width, height, depth, kind = struct.unpack('>IIBB', raw[16:26])
    return raw, width, height, depth, kind


def corner(path):
    raw, _, _, depth, kind = png_info(path)
    if depth != 8 or kind not in (2, 6):
        raise ValueError('Expected RGB or RGBA source')
    chunks = []
    offset = 8
    while offset < len(raw):
        size = struct.unpack('>I', raw[offset:offset + 4])[0]
        if raw[offset + 4:offset + 8] == b'IDAT':
            chunks.append(raw[offset + 8:offset + 8 + size])
        offset += size + 12
    row = zlib.decompress(b''.join(chunks))
    if row[0] > 4:
        raise ValueError('Unsupported PNG filter')
    # Every predictor for the first pixel of the first row is zero.
    if kind == 6 and row[4] != 255:
        raise ValueError('The approved flat artwork must be opaque')
    return tuple(row[1:4])


def solid_png(size, rgb):
    def chunk(kind, data):
        return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))
    pixels = (b'\0' + bytes(rgb) * size) * size
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', size, size, 8, 2, 0, 0, 0)) + chunk(b'sRGB', b'\0') + chunk(b'IDAT', zlib.compress(pixels, 9)) + chunk(b'IEND', b'')


def main():
    for source in (FLAT, FRONT):
        _, width, height, depth, _ = png_info(source)
        if (width, height, depth) != (1024, 1024, 8):
            raise ValueError('Approved source must be 1024 square with 8-bit channels')
    background = solid_png(1024, corner(FLAT))
    outputs = []
    for relative in ('harmony/AppScope/resources/base/media', 'harmony/entry/src/main/resources/base/media'):
        directory = ROOT / relative
        directory.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(FRONT, directory / 'foreground.png')
        (directory / 'background.png').write_bytes(background)
        outputs += [directory / 'foreground.png', directory / 'background.png']
    start = ROOT / 'harmony/entry/src/main/resources/base/media/startIcon.png'
    shutil.copyfile(FLAT, start)
    outputs.append(start)
    manifest = {'sdk': 'HarmonyOS 7 / API 26', 'purpose': 'Package icon layers and startup artwork; no store upload',
                'sources': [str(path.relative_to(ROOT)) for path in (FLAT, FRONT)], 'exports': []}
    for path in outputs:
        raw, width, height, depth, kind = png_info(path)
        manifest['exports'].append({'path': str(path.relative_to(ROOT)), 'width': width, 'height': height,
                                    'depth': depth, 'alpha': kind == 6, 'sha256': hashlib.sha256(raw).hexdigest()})
    (ROOT / 'harmony/icon-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')


if __name__ == '__main__':
    main()
