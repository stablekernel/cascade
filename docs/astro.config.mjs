// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';

// GitHub project page: https://stablekernel.github.io/cascade
// `site` + `base` must match the Pages URL so generated links and assets resolve.
export default defineConfig({
  site: 'https://stablekernel.github.io',
  base: '/cascade',
  integrations: [
    // astro-mermaid renders ```mermaid fenced code blocks client-side at runtime.
    // It needs no headless browser at build time, keeping `npm run build` fast and
    // dependency-light. `theme: 'dark'` matches the dark-default Cascade brand and
    // mermaid auto-syncs when the reader toggles the theme.
    mermaid({
      theme: 'dark',
      autoTheme: true,
    }),
    starlight({
      title: 'Cascade',
      tagline: 'Declarative trunk-based CI/CD for GitHub Actions.',
      description:
        'Cascade is a Go CLI that orchestrates multi-environment release and promotion pipelines from a single manifest by generating GitHub Actions workflows.',
      logo: {
        src: './src/assets/logo.png',
        alt: 'Cascade logo',
        replacesTitle: false,
      },
      favicon: '/favicon.png',
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/stablekernel/cascade',
        },
      ],
      customCss: ['./src/styles/cascade.css'],
      head: [
        // Social card / og:image (brand art).
        {
          tag: 'meta',
          attrs: { property: 'og:image', content: '/cascade/social-card.png' },
        },
        {
          tag: 'meta',
          attrs: { name: 'twitter:card', content: 'summary_large_image' },
        },
      ],
      // Sidebar order mirrors the previous MkDocs nav exactly.
      sidebar: [
        { label: 'Getting Started', link: '/getting-started/' },
        { label: 'Manifest Reference', link: '/configuration/' },
        { label: 'Callback Contract', link: '/callback-contract/' },
        {
          label: 'Workflows',
          items: [{ label: 'Overview', link: '/workflows/' }],
        },
        { label: 'CLI Reference', link: '/cli-reference/' },
        { label: 'Architecture', link: '/architecture/' },
        { label: 'Versioning & Schema', link: '/versioning/' },
      ],
    }),
  ],
});
