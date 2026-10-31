#!/usr/bin/env python3
"""Explicit live-model test of the actual v1.14.2 -> v1.14.10 fork conflicts."""
import argparse
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--control', type=Path, required=True)
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location('resolver', args.control / 'agent_resolver.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    with tempfile.TemporaryDirectory(prefix='historical-e2e-', dir=args.state) as directory:
        root = Path(directory) / 'repo'
        subprocess.run(['git', 'clone', '--quiet', '--no-hardlinks', str(args.source), str(root)], check=True)

        def git(*command, check=True):
            result = subprocess.run(['git', *command], cwd=root, text=True, check=check,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            return result.stdout.strip()

        git('checkout', '-q', '-b', 'resolver-e2e', '6efe3956')
        git('merge', '--no-edit', 'v1.14.10', check=False)
        conflicts = git('diff', '--name-only', '--diff-filter=U').splitlines()
        expected = ['apps/manager-server/internal/http/router/router.go',
                    'apps/web/src/features/demo/demoFixtures.ts']
        assert sorted(conflicts) == sorted(expected), conflicts
        print('Reproduced both real historical conflicts', flush=True)
        agent = module.Resolver(root, args.state, 'historical-e2e')
        result = agent.run('Resolve this real historical merge. Personal baseline: 6efe3956. '
                           'Upstream: v1.14.10. Preserve both routing-priority handlers and the '
                           'translated Codex quota labels, while including upstream favicon routes. '
                           'Conflicted files: ' + json.dumps(conflicts))
        for path in conflicts:
            assert '<<<<<<<' not in (root / path).read_text(), path
        router = (root / expected[0]).read_text()
        for symbol in ('routingprioritycontroller.Path', 'routingprioritycontroller.PinPath',
                       'panelHandler.Favicon', 'panelHandler.AppleTouchIcon'):
            assert symbol in router, symbol
        demo = (root / expected[1]).read_text()
        assert "labelKey: 'codex_quota.primary_window'" in demo
        assert "labelKey: 'codex_quota.secondary_window'" in demo
        git('add', '--all')
        git('diff', '--cached', '--check', '--', *result['changed_files'])
        git('-c', 'core.hooksPath=/dev/null', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'Test historical merge resolution')
        review = agent.run('Review this historical conflict resolution independently. '
                           'Personal baseline: 6efe3956. Upstream: v1.14.10. Agent-edited files: ' +
                           json.dumps(result['changed_files']) +
                           '. Other changes are the upstream merge. Check preservation of both '
                           'routing-priority bindings, the new favicon bindings, and translated '
                           'Codex labels. The controller has checked those invariants and conflict '
                           'markers independently. Return your approval or concrete objections.', review=True)
        if not review['approved']:
            raise RuntimeError('Independent review rejected resolution: ' + json.dumps(review))
        (args.state / 'historical-conflict.patch').write_text(git('diff', 'v1.14.10', 'HEAD', '--', *conflicts))
        report = {'passed': True, 'conflicts': conflicts, 'repair': result, 'review': review}
        (args.state / 'historical-e2e-result.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2), flush=True)


if __name__ == '__main__':
    main()
