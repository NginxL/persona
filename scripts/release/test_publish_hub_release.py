"""SSH publication setup fails closed without leaking proxy connections."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, patch

# These setup tests never establish SSH; deployment installs the real dependency.
with patch.dict(sys.modules, {'paramiko': Mock()}):
    import publish_hub_release as publisher
import promote_hub_release as promoter
from promote_hub_release import validate_next
from test_node_artifacts import release


class PublisherSetupTests(unittest.TestCase):
    def test_item_custom_labels_and_builtin_ids(self):
        fields = [{'id': 'random-host', 'label': 'host', 'value': 'example.invalid'},
                  {'id': 'random-port', 'label': 'port', 'value': '2222'},
                  {'id': 'username', 'label': 'User', 'value': 'fixture'},
                  {'id': 'password', 'label': 'Password', 'value': 'test-secret'}]
        self.assertEqual(publisher.credential_fields({'fields': fields}),
                         {'host': 'example.invalid', 'port': '2222',
                          'username': 'fixture', 'password': 'test-secret'})
        fields.append({'id': 'duplicate', 'label': 'host', 'value': 'other.invalid'})
        with self.assertRaises(ValueError):
            publisher.credential_fields({'fields': fields})

    def test_proxy_timeout_closes_every_socket(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / 'config.json'
            config.write_text(json.dumps({'credentials_command': ['credentials'],
                                         'remote_root': '/example',
                                         'ssh_proxy': 'http://proxy.invalid:8080'}))
            (root / 'latest.json').write_text(json.dumps({'build': 1}))
            sockets = [Mock() for _ in range(3)]
            for sock in sockets:
                sock.recv.side_effect = TimeoutError('fixture timeout')
            credentials = json.dumps({'host': 'example.invalid', 'username': 'fixture',
                                      'password': 'test-secret'}).encode()
            with patch('sys.argv', ['publisher', '--config', str(config), '--artifact-dir', directory]), \
                    patch.object(publisher.subprocess, 'check_output', return_value=credentials), \
                    patch.object(publisher.paramiko, 'SSHClient'), \
                    patch.object(publisher.socket, 'create_connection', side_effect=sockets), \
                    patch.object(publisher.time, 'sleep'):
                with self.assertRaises(TimeoutError):
                    publisher.main()
            for sock in sockets:
                sock.close.assert_called()

    def test_connected_publisher_enables_existing_transport_keepalive(self):
        credentials = json.dumps({'host': 'example.invalid', 'username': 'fixture',
                                  'password': 'test-secret'}).encode()
        client = Mock()
        with patch.object(publisher.subprocess, 'check_output', return_value=credentials), \
                patch.object(publisher.paramiko, 'SSHClient', return_value=client):
            connected, sock = publisher.connect_publisher({'credentials_command': ['credentials']})
        self.assertIs(connected, client)
        self.assertIsNone(sock)
        client.get_transport.return_value.set_keepalive.assert_called_once_with(30)

    def test_key_file_login_offers_only_the_configured_key(self):
        fields = [{'id': 'host', 'value': 'example.invalid'},
                  {'id': 'username', 'value': 'fixture'},
                  {'id': 'key_filename', 'label': 'Key file', 'value': '~/keys/publisher'}]
        client = Mock()
        with patch.object(publisher.subprocess, 'check_output',
                          return_value=json.dumps({'fields': fields}).encode()), \
                patch.object(publisher.paramiko, 'SSHClient', return_value=client):
            publisher.connect_publisher({'credentials_command': ['credentials']})
        client.connect.assert_called_once_with(
            'example.invalid', port=22, username='fixture', password=None,
            key_filename=str(Path('~/keys/publisher').expanduser()),
            look_for_keys=False, allow_agent=False, timeout=20, sock=None)

    def test_password_login_is_unchanged(self):
        credentials = json.dumps({'host': 'example.invalid', 'port': '2222', 'username': 'fixture',
                                  'password': 'test-secret'}).encode()
        client = Mock()
        with patch.object(publisher.subprocess, 'check_output', return_value=credentials), \
                patch.object(publisher.paramiko, 'SSHClient', return_value=client):
            publisher.connect_publisher({'credentials_command': ['credentials']})
        client.connect.assert_called_once_with(
            'example.invalid', port=2222, username='fixture', password='test-secret',
            look_for_keys=False, allow_agent=False, timeout=20, sock=None)

    def test_login_needs_exactly_one_method_before_connecting(self):
        for extra in ({}, {'password': ''}, {'password': 'test-secret', 'key_filename': '/keys/publisher'}):
            with self.subTest(extra=extra):
                credentials = json.dumps({'host': 'example.invalid', 'username': 'fixture', **extra}).encode()
                with patch.object(publisher.subprocess, 'check_output', return_value=credentials), \
                        patch.object(publisher.paramiko, 'SSHClient') as client, \
                        patch.object(publisher.socket, 'create_connection') as connection:
                    with self.assertRaisesRegex(ValueError, 'exactly one of password or key_filename'):
                        publisher.connect_publisher({'credentials_command': ['credentials'],
                                                     'ssh_proxy': 'http://proxy.invalid:8080'})
                client.assert_not_called()
                connection.assert_not_called()

    def test_reconciliation_still_rejects_other_platforms(self):
        manifest = {'files': {'darwin-arm64': {}}, 'build': 1}
        with self.assertRaises(ValueError):
            validate_next(manifest, manifest)

    def test_rolled_back_build_is_rejected_before_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            staged = root / 'releases/hub/.upload-test'
            staged.mkdir(parents=True)
            (root / 'hub.json').write_text('{}')
            (staged / 'latest.json').write_text(json.dumps({'build': 42}))
            state = {'auto': True, 'can_update': True,
                     'latest': {'build': 42, 'bad': True}}
            with patch.object(promoter, 'api', return_value=state) as api, \
                    patch.object(promoter, 'nodes', return_value={}):
                with self.assertRaisesRegex(RuntimeError, 'already rolled back'):
                    promoter.promote({'root': directory, 'staged': str(staged)})
            api.assert_called_once_with(root, 'hub/update')
            self.assertTrue((staged / 'latest.json').exists())
            self.assertFalse((root / 'releases/hub/latest.json').exists())



    def test_unsafe_remote_directory_is_rejected_before_any_put_or_promotion(self):
        for linked in ['releases', 'releases/hub', 'releases/hub-main']:
            with self.subTest(linked=linked), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                runtime = root / 'runtime'
                feed = runtime / 'releases/hub-main'
                feed.mkdir(parents=True)
                (runtime / 'releases/hub').mkdir()
                path = runtime / linked
                moved = path.with_name(path.name + '-real')
                path.rename(moved)
                path.symlink_to(moved, target_is_directory=True)
                config = root / 'config.json'
                config.write_text(json.dumps({'credentials_command': ['credentials'],
                                             'remote_root': str(runtime),
                                             'release_namespace': 'hub-main'}))
                (root / 'latest.json').write_text(json.dumps({
                    'build': 1, 'files': {'linux-amd64': {'name': 'artifact.gz'}}}))
                sftp = Mock()
                sftp.lstat.side_effect = lambda path: Path(path).lstat()
                sftp.normalize.side_effect = lambda path: str(Path(path).resolve(strict=True))
                client = MagicMock()
                client.open_sftp.return_value.__enter__.return_value = sftp
                credentials = json.dumps({'host': 'example.invalid', 'username': 'fixture',
                                          'password': 'test-secret'}).encode()
                with patch('sys.argv', ['publisher', '--config', str(config), '--artifact-dir', directory]), \
                        patch.object(publisher.subprocess, 'check_output', return_value=credentials), \
                        patch.object(publisher.paramiko, 'SSHClient', return_value=client):
                    with self.assertRaisesRegex(ValueError, 'non-symlink'):
                        publisher.main()
                sftp.put.assert_not_called()
                client.exec_command.assert_not_called()

    def test_remote_upload_rechecks_paths_before_every_put(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = root / 'runtime'
            runtime.mkdir()
            config = root / 'config.json'
            config.write_text(json.dumps({'credentials_command': ['credentials'],
                                         'remote_root': str(runtime)}))
            (root / 'latest.json').write_text(json.dumps({
                'build': 1, 'files': {'linux-amd64': {'name': 'artifact.gz'}}}))
            sftp = Mock()
            sftp.lstat.side_effect = lambda path: Path(path).lstat()
            sftp.normalize.side_effect = lambda path: str(Path(path).resolve(strict=True))
            sftp.mkdir.side_effect = lambda path, mode: Path(path).mkdir(mode=mode)
            def replace_staged(local, remote):
                staged = Path(remote).parent
                moved = staged.with_name('.upload-moved')
                staged.rename(moved)
                staged.symlink_to(moved, target_is_directory=True)
            sftp.put.side_effect = replace_staged
            client = MagicMock()
            client.open_sftp.return_value.__enter__.return_value = sftp
            credentials = json.dumps({'host': 'example.invalid', 'username': 'fixture',
                                      'password': 'test-secret'}).encode()
            with patch('sys.argv', ['publisher', '--config', str(config), '--artifact-dir', directory]), \
                    patch.object(publisher.subprocess, 'check_output', return_value=credentials), \
                    patch.object(publisher.paramiko, 'SSHClient', return_value=client):
                with self.assertRaisesRegex(ValueError, 'non-symlink'):
                    publisher.main()
            self.assertEqual(sftp.put.call_count, 1)
            client.exec_command.assert_not_called()


    def publish_with(self, extra):
        """Run the hub publisher up to its remote command with SFTP and SSH fakes; return (connect, client)."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = root / 'runtime'
            runtime.mkdir()
            config = root / 'config.json'
            config.write_text(json.dumps({'credentials_command': ['credentials'],
                                         'remote_root': str(runtime), **extra}))
            if extra.get('publish_nodes'):
                packages = release(root, nodes=True)
                hub = copy.deepcopy(packages)
                hub.pop('nodes')
                hub['files'].pop('darwin-arm64')
            else:
                hub = {'build': 1, 'files': {'linux-amd64': {'name': 'artifact.gz'}}}
                (root / 'artifact.gz').write_bytes(b'fixture')
            (root / 'latest.json').write_text(json.dumps(hub))
            sftp = Mock()
            sftp.lstat.side_effect = lambda path: Path(path).lstat()
            sftp.normalize.side_effect = lambda path: str(Path(path).resolve(strict=True))
            sftp.mkdir.side_effect = lambda path, mode: Path(path).mkdir(mode=mode)
            client = MagicMock()
            client.open_sftp.return_value.__enter__.return_value = sftp
            stdout, stderr = MagicMock(), Mock()
            stdout.__iter__.return_value = iter(())
            stdout.channel.recv_exit_status.return_value = 0
            stderr.read.return_value = b''
            client.exec_command.return_value = Mock(), stdout, stderr
            connect = Mock(return_value=(client, None))
            with patch('sys.argv', ['publisher', '--config', str(config), '--artifact-dir', directory]), \
                    patch.object(publisher, 'connect_publisher', connect):
                try:
                    publisher.main()
                finally:
                    self.last = (connect, client)
            return client.exec_command.call_args.args[0]

    def test_node_publication_uploads_both_programs_once(self):
        self.publish_with({'publish_nodes': True})
        _, client = self.last
        sftp = client.open_sftp.return_value.__enter__.return_value
        names = [Path(call.args[0]).name for call in sftp.put.call_args_list]
        self.assertCountEqual(names, ['latest.json', 'node-manifest.json'] + [
            f'{program}-{platform}-aaaaaaaaaaaa.gz'
            for program in ('personad', 'persona-node') for platform in ('linux-amd64', 'darwin-arm64')])

    def test_remote_program_runs_under_python3_by_default(self):
        self.assertEqual(self.publish_with({}), 'python3 -')

    def test_configured_remote_python_runs_the_program(self):
        self.assertEqual(self.publish_with({'remote_python': '/opt/python3.13/bin/python3'}),
                         '/opt/python3.13/bin/python3 -')

    def test_invalid_remote_python_fails_before_connecting(self):
        for value in ('', 'python3 -c evil', ['python3'], 'py\tthon'):
            with self.subTest(value=value):
                with self.assertRaisesRegex(ValueError, 'remote_python'):
                    self.publish_with({'remote_python': value})
                connect, client = self.last
                connect.assert_not_called()
                client.exec_command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
