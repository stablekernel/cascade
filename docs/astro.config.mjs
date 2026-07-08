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
      // Mermaid follows the Starlight light/dark toggle. We keep autoTheme so
      // node fills and text stay legible in both modes, and brand only the
      // accent surfaces (edges, borders, cluster outlines, active accent) with
      // cascade teal and copper, colors that read on both the dark slate and
      // the light gray Starlight backgrounds.
      autoTheme: true,
      mermaidConfig: {
        themeVariables: {
          fontFamily: 'ui-sans-serif, system-ui, sans-serif',
          // Cascade cyan-teal drives edges, focus accents, and active states.
          primaryColor: '#0E8B82',
          primaryBorderColor: '#36D0C4',
          primaryTextColor: '#F8FAFC',
          lineColor: '#36D0C4',
          // The flowchart renderer keys node fill, border, and text off mainBkg,
          // nodeBorder, and nodeTextColor rather than the primary* variables, so
          // brand all three explicitly. A mid-tone teal fill with light text reads
          // on both the dark slate and the light gray Starlight backgrounds and
          // keeps the diagrams on the same palette as the README badges.
          mainBkg: '#0E8B82',
          nodeBorder: '#36D0C4',
          nodeTextColor: '#F8FAFC',
          // Subgraph (cluster) outline in teal with a transparent fill so the
          // Starlight surface shows through in either mode; title in teal.
          clusterBorder: '#36D0C4',
          clusterBkg: 'transparent',
          titleColor: '#36D0C4',
          // Keep edge labels legible over either background.
          edgeLabelBackground: '#0B1220',
          // Copper and ember secondary accent for highlighted nodes.
          secondaryColor: '#B87333',
          secondaryBorderColor: '#E8702A',
          tertiaryColor: '#B87333',
          tertiaryBorderColor: '#E8702A',
        },
      },
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
      // Sidebar mirrors the journey: orient (Why Cascade), start (mental model +
      // tutorial), task guides (operator how-tos), reference (exhaustive lookup),
      // security, then internals (contributor depth). Every label pairs with its
      // page's `title` frontmatter.
      sidebar: [
        { label: 'Why Cascade', link: '/start/why-cascade/' },
        { label: 'Start here', items: [
            { label: 'How Cascade works', link: '/start/how-it-works/' },
            { label: 'Getting started',   link: '/start/getting-started/' },
        ]},
        { label: 'Task guides', items: [
            { label: 'Adopt an existing pipeline', link: '/guides/adopt/' },
            { label: 'Add or change environments', link: '/guides/environments/' },
            { label: 'Promote a release',          link: '/guides/promote/' },
            { label: 'Run a hotfix',               link: '/guides/hotfix/' },
            { label: 'Roll back an environment',   link: '/guides/rollback/' },
            { label: 'Simulate and verify',        link: '/guides/simulate-and-verify/' },
            { label: 'Coordinate multiple repos',  link: '/guides/multi-repo/' },
            { label: 'Split a repo into components', link: '/guides/components/' },
            { label: 'Visualize the pipeline',     link: '/guides/visualize/' },
        ]},
        { label: 'Reference', items: [
            { label: 'Manifest',            link: '/reference/manifest/' },
            { label: 'Callback contract',   link: '/reference/callbacks/' },
            { label: 'CLI',                 link: '/reference/cli/' },
            { label: 'Generated workflows', link: '/reference/generated-workflows/' },
            { label: 'Versioning & schema', link: '/reference/versioning/' },
        ]},
        { label: 'Security & hardening', link: '/security/' },
        { label: 'Internals', items: [
            { label: 'Architecture',            link: '/internals/architecture/' },
            { label: 'How Cascade is tested',   link: '/internals/testing/' },
            { label: 'Feature coverage matrix', link: '/internals/coverage-matrix/' },
            { label: 'Release orchestration',   link: '/internals/release-orchestration/' },
        ]},
      ],
    }),
  ],
});
