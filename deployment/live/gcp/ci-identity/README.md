# CI identity stack (persistent — NEVER torn down)

This Terragrunt config provisions the **keyless** identity plumbing the GCP
conformance lane uses to reach GCP: a Workload Identity Federation (WIF) pool + a
GitHub OIDC provider locked to a single `owner/repo`, the `tessera-ci` service
account the workflow impersonates, and that account's project roles. It also
enables the required APIs.

It is applied **once** by a human acting as project Owner and then effectively
never changes. The WIF pool, provider, and service account carry
`lifecycle { prevent_destroy = true }` because **deleting a WIF pool reserves its
name for 30 days**, which would lock CI out for a month.

See [`deployment/README.md`](../../../README.md#enabling-the-gcp-conformance-lane)
for the full bootstrap order.

## Environment variables it reads

| Variable            | Required | Default       | Purpose                                    |
| ------------------- | -------- | ------------- | ------------------------------------------ |
| `GOOGLE_PROJECT`    | yes      | —             | Project ID (state bucket + provider)       |
| `GOOGLE_REGION`     | no       | `us-central1` | Region                                     |
| `GITHUB_REPOSITORY` | yes      | —             | `owner/repo` the WIF provider is locked to |

`GITHUB_REPOSITORY` is set automatically inside GitHub Actions. When applying
locally, you **must** export it to bind the provider to the right repo (e.g.
`transparency-dev/tessera` for upstream, or your fork). It is a WIF security
boundary, so the module has no default — an unset value fails the apply rather
than silently locking CI to some other repo.

> [!WARNING]
> The `tessera-ci` service account this stack creates holds
> `roles/cloudsql.admin` and `roles/storage.admin`. The GCS conformance lane can
> be run against a PR by adding the **`run-gcs-conformance`** label, which
> executes **that PR's** workflow code with this service account. Treat the label
> as granting the PR those permissions: review the PR's workflow/build changes
> before labeling, and only label PRs you trust.

## Apply (human, as Owner)

```bash
export GOOGLE_PROJECT=<your-gcp-project-id>
export GOOGLE_REGION=us-central1
export GITHUB_REPOSITORY=patflynn/trillian-tessera   # or transparency-dev/tessera upstream

terragrunt apply --terragrunt-working-dir deployment/live/gcp/ci-identity
```

Then read the outputs and set them as repo **variables** (not secrets):

```bash
terragrunt output --terragrunt-working-dir deployment/live/gcp/ci-identity

gh variable set GCP_WIF_PROVIDER --body "<wif_provider>"
gh variable set GCP_CI_SA_EMAIL  --body "<ci_sa_email>"
gh variable set GCP_PROJECT_ID   --body "<project_id>"
gh variable set GCP_REGION       --body "<region>"
gh variable set GCP_CONFORMANCE_ENABLED --body "true"
```

## Outputs

- `wif_provider` — full provider resource name for `google-github-actions/auth`.
- `ci_sa_email` — CI service account email (also the workflow's impersonation target).
- `project_id`, `project_number`, `region`.
