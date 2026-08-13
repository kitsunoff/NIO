# Helm repository seam

Everything in `docs-site/static/` is copied verbatim into the published site, so a
file at `docs-site/static/charts/index.yaml` is served at
`https://kitsunoff.github.io/NIO/charts/index.yaml` and

```sh
helm repo add nio https://kitsunoff.github.io/NIO/charts
```

resolves against this same GitHub Pages deployment.

This directory is the seam, deliberately left empty of chart artifacts. The Helm
chart workstream is expected to drop its generated `index.yaml` and packaged
`.tgz` files **here**, so that the existing `.github/workflows/pages.yml` picks
them up and publishes them as part of the one and only Pages deployment.

A second workflow calling `actions/deploy-pages` would clobber this one — GitHub
Pages has a single deployment per repository. There must be exactly one Pages
workflow, and it is `.github/workflows/pages.yml`.
