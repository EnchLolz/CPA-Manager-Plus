import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { sharedKeysApi, type SharedKey } from '@/services/api/sharedKeys';
import { useAuthStore, useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import styles from './SharedKeysCard.module.scss';

export function SharedKeysCard({
  serviceBase,
  disabled,
}: {
  serviceBase: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const managementKey = useAuthStore((state) => state.managementKey);
  const notify = useNotificationStore((state) => state.showNotification);
  const confirm = useNotificationStore((state) => state.showConfirmation);
  const api = useMemo(
    () => sharedKeysApi(serviceBase, managementKey),
    [serviceBase, managementKey]
  );
  const [keys, setKeys] = useState<SharedKey[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const mutation = useRef(false);
  const [open, setOpen] = useState(false);
  const [secret, setSecret] = useState('');
  const [publicBase, setPublicBase] = useState('');
  const [name, setName] = useState('');
  const [models, setModels] = useState('');
  const [days, setDays] = useState('7');
  const [tokens, setTokens] = useState('');
  const [dollars, setDollars] = useState('10');
  const [formError, setFormError] = useState('');
  const id = useId();
  const baseUrl = publicBase || `${serviceBase.replace(/\/$/, '')}/share/v1`;
  const message = (cause: unknown) =>
    cause instanceof Error ? cause.message : t('shared_keys.request_failed');

  const refresh = useCallback(async () => {
    try {
      const result = await api.list();
      setKeys(result.items);
      setPublicBase(result.gateway_base_url || '');
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('shared_keys.request_failed'));
    } finally {
      setLoading(false);
    }
  }, [api, t]);

  useEffect(() => {
    let active = true;
    const load = async () => {
      try {
        const result = await api.list();
        if (active) {
          setKeys(result.items);
          setPublicBase(result.gateway_base_url || '');
          setError('');
        }
      } catch (cause) {
        if (active)
          setError(cause instanceof Error ? cause.message : t('shared_keys.request_failed'));
      } finally {
        if (active) setLoading(false);
      }
    };
    void load();
    const interval = window.setInterval(() => {
      if (!document.hidden) void load();
    }, 15000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [api, t]);

  const create = async () => {
    if (mutation.current) return;
    const lifetime = Number(days);
    const tokenCap = tokens.trim() ? Number(tokens) : 0;
    const cost = dollars.trim() ? Number(dollars) : 0;
    if (
      !name.trim() ||
      !models.trim() ||
      !Number.isFinite(lifetime) ||
      lifetime <= 0 ||
      lifetime > 365 ||
      !Number.isSafeInteger(tokenCap) ||
      tokenCap < 0 ||
      tokenCap > 1e12 ||
      !Number.isFinite(cost) ||
      cost < 0 ||
      cost > 1e6 ||
      (tokenCap === 0 && cost === 0)
    ) {
      setFormError(t('shared_keys.validation'));
      return;
    }
    mutation.current = true;
    setBusy(true);
    setFormError('');
    try {
      const result = await api.create({
        name: name.trim(),
        models: models
          .split(/[\n,]/)
          .map((m) => m.trim())
          .filter(Boolean),
        expires_at: Date.now() + Math.round(lifetime * 86400000),
        max_tokens: tokenCap,
        max_cost_micros: Math.round(cost * 1e6),
      });
      setKeys((previous) => [result.item, ...previous]);
      setSecret(result.key);
      setPublicBase(result.gateway_base_url || '');
    } catch (cause) {
      setFormError(message(cause));
    } finally {
      mutation.current = false;
      setBusy(false);
    }
  };

  const revoke = (key: SharedKey) =>
    confirm({
      title: t('shared_keys.revoke'),
      message: t('shared_keys.revoke_confirm', { name: key.name }),
      confirmText: t('shared_keys.revoke'),
      variant: 'danger',
      onConfirm: async () => {
        if (mutation.current) return;
        mutation.current = true;
        setBusy(true);
        try {
          await api.revoke(key.id);
          await refresh();
        } catch (cause) {
          notify(message(cause), 'error');
        } finally {
          mutation.current = false;
          setBusy(false);
        }
      },
    });

  const status = (key: SharedKey) =>
    key.revoked
      ? 'revoked'
      : key.expires_at <= Date.now()
        ? 'expired'
        : key.blocked
          ? 'paused'
          : key.in_flight
            ? 'running'
            : (key.max_tokens > 0 && key.used_tokens >= key.max_tokens) ||
                (key.max_cost_micros > 0 && key.used_cost_micros >= key.max_cost_micros)
              ? 'exhausted'
              : 'active';

  const close = () => {
    if (!busy) {
      setOpen(false);
      setSecret('');
      setFormError('');
    }
  };
  return (
    <section className={styles.section} aria-label={t('shared_keys.title')}>
      <div className={styles.header}>
        <div>
          <h3>{t('shared_keys.title')}</h3>
          <p className="hint">{t('shared_keys.description')}</p>
        </div>
        <Button
          size="sm"
          disabled={disabled || busy || loading || Boolean(error)}
          onClick={() => {
            setName('');
            setModels('');
            setDays('7');
            setTokens('');
            setDollars('10');
            setFormError('');
            setSecret('');
            setOpen(true);
          }}
        >
          {t('shared_keys.create')}
        </Button>
      </div>
      <p className="hint">{t('shared_keys.accounting_note')}</p>
      {error && (
        <div className="error-box" role="alert">
          {error}{' '}
          <Button size="xs" onClick={() => void refresh()}>
            {t('common.refresh')}
          </Button>
        </div>
      )}
      {loading ? (
        <p className="hint">{t('common.loading')}</p>
      ) : !keys.length && !error ? (
        <p className="hint">{t('shared_keys.empty')}</p>
      ) : null}
      <div className={styles.list}>
        {keys.map((key) => (
          <div className={styles.row} key={key.id}>
            <div className={styles.details}>
              <div className={styles.title}>
                <strong>{key.name}</strong>
                <span className={styles.status}>{t(`shared_keys.status_${status(key)}`)}</span>
              </div>
              <div className="hint">{key.models.join(', ')}</div>
              <div className={styles.usage}>
                {key.max_tokens > 0 && (
                  <span>
                    {t('shared_keys.token_usage', {
                      used: key.used_tokens.toLocaleString(),
                      cap: key.max_tokens.toLocaleString(),
                    })}
                  </span>
                )}
                {key.max_cost_micros > 0 && (
                  <span>
                    {t('shared_keys.cost_usage', {
                      used: (key.used_cost_micros / 1e6).toFixed(6),
                      cap: (key.max_cost_micros / 1e6).toFixed(2),
                    })}
                  </span>
                )}
                <span>
                  {t('shared_keys.expires', { date: new Date(key.expires_at).toLocaleString() })}
                </span>
              </div>
              {key.blocked && <p className="hint">{t('shared_keys.paused_note')}</p>}
            </div>
            {!key.revoked && (
              <Button
                variant="danger"
                size="xs"
                disabled={disabled || busy}
                onClick={() => revoke(key)}
              >
                {t('shared_keys.revoke')}
              </Button>
            )}
          </div>
        ))}
      </div>
      <Modal
        open={open}
        onClose={close}
        closeDisabled={busy}
        title={t(secret ? 'shared_keys.created' : 'shared_keys.create')}
        footer={
          secret ? (
            <Button onClick={close}>{t('common.close')}</Button>
          ) : (
            <>
              <Button variant="secondary" onClick={close} disabled={busy}>
                {t('common.cancel')}
              </Button>
              <Button onClick={() => void create()} loading={busy} disabled={busy}>
                {t('shared_keys.create')}
              </Button>
            </>
          )
        }
      >
        {secret ? (
          <div className={styles.result}>
            <p>{t('shared_keys.copy_once')}</p>
            <label htmlFor={`${id}-base`}>{t('shared_keys.base_url')}</label>
            <input id={`${id}-base`} className="input" value={baseUrl} readOnly />
            <label htmlFor={`${id}-secret`}>{t('shared_keys.api_key')}</label>
            <input
              id={`${id}-secret`}
              className="input"
              value={secret}
              readOnly
              autoComplete="off"
            />
            <Button
              onClick={async () => {
                const copied = await copyToClipboard(`Base URL: ${baseUrl}\nAPI key: ${secret}`);
                notify(
                  t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
                  copied ? 'success' : 'error'
                );
              }}
            >
              {t('shared_keys.copy_details')}
            </Button>
            <p className="hint">{t('shared_keys.endpoint_note')}</p>
          </div>
        ) : (
          <div className={styles.form}>
            <label htmlFor={`${id}-name`}>{t('shared_keys.name')}</label>
            <input
              className="input"
              id={`${id}-name`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={120}
              disabled={busy}
              autoFocus
            />
            <label htmlFor={`${id}-models`}>{t('shared_keys.models')}</label>
            <textarea
              className="input"
              id={`${id}-models`}
              value={models}
              onChange={(e) => setModels(e.target.value)}
              disabled={busy}
              rows={2}
              aria-describedby={`${id}-models-help`}
            />
            <p id={`${id}-models-help`} className="hint">
              {t('shared_keys.models_help')}
            </p>
            <label htmlFor={`${id}-days`}>{t('shared_keys.days')}</label>
            <input
              className="input"
              id={`${id}-days`}
              type="number"
              min="0.001"
              max="365"
              step="any"
              value={days}
              onChange={(e) => setDays(e.target.value)}
              disabled={busy}
            />
            <div className={styles.caps}>
              <div>
                <label htmlFor={`${id}-cost`}>{t('shared_keys.cost')}</label>
                <input
                  className="input"
                  id={`${id}-cost`}
                  type="number"
                  min="0"
                  max="1000000"
                  step="0.01"
                  value={dollars}
                  onChange={(e) => setDollars(e.target.value)}
                  disabled={busy}
                />
              </div>
              <div>
                <label htmlFor={`${id}-tokens`}>{t('shared_keys.tokens')}</label>
                <input
                  className="input"
                  id={`${id}-tokens`}
                  type="number"
                  min="0"
                  max="1000000000000"
                  step="1"
                  value={tokens}
                  onChange={(e) => setTokens(e.target.value)}
                  disabled={busy}
                />
              </div>
            </div>
            <p className="hint">{t('shared_keys.caps_help')}</p>
            {formError && (
              <div className="error-box" role="alert">
                {formError}
              </div>
            )}
          </div>
        )}
      </Modal>
    </section>
  );
}
