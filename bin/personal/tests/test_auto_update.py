import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('updater', Path(__file__).parents[1] / 'auto-update.py')
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


def old_container():
    return {
        'Mounts': [{'Type': 'volume', 'Name': 'original', 'Destination': '/data'}],
        'HostConfig': {'NetworkMode': 'bridge', 'Binds': ['original:/data'],
                       'PortBindings': {'18317/tcp': [{'HostIp': '100.79.242.61', 'HostPort': '18317'}]}},
        'Config': {'Env': ['CUSTOM=keep-me', 'CLAUDE_RESET_PRIORITY=true'], 'Cmd': None},
    }


class UpdateTests(unittest.TestCase):
    def test_service_stop_recovers_without_checking_for_an_update(self):
        with tempfile.TemporaryDirectory() as state, patch.object(updater, 'STATE', Path(state)), \
                patch.object(updater.sys, 'argv', ['auto-update.py', '--recover-only']), \
                patch.object(updater, 'recover') as recover, patch.object(updater, 'update') as update:
            updater.main()
            recover.assert_called_once_with()
            update.assert_not_called()

    def test_preserves_environment_binding_and_changes_only_data_volume(self):
        args = updater.create_args(old_container(), 'new-image', 'new-volume')
        self.assertIn('CUSTOM=keep-me', args)
        self.assertIn('100.79.242.61:18317:18317/tcp', args)
        self.assertIn('new-volume:/data', args)
        self.assertNotIn('original:/data', args)
        self.assertEqual(args[-1], 'new-image')

    def test_rejects_unexpected_mounts_before_touching_running_service(self):
        old = old_container()
        old['Mounts'].append({'Type': 'bind', 'Destination': '/extra'})
        with self.assertRaises(RuntimeError):
            updater.create_args(old, 'new-image', 'new-volume')

    def test_failed_copy_restarts_original_without_removing_it(self):
        self.exercise_failure('copy')

    def test_failed_new_container_restores_original_name_and_volume(self):
        self.exercise_failure('start')

    def test_failed_health_restores_original_name_and_volume(self):
        self.exercise_failure('health')

    def exercise_failure(self, phase):
        names = {updater.NAME}
        calls = []
        failed = False

        def docker(*args, **kwargs):
            nonlocal failed
            calls.append(args)
            if args[0] == 'ps':
                return '\n'.join(names)
            if args[0] == 'rename':
                names.remove(args[1])
                names.add(args[2])
            if args[0] == 'create':
                names.add(updater.NAME)
            if args[0] == 'rm':
                names.discard(args[-1])
            if not failed and ((phase == 'copy' and args[0] == 'run') or
                               (phase == 'start' and args[0] == 'start')):
                failed = True
                raise RuntimeError('injected failure')
            return 0

        health_count = 0

        def health(*args, **kwargs):
            nonlocal health_count
            health_count += 1
            if phase == 'health' and health_count == 1:
                raise RuntimeError('injected health failure')

        with tempfile.TemporaryDirectory() as state, patch.object(updater, 'STATE', Path(state)), \
                patch.object(updater, 'docker', side_effect=docker), \
                patch.object(updater, 'inspect', return_value=old_container()), \
                patch.object(updater, 'healthy', side_effect=health):
            with self.assertRaises(RuntimeError):
                updater.deploy('new-image', 'abc')
            self.assertEqual(names, {updater.NAME})
            self.assertIn(('start', updater.NAME), calls)
            if phase == 'copy':
                self.assertNotIn(('rm', '-f', updater.NAME), calls)
            self.assertFalse((Path(state) / 'deployment.json').exists())

    def test_crash_recovery_restores_renamed_previous_container(self):
        with tempfile.TemporaryDirectory() as state, patch.object(updater, 'STATE', Path(state)), \
                patch.object(updater, 'healthy') as health, patch.object(updater, 'docker') as docker:
            updater.save('deployment.json', {'old': 'backup', 'volume': 'failed-volume', 'committed': False})
            docker.return_value = 'backup\n' + updater.NAME
            updater.recover()
            docker.assert_any_call('rm', '-f', updater.NAME)
            docker.assert_any_call('rename', 'backup', updater.NAME)
            health.assert_called_once_with(updater.NAME)

    def test_committed_deploy_is_not_rolled_back_on_next_run(self):
        with tempfile.TemporaryDirectory() as state, patch.object(updater, 'STATE', Path(state)), \
                patch.object(updater, 'docker') as docker:
            updater.save('deployment.json', {'committed': True})
            updater.recover()
            docker.assert_not_called()


if __name__ == '__main__':
    unittest.main()
