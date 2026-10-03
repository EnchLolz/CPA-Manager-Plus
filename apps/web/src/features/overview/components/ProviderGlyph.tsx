import { useThemeStore } from '@/stores';
import { getAuthFileIcon, getTypeColor } from '@/features/authFiles/constants';
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

/** Provider logo in a tinted badge, falling back to the first letter. */
export function ProviderGlyph({ provider, size = 'md' }: ProviderGlyphProps) {
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const extra = EXTRA_ICONS[provider.toLowerCase()];
  const icon = getAuthFileIcon(provider, resolvedTheme) ?? (extra ? extra[resolvedTheme] : null);
  const colors = getTypeColor(provider, resolvedTheme);
  return (
    <span
      className={`${styles.glyph} ${size === 'sm' ? styles.glyphSm : ''}`}
      style={{ background: colors.bg, color: colors.text }}
      aria-hidden="true"
    >
      {icon ? <img src={icon} alt="" /> : provider.charAt(0).toUpperCase()}
    </span>
  );
}
