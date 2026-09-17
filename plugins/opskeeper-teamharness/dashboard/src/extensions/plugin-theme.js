// OpsKeeper plugin surfaces run inside host-managed theme variables. The host's
// muted text token is decorative and has proven too low-contrast for plugin
// content. Scope this override at every extension entry point.
export const opskeeperPluginThemeStyle = {
  '--muted-foreground': 'var(--foreground)',
};

export const opskeeperDarkPanelStyle = {
  ...opskeeperPluginThemeStyle,
  '--muted-foreground': 'rgba(249, 250, 251, 0.86)',
  background: '#111827',
  color: '#f9fafb',
};
