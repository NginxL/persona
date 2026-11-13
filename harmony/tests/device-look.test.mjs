import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseDeviceLookAvatarFile, contrastRatio, deviceLookAccentText, deviceLookPackageJSON, deviceLookPalette, parseDeviceLookPackage,
  validDeviceLookFile, validDeviceLookOwner, validDeviceLookPackageId } from '../entry/src/main/ets/core/DeviceLook.ts';

function info(overrides = {}) {
  return {
    id: 'nwverse',
    rev: 'a'.repeat(64),
    bytes: 4096,
    manifest: {
      format: 1,
      id: 'nwverse',
      name: 'Milk Frog',
      avatar: { sizes: { '64': 'avatar/64.png', '128': 'avatar/128.png', '256': 'avatar/256.png' } },
      dot: { states: { idle: { file: 'dot/idle.png' }, working: { file: 'dot/working.png' } } },
      colors: {
        light: { accent: '#7A6A52', bubble: '#996920', bubble_text: '#FFFFFF' },
        dark: { accent: '#958469', bubble: '#9A6B21', bubble_text: '#FFFFFF' },
      },
      about: 'NWVERSE level',
    },
    ...overrides,
  };
}

test('parses a valid Hub avatar package and chooses a real image at the requested size', () => {
  const pack = parseDeviceLookPackage(info());
  assert.ok(pack);
  assert.equal(pack.id, 'nwverse');
  assert.equal(pack.name, 'Milk Frog');
  assert.equal(chooseDeviceLookAvatarFile(pack.avatarFiles, 96)?.file, 'avatar/128.png');
  assert.equal(pack.colors?.accentLight, '#7A6A52');
  const restored = parseDeviceLookPackage(JSON.parse(deviceLookPackageJSON(pack)));
  assert.equal(restored?.dotIdleFile, 'dot/idle.png');
  assert.deepEqual(restored?.avatarFiles, pack.avatarFiles);
  assert.equal(deviceLookPackageJSON(pack).length < 128 * 1024, true);
});

test('rejects reserved ids, unsafe owner ids, traversal paths, missing idle assets and oversized packages', () => {
  for (const id of ['persona', 'persona-visor', 'persona-circuit', 'jarvis', 'jarvis-blob']) {
    assert.equal(validDeviceLookPackageId(id), false, id);
  }
  assert.equal(validDeviceLookPackageId('../nwverse'), false);
  assert.equal(validDeviceLookOwner('../hub'), false);
  assert.equal(validDeviceLookOwner('hub-01'), true);
  assert.equal(validDeviceLookFile('avatar/128.png'), true);
  for (const path of ['/avatar.png', '../avatar.png', 'avatar/../128.png', 'avatar/%2e%2e/128.png', '.hidden.png']) {
    assert.equal(validDeviceLookFile(path), false, path);
  }
  assert.equal(parseDeviceLookPackage(info({ bytes: 16 * 1024 * 1024 + 1 })), undefined);
  const missingIdle = info();
  missingIdle.manifest.dot.states = { thinking: { file: 'dot/thinking.png' } };
  assert.equal(parseDeviceLookPackage(missingIdle), undefined);
  const unknownState = info();
  unknownState.manifest.dot.states.sleeping = { file: 'dot/sleeping.png' };
  assert.equal(parseDeviceLookPackage(unknownState), undefined);
  const manySizes = info();
  for (let index = 0; index < 65; index++) manySizes.manifest.avatar.sizes[String(index + 1)] = 'avatar/' + String(index + 1) + '.png';
  assert.equal(parseDeviceLookPackage(manySizes), undefined);
  const traversal = info();
  traversal.manifest.avatar.sizes['128'] = '../avatar.png';
  assert.equal(parseDeviceLookPackage(traversal), undefined);
});

test('preset accents keep readable brand and bubble colors in both appearances', () => {
  const canonical = {
    blue: ['#007AFF', '#0A84FF'], coral: ['#D4683F', '#CD6E45'],
    lavender: ['#7A5AF8', '#8979EB'], teal: ['#27747F', '#2A9798'], lark: ['#3370FF', '#4C88FF']
  };
  for (const accent of ['style', 'blue', 'coral', 'lavender', 'teal', 'lark']) {
    const palette = deviceLookPalette(accent, 'glass', undefined);
    if (accent !== 'style') {
      assert.equal(palette.light.brand, canonical[accent][0], accent + ' canonical light brand');
      assert.equal(palette.dark.brand, canonical[accent][1], accent + ' canonical dark brand');
    }
    assert.ok(contrastRatio(palette.light.emphasize, '#FFFFFF') >= 4.5, accent + ' light emphasize');
    assert.ok(contrastRatio(palette.dark.emphasize, '#000000') >= 4.5, accent + ' dark emphasize');
    for (const mode of ['light', 'dark']) assert.ok(contrastRatio(palette[mode].brand, deviceLookAccentText(palette[mode].brand)) >= 4.5, accent + ' ' + mode + ' emphasized control');
    assert.ok(contrastRatio(palette.light.userBubble, palette.light.userBubbleText) >= 4.5, accent + ' light bubble');
    assert.ok(contrastRatio(palette.dark.userBubble, palette.dark.userBubbleText) >= 4.5, accent + ' dark bubble');
  }
});

test('avatar accent is independent of style and its weak package colors are adjusted', () => {
  const pack = parseDeviceLookPackage(info());
  assert.ok(pack?.colors);
  const glass = deviceLookPalette('avatar', 'glass', pack?.colors);
  const board = deviceLookPalette('avatar', 'board', pack?.colors);
  assert.equal(glass.light.brand, board.light.brand);
  assert.ok(contrastRatio(glass.light.brand, '#FFFFFF') >= 4.5);
  assert.ok(contrastRatio(glass.light.emphasize, '#FFFFFF') >= 4.5);
  assert.ok(contrastRatio(glass.dark.brand, '#000000') >= 4.5);
  assert.ok(contrastRatio(glass.dark.emphasize, '#000000') >= 4.5);
  assert.ok(contrastRatio(glass.light.userBubble, glass.light.userBubbleText) >= 4.5);
  assert.ok(contrastRatio(glass.dark.userBubble, glass.dark.userBubbleText) >= 4.5);
  assert.notEqual(deviceLookPalette('style', 'glass', undefined).light.userBubble,
    deviceLookPalette('style', 'board', undefined).light.userBubble);
});

test('mid-gray supplied bubble text reaches contrast through the better endpoint',()=>{
 const input=info();input.manifest.colors.light.bubble_text='#888888';input.manifest.colors.dark.bubble_text='#888888';
 const pack=parseDeviceLookPackage(input),palette=deviceLookPalette('avatar','glass',pack.colors);
 assert.ok(contrastRatio(palette.light.userBubble,palette.light.userBubbleText)>=4.5);assert.ok(contrastRatio(palette.dark.userBubble,palette.dark.userBubbleText)>=4.5);
});
