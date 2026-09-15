import * as React from 'react';

const PLUGIN_THEMES = {
  light: {
    background: '#f6f8fc',
    foreground: '#172033',
    card: '#ffffff',
    cardForeground: '#172033',
    muted: '#e7ecf4',
    mutedForeground: '#46536b',
    border: '#d5deeb',
    primary: '#1d4ed8',
    primaryForeground: '#ffffff',
  },
  dark: {
    background: '#0b1220',
    foreground: '#f8fafc',
    card: '#111a2b',
    cardForeground: '#f8fafc',
    muted: '#1f2b41',
    mutedForeground: '#c3cddb',
    border: '#2c3b52',
    primary: '#60a5fa',
    primaryForeground: '#0b1220',
  },
};

export function resolvePluginTheme(documentRef = globalThis.document) {
  if (!documentRef?.documentElement) return 'light';
  const root = documentRef.documentElement;
  if (root.classList.contains('dark') || root.dataset.theme === 'dark') return 'dark';
  if (root.classList.contains('light') || root.dataset.theme === 'light') return 'light';
  return globalThis.matchMedia?.('(prefers-color-scheme: dark)')?.matches ? 'dark' : 'light';
}

export function getPluginThemeStyle(themeName) {
  const theme = PLUGIN_THEMES[themeName] || PLUGIN_THEMES.light;
  return {
    '--ok-background': theme.background,
    '--ok-foreground': theme.foreground,
    '--ok-card': theme.card,
    '--ok-card-foreground': theme.cardForeground,
    '--ok-muted': theme.muted,
    '--ok-muted-foreground': theme.mutedForeground,
    '--ok-border': theme.border,
    '--ok-primary': theme.primary,
    '--ok-primary-foreground': theme.primaryForeground,
    background: theme.background,
    color: theme.foreground,
  };
}

export function usePluginTheme() {
  const [theme, setTheme] = React.useState(() => resolvePluginTheme());

  React.useEffect(() => {
    if (!globalThis.document?.documentElement || typeof MutationObserver === 'undefined') {
      return undefined;
    }

    const root = globalThis.document.documentElement;
    const media = globalThis.matchMedia?.('(prefers-color-scheme: dark)');
    const syncTheme = () => setTheme(resolvePluginTheme());
    const observer = new MutationObserver(syncTheme);
    observer.observe(root, {
      attributes: true,
      attributeFilter: ['class', 'data-theme'],
    });
    media?.addEventListener?.('change', syncTheme);

    return () => {
      observer.disconnect();
      media?.removeEventListener?.('change', syncTheme);
    };
  }, []);

  return theme;
}
