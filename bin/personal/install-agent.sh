#!/usr/bin/env bash
# Install the updater and its inference broker configuration on Kiwi.
set -euo pipefail
source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
state="${CPAMP_UPDATE_STATE:-$HOME/.local/state/cpamp-auto-update}"
test -d "$state/repo/.git"
systemctl --user stop cpamp-auto-update.timer
if systemctl --user is-active --quiet cpamp-auto-update.service; then
  systemctl --user start cpamp-auto-update.timer
  echo 'Updater is running; install after it finishes.' >&2
  exit 1
fi
trap 'systemctl --user start cpamp-auto-update.timer' EXIT
umask 077
python3 - "$state" <<'PY'
import json, os, pathlib, sys, tomllib, yaml
state = pathlib.Path(sys.argv[1])
proxy = yaml.safe_load((pathlib.Path.home() / 'cliproxyapi/config.yaml').read_text())
codex_home = pathlib.Path(os.environ.get('CODEX_HOME', str(pathlib.Path.home() / '.codex')))
codex = tomllib.loads((codex_home / 'config.toml').read_text())
host = proxy.get('host') or '127.0.0.1'
if host == '0.0.0.0':
    host = '127.0.0.1'
config = {'base_url': f'http://{host}:{proxy.get("port", 8317)}/v1',
          'api_key': proxy['api-keys'][0], 'model': codex['model'],
          'effort': codex.get('model_reasoning_effort', 'medium'), 'timeout_seconds': 900}
temporary = state / 'agent-config.json.new'
temporary.write_text(json.dumps(config) + '\n')
temporary.chmod(0o600)
temporary.replace(state / 'agent-config.json')
print('Configured isolated resolver using', config['model'], config['effort'])
PY
for file in agent_resolver.py agent-instructions.md Dockerfile.agent auto-update.py; do
  install -m 600 "$source_dir/$file" "$state/$file.new"
  mv "$state/$file.new" "$state/$file"
done
install -m 600 "$source_dir/cpamp-auto-update.service" "$HOME/.config/systemd/user/cpamp-auto-update.service"
install -m 600 "$source_dir/cpamp-auto-update.timer" "$HOME/.config/systemd/user/cpamp-auto-update.timer"
systemctl --user daemon-reload
systemctl --user enable --now cpamp-auto-update.timer
echo 'Agent-assisted updates installed.'
