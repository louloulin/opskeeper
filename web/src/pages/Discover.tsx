// Discover — one shell over the three extension surfaces (D6).
// Tab state lives entirely in `?tab=` so every entry point (sidebar,
// redirects, bookmarks) is a plain link.
//
// Unknown values fall back to `skills` but are LEFT IN the URL: the legacy
// `/skills?tab=install` deep link therefore lands on the skills panel, where
// the embedded SkillsPage reads the same `?tab=` param (Skills.tsx:32) and
// shows its install subtab — but only for an admin; a non-admin sees the
// catalog regardless of the param.
//
// No PageHeader and no <main> at this level: each embedded page renders its
// own (Skills.tsx:47 / PluginMarketplace.tsx:50 / Crystallized.tsx). A header
// here would stack two title bars, and a <main> here would nest a <main>
// inside a <main>. The root is therefore a plain <div>, leaving the embedded
// page's <main> as the page's only one.
import { useSearchParams } from 'react-router-dom';
import { tr } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import SkillsPage from './Skills';
import PluginMarketplacePage from './PluginMarketplace';
import CrystallizedPage from './Crystallized';

export type DiscoverTab = 'skills' | 'plugins' | 'crystals';

const TABS: Array<{ id: DiscoverTab; zh: string; en: string }> = [
  { id: 'skills', zh: '技能', en: 'Skills' },
  { id: 'plugins', zh: '插件', en: 'Plugins' },
  { id: 'crystals', zh: '自愈结晶', en: 'Crystals' },
];

export function Discover() {
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get('tab');
  const tab: DiscoverTab = raw === 'plugins' || raw === 'crystals' ? raw : 'skills';

  const select = (next: DiscoverTab) => {
    setSearchParams(
      (prev) => {
        const p = new URLSearchParams(prev);
        p.set('tab', next);
        return p;
      },
      { replace: true },
    );
  };

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      <div role="tablist" className="flex gap-1 self-start rounded-rk-md bg-zinc-900/60 p-1">
        {TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            type="button"
            aria-selected={tab === t.id}
            onClick={() => select(t.id)}
            className={cn(
              'rounded-full px-3 py-1.5 text-xs transition-colors',
              tab === t.id ? 'bg-zinc-100 text-zinc-900' : 'text-zinc-400 hover:text-zinc-200',
            )}
          >
            {tr(t.zh, t.en)}
          </button>
        ))}
      </div>
      <div role="tabpanel" className="flex flex-1 flex-col overflow-hidden">
        {tab === 'skills' && <SkillsPage />}
        {tab === 'plugins' && <PluginMarketplacePage />}
        {tab === 'crystals' && <CrystallizedPage />}
      </div>
    </div>
  );
}
