// @ts-check
// Docusaurus configuration for the NIO documentation site.
//
// Deployed to GitHub Pages at https://kitsunoff.github.io/NIO/ by
// .github/workflows/pages.yml. baseUrl must stay "/NIO/" — the Helm chart
// workstream serves its repository index from the same deployment at
// /NIO/charts/index.yaml (see static/charts/README.md).

import {themes as prismThemes} from 'prism-react-renderer';

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'NIO',
  tagline: 'A Kubernetes operator that converges real NixOS machines',
  favicon: 'img/favicon.svg',

  url: 'https://kitsunoff.github.io',
  baseUrl: '/NIO/',

  organizationName: 'kitsunoff',
  projectName: 'NIO',

  // Broken internal links fail the production build. This is deliberate and
  // must not be downgraded to 'warn': a hand-written reference rots silently
  // otherwise.
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  onDuplicateRoutes: 'throw',

  markdown: {
    // 'detect' parses .md as CommonMark and .mdx as MDX. The design corpus is
    // imported prose full of angle brackets and braces that MDX would try to
    // read as JSX, so every page here is .md.
    format: 'detect',
    hooks: {
      onBrokenMarkdownLinks: 'throw',
      onBrokenMarkdownImages: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          routeBasePath: '/',
          sidebarPath: './sidebars.js',
          editUrl: 'https://github.com/kitsunoff/NIO/tree/main/docs-site/',
          showLastUpdateTime: false,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      }),
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      colorMode: {
        respectPrefersColorScheme: true,
      },
      navbar: {
        title: 'NIO',
        items: [
          {
            type: 'docSidebar',
            sidebarId: 'docs',
            position: 'left',
            label: 'Documentation',
          },
          {
            href: 'https://github.com/kitsunoff/NIO/releases',
            label: 'Releases',
            position: 'right',
          },
          {
            href: 'https://github.com/kitsunoff/NIO',
            label: 'GitHub',
            position: 'right',
          },
        ],
      },
      footer: {
        style: 'dark',
        links: [
          {
            title: 'Docs',
            items: [
              {label: 'Getting started', to: '/getting-started/installation'},
              {label: 'Concepts', to: '/concepts/overview'},
              {label: 'Reference', to: '/reference/'},
            ],
          },
          {
            title: 'Project',
            items: [
              {label: 'GitHub', href: 'https://github.com/kitsunoff/NIO'},
              {label: 'Releases', href: 'https://github.com/kitsunoff/NIO/releases'},
              {
                label: 'Issues',
                href: 'https://github.com/kitsunoff/NIO/issues',
              },
            ],
          },
        ],
        copyright: 'NIO is licensed under the Apache License 2.0.',
      },
      prism: {
        theme: prismThemes.github,
        darkTheme: prismThemes.dracula,
        additionalLanguages: ['bash', 'yaml', 'json', 'nix', 'go', 'diff'],
      },
    }),
};

export default config;
