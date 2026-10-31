from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch, Mock
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).parents[1]))
import agent_resolver as resolver

spec = importlib.util.spec_from_file_location('updater_agent_test', Path(__file__).parents[1] / 'auto-update.py')
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


class ResolverTests(unittest.TestCase):
    def test_image_build_context_contains_no_private_broker_configuration(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            (state / 'agent-config.json').write_text(json.dumps({'model': 'configured-model', 'api_key': 'private-key'}))

            def build(*args, **kwargs):
                context = Path(args[-1])
                self.assertEqual([p.name for p in context.iterdir()], ['Dockerfile'])
                self.assertNotIn('private-key', (context / 'Dockerfile').read_text())

            with patch.object(resolver, 'command', side_effect=build):
                resolver.Resolver(state, state, 'owner').prepare()

    def test_broker_exposes_only_inference_and_keeps_real_credential_private(self):
        received = []

        class Upstream(BaseHTTPRequestHandler):
            def do_POST(self):
                received.append((self.path, self.headers.get('Authorization')))
                self.rfile.read(int(self.headers['Content-Length']))
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.end_headers()
                self.wfile.write(b'data: {"type":"response.completed"}\n\n')

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            config = {'base_url': f'http://127.0.0.1:{server.server_port}/v1', 'api_key': 'private-model-key'}
            with resolver.broker(config, '127.0.0.1') as (port, token):
                base = f'http://127.0.0.1:{port}'
                for path, auth in [('/v0/management/config', token), ('/v1/responses', 'wrong')]:
                    request = urllib.request.Request(base + path, data=b'{}', headers={'Authorization': 'Bearer ' + auth})
                    with self.assertRaises(urllib.error.HTTPError) as failure:
                        urllib.request.urlopen(request)
                    self.assertEqual(failure.exception.code, 403)
                self.assertEqual(received, [])
                request = urllib.request.Request(base + '/v1/responses', data=b'{}', headers={'Authorization': 'Bearer ' + token})
                with urllib.request.urlopen(request) as response:
                    body = response.read()
                self.assertIn(b'response.completed', body)
                self.assertNotIn(b'private-model-key', body)
                self.assertEqual(received, [('/v1/responses', 'Bearer private-model-key')])
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_protected_files_and_test_deletions_are_rejected_before_copying(self):
        for path in ['bin/personal/auto-update.py', '.github/workflows/test.yml', 'tests/test_x.py', 'x_test.go']:
            with self.subTest(path=path), tempfile.TemporaryDirectory() as root_dir, tempfile.TemporaryDirectory() as work_dir:
                root, work = Path(root_dir), Path(work_dir)
                for base in (root, work):
                    file = base / path
                    file.parent.mkdir(parents=True, exist_ok=True)
                    file.write_text('original')
                before = resolver.snapshot(work)
                (work / path).unlink()
                with self.assertRaises(RuntimeError):
                    resolver.apply_edits(root, work, before)
                self.assertEqual((root / path).read_text(), 'original')

    def test_source_repair_applies_but_new_symlinks_are_rejected(self):
        with tempfile.TemporaryDirectory() as root_dir, tempfile.TemporaryDirectory() as work_dir:
            root, work = Path(root_dir), Path(work_dir)
            (root / 'source.py').write_text('old')
            (work / 'source.py').write_text('old')
            before = resolver.snapshot(work)
            (work / 'source.py').write_text('fixed')
            self.assertEqual(resolver.apply_edits(root, work, before), ['source.py'])
            self.assertEqual((root / 'source.py').read_text(), 'fixed')
            before = resolver.snapshot(work)
            (work / 'escape').symlink_to('/etc/passwd')
            with self.assertRaisesRegex(RuntimeError, 'symlink'):
                resolver.apply_edits(root, work, before)

    def test_agent_cannot_replace_test_commands_with_a_success_stub(self):
        with tempfile.TemporaryDirectory() as root_dir, tempfile.TemporaryDirectory() as work_dir:
            root, work = Path(root_dir), Path(work_dir)
            original = '{"scripts":{"test:web":"vitest run"}}'
            (root / 'package.json').write_text(original)
            (work / 'package.json').write_text(original)
            before = resolver.snapshot(work)
            (work / 'package.json').write_text('{"scripts":{"test:web":"true"}}')
            with self.assertRaisesRegex(RuntimeError, 'validation commands'):
                resolver.apply_edits(root, work, before)
            self.assertEqual((root / 'package.json').read_text(), original)

    def test_timeout_kills_agent_and_cleans_network_without_exposing_untracked_secrets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'repo'
            state = Path(directory) / 'state'
            root.mkdir()
            state.mkdir()
            (root / '.git').mkdir()
            (root / 'source.py').write_text('old')
            (root / '.env').write_text('private-production-secret')
            (state / 'agent-config.json').write_text(json.dumps({'api_key': 'real-key', 'base_url': 'http://model/v1', 'model': 'configured-model'}))
            commands = []

            def command(*args, **kwargs):
                if args[:3] == ('docker', 'network', 'inspect'):
                    return '[{"IPAM":{"Config":[{"Gateway":"127.0.0.1"}]}}]'

            @contextmanager
            def broker(*args):
                yield 8000, 'ephemeral-token'

            def run(args, **kwargs):
                commands.append(args)
                if args[:2] == ['docker', 'run']:
                    mounts = [args[i + 1] for i, arg in enumerate(args[:-1]) if arg == '-v']
                    workspace = Path(next(m.split(':')[0] for m in mounts if m.endswith(':/workspace')))
                    self.assertFalse((workspace / '.env').exists())
                    self.assertNotIn('real-key', ' '.join(args))
                    self.assertTrue(any(m.endswith(':/workspace/.git:ro') for m in mounts))
                    self.assertNotIn('/var/run/docker.sock', ' '.join(args))
                    raise subprocess.TimeoutExpired(args, 1)
                return Mock(returncode=0)

            with patch.object(resolver, 'command', side_effect=command), \
                    patch.object(resolver, 'tracked_files', return_value=['source.py']), \
                    patch.object(resolver, 'broker', broker), patch.object(resolver.subprocess, 'run', side_effect=run):
                agent = resolver.Resolver(root, state, 'test-owner')
                with self.assertRaises(subprocess.TimeoutExpired):
                    agent.run('Resolve the conflict')
            self.assertTrue(any(args[:3] == ['docker', 'rm', '-f'] for args in commands))
            self.assertTrue(any(args[:3] == ['docker', 'network', 'rm'] for args in commands))
            self.assertEqual((root / 'source.py').read_text(), 'old')


class RepairIntegrationTests(unittest.TestCase):
    def test_failed_validation_and_review_each_require_a_new_verified_repair(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(updater, 'STATE', Path(directory)), \
                patch.object(updater, 'verify', side_effect=[RuntimeError('test failed'), None, None]) as verify, \
                patch.object(updater, 'docker'), patch.object(updater, 'healthy'), \
                patch.object(updater, 'run', return_value='a' * 40):
            repair = Mock()
            repair.review.side_effect = [{'approved': False, 'findings': ['lost routing pin']}, {'approved': True}]
            sha, image = updater.validate_and_build('v1.0.0', repair)
            self.assertEqual(verify.call_count, 3)
            self.assertEqual(repair.repair.call_count, 2)
            self.assertIn('test failed', repair.repair.call_args_list[0].args[0])
            self.assertIn('lost routing pin', repair.repair.call_args_list[1].args[0])
            self.assertEqual(sha, 'a' * 40)
            self.assertTrue(image.endswith('a' * 12))

    def test_interrupted_candidate_is_reset_before_another_attempt(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(updater, 'STATE', Path(directory)), \
                patch.object(updater, 'ROOT', Path(directory)), patch.object(updater, 'run') as run:
            updater.save('candidate.json', {'root': str(Path(directory).resolve()), 'original': 'b' * 40})
            updater.recover_candidate()
            run.assert_any_call('git', 'reset', '--hard', 'b' * 40)
            run.assert_any_call('git', 'clean', '-fd')
            self.assertFalse((Path(directory) / 'candidate.json').exists())

    def test_control_only_updates_do_not_skip_new_releases_or_runtime_changes(self):
        sha = 'a' * 40
        with patch.object(updater, 'run', side_effect=[0, 'bin/personal/agent_resolver.py\ndocs/personal-fork.md']):
            self.assertTrue(updater.control_only_update(sha, 'v1.0.0'))
        with patch.object(updater, 'run', return_value=1):
            self.assertFalse(updater.control_only_update(sha, 'v2.0.0'))
        with patch.object(updater, 'run', side_effect=[0, 'apps/web/src/App.tsx']):
            self.assertFalse(updater.control_only_update(sha, 'v1.0.0'))
        self.assertFalse(updater.control_only_update('', 'v1.0.0'))

    def test_agent_result_is_committed_as_merge_by_controller(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def git(*args, check=True):
                return subprocess.run(['git', *args], cwd=root, check=check, text=True,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()

            git('init', '-q')
            git('config', 'user.name', 'Test')
            git('config', 'user.email', 'test@example.com')
            (root / 'source.txt').write_text('base\n')
            git('add', '.')
            git('commit', '-qm', 'base')
            git('checkout', '-qb', 'upstream')
            (root / 'source.txt').write_text('upstream\n')
            git('commit', '-qam', 'upstream')
            git('checkout', '-qb', 'personal', 'HEAD~1')
            (root / 'source.txt').write_text('personal\n')
            git('commit', '-qam', 'personal')
            agent = Mock()

            def fix(task):
                self.assertIn('conflict', task)
                (root / 'source.txt').write_text('personal and upstream\n')
                return {'changed_files': ['source.txt'], 'status': 'resolved'}

            agent.run.side_effect = fix

            def run(*args, capture=False, **kwargs):
                result = subprocess.run(args, cwd=root, check=kwargs.get('check', True), text=True,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                return result.stdout.strip() if capture else result.returncode

            with patch.object(updater, 'ROOT', root), patch.object(updater, 'STATE', root), \
                    patch.object(updater, 'run', side_effect=run), patch.object(updater, 'Resolver', return_value=agent):
                session = updater.RepairSession('upstream')
                git('merge', 'upstream', check=False)
                self.assertTrue(git('ls-files', '--unmerged'))
                session.repair('Resolve the conflict')
                self.assertEqual(git('ls-files', '--unmerged'), '')
                self.assertEqual(len(git('rev-list', '--parents', '-n', '1', 'HEAD').split()), 3)
                self.assertEqual((root / 'source.txt').read_text(), 'personal and upstream\n')

    def test_repair_attempts_and_failed_candidate_retry_are_bounded(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(updater, 'STATE', Path(directory)), \
                patch.object(updater, 'run', return_value='revision'), patch.object(updater, 'Resolver') as factory:
            factory.return_value.run.return_value = {'changed_files': ['source.py']}
            session = updater.RepairSession('v1.0.0')
            for _ in range(3):
                session.repair('failed validation')
            with self.assertRaisesRegex(RuntimeError, 'three attempts'):
                session.repair('still failing')
            self.assertEqual(factory.return_value.run.call_count, 3)
            session.failed()
            another = updater.RepairSession('v1.0.0')
            with self.assertRaisesRegex(RuntimeError, 'cooling down'):
                another.repair('retry')
            self.assertEqual(factory.return_value.run.call_count, 3)


if __name__ == '__main__':
    unittest.main()
