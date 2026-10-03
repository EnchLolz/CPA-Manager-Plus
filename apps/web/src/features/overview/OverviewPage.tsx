/**
 * Overview (personal fork): one screen for "how much is left on each
 * subscription and which one is routing first". Everything else in the panel
 * stays upstream.
 */
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconEye, IconEyeOff, IconRefreshCw } from '@/components/ui/icons';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { getProviderLabel } from '@/features/accounts/model/accountsPagePresentation';
import { canRefreshProvider, useOverviewData } from './hooks/useOverviewData';
import { ProviderSummaryCard } from './components/ProviderSummaryCard';
import { CredentialRow } from './components/CredentialRow';
import { RoutingCard } from './components/RoutingCard';
import styles from './OverviewPage.module.scss';

const SHOW_EMAILS_KEY = 'overview.showEmails';

export function OverviewPage() {
  const { t, i18n } = useTranslation();
  const data = useOverviewData();
  const [filter, setFilter] = useState<string>('all');
  const [showEmails, setShowEmails] = useState(() => localStorage.getItem(SHOW_EMAILS_KEY) === '1');

  useEffect(() => {
    localStorage.setItem(SHOW_EMAILS_KEY, showEmails ? '1' : '0');
  }, [showEmails]);

  const locale = i18n.language || 'en-US';
  const tabs = useMemo(
    () => [
      { id: 'all', label: `All ${data.credentials.length}` },
      ...data.providers.map((p) => ({
        id: p.provider,
        label: `${getProviderLabel(p.provider, t)} ${p.credentials.length}`,
      })),
    ],
    [data.credentials.length, data.providers, t]
  );
  const visibleProviders = filter === 'all' ? data.providers : data.providers.filter((p) => p.provider === filter);
  const withData = data.credentials.filter((c) => c.windows.length > 0).length;
  const routingRankByName = useMemo(() => {
    const map = new Map<string, number>();
    if (data.routing?.enabled) {
      data.routing.order.forEach((account, index) => map.set(account.name, index + 1));
    }
    return map;
  }, [data.routing]);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Overview</h1>
          <div className={styles.subtitle}>
            <span className={styles.subtitleBar} />
            {data.credentials.length} credentials · {withData} with quota
            {data.lastLoadedAtMs && (
              <>
                {' '}· updated{' '}
                {new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' }).format(data.lastLoadedAtMs)}
              </>
            )}
          </div>
        </div>
        <div className={styles.headerActions}>
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

      {data.providers.length > 0 && (
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
        {data.providers.map((provider) => (
          <ProviderSummaryCard
            key={provider.provider}
            provider={provider}
            label={getProviderLabel(provider.provider, t)}
            nowMs={data.nowMs}
            locale={locale}
            active={filter === provider.provider}
            onSelect={() => setFilter((current) => (current === provider.provider ? 'all' : provider.provider))}
          />
        ))}
        {!data.loading && data.providers.length === 0 && (
          <div className={styles.empty}>No credentials found. Add OAuth logins first.</div>
        )}
      </section>

      {data.routing && (filter === 'all' || filter === 'claude') && (
        <RoutingCard status={data.routing} nowMs={data.nowMs} locale={locale} showEmails={showEmails} />
      )}

      {visibleProviders.map((provider) => (
        <section key={provider.provider} className={styles.providerSection}>
          <h2 className={styles.sectionTitle}>
            {getProviderLabel(provider.provider, t)}
            <span className={styles.sectionCount}>{provider.credentials.length}</span>
          </h2>
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
                routingRank={routingRankByName.get(credential.fileName) ?? null}
                onRefresh={() => void data.refreshCredential(credential)}
              />
            ))}
          </div>
        </section>
      ))}

      {data.loading && data.credentials.length === 0 && <div className={styles.empty}>Loading…</div>}
    </div>
  );
}
