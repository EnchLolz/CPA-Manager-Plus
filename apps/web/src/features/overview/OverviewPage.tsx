/**
 * Overview (personal fork): one screen for "how much is left on each
 * subscription and which one is routing first". Everything else in the panel
 * stays upstream.
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconEye, IconEyeOff, IconRefreshCw, IconSidebarAuthFiles } from '@/components/ui/icons';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { getProviderLabel } from '@/features/accounts/model/accountsPagePresentation';
import { canRefreshProvider, useOverviewData } from './hooks/useOverviewData';
import { buildOverviewProviders } from './model/overviewModel';
import { ProviderSummaryCard } from './components/ProviderSummaryCard';
import { CredentialRow } from './components/CredentialRow';
import { RoutingDisclosure } from './components/RoutingDisclosure';
import { ProviderGlyph } from './components/ProviderGlyph';
import styles from './OverviewPage.module.scss';

const SHOW_EMAILS_KEY = 'overview.showEmails';
const SHOW_DISABLED_KEY = 'overview.showDisabled';
const PROVIDER_LABEL_OVERRIDES: Record<string, string> = { openai: 'OpenAI', xai: 'xAI' };

export function OverviewPage() {
  const { t, i18n } = useTranslation();
  const data = useOverviewData();
  const [filter, setFilter] = useState<string>('all');
  const [showEmails, setShowEmails] = useState(() => localStorage.getItem(SHOW_EMAILS_KEY) === '1');
  const [showDisabled, setShowDisabled] = useState(() => localStorage.getItem(SHOW_DISABLED_KEY) === '1');

  useEffect(() => {
    localStorage.setItem(SHOW_EMAILS_KEY, showEmails ? '1' : '0');
  }, [showEmails]);
  useEffect(() => {
    localStorage.setItem(SHOW_DISABLED_KEY, showDisabled ? '1' : '0');
  }, [showDisabled]);

  // Disabled credentials are hidden by default; they are not routed and only
  // add noise. The toggle brings them back for a quick check.
  const disabledCount = data.credentials.filter((c) => c.disabled).length;
  const credentials = useMemo(
    () => (showDisabled ? data.credentials : data.credentials.filter((c) => !c.disabled)),
    [data.credentials, showDisabled]
  );
  const providers = useMemo(() => buildOverviewProviders(credentials), [credentials]);

  const locale = i18n.language || 'en-US';
  const providerLabel = useCallback(
    (provider: string) => PROVIDER_LABEL_OVERRIDES[provider] ?? getProviderLabel(provider, t),
    [t]
  );
  const tabs = useMemo(
    () => [
      {
        id: 'all',
        label: (
          <span className={styles.tabLabel}>
            All<span className={styles.tabCount}>{credentials.length}</span>
          </span>
        ),
      },
      ...providers.map((p) => ({
        id: p.provider,
        label: (
          <span className={styles.tabLabel}>
            <ProviderGlyph provider={p.provider} size="sm" />
            {providerLabel(p.provider)}
            <span className={styles.tabCount}>{p.credentials.length}</span>
          </span>
        ),
      })),
    ],
    [credentials.length, providers, providerLabel]
  );
  const visibleProviders = filter === 'all' ? providers : providers.filter((p) => p.provider === filter);
  const withData = credentials.filter((c) => c.windows.length > 0).length;
  const routingRank = useMemo(() => {
    const byProvider = new Map<string, Map<string, number>>();
    for (const [provider, status] of Object.entries(data.routing)) {
      if (!status.enabled) continue;
      byProvider.set(provider, new Map(status.order.map((account, index) => [account.name, index + 1])));
    }
    return byProvider;
  }, [data.routing]);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Overview</h1>
          <div className={styles.subtitle}>
            <span className={styles.subtitleBar} />
            {credentials.length} credentials · {withData} with quota
            {!showDisabled && disabledCount > 0 && <> · {disabledCount} disabled hidden</>}
            {data.lastLoadedAtMs && (
              <>
                {' '}· updated{' '}
                {new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' }).format(data.lastLoadedAtMs)}
              </>
            )}
          </div>
        </div>
        <div className={styles.headerActions}>
          {disabledCount > 0 && (
            <button type="button" className={styles.ghostButton} onClick={() => setShowDisabled((v) => !v)}>
              <IconSidebarAuthFiles size={14} />
              <span>{showDisabled ? 'Hide disabled' : `Show disabled (${disabledCount})`}</span>
            </button>
          )}
          <button type="button" className={styles.ghostButton} onClick={() => setShowEmails((v) => !v)}>
            {showEmails ? <IconEyeOff size={14} /> : <IconEye size={14} />}
            <span>{showEmails ? 'Hide emails' : 'Show emails'}</span>
          </button>
          <button
            type="button"
            className={styles.primaryButton}
            onClick={() => void data.refreshAll()}
            disabled={data.refreshingAll}
          >
            <IconRefreshCw size={14} className={data.refreshingAll ? styles.spin : undefined} />
            <span>{data.refreshingAll ? 'Refreshing…' : 'Refresh all'}</span>
          </button>
        </div>
      </header>

      {data.error && <div className={styles.notice}>{data.error}</div>}

      {providers.length > 0 && (
        <SegmentedTabs
          items={tabs}
          activeTab={filter}
          onChange={setFilter}
          ariaLabel="Provider filter"
          idBase="overview-provider"
          className={styles.tabs}
        />
      )}

      <section className={styles.summaryGrid} aria-label="Provider summary">
        {providers.map((provider) => (
          <ProviderSummaryCard
            key={provider.provider}
            provider={provider}
            label={providerLabel(provider.provider)}
            nowMs={data.nowMs}
            locale={locale}
            active={filter === provider.provider}
            onSelect={() => setFilter((current) => (current === provider.provider ? 'all' : provider.provider))}
          />
        ))}
        {!data.loading && providers.length === 0 && (
          <div className={styles.empty}>No credentials found. Add OAuth logins first.</div>
        )}
      </section>

      {visibleProviders.map((provider) => (
        <section key={provider.provider} className={styles.providerSection}>
          <div className={styles.sectionHead}>
            <h2 className={styles.sectionTitle}>
              {providerLabel(provider.provider)}
              <span className={styles.sectionCount}>{provider.credentials.length}</span>
            </h2>
            {data.routing[provider.provider] && (
              <RoutingDisclosure
                status={data.routing[provider.provider]}
                nowMs={data.nowMs}
                locale={locale}
                showEmails={showEmails}
              />
            )}
          </div>
          <div className={styles.rows}>
            {provider.credentials.map((credential) => (
              <CredentialRow
                key={credential.key}
                credential={credential}
                nowMs={data.nowMs}
                locale={locale}
                showEmails={showEmails}
                refreshing={data.refreshing.has(credential.key)}
                canRefresh={!credential.disabled && canRefreshProvider(credential.provider)}
                routingRank={routingRank.get(provider.provider)?.get(credential.fileName) ?? null}
                onRefresh={() => void data.refreshCredential(credential)}
              />
            ))}
          </div>
        </section>
      ))}

      {data.loading && credentials.length === 0 && <div className={styles.empty}>Loading…</div>}
    </div>
  );
}
