# GitHub Actions workflows

These files belong in `.github/workflows/`. They are kept here because the token used to open the
first pull request may not change workflows (GitHub requires the `workflow` scope for that).

Move them after review:

```shell
mkdir -p .github && git mv ci/github-workflows .github/workflows && git rm .github/workflows/README.md
```

- `test.yml`: build, lint, docs check, tests with Terraform and OpenTofu against the mock API.
- `release.yml`: runs on a `v*` tag, builds with GoReleaser and signs the checksums with GPG
  (repository secrets `GPG_PRIVATE_KEY` and `PASSPHRASE`). Needed for the Terraform and OpenTofu registries.
