export const COMPACT_BINARY_COMMAND =
  'cpa-manager-plus compact-usage --db-path /path/to/usage.sqlite';

export const COMPACT_USAGE_COMMAND = COMPACT_BINARY_COMMAND;

export const COMPACT_DOCKER_COMPOSE_COMMAND =
  'docker compose stop cpa-manager-plus && \\\n  docker compose run --rm --no-deps cpa-manager-plus compact-usage --db-path /data/usage.sqlite && \\\n  docker compose start cpa-manager-plus';

export const COMPACT_DOCKER_RUN_COMMAND =
  'docker stop cpa-manager-plus && \\\n  docker run --rm -v cpa-manager-plus-data:/data seakee/cpa-manager-plus:latest compact-usage --db-path /data/usage.sqlite && \\\n  docker start cpa-manager-plus';

export const BACKUP_FILES = [
  'usage.sqlite',
  'usage.sqlite-wal',
  'usage.sqlite-shm',
  'data.key',
  'usage-archives/',
] as const;

export type CompactDeploymentType = 'docker-compose' | 'docker-run' | 'binary';
