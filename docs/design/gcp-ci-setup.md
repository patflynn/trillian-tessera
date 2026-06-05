# Runbook: Set up GCP CI for the fork (keyless, gates merges to `main`)

**Audience:** an agent acting with **patflynn's personal Google Cloud auth** (`gcloud` logged in as
Owner of the personal project) **and** `gh` authenticated as `patflynn` with admin on the fork.
**Goal:** every PR on `patflynn/trillian-tessera` must pass tests — including a real **GCS + Cloud
SQL conformance run** — before it can merge to the fork's `main`. **Keyless throughout** (Workload
Identity Federation; no long-lived service-account keys).

This sets up CI *before* the storage code lands. See `docs/design/cloud-agnostic-storage.md` for the
design. The conformance lane references a not-yet-existing GCS+MySQL conformance binary; it is added
now as **non-required** and flipped to **required** once that binary exists (see §9 sequencing).

---

## 0. Preconditions the agent must verify first

```bash
gcloud auth list                       # active account = patflynn personal, Owner on the project
gcloud config get-value project        # or set it in §1
gh auth status                         # logged in as patflynn; scopes include 'workflow' and admin
gh repo view patflynn/trillian-tessera --json viewerPermission -q .viewerPermission   # ADMIN
```

Also confirm the project has **billing enabled** and is **not** constrained by an org policy that
blocks Workload Identity or service-account use (personal projects usually have none):

```bash
gcloud beta billing projects describe "$PROJECT_ID" --format='value(billingEnabled)'   # true
gcloud org-policies list --project="$PROJECT_ID" 2>/dev/null   # expect empty/none on a personal project
```

> ⚠️ If `gcloud`/`gh` are not authenticated, STOP and ask the human to run `gcloud auth login` /
> `gh auth login` via the `! <cmd>` prompt. The agent must not invent credentials.

> ⚠️ When committing the workflow YAML in §5–§6, **push over SSH** (`git push fork …`). Pushing
> `.github/workflows/*` over HTTPS fails without the `workflow` token scope; SSH is exempt.

---

## 1. Inputs — fill these in, then `export` them

```bash
export PROJECT_ID="REPLACE_with_patflynn_personal_project_id"
export REGION="us-central1"
export REPO="patflynn/trillian-tessera"
export PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"

# Names this runbook creates (safe defaults):
export POOL="github-pool"
export PROVIDER="github-provider"
export CI_SA="tessera-ci"                       # service account id (not email)
export CI_SA_EMAIL="${CI_SA}@${PROJECT_ID}.iam.gserviceaccount.com"
export SQL_INSTANCE="tessera-ci-mysql"          # long-lived shared Cloud SQL instance
export SQL_TIER="db-g1-small"                    # smallest practical; bump if hammer saturates it

gcloud config set project "$PROJECT_ID"
```

---

## 2. Enable APIs

```bash
gcloud services enable \
  iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com \
  cloudresourcemanager.googleapis.com \
  storage.googleapis.com sqladmin.googleapis.com logging.googleapis.com \
  --project="$PROJECT_ID"
```
(Artifact Registry / Cloud Run are intentionally **not** enabled — CI runs the binaries on the
GitHub runner. Enable `artifactregistry.googleapis.com run.googleapis.com` later only if you move to
a container-based conformance run.)

---

## 3. Workload Identity Federation — keyless GitHub Actions → GCP

Create the pool + an OIDC provider locked to **this repo**, then a CI service account the workflow
impersonates.

```bash
# 3a. Pool + GitHub OIDC provider (repo-scoped attribute condition is the security boundary)
gcloud iam workload-identity-pools create "$POOL" \
  --location=global --display-name="GitHub Actions" --project="$PROJECT_ID"

gcloud iam workload-identity-pools providers create-oidc "$PROVIDER" \
  --location=global --workload-identity-pool="$POOL" --project="$PROJECT_ID" \
  --display-name="GitHub OIDC" \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner,attribute.ref=assertion.ref" \
  --attribute-condition="assertion.repository=='${REPO}'"

# 3b. CI service account
gcloud iam service-accounts create "$CI_SA" \
  --display-name="Tessera fork CI" --project="$PROJECT_ID"

# 3c. Let the repo's WIF identities impersonate the SA
WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL}/attribute.repository/${REPO}"
gcloud iam service-accounts add-iam-policy-binding "$CI_SA_EMAIL" \
  --role=roles/iam.workloadIdentityUser --member="$WIF_PRINCIPAL" --project="$PROJECT_ID"

# 3d. Roles the CI SA needs (scope tighter later if desired)
for role in \
  roles/storage.admin \
  roles/cloudsql.client \
  roles/cloudsql.instanceUser \
  roles/logging.logWriter ; do
  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${CI_SA_EMAIL}" --role="$role" --condition=None
done

# 3e. The provider resource name the workflow will reference
export WIF_PROVIDER="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL}/providers/${PROVIDER}"
echo "WIF_PROVIDER=$WIF_PROVIDER"
```

> **Security:** the `attribute-condition` ties the federation to `patflynn/trillian-tessera`. GitHub
> only issues OIDC tokens to workflows running **in this repo's context** — PRs from *external* forks
> do **not** get the token, so they can't touch your project. klaus agents push branches to this fork
> and open same-repo PRs, so OIDC works for them. Keep `pull_request_target` out of these workflows.

---

## 4. Long-lived Cloud SQL (MySQL) instance + keyless DB auth

Instance bring-up is slow (~5–10 min), so create **one** shared instance now; each CI run uses a
**fresh database** it drops at the end. Use **IAM database authentication** so the runner needs no DB
password (keyless).

```bash
gcloud sql instances create "$SQL_INSTANCE" \
  --project="$PROJECT_ID" --region="$REGION" \
  --database-version=MYSQL_8_0 --tier="$SQL_TIER" --edition=enterprise \
  --database-flags=cloudsql_iam_authentication=on \
  --storage-size=10 --storage-auto-increase

# IAM DB user for the CI service account (note: username is the SA email WITHOUT the .gserviceaccount.com suffix)
gcloud sql users create "${CI_SA}@${PROJECT_ID}.iam" \
  --instance="$SQL_INSTANCE" --type=cloud_iam_service_account --project="$PROJECT_ID"

export SQL_CONNECTION_NAME="$(gcloud sql instances describe "$SQL_INSTANCE" --project="$PROJECT_ID" --format='value(connectionName)')"
echo "SQL_CONNECTION_NAME=$SQL_CONNECTION_NAME"
```

> The CI SA also needs `roles/cloudsql.client` + `roles/cloudsql.instanceUser` (granted in 3d) to
> connect via the Auth Proxy with `--auto-iam-authn`.
> **Fallback if IAM DB auth is fiddly:** create a normal user with a password, store it as a GitHub
> secret `CI_MYSQL_PASSWORD`, and skip `--auto-iam-authn`. This re-introduces one secret; prefer IAM.

GCS buckets are **per-run** (created/destroyed by the workflow) — nothing to pre-provision.

---

## 5. GitHub repo configuration (variables)

No secrets needed for the keyless path. Set repo **variables** the workflow reads:

```bash
gh variable set GCP_PROJECT_ID       --repo "$REPO" --body "$PROJECT_ID"
gh variable set GCP_PROJECT_NUMBER   --repo "$REPO" --body "$PROJECT_NUMBER"
gh variable set GCP_REGION           --repo "$REPO" --body "$REGION"
gh variable set GCP_WIF_PROVIDER     --repo "$REPO" --body "$WIF_PROVIDER"
gh variable set GCP_CI_SA_EMAIL      --repo "$REPO" --body "$CI_SA_EMAIL"
gh variable set GCP_SQL_CONNECTION   --repo "$REPO" --body "$SQL_CONNECTION_NAME"
gh variable set GCP_SQL_IAM_USER     --repo "$REPO" --body "${CI_SA}@${PROJECT_ID}.iam"
gh variable list --repo "$REPO"
```

---

## 6. Add the GCS conformance workflow

Create `.github/workflows/gcp_gcs_conformance.yml` on a branch and push **over SSH**. It auths via
WIF, spins a per-run bucket + database, builds the conformance server + hammer, runs them **on the
runner**, and tears everything down.

```yaml
name: GCP GCS Conformance

on:
  pull_request:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read
  id-token: write          # required for WIF/OIDC

concurrency:                # Cloud SQL shared instance — serialize runs
  group: gcp-gcs-conformance
  cancel-in-progress: false

jobs:
  conformance:
    runs-on: ubuntu-latest
    env:
      RUN_ID: ${{ github.run_id }}-${{ github.run_attempt }}
    steps:
      - uses: actions/checkout@v4
        with: { persist-credentials: false }

      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }

      - id: auth
        uses: google-github-actions/auth@v2
        with:
          project_id: ${{ vars.GCP_PROJECT_ID }}
          workload_identity_provider: ${{ vars.GCP_WIF_PROVIDER }}
          service_account: ${{ vars.GCP_CI_SA_EMAIL }}

      - uses: google-github-actions/setup-gcloud@v2

      - name: Create per-run GCS bucket
        run: |
          echo "BUCKET=tessera-ci-${RUN_ID}" >> "$GITHUB_ENV"
          gcloud storage buckets create "gs://tessera-ci-${RUN_ID}" \
            --location="${{ vars.GCP_REGION }}" --uniform-bucket-level-access

      - name: Create per-run database
        run: |
          DB="ci_${RUN_ID//-/_}"
          echo "DB=$DB" >> "$GITHUB_ENV"
          gcloud sql databases create "$DB" --instance=tessera-ci-mysql

      - name: Start Cloud SQL Auth Proxy (IAM auth)
        run: |
          curl -fsSL -o cloud-sql-proxy \
            https://storage.googleapis.com/cloud-sql-connectors/cloud-sql-proxy/v2.14.1/cloud-sql-proxy.linux.amd64
          chmod +x cloud-sql-proxy
          ./cloud-sql-proxy --auto-iam-authn --port 3306 "${{ vars.GCP_SQL_CONNECTION }}" &
          for i in $(seq 1 30); do (echo > /dev/tcp/127.0.0.1/3306) >/dev/null 2>&1 && break; sleep 1; done

      - name: Generate ephemeral log keys
        run: |
          go run github.com/transparency-dev/serverless-log/cmd/generate_keys@latest \
            --key_name=tessera/ci --out_priv=key.sec --out_pub=key.pub
          echo "TESSERA_SIGNER=$(cat key.sec)" >> "$GITHUB_ENV"
          echo "TESSERA_VERIFIER=$(cat key.pub)" >> "$GITHUB_ENV"

      # ---- The two steps below depend on the GCS+MySQL conformance binary (Phase 1 of the impl). ----
      # Until that binary exists this job is NON-REQUIRED (see §9). Flag/flag names are placeholders
      # to be reconciled with the actual cmd/conformance/gcs entrypoint when it lands.
      - name: Run conformance server (background)
        run: |
          MYSQL_URI="${{ vars.GCP_SQL_IAM_USER }}@tcp(127.0.0.1:3306)/${DB}?parseTime=true"
          go run ./cmd/conformance/gcs \
            --bucket="${BUCKET}" --mysql_uri="${MYSQL_URI}" \
            --signer="${TESSERA_SIGNER}" --listen=:2024 &
          for i in $(seq 1 30); do curl -fsS localhost:2024/healthz >/dev/null 2>&1 && break; sleep 1; done

      - name: Run hammer
        run: |
          go run ./internal/hammer \
            --log_url="http://localhost:2024" --log_public_key="${TESSERA_VERIFIER}" \
            --num_writers=8 --max_write_ops=200 --num_mmd_verifiers=2 \
            --leaf_min_size=512 --show_ui=false

      - name: Teardown
        if: always()
        run: |
          gcloud storage rm -r "gs://${BUCKET}" --quiet || true
          gcloud sql databases delete "${DB}" --instance=tessera-ci-mysql --quiet || true
```

Commit + push over SSH:

```bash
cd ~/hack/trillian-tessera
git switch -c ci/gcp-gcs-conformance origin/main 2>/dev/null || git switch ci/gcp-gcs-conformance
git add .github/workflows/gcp_gcs_conformance.yml
git commit -m "Add GCS + Cloud SQL conformance CI (keyless WIF, runner-hosted)"
git push fork ci/gcp-gcs-conformance        # SSH remote 'fork' = git@github.com:patflynn/trillian-tessera.git
```
Open a same-repo PR into `main` so the new workflow (and the existing test/lint jobs) execute.

---

## 7. Disable inherited workflows that can't pass on the fork

`aws_integration_test.yml` runs on push-to-main and needs AWS creds → it will fail on the fork.
Disable it (and review the rest):

```bash
gh workflow list --repo "$REPO" --all
gh workflow disable "AWS Conformance Test" --repo "$REPO"
# Consider disabling others that need upstream-only secrets/data refs, e.g. benchmark/scorecard:
# gh workflow disable "Benchmark" --repo "$REPO"
# gh workflow disable "Scorecard supply-chain security" --repo "$REPO"
```
Leave `go_test`, `integration_test`, `golangci-lint`, `govulncheck`, `codeql` enabled — they pass on
the fork with no secrets and form the baseline PR gate.

---

## 8. Branch protection on `main` (the actual merge gate)

GitHub keys required checks off the **check name**, which you can't reliably guess — read them from
the test PR, then apply protection.

```bash
PR=<the PR number from §6>
gh pr checks "$PR" --repo "$REPO"        # note the exact names, e.g. "test", "lint", "GCP GCS Conformance / conformance"
```

Apply protection (replace the contexts with the names you just read). Start with the **always-green**
checks as required; add the conformance check in §9 once it’s green:

```bash
gh api -X PUT "repos/${REPO}/branches/main/protection" \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["test", "lint", "integration"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
```

Notes:
- `strict: true` = branch must be up to date with `main` before merge.
- `required_pull_request_reviews: null` = no human review required — chosen so **klaus auto-merge can
  still operate**. If you want review gating, set it, but klaus's auto-merge will then need an
  approval source.
- `enforce_admins: false` = you/klaus can break glass in an emergency. Set `true` for strict mode.

---

## 9. Sequencing: when the conformance check becomes required

The conformance job can't pass until **Phase 1 of the implementation** lands the native-GCS
`objStore` and a `cmd/conformance/gcs` entrypoint (see the design doc). Therefore:

1. **Now:** required checks = `test`, `lint`, `integration` (green today). Conformance job runs but is
   **not** in the required list.
2. **After** the GCS+MySQL conformance binary merges and the job goes green on a PR: add its check
   name to `required_status_checks.contexts` (re-run the §8 `gh api` call with the extra context).
   From then on, no code reaches `main` without a real GCS+Cloud SQL conformance pass.

---

## 10. Verify end-to-end

```bash
gh pr checks "$PR" --repo "$REPO" --watch          # all required checks green
gh api "repos/${REPO}/branches/main/protection" -q '.required_status_checks.contexts'
# Confirm a deliberately-failing change is blocked from merge, then close it.
```

---

## 11. Cost, teardown, and gotchas

- **Cost drivers:** the always-on Cloud SQL instance (≈ a few $/day at `db-g1-small`) and transient
  GCS. To pause spend between work sessions: `gcloud sql instances patch tessera-ci-mysql
  --activation-policy=NEVER` (and `=ALWAYS` to resume). Per-run buckets/DBs are deleted by the job.
- **Full teardown:** delete the SQL instance, the WIF pool/provider, the CI SA, and the repo
  variables; re-enable any disabled workflows.
- **Org policy:** if `gcloud storage buckets create` or SQL creation returns a policy error, the
  personal project has a constraint to relax — surface it to the human; don't work around it.
- **Hammer flags** in §6 are nominal; reconcile with `internal/hammer` actual flags when wiring.
- **Idempotency:** most `create` commands error if the resource exists — that's fine on re-runs;
  the agent should treat "already exists" as success and continue.
```
