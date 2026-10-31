#!/usr/bin/env python3
"""Kiwi's unattended stable-release updater. Run from a dedicated checkout."""
import fcntl
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parent))
from agent_resolver import Resolver, cleanup as cleanup_agents

ROOT = Path(os.environ.get('CPAMP_REPO', str(Path(__file__).resolve().parents[2])))
STATE = Path(os.environ.get('CPAMP_UPDATE_STATE', str(Path.home() / '.local/state/cpamp-auto-update')))
NAME = os.environ.get('CPAMP_CONTAINER', 'cpa-manager-plus')


def run(*args, capture=False, cwd=ROOT, check=True, log=None):
    result = subprocess.run(args, cwd=cwd, text=True, check=check,
                            stdout=subprocess.PIPE if capture else log,
                            stderr=subprocess.STDOUT if log else None)
    return result.stdout.strip() if capture else result.returncode


def docker(*args, **kwargs):
    try:
        return run('docker', *args, **kwargs)
    except subprocess.CalledProcessError as exc:
        # Docker creation arguments include the existing environment. Never
        # copy those values into a status file or exception traceback.
        raise RuntimeError(f'Docker {args[0]} failed with exit code {exc.returncode}') from None


def inspect(name):
    return json.loads(docker('inspect', name, capture=True))[0]


def save(name, value):
    temp = STATE / (name + '.tmp')
    temp.write_text(json.dumps(value, indent=2) + '\n')
    temp.replace(STATE / name)


def healthy(name, timeout=120, stable=15):
    """Check the web app, not just container existence; allow startup migrations."""
    deadline = time.monotonic() + timeout
    since = None
    while time.monotonic() < deadline:
        ok = docker('exec', name, 'sh', '-c',
                    'wget -T 5 -q -O /dev/null http://127.0.0.1:18317/health && '
                    'wget -T 5 -q -O /dev/null http://127.0.0.1:18317/management.html',
                    check=False) == 0
        if ok:
            since = since or time.monotonic()
            if time.monotonic() - since >= stable:
                return
        else:
            since = None
        time.sleep(2)
    raise RuntimeError(f'{name} did not become and remain healthy')


def create_args(old, image, volume):
    """Preserve the deployed environment and bindings; reject unsupported layouts."""
    mounts = old['Mounts']
    if len(mounts) != 1 or mounts[0]['Type'] != 'volume' or mounts[0]['Destination'] != '/data':
        raise RuntimeError('Expected one named /data volume; refusing to change deployment layout')
    host = old['HostConfig']
    if host['NetworkMode'] != 'bridge' or host.get('Privileged') or host.get('Binds') not in (None, [mounts[0]['Name'] + ':/data']):
        raise RuntimeError('Unsupported host settings; refusing to discard custom settings')
    args = ['create', '--name', NAME, '--restart', 'unless-stopped', '-v', volume + ':/data']
    for key, bindings in (host.get('PortBindings') or {}).items():
        for binding in bindings:
            args += ['-p', f"{binding['HostIp']}:{binding['HostPort']}:{key}"]
    for entry in old['Config']['Env']:
        args += ['-e', entry]
    args += [image]
    args += old['Config'].get('Cmd') or []
    return args


def recover():
    """A killed updater must not strand a stopped or half-upgraded deployment."""
    journal = STATE / 'deployment.json'
    if not journal.exists():
        return
    tx = json.loads(journal.read_text())
    if tx.get('committed'):
        return
    old = tx['old']
    names = docker('ps', '-a', '--format', '{{.Names}}', capture=True).splitlines()
    if old in names:
        if NAME in names:
            docker('rm', '-f', NAME)
        docker('rename', old, NAME)
    docker('start', NAME)
    healthy(NAME)
    docker('volume', 'rm', tx['volume'], check=False)
    journal.unlink()
    print('Recovered the previous deployment', flush=True)


def deploy(image, sha):
    old = inspect(NAME)
    args_check = create_args(old, image, 'validation-only')
    del args_check
    stamp = time.strftime('%Y%m%d%H%M%S', time.gmtime())
    backup = NAME + '-rollback-' + stamp
    volume = NAME + '-data-' + stamp
    source = old['Mounts'][0]['Name']
    # Pull copy utility before the downtime begins.
    docker('pull', 'alpine:3.21')
    docker('volume', 'create', '--label', 'cpamp.auto-update=true', volume)
    save('deployment.json', {'old': backup, 'volume': volume, 'sha': sha, 'committed': False})
    try:
        docker('stop', '--timeout', '60', NAME)
        # Copy only after a clean shutdown, including SQLite WAL and archive files.
        docker('run', '--rm', '-v', source + ':/source:ro', '-v', volume + ':/target',
               'alpine:3.21', 'sh', '-ec', 'cp -a /source/. /target/')
        docker('rename', NAME, backup)
        docker(*create_args(old, image, volume))
        docker('start', NAME)
        healthy(NAME)
        save('deployment.json', {'old': backup, 'volume': volume, 'sha': sha, 'committed': True})
    except BaseException:
        recover()
        raise
    print(f'Deployed {sha}; rollback container: {backup}', flush=True)
    # Retain two known-good deployments. Never prune unrelated Docker resources.
    names = docker('ps', '-a', '--format', '{{.Names}}', capture=True).splitlines()
    backups = sorted(n for n in names if re.fullmatch(re.escape(NAME) + r'-rollback-\d{14}', n))
    for name in backups[:-2]:
        previous = inspect(name)
        if previous['State']['Running']:
            continue
        docker('rm', name)
        for mount in previous['Mounts']:
            if mount['Type'] == 'volume':
                labels = json.loads(docker('volume', 'inspect', mount['Name'], capture=True))[0].get('Labels') or {}
                if labels.get('cpamp.auto-update') == 'true':
                    docker('volume', 'rm', mount['Name'], check=False)


def verify():
    with (STATE / 'validation.log').open('w') as log:
        _verify(log)


def _verify(log):
    dockerfile = (ROOT / 'Dockerfile.manager-server').read_text()
    node = re.search(r'FROM\s+--platform=\$BUILDPLATFORM\s+(\S+)\s+AS web-build', dockerfile)[1]
    go = re.search(r'FROM\s+--platform=\$BUILDPLATFORM\s+(\S+)\s+AS service-build', dockerfile)[1]
    uid = f'{os.getuid()}:{os.getgid()}'
    test_image = 'cpa-manager-plus:verify-node'
    docker('build', '-f', 'bin/personal/Dockerfile.verify', '--build-arg',
           'NODE_IMAGE=' + node, '-t', test_image, '.', log=log)
    # Frontend and Go checks use separate source trees and can run together.
    # Bound test concurrency so scheduled checks leave headroom for live traffic.
    with ThreadPoolExecutor(max_workers=2) as pool:
        checks = [
            pool.submit(docker, 'run', '--rm', '--cpus', '2', '--cpu-shares', '128',
                        '--memory', '3g', '--memory-swap', '3g', '--user', uid, '-e', 'HOME=/tmp',
                        '-e', 'GIT_CONFIG_COUNT=1', '-e', 'GIT_CONFIG_KEY_0=safe.directory',
                        '-e', 'GIT_CONFIG_VALUE_0=/src',
                        '-v', str(ROOT) + ':/src', '-w', '/src', test_image, 'sh', '-ec',
                        'npm ci && npm run lint && npm run type-check && '
                        'npm run test:web -- --maxWorkers=4 && npm run test:repo -- --maxWorkers=4', log=log),
            pool.submit(docker, 'run', '--rm', '--cpus', '2', '--cpu-shares', '128',
                        '--memory', '3g', '--memory-swap', '3g', '--user', uid, '-e', 'HOME=/tmp',
                        '-e', 'GOCACHE=/tmp/go-cache', '-e', 'GOMAXPROCS=4',
                        '-v', str(ROOT) + ':/src', '-w', '/src/apps/manager-server', go,
                        'sh', '-ec', 'go test -p 4 ./...', log=log),
        ]
        failures = []
        for check in checks:
            try:
                check.result()
            except Exception as exc:
                failures.append(exc)
        if failures:
            raise failures[0]
    run(sys.executable, '-m', 'unittest', 'discover', '-s', 'bin/personal/tests', '-v', log=log)


class RepairSession:
    """Bound model work; the updater alone commits, tests, publishes and deploys."""
    def __init__(self, tag):
        self.tag = tag
        self.original = run('git', 'rev-parse', 'HEAD', capture=True)
        self.target = run('git', 'rev-parse', tag, capture=True)
        self.key = self.original + ':' + self.target
        self.attempts = 0
        self.resolver = None
        self.changed_files = set()
        self.conflict_files = set()

    def task(self, reason):
        return (f'Personal baseline: {self.original}\nUpstream release: {self.tag} ({self.target})\n'
                f'Inspect the Git versions and retain the personal behavior.\n{reason}')

    def repair(self, reason):
        if self.attempts >= 3:
            raise RuntimeError('Automatic repair exhausted its three attempts')
        cooldown = STATE / 'repair-backoff.json'
        if self.resolver is None:
            if cooldown.exists():
                previous = json.loads(cooldown.read_text())
                if previous.get('key') == self.key and previous.get('retry_after', 0) > time.time():
                    raise RuntimeError('Automatic repair is cooling down; it retries within six hours')
            self.attempts += 1
            self.resolver = Resolver(ROOT, STATE, NAME)
            self.resolver.prepare()
        else:
            self.attempts += 1
        save('status.json', {'state': 'repairing', 'upstream': self.tag, 'attempt': self.attempts})
        print(f'Automatic repair {self.attempts}/3 for {self.tag}', flush=True)
        self.conflict_files.update(run('git', 'diff', '--name-only', '--diff-filter=U', capture=True).splitlines())
        result = self.resolver.run(self.task(reason))
        if not result.get('changed_files'):
            raise RuntimeError('Automatic repair made no changes')
        self.changed_files.update(result['changed_files'])
        run('git', 'add', '--all')
        checked = sorted(self.changed_files | self.conflict_files)
        try:
            run('git', 'diff', '--cached', '--check', '--', *checked, capture=True)
        except subprocess.CalledProcessError as exc:
            return self.repair('Conflict-marker or whitespace checks still fail in the repaired files:\n' + str(exc.output))
        if (ROOT / '.git/MERGE_HEAD').exists():
            run('git', '-c', 'core.hooksPath=/dev/null', '-c', 'commit.gpgsign=false', 'commit', '--no-edit')
        else:
            run('git', '-c', 'core.hooksPath=/dev/null', '-c', 'commit.gpgsign=false', 'commit', '-m',
                f'Repair personal integration with {self.tag}')

    def review(self):
        if self.resolver is None:
            return None
        return self.resolver.run(self.task(
            'Independent validation has passed. Review all integration and repair changes. '
            'Reject lost personal behavior, weakened checks, unsafe changes or unresolved conflicts. '
            'Other changes are the upstream merge. Agent-edited files: ' +
            json.dumps(sorted(self.changed_files))), review=True)

    def failed(self):
        if self.attempts:
            save('repair-backoff.json', {'key': self.key, 'retry_after': time.time() + 6 * 3600})


def diagnostic_tail():
    sections = []
    for name in ('validation.log', 'build.log'):
        path = STATE / name
        if path.exists():
            sections.append(name + ':\n' + path.read_text(errors='replace')[-24000:])
    return '\n\n'.join(sections)


def validate_and_build(tag, repair):
    while True:
        try:
            (STATE / 'build.log').unlink(missing_ok=True)
            print('Running independent validation', flush=True)
            verify()
            sha = run('git', 'rev-parse', 'HEAD', capture=True)
            image = 'cpa-manager-plus:auto-' + sha[:12]
            with (STATE / 'build.log').open('w') as log:
                docker('build', '-f', 'Dockerfile.manager-server', '--build-arg', 'VERSION=ench-' + tag,
                       '--build-arg', 'SOURCE_COMMIT=' + sha,
                       '--label', 'org.opencontainers.image.revision=' + sha,
                       '--label', 'org.opencontainers.image.version=ench-' + tag, '-t', image, '.', log=log)
            smoke = NAME + '-smoke'
            docker('rm', '-f', smoke, check=False)
            try:
                docker('run', '-d', '--name', smoke, '--network', 'none', image)
                healthy(smoke, stable=2)
            finally:
                docker('rm', '-f', smoke, check=False)
        except Exception as exc:
            repair.repair(f'Independent validation/build failed: {exc}\n{diagnostic_tail()}')
            continue
        review = repair.review()
        if review is not None and not review['approved']:
            repair.repair('Independent review rejected the repair:\n' + json.dumps(review))
            continue
        return sha, image


def install_control():
    if not (STATE / 'auto-update.py').exists():
        return
    for name in ('agent_resolver.py', 'agent-instructions.md', 'Dockerfile.agent', 'auto-update.py'):
        installed = STATE / name
        content = (ROOT / 'bin/personal' / name).read_bytes()
        if installed.exists() and installed.read_bytes() == content:
            continue
        temp = STATE / (name + '.new')
        temp.write_bytes(content)
        temp.chmod(0o600)
        temp.replace(installed)


def recover_candidate():
    marker = STATE / 'candidate.json'
    if not marker.exists():
        return
    candidate = json.loads(marker.read_text())
    if candidate.get('root') != str(ROOT.resolve()) or not re.fullmatch(r'[0-9a-f]{40}', candidate.get('original', '')):
        raise RuntimeError('Invalid interrupted-candidate journal')
    run('git', 'merge', '--abort', check=False)
    run('git', 'reset', '--hard', candidate['original'])
    run('git', 'clean', '-fd')
    marker.unlink()


def control_only_update(deployed, tag):
    if not re.fullmatch(r'[0-9a-f]{40}', deployed or ''):
        return False
    if run('git', 'merge-base', '--is-ancestor', tag, deployed, check=False) != 0:
        return False
    paths = run('git', 'diff', '--name-only', deployed, 'HEAD', capture=True).splitlines()
    return bool(paths) and all(path.startswith('bin/personal/') or path == 'docs/personal-fork.md' for path in paths)


def update():
    recover()
    if run('git', 'status', '--porcelain', '--untracked-files=no', capture=True):
        raise RuntimeError('Updater checkout contains tracked edits')
    run('git', 'fetch', 'origin', 'personal')
    run('git', 'fetch', 'upstream', '--tags')
    request = urllib.request.Request('https://api.github.com/repos/seakee/CPA-Manager-Plus/releases/latest',
                                     headers={'User-Agent': 'cpamp-personal-updater'})
    with urllib.request.urlopen(request, timeout=30) as response:
        release = json.load(response)
    tag = release['tag_name']
    if release['draft'] or release['prerelease'] or not re.fullmatch(r'v\d+\.\d+\.\d+', tag):
        raise RuntimeError('Latest release is not a stable version')
    run('git', 'checkout', '-B', 'auto-update-candidate', 'origin/personal')
    repair = RepairSession(tag)
    save('candidate.json', {'root': str(ROOT.resolve()), 'original': repair.original})
    try:
        try:
            run('git', '-c', 'core.hooksPath=/dev/null', '-c', 'commit.gpgsign=false', 'merge', '--no-edit', tag)
        except subprocess.CalledProcessError:
            conflicts = run('git', 'diff', '--name-only', '--diff-filter=U', capture=True)
            repair.repair('Resolve these merge conflicts, inspecting all three stages:\n' + conflicts)
        sha = run('git', 'rev-parse', 'HEAD', capture=True)
        current = inspect(NAME)['Config'].get('Labels') or {}
        if current.get('org.opencontainers.image.revision') == sha:
            healthy(NAME, timeout=30, stable=2)
            (STATE / 'candidate.json').unlink()
            return {'state': 'current', 'upstream': tag, 'sha': sha}
        if control_only_update(current.get('org.opencontainers.image.revision'), tag):
            run(sys.executable, '-m', 'unittest', 'discover', '-s', 'bin/personal/tests', '-v')
            install_control()
            healthy(NAME, timeout=30, stable=2)
            (STATE / 'candidate.json').unlink()
            return {'state': 'current', 'upstream': tag, 'sha': sha,
                    'deployed_sha': current['org.opencontainers.image.revision'], 'automation_updated': True}
        sha, image = validate_and_build(tag, repair)
    except Exception:
        repair.failed()
        run('git', 'merge', '--abort', check=False)
        run('git', 'reset', '--hard', repair.original)
        run('git', 'clean', '-fd')
        (STATE / 'candidate.json').unlink(missing_ok=True)
        raise
    # A non-force push refuses a concurrent personal-branch change.
    run('git', 'push', 'origin', 'HEAD:personal')
    (STATE / 'candidate.json').unlink()
    deploy(image, sha)
    # Keep the installed runner in step with tested code, without executing a
    # partially updated script in this process.
    install_control()
    return {'state': 'deployed', 'upstream': tag, 'sha': sha, 'image': image, 'agent_repairs': repair.attempts}


def main():
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (STATE / 'lock').open('w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        cleanup_agents(NAME)
        recover_candidate()
        if sys.argv[1:] == ['--recover-only']:
            recover()
            return
        try:
            result = update()
            save('status.json', dict(result, checked_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())))
        except Exception as exc:
            save('status.json', {'state': 'failed', 'error': str(exc),
                                 'checked_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())})
            raise


if __name__ == '__main__':
    main()
