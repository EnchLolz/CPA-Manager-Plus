"""Isolated Codex repair/review with an inference-only credential broker."""
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import hmac
import http.client
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlsplit

CONTROL = Path(__file__).resolve().parent
PROTECTED = ('.git', '.github', '.codex', '.agents', 'bin/personal', 'AGENTS.md', 'CLAUDE.md', 'CHANGELOG.md',
             '.gitignore', '.gitattributes', '.dockerignore', '.env', '.env.local', '.npmrc', '.gitmodules',
             'package-lock.json', 'apps/manager-server/go.sum')
IMAGE = 'cpa-manager-plus:resolver-0.164.0'


def protected(path):
    return (Path(path).name in ('AGENTS.md', 'CLAUDE.md') or
            any(path == prefix or path.startswith(prefix + '/') for prefix in PROTECTED))


def command(*args, cwd=None, capture=False):
    result = subprocess.run(args, cwd=cwd, check=True, text=True,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout.strip() if capture else None


@contextmanager
def broker(config, address):
    """Only this host-side process sees the real model credential."""
    token = secrets.token_urlsafe(32)
    upstream = urlsplit(config['base_url'])
    if upstream.scheme not in ('http', 'https') or upstream.query or upstream.fragment:
        raise RuntimeError('Invalid inference gateway URL')

    class Handler(BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.0'

        def log_message(self, *args):
            pass

        def do_GET(self):
            self.forward()

        def do_POST(self):
            self.forward()

        def forward(self):
            self.connection.settimeout(30)
            if not hmac.compare_digest(self.headers.get('Authorization', ''), 'Bearer ' + token):
                self.send_error(403)
                return
            routes = {('GET', '/v1/models'), ('POST', '/v1/responses'),
                      ('POST', '/v1/responses/compact')}
            if (self.command, self.path) not in routes:
                self.send_error(403)
                return
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if length < 0 or length > 32 * 1024 * 1024:
                    self.send_error(413)
                    return
                body = self.rfile.read(length) if length else None
                connection_type = http.client.HTTPSConnection if upstream.scheme == 'https' else http.client.HTTPConnection
                connection = connection_type(upstream.hostname, upstream.port, timeout=180)
                headers = {'Authorization': 'Bearer ' + config['api_key'],
                           'Content-Type': self.headers.get('Content-Type', 'application/json')}
                path = upstream.path.rstrip('/') + self.path.removeprefix('/v1')
                connection.request(self.command, path, body, headers)
                response = connection.getresponse()
                self.send_response(response.status)
                self.send_header('Content-Type', response.getheader('Content-Type', 'application/json'))
                self.end_headers()
                while chunk := response.read1(65536):
                    self.wfile.write(chunk)
                    self.wfile.flush()
                connection.close()
            except (OSError, ValueError, http.client.HTTPException):
                # Never echo a gateway URL, credential, or request body.
                self.close_connection = True

    server = ThreadingHTTPServer((address, 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server.server_port, token
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def tracked_files(root):
    result = subprocess.run(['git', 'ls-files', '-z'], cwd=root, check=True, stdout=subprocess.PIPE)
    return sorted(set(p.decode() for p in result.stdout.split(b'\0') if p))


def snapshot(directory):
    result = {}
    for path in directory.rglob('*'):
        relative = path.relative_to(directory).as_posix()
        if relative == '.git' or relative.startswith('.git/'):
            continue
        if path.is_symlink():
            result[relative] = ('symlink', os.readlink(path))
        elif path.is_file():
            result[relative] = ('file', path.read_bytes(), path.stat().st_mode & 0o777)
    return result


def apply_edits(root, workspace, before):
    after = snapshot(workspace)
    changed = sorted(p for p in before.keys() | after.keys() if before.get(p) != after.get(p))
    for path in changed:
        if protected(path) or Path(path).name.upper().startswith('CHANGELOG'):
            raise RuntimeError(f'Agent attempted to modify protected file: {path}')
        if path in after and after[path][0] != 'file':
            raise RuntimeError(f'Agent attempted to create or change a symlink: {path}')
        if path not in after and ('.test.' in path or path.endswith('_test.go') or path.startswith('tests/')):
            raise RuntimeError(f'Agent attempted to remove a test: {path}')
        if Path(path).name == 'package.json' and path in before and path in after:
            if json.loads(before[path][1]).get('scripts') != json.loads(after[path][1]).get('scripts'):
                raise RuntimeError(f'Agent attempted to change validation commands: {path}')
        destination = root / path
        for parent in (destination, *destination.parents):
            if parent == root:
                break
            if parent.is_symlink():
                raise RuntimeError(f'Unsafe destination for agent edit: {path}')
        if path not in before:
            ignored = subprocess.run(['git', 'check-ignore', '-q', '--', path], cwd=root).returncode
            if ignored == 0:
                raise RuntimeError(f'Agent created an ignored file: {path}')
    for path in changed:
        destination = root / path
        if path not in after:
            destination.unlink(missing_ok=True)
        else:
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(after[path][1])
            destination.chmod(after[path][2])
    return changed


class Resolver:
    def __init__(self, root, state, owner):
        self.root, self.state, self.owner = Path(root).resolve(), Path(state).resolve(), owner
        path = Path(os.environ.get('CPAMP_AGENT_CONFIG', str(self.state / 'agent-config.json')))
        self.config = json.loads(path.read_text())
        self.timeout = min(max(int(self.config.get('timeout_seconds', 900)), 30), 1200)

    def prepare(self):
        # The installed control directory also holds private broker settings.
        # Never send that directory as an image build context.
        with tempfile.TemporaryDirectory(prefix='agent-image-') as directory:
            context = Path(directory)
            shutil.copyfile(CONTROL / 'Dockerfile.agent', context / 'Dockerfile')
            command('docker', 'build', '-f', str(context / 'Dockerfile'), '-t', IMAGE, str(context))

    def run(self, task, review=False):
        label = 'cpamp.resolver=' + self.owner
        suffix = secrets.token_hex(6)
        network, container = 'cpamp-agent-net-' + suffix, 'cpamp-agent-' + suffix
        history = self.state / 'agent-runs'
        history.mkdir(parents=True, exist_ok=True, mode=0o700)
        log = history / (time.strftime('%Y%m%dT%H%M%S') + '-' + suffix + '.log')
        command('docker', 'network', 'create', '--internal', '--label', label, network)
        try:
            info = json.loads(command('docker', 'network', 'inspect', network, capture=True))[0]
            address = info['IPAM']['Config'][0]['Gateway']
            with tempfile.TemporaryDirectory(prefix='agent-', dir=self.state) as temporary:
                temporary = Path(temporary)
                workspace, output, control = (temporary / name for name in ('workspace', 'output', 'control'))
                for directory in (workspace, output, control):
                    directory.mkdir()
                for path in tracked_files(self.root):
                    source, destination = self.root / path, workspace / path
                    if source.is_file() or source.is_symlink():
                        destination.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copy2(source, destination, follow_symlinks=False)
                (workspace / '.git').mkdir()
                before = snapshot(workspace)
                schema = ({'approved': {'type': 'boolean'}, 'summary': {'type': 'string'},
                           'findings': {'type': 'array', 'items': {'type': 'string'}}} if review else
                          {'status': {'type': 'string', 'enum': ['resolved', 'blocked']},
                           'summary': {'type': 'string'},
                           'preserved_features': {'type': 'array', 'items': {'type': 'string'}}})
                (control / 'schema.json').write_text(json.dumps({'type': 'object', 'properties': schema,
                                                               'required': list(schema), 'additionalProperties': False}))
                instructions = (CONTROL / 'agent-instructions.md').read_text()
                prompt = instructions + '\n\n' + ('REVIEW ONLY\n' if review else 'REPAIR TASK\n') + task
                with broker(self.config, address) as (port, token):
                    environment = control / 'environment'
                    environment.write_text('CODEX_API_KEY=' + token + '\n')
                    environment.chmod(0o600)
                    args = ['docker', 'run', '-i', '--name', container, '--label', label, '--network', network,
                            '--user', f'{os.getuid()}:{os.getgid()}', '--cpus', '2', '--memory', '3g',
                            '--memory-swap', '3g', '--pids-limit', '256', '--cap-drop', 'ALL',
                            '--security-opt', 'no-new-privileges', '--read-only', '--tmpfs', '/tmp:rw,exec,mode=1777',
                            '--tmpfs', '/home/agent:rw,exec,mode=1777',
                            '-e', 'HOME=/home/agent', '-e', 'CODEX_HOME=/home/agent/.codex', '-e', 'GIT_OPTIONAL_LOCKS=0',
                            '-e', 'GIT_CONFIG_COUNT=1', '-e', 'GIT_CONFIG_KEY_0=safe.directory',
                            '-e', 'GIT_CONFIG_VALUE_0=/workspace', '--env-file', str(environment),
                            '-v', str(workspace) + ':/workspace' + (':ro' if review else ''),
                            '-v', str(self.root / '.git') + ':/workspace/.git:ro',
                            '-v', str(control) + ':/control:ro', '-v', str(output) + ':/output', '-w', '/workspace',
                            IMAGE, 'sh', '-c', 'mkdir -p /home/agent/.codex && exec "$@"', 'sh',
                            'codex', 'exec', '--ignore-user-config', '--ignore-rules', '--ephemeral',
                            '--dangerously-bypass-approvals-and-sandbox', '--json',
                            '--model', self.config['model'], '-c', 'model_provider="resolver_broker"',
                            '-c', 'model_reasoning_effort=' + json.dumps(self.config.get('effort', 'medium')),
                            '-c', 'model_providers.resolver_broker.name="Resolver inference broker"',
                            '-c', 'model_providers.resolver_broker.base_url=' + json.dumps(f'http://{address}:{port}/v1'),
                            '-c', 'model_providers.resolver_broker.env_key="CODEX_API_KEY"',
                            '-c', 'model_providers.resolver_broker.wire_api="responses"',
                            '-c', 'shell_environment_policy.inherit="none"',
                            '--output-schema', '/control/schema.json', '-o', '/output/result.json', '-']
                    try:
                        with log.open('w') as transcript:
                            result = subprocess.run(args, input=prompt, text=True, stdout=transcript,
                                                    stderr=subprocess.STDOUT, timeout=self.timeout)
                    finally:
                        subprocess.run(['docker', 'rm', '-f', container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    if result.returncode:
                        raise RuntimeError(f'Agent exited {result.returncode}; see {log.name}')
                result = json.loads((output / 'result.json').read_text())
                if review:
                    if not isinstance(result.get('approved'), bool) or not isinstance(result.get('findings'), list):
                        raise RuntimeError('Invalid agent review result')
                elif result.get('status') != 'resolved':
                    raise RuntimeError('Agent could not resolve: ' + str(result.get('summary', 'No summary')))
                else:
                    result['changed_files'] = apply_edits(self.root, workspace, before)
                report = log.with_suffix('.json')
                report.write_text(json.dumps(result, indent=2) + '\n')
                return result
        finally:
            subprocess.run(['docker', 'rm', '-f', container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def cleanup(owner):
    """Remove only this updater's agents after an interrupted run."""
    label = 'cpamp.resolver=' + owner
    for kind in ('container', 'network'):
        options = ['-a', '-q'] if kind == 'container' else ['-q']
        ids = command('docker', kind, 'ls', *options, '--filter', 'label=' + label, capture=True).splitlines()
        for identifier in ids:
            args = ['docker', kind, 'rm'] + (['-f'] if kind == 'container' else []) + [identifier]
            subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
