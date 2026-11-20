"""Acceptance counterexamples for an explicitly empty/unassigned manager."""
import copy
import datetime as dt
import json
from pathlib import Path
import unittest
from unittest.mock import patch

from manager_idle import collect
import promote_manager_release as promoter


def session(sid='main', **extra):
    return dict(dict(manager='m-fixture', machine_id='', session_id=sid,
                     rev=1, live=True, assistant=True, state='idle', status='idle', starting=False,
                     pending_interactions=0, background=[], subagents=[], compaction=None), **extra)


class EmptyIdleCollectionTests(unittest.TestCase):
    def setUp(self):
        self.target = dict(manager_id='m-fixture', machine_id='', main='')
        now = dt.datetime.now(dt.timezone.utc).isoformat()
        self.assistant_path = 'assistant?manager=m-fixture&archived=true'
        self.answers = {
            'managers': dict(managers=[dict(id='m-fixture', online=True, deploy='service')]),
            'machines': dict(machines=[]),
            self.assistant_path: dict(manager='m-fixture', machine_id=None, main=None,
                                      conversations=[], update_inventory=dict(
                                          schema=1, complete=True, revision='r1', observed_at=now)),
            'sessions?machine_id=': dict(sessions=[], update_inventory=dict(
                schema=1, complete=True, revision='r1', observed_at=now)),
        }
        self.calls = []

    def read(self, root, path):
        self.calls.append(path)
        if path.startswith('sessions?'):
            path = 'sessions?machine_id='
        return copy.deepcopy(self.answers[path])

    def collect(self, **kwargs):
        return collect(Path('/fixture'), self.target, self.read, **kwargs)

    def assert_no_sessions_read(self):
        self.assertFalse(any(path.startswith('sessions?') for path in self.calls), self.calls)

    def test_empty_collect_accepts_null_or_empty_profile_for_identity_and_full_reads(self):
        for identity_only in (True, False):
            for profile_empty in (None, ''):
                with self.subTest(identity_only=identity_only, profile_empty=profile_empty):
                    self.calls = []
                    self.answers[self.assistant_path]['main'] = profile_empty
                    self.answers[self.assistant_path]['machine_id'] = profile_empty
                    result = self.collect(identity_only=identity_only)
                    self.assert_no_sessions_read()
                    self.assertIsNotNone(result)
                    if not identity_only:
                        _, identity, digest = result
                        self.assertEqual(identity['main'], '')
                        self.assertEqual(identity['machine_id'], '')
                        self.assertEqual(len(digest), 64)
                        for path in ('managers', 'machines', self.assistant_path):
                            self.assertEqual(self.calls.count(path), 2, self.calls)

    def test_empty_collect_rejects_any_owned_machine(self):
        self.answers['machines']['machines'] = [
            dict(id='box', manager='m-fixture', machine_uid='uid', online=True)]
        with self.assertRaises(ValueError):
            self.collect()
        self.assert_no_sessions_read()

    def test_partial_target_is_rejected_before_any_read(self):
        for main, machine in (('main', ''), ('', 'box')):
            with self.subTest(main=main, machine=machine):
                self.target.update(main=main, machine_id=machine)
                self.calls = []
                with self.assertRaises(ValueError):
                    self.collect()
                self.assertEqual(self.calls, [])

    def test_empty_collect_rejects_new_main_machine_or_owned_coverage_on_second_read(self):
        for change in ('main', 'machine_id', 'machines'):
            with self.subTest(change=change):
                self.calls = []
                reads = {self.assistant_path: 0, 'machines': 0}

                def changing_read(root, path):
                    result = self.read(root, path)
                    if path in reads:
                        reads[path] += 1
                    if reads[self.assistant_path] == 2 and path == self.assistant_path:
                        if change == 'main':
                            result['main'] = 'new-main'
                        elif change == 'machine_id':
                            result['machine_id'] = 'new-box'
                    if change == 'machines' and path == 'machines' and reads[path] == 2:
                        result['machines'] = [
                            dict(id='new-box', manager='m-fixture', machine_uid='new-uid', online=True)]
                    return result

                with self.assertRaises(ValueError):
                    collect(Path('/fixture'), self.target, changing_read)
                changed_path = 'machines' if change == 'machines' else self.assistant_path
                self.assertGreaterEqual(reads[changed_path], 2, 'later assignment was never evaluated')
                self.assert_no_sessions_read()

    def test_empty_collect_rejects_activity_on_second_read(self):
        reads = [0]

        def active_read(root, path):
            result = self.read(root, path)
            if path == self.assistant_path:
                reads[0] += 1
                if reads[0] == 2:
                    result['conversations'] = [session()]
            return result

        with self.assertRaises(ValueError):
            collect(Path('/fixture'), self.target, active_read)
        self.assertGreaterEqual(reads[0], 2, 'later activity was never evaluated')
        self.assert_no_sessions_read()

    def test_empty_collect_rejects_display_only_or_incomplete_coverage(self):
        original_assistant = copy.deepcopy(self.answers[self.assistant_path])
        original_machines = copy.deepcopy(self.answers['machines'])
        cases = []
        display_only = copy.deepcopy(original_assistant)
        display_only.pop('update_inventory')
        cases.append(('display_only', display_only, original_machines))
        for change in (dict(cursor='next'), dict(has_more=True), dict(total=1)):
            cases.append((str(change), original_assistant, dict(original_machines, **change)))
        for name, assistant, machines in cases:
            for identity_only in (True, False):
                with self.subTest(name=name, identity_only=identity_only):
                    self.calls = []
                    self.answers[self.assistant_path] = copy.deepcopy(assistant)
                    self.answers['machines'] = copy.deepcopy(machines)
                    with self.assertRaises(ValueError):
                        self.collect(identity_only=identity_only)
                    self.assert_no_sessions_read()
        self.answers[self.assistant_path] = original_assistant
        self.answers['machines'] = original_machines

    def test_identity_only_empty_collect_rejects_coverage_revision_change(self):
        for identity_only in (True, False):
            with self.subTest(identity_only=identity_only):
                reads = [0]

                def changing_read(root, path):
                    result = self.read(root, path)
                    if path == self.assistant_path:
                        reads[0] += 1
                        if reads[0] == 2:
                            result['update_inventory']['revision'] = 'changed'
                    return result

                with self.assertRaises(ValueError):
                    collect(Path('/fixture'), self.target, changing_read, identity_only=identity_only)
                self.assertGreaterEqual(reads[0], 2)
                self.assert_no_sessions_read()


class EmptyPublisherEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.target = dict(manager_id='m-fixture', main='', machine_id='',
                           dir='/fixture/manager', binary='/fixture/manager/bin/personad',
                           unit='com.persona.manager.m-fixture',
                           plist_path='/fixture/manager.plist', uid=501,
                           previous_sha256='a' * 64, config_sha256='b' * 64,
                           plist_sha256='c' * 64)
        self.identity = dict(machines=[])
        now = dt.datetime.now(dt.timezone.utc).isoformat()
        self.evidence = dict(schema=1, complete=True, idle=True, complete_reads=2,
                             sha256='d' * 64, started_at=now, completed_at=now,
                             authority=dict(manager='m-fixture', main='',
                                            machine_id='', revision='e' * 64, unassigned=True),
                             inventory=dict(target=dict(manager='m-fixture', main='',
                                                        machine_id='', machine_uid='',
                                                        authority_revision='e' * 64)))

    def read(self):
        return promoter.idle_evidence(['ssh', 'example.invalid'], self.target,
                                      self.identity, '/fixture/candidate')

    def test_empty_evidence_invokes_candidate_with_explicit_empty_identity(self):
        with patch.object(promoter, 'remote', return_value=self.evidence) as remote:
            self.assertEqual(self.read(), ('d' * 64, dt.datetime.fromisoformat(self.evidence['completed_at'])))
        args, _ = remote.call_args
        self.assertEqual(args[1][:6],
                         ['/fixture/candidate', 'manager', 'update', 'inventory', '--plan', '-'])
        self.assertEqual(args[1][6:], ['--main', '', '--machine', '', '--machine-uid', ''])
        plan = json.loads(args[2])
        self.assertEqual(plan['manager_id'], 'm-fixture')
        self.assertIs(plan['unassigned'], True)
        for field in ('main', 'machine_id', 'machine_uid'):
            self.assertIn(field, plan)
            self.assertEqual(plan[field], '')

    def test_empty_evidence_requires_explicit_unassigned_authority(self):
        evidence = copy.deepcopy(self.evidence)
        del evidence['authority']['unassigned']
        with patch.object(promoter, 'remote', return_value=evidence), self.assertRaises(ValueError):
            self.read()

    def test_empty_identity_rejects_owned_machine_before_remote(self):
        self.identity['machines'] = [('box', 'uid')]
        with patch.object(promoter, 'remote') as remote, self.assertRaises(ValueError):
            self.read()
        remote.assert_not_called()

    def test_partial_identity_rejects_before_remote(self):
        for main, machine in (('main', ''), ('', 'box')):
            with self.subTest(main=main, machine=machine):
                self.target.update(main=main, machine_id=machine)
                with patch.object(promoter, 'remote') as remote, self.assertRaises(ValueError):
                    self.read()
                remote.assert_not_called()


if __name__ == '__main__':
    unittest.main()
