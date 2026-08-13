// @ts-check

/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
const sidebars = {
  docs: [
    'index',
    {
      type: 'category',
      label: 'Getting started',
      collapsed: false,
      items: [
        'getting-started/installation',
        'getting-started/first-machine',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'concepts/overview',
        'concepts/machines-and-configurations',
        'concepts/workloads',
        'concepts/revisions',
        'concepts/store-and-builder',
        'concepts/clusters',
      ],
    },
    {
      type: 'category',
      label: 'How-to guides',
      items: [
        'how-to/shared-store-and-builder',
        'how-to/accelerate-converge',
        'how-to/cluster-membership',
        'how-to/secrets-sops-age',
        'how-to/troubleshoot-converge',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: [
        'reference/index',
        'reference/machine',
        'reference/nixosconfiguration',
        'reference/nixcluster',
        'reference/nixstore',
        'reference/nixbuilder',
        'reference/nixspec',
        'reference/nixjob',
        'reference/nixcronjob',
        'reference/nixdeployment',
        'reference/nixstatefulset',
        'reference/cli',
      ],
    },
    {
      type: 'category',
      label: 'Design',
      items: [
        'design/index',
        'design/decisions',
        'design/nix-workloads',
        'design/nixosconfiguration-orchestrator',
        'design/cluster-converge',
        'design/store-builder-ssh',
      ],
    },
  ],
};

export default sidebars;
