import type { MetadataRoute } from 'next';
import { SITE } from '@/lib/site';

const EN_PATHS = [
  '/',
  '/platform',
  '/workers',
  '/use-cases',
  '/demo',
  '/integrations',
  '/security',
  '/open-source',
  '/faq',
  '/roadmap',
  '/changelog',
  '/brand',
  '/trademark',
  '/docs',
  '/docs/getting-started',
  '/docs/architecture',
  '/docs/deployment',
  '/docs/operations',
  '/docs/security-model',
  '/docs/plugins',
  '/docs/integrations',
  '/docs/api',
  '/docs/workflow-catalog',
  '/docs/harness-guide',
  '/docs/migration',
];

export default function sitemap(): MetadataRoute.Sitemap {
  const now = new Date();
  const base = SITE.url;

  const enEntries: MetadataRoute.Sitemap = EN_PATHS.map((p) => ({
    url: `${base}${p}`,
    lastModified: now,
    changeFrequency: 'weekly',
    priority: p === '/' ? 1 : 0.7,
    alternates: {
      languages: {
        en: `${base}${p}`,
        'zh-CN': `${base}/zh${p === '/' ? '' : p}`,
      },
    },
  }));

  const zhEntries: MetadataRoute.Sitemap = EN_PATHS.map((p) => ({
    url: `${base}/zh${p === '/' ? '' : p}`,
    lastModified: now,
    changeFrequency: 'weekly',
    priority: p === '/' ? 0.9 : 0.6,
    alternates: {
      languages: {
        en: `${base}${p}`,
        'zh-CN': `${base}/zh${p === '/' ? '' : p}`,
      },
    },
  }));

  return [...enEntries, ...zhEntries];
}
