import { useThemeStore } from '@/stores';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import iconOpenAILight from '@/assets/icons/openai-light.svg';
import iconOpenAIDark from '@/assets/icons/openai-dark.svg';

// Providers the shared icon map leaves out but that have assets.
const EXTRA_ICONS: Record<string, { light: string; dark: string }> = {
  openai: { light: iconOpenAILight, dark: iconOpenAIDark },
};
import styles from '../OverviewPage.module.scss';

interface ProviderGlyphProps {
  provider: string;
  size?: 'sm' | 'md';
}

/** Provider logo, falling back to the first letter. */
export function ProviderGlyph({ provider, size = 'md' }: ProviderGlyphProps) {
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const extra = EXTRA_ICONS[provider.toLowerCase()];
  const icon = getAuthFileIcon(provider, resolvedTheme) ?? (extra ? extra[resolvedTheme] : null);
  return (
    <span className={`${styles.glyph} ${size === 'sm' ? styles.glyphSm : ''}`} aria-hidden="true">
      {icon ? <img src={icon} alt="" /> : provider.charAt(0).toUpperCase()}
    </span>
  );
}
