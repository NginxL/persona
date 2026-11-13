import { describe, it, expect } from '@ohos/hypium';
import { abilityAccessCtrl, Permissions } from '@kit.AbilityKit';
import { contact } from '@kit.ContactsKit';
import { calendarManager } from '@kit.CalendarKit';
import { util } from '@kit.ArkTS';
import { prepareContactWrite, prepareCalendarWrite } from '../../../main/ets/platform/NativePhoneWrites';
import { Wire } from '../../../main/ets/models/Wire';
import { nativeTestContext } from './NativeContext';

async function permissions(values: Permissions[]): Promise<void> {
  const access = abilityAccessCtrl.createAtManager();
  await access.requestPermissionsFromUser(nativeTestContext(), values);
  for (const value of values) expect(access.getSelfPermissionStatus(value)).assertEqual(abilityAccessCtrl.PermissionStatus.GRANTED);
}

/** Optional suite: mutations target only newly created, uniquely named emulator fixtures. */
export default function nativePhoneTests(): void {
  describe('NativePhone', (): void => {
    it('contact_create_update_and_fresh_snapshot_guard_use_real_native_records', 0, async (): Promise<void> => {
      const context = nativeTestContext();
      await permissions(['ohos.permission.READ_CONTACTS', 'ohos.permission.WRITE_CONTACTS'] as Permissions[]);
      let key = '', createdId: number = 0;
      try {
        const creating = await prepareContactWrite(context, Wire.payload(['given_name', 'family_name', 'phones'], ['Persona', util.generateRandomUUID(),
          [Wire.payload(['value', 'label'], ['+1 202 555 0100', 'work'])]]), false);
        const created = Wire.object(await creating.execute((): boolean => true), 'contact');
        createdId = Number(Wire.text(created, 'id'));
        key = await contact.queryKey(context, createdId); expect(key.length > 0).assertTrue();
        const updating = await prepareContactWrite(context, Wire.payload(['id', 'emails'], [key, [Wire.payload(['value', 'label'], ['native@example.com', 'work'])]]), true);
        await updating.execute((): boolean => true);
        const saved = await contact.queryContact(context, key);
        expect(saved.phoneNumbers?.some((value: contact.PhoneNumber): boolean => value.phoneNumber.replace(/\s/g, '') === '+12025550100')).assertTrue();
        expect(saved.emails?.some((value: contact.Email): boolean => value.email === 'native@example.com')).assertTrue();
        const stale = await prepareContactWrite(context, Wire.payload(['id', 'company'], [key, 'Must not be saved']), true);
        await contact.updateContact(context, { id: saved.id, key: key, organization: { name: 'External fixture update' } },
          { attributes: [contact.Attribute.ATTR_ORGANIZATION] });
        let rejected = false;
        try { await stale.execute((): boolean => true); } catch (error) { rejected = (error as Error).message === 'contact_changed'; }
        expect(rejected).assertTrue(); expect((await contact.queryContact(context, key)).organization?.name).assertEqual('External fixture update');
      } finally {
        if (createdId > 0) {
          const cleanupKey = key || await contact.queryKey(context, createdId);
          if (!cleanupKey) throw new Error('native_contact_cleanup_failed');
          await contact.deleteContact(context, cleanupKey);
        }
      }
    });

    it('calendar_update_and_delete_preserve_unedited_native_event_fields', 0, async (): Promise<void> => {
      const context = nativeTestContext();
      await permissions(['ohos.permission.READ_CALENDAR', 'ohos.permission.WRITE_CALENDAR'] as Permissions[]);
      const manager = calendarManager.getCalendarManager(context);
      let calendar: calendarManager.Calendar | undefined;
      try {
        calendar = await manager.createCalendar({ name: 'persona-native-' + util.generateRandomUUID(), type: calendarManager.CalendarType.LOCAL,
          displayName: 'Persona native verification' });
        const start = Math.floor(Date.now() / 60000) * 60000 + 86400000, end = start + 3600000;
        const id = await calendar.addEvent({ type: calendarManager.EventType.NORMAL, title: 'Native verification', startTime: start,
          endTime: end, description: 'Retained fixture note', location: { location: 'Synthetic fixture location' } });
        const nativeEvent = (await calendar.getEvents(calendarManager.EventFilter.filterById([id]), ['id', 'recurrenceRule']))[0];
        console.info('PersonaNativeCalendarRecurrence ' + JSON.stringify(nativeEvent.recurrenceRule));
        const change = await prepareCalendarWrite(context, Wire.payload(['id', 'title'], [String(id), 'Updated native verification']), false);
        await change.execute((): boolean => true);
        const saved = (await calendar.getEvents(calendarManager.EventFilter.filterById([id]), ['id', 'title', 'startTime', 'endTime', 'description', 'location']))[0];
        expect(saved.title).assertEqual('Updated native verification'); expect(saved.startTime).assertEqual(start); expect(saved.endTime).assertEqual(end);
        expect(saved.description).assertEqual('Retained fixture note'); expect(saved.location?.location).assertEqual('Synthetic fixture location');
        const removal = await prepareCalendarWrite(context, Wire.payload(['id'], [String(id)]), true); await removal.execute((): boolean => true);
        expect((await calendar.getEvents(calendarManager.EventFilter.filterById([id]), ['id'])).length).assertEqual(0);
        const series = await calendar.addEvent({ type: calendarManager.EventType.NORMAL, title: 'Native recurring verification',
          startTime: start, endTime: end, recurrenceRule: { recurrenceFrequency: calendarManager.RecurrenceFrequency.YEARLY, count: 2 } });
        for (const deleting of [false, true]) {
          let rejected = false;
          try { await prepareCalendarWrite(context, Wire.payload(['id', 'title'], [String(series), 'Must not replace the series']), deleting); }
          catch (error) { rejected = (error as Error).message === 'calendar_occurrence_unsupported'; }
          expect(rejected).assertTrue();
        }
        expect((await calendar.getEvents(calendarManager.EventFilter.filterById([series]), ['id', 'title']))[0].title).assertEqual('Native recurring verification');
      } finally { if (calendar) await manager.deleteCalendar(calendar); }
    });
  });
}
