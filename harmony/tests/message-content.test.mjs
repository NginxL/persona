import test from 'node:test';
import assert from 'node:assert/strict';
import {parseMessageContent} from '../entry/src/main/ets/core/MessageContent.ts';
test('Hub image notes become image paths and never appear as implementation text',()=>{
 const shown=parseMessageContent('Look [Image #1]\n<attached_images>\n/tmp/one.jpg\npath: /tmp/two screen.png\n/tmp/one.jpg\n</attached_images>');
 assert.equal(shown.text,'Look');assert.deepEqual(shown.images,['/tmp/one.jpg','/tmp/two screen.png']);assert.deepEqual(shown.files,[]);
});
test('legacy and pasted content retain user words with safe local media references',()=>{
 const shown=parseMessageContent('<pasted_content>Read this</pasted_content>\n相关图片：~/a.jpg、/tmp/b.png');
 assert.equal(shown.text,'Read this');assert.deepEqual(shown.images,['~/a.jpg','/tmp/b.png']);
 assert.deepEqual(parseMessageContent('<attached_images>\nhttps://example.test/a.jpg\n//example.test/a.jpg\n/tmp/a\u0000.jpg\n</attached_images>').images,[]);
});
test('file chips exclude web URLs, model IDs and photo-note paths',()=>{
 assert.deepEqual(parseMessageContent('Read /work/project/file.md and src/app.ts; https://example.test/a.jpg deepseek/v4.1-flash\n<attached_images>\n/tmp/photo.jpg\n</attached_images>').files,['/work/project/file.md','src/app.ts']);
});
