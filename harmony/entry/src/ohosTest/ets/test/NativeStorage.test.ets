import { Wire } from '../../../main/ets/models/Wire';
import { ChatDraftStore, ChatDraft, ChatOutboxEntry } from '../../../main/ets/models/ChatDraftStore';
import { describe, it, expect } from '@ohos/hypium';
import { nativeTestContext } from './NativeContext';
import { relationalStore } from '@kit.ArkData';
import { util } from '@kit.ArkTS';
import { LocalStore } from '../../../main/ets/platform/LocalStore';
import { CredentialVault } from '../../../main/ets/platform/CredentialVault';
import { MutationStore } from '../../../main/ets/models/MutationStore';
import { readerLink } from '../../../main/ets/platform/ReaderLink';
import { boundedTranscriptCache, TRANSCRIPT_CACHE_BYTES, utf8Bytes } from '../../../main/ets/core/TranscriptCache';
import { TranscriptItem, TranscriptSnapshot } from '../../../main/ets/core/Domain';

export default function nativeStorageTests(): void {
  describe('NativeStorage', (): void => {
    it('asset_credentials_are_updated_and_removed_without_cross_hub_access', 0, async (): Promise<void> => {
      const context = nativeTestContext();
      const vault = new CredentialVault(context), id = 'test-' + util.generateRandomUUID();
      try {
        expect(await vault.get(id)).assertNull();
        await vault.put(id, 'synthetic-first'); expect(await vault.get(id)).assertEqual('synthetic-first');
        await vault.put(id, 'synthetic-second'); expect(await vault.get(id)).assertEqual('synthetic-second');
        expect(await vault.get(id + '-other')).assertNull();
        await vault.remove(id); expect(await vault.get(id)).assertNull();
      } finally { await vault.remove(id); }
    });
    it('an_uncertain_write_stays_blocked_after_the_real_database_reopens', 0, async (): Promise<void> => {
      const context = nativeTestContext();
      const name = 'native-test-' + util.generateRandomUUID() + '.db';
      const first = new LocalStore(name), reopened = new LocalStore(name);
      try {
        await first.open(context);
        const store = new MutationStore(first, 'synthetic-scope');
        const record = await store.begin('/api/v1/sessions/test/test/messages', 'POST', '{"text":"synthetic draft"}');
        await store.finish(record, 'unknown'); await first.close();
        await reopened.open(context); const restored = new MutationStore(reopened, 'synthetic-scope'); await restored.load();
        expect(restored.blocked().length).assertEqual(1); expect(restored.blocked()[0].id).assertEqual(record.id);
        let rejected = false;
        try { await restored.begin(record.path, 'POST', '{"text":"duplicate"}'); } catch (_) { rejected = true; }
        expect(rejected).assertTrue();
        await restored.acknowledge(record.id); expect(restored.blocked().length).assertEqual(0);
      } finally { await first.close(); await reopened.close(); await relationalStore.deleteRdbStore(context, name); }
    });
    it('bounded_unicode_history_survives_the_native_database_window_and_reopen', 0, async (): Promise<void> => {
      const context = nativeTestContext(), name = 'cache-test-' + util.generateRandomUUID() + '.db';
      const first = new LocalStore(name), reopened = new LocalStore(name);
      const items: TranscriptItem[] = [];
      for (let index = 0; index < 120; index++) {
        const item: TranscriptItem = { id: String(index), seq: index + 1, kind: 'text', role: 'assistant', text: '世界🌍'.repeat(1000) };
        items.push(item);
      }
      const snapshot: TranscriptSnapshot = { items: items, covered: 120, history: 'synthetic', hasMore: false, loadedOnce: true };
      const encoded = boundedTranscriptCache(snapshot);
      expect(utf8Bytes(encoded) <= TRANSCRIPT_CACHE_BYTES).assertTrue();
      try {
        await first.open(context); await first.put('transcript:synthetic', encoded); await first.close();
        await reopened.open(context); expect(await reopened.get('transcript:synthetic')).assertEqual(encoded);
      } finally { await first.close(); await reopened.close(); await relationalStore.deleteRdbStore(context, name); }
    });
    it('forgetting_one_hub_removes_its_cached_data_and_blocks_late_writes_across_database_handles', 0, async (): Promise<void> => {
      const context = nativeTestContext(), name = 'purge-test-' + util.generateRandomUUID() + '.db';
      const first = new LocalStore(name), second = new LocalStore(name);
      const own = 'draft:' + JSON.stringify([JSON.stringify(['hub-a', 'personal']), 'session', 'machine', 'chat']);
      const other = 'snapshot:' + JSON.stringify(['hub-ab', 'personal']);
      try {
        await first.open(context); await second.open(context);
        await first.put(own, 'synthetic draft'); await first.put(other, 'other Hub'); await first.put('preferences', 'global');
        const late = second.put(own, 'late result').catch(() => {});
        await first.purgeHub('hub-a'); await late;
        let blocked = false;
        try { await second.put(own, 'resurrection'); } catch (_) { blocked = true; }
        expect(blocked).assertTrue(); expect((await first.keys()).includes(own)).assertFalse();
        expect(await second.get(other)).assertEqual('other Hub'); expect(await second.get('preferences')).assertEqual('global');
        first.allowHub('hub-a'); expect(await second.get(own)).assertEqual('');
        const oldEpochWrite = second.put(own, 'queued before forget').catch(() => {});
        const purging = first.purgeHub('hub-a'); first.allowHub('hub-a');
        await oldEpochWrite; await purging; expect(await first.get(own)).assertEqual('');
        await second.put(own, 'new pairing'); expect(await first.get(own)).assertEqual('new pairing');
      } finally { await first.close(); await second.close(); await relationalStore.deleteRdbStore(context, name); }
    });
    it('versioned_drafts_and_sharded_outbox_survive_reopen_and_forget', 0, async (): Promise<void> => {
      const context = nativeTestContext(), name = 'outbox-test-' + util.generateRandomUUID() + '.db';
      const first = new LocalStore(name), reopened = new LocalStore(name), scope = JSON.stringify(['hub-outbox', 'personal']);
      const target = JSON.stringify([scope, 'session', 'machine', 'chat']);
      try {
        await first.open(context); const store = new ChatDraftStore(first), draft = new ChatDraft();
        draft.text = '世界🌍'.repeat(20000); draft.revision = 'first-draft'; await store.save(target, draft);
        for (let index = 0; index < 20; index++) {
          const entry = new ChatOutboxEntry(); entry.id = 'native-op-' + index; entry.target = target + index; entry.revision = 'draft-' + index; entry.text = 'x'.repeat(100000);
          await store.stage(scope, entry);
        }
        expect((await first.get('outbox:' + scope)).length < 1000).assertTrue(); await first.close();
        await reopened.open(context); const restored = new ChatDraftStore(reopened);
        expect((await restored.read(target)).text).assertEqual(draft.text); expect((await restored.list(scope)).length).assertEqual(20);
        draft.revision = 'new-draft'; await restored.save(target, draft); expect(await restored.acknowledge(target, 'first-draft')).assertFalse();
        expect((await restored.read(target)).revision).assertEqual('new-draft');
        const key = 'draft:' + target, oldEpoch = reopened.revision(key);
        await reopened.purgeHub('hub-outbox'); reopened.allowHub('hub-outbox');
        let blocked = false; try { await reopened.put(key, 'old transaction', oldEpoch); } catch (_) { blocked = true; }
        expect(blocked).assertTrue(); expect((await restored.read(target)).text).assertEqual(''); expect((await restored.list(scope)).length).assertEqual(0);
      } finally { await first.close(); await reopened.close(); await relationalStore.deleteRdbStore(context, name); }
    });
    it('near_limit_photo_payloads_keep_only_readable_operation_metadata_on_disk', 0, async (): Promise<void> => {
      const context = nativeTestContext(), name = 'photo-journal-' + util.generateRandomUUID() + '.db';
      const first = new LocalStore(name), reopened = new LocalStore(name), scope = JSON.stringify(['photo-hub', 'personal']);
      try {
        await first.open(context); const journal = new MutationStore(first, scope);
        const body = Wire.payload(['text', 'images'], ['Synthetic photo', [Wire.payload(['mime', 'data'], ['image/jpeg', 'A'.repeat(3 * 1024 * 1024)])]]);
        const record = await journal.begin('/api/v1/sessions/machine/chat/messages', 'POST', JSON.stringify(body));
        await journal.finish(record, 'unknown'); expect(record.payload).assertEqual('');
        expect((await first.get('mutations:' + scope + ':' + record.id)).length < 2048).assertTrue(); await first.close();
        await reopened.open(context); const restored = new MutationStore(reopened, scope); await restored.load();
        expect(restored.blocked().length).assertEqual(1); expect(restored.blocked()[0].id).assertEqual(record.id); expect(restored.blocked()[0].payload).assertEqual('');
      } finally { await first.close(); await reopened.close(); await relationalStore.deleteRdbStore(context, name); }
    });
    it('reader_links_never_admit_local_schemes_or_embedded_credentials', 0, (): void => {
      expect(readerLink('https://example.test/page?q=1')).assertEqual('https://example.test/page?q=1');
      for (const address of ['file:///data/private', 'javascript:alert(1)', 'data:text/html,test', 'https://user:secret@example.test/', 'https://example.test/\r\nX-Test: bad'])
        expect(readerLink(address)).assertEqual('');
    });
  });
}
