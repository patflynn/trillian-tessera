# Design: Run-anywhere object-storage backend (cloud + on-prem), proven on GCP CI

**Status:** Draft · **Owner:** patflynn · **Scope:** personal fork experiment (`patflynn/trillian-tessera`)

**Chosen direction (2026-06-05):** S3-interop + MySQL (this doc), with **keyless auth as a general,
cross-provider feature** (not GCP-only). Firestore-native backend deferred; its plan is preserved
on the `firestore-plan` branch of the fork. Next step is the Phase-0 spike (conditional-write +
general keyless auth: GCS bearer [S3] and MinIO STS web-identity [S2]).

## Summary

Tessera's `storage/aws` backend is, underneath the SDK, just **two portable protocols**:
S3-compatible object storage for serving (entry bundles, tiles, checkpoints) and
MySQL for write coordination. This proposal turns that latent portability into an
explicit, tested backend that **runs anywhere** those two protocols exist — a managed
cloud (GCS/Cloud SQL, S3/Aurora), an **on-prem Kubernetes cluster with MinIO + MySQL**,
or any other S3-/MySQL-compatible combination (Ceph/RGW, Cloudflare R2, Vitess, …).

**Two principles drive the design:**

1. **Run anywhere, no lock-in.** The same backend binary must run unchanged on a public cloud
   *and* fully on-prem (k8s + MinIO + MySQL) with no dependency on any single vendor's services.
2. **Auth is the operator's choice, not ours.** Where a platform offers keyless identity
   (e.g. GCP **Workload Identity**), operators can use it and store **no secrets at all**. Where
   they'd rather use **long-lived access keys** (HMAC) — on AWS, MinIO, on-prem, or simply by
   preference — that is a **first-class, fully-supported path**, not a degraded fallback.

We *prove* the run-anywhere claim by **standing up the conformance CI on GCP** (GCS via
S3-interoperability + Cloud SQL, authenticated with Workload Identity) instead of AWS
(S3 + Aurora + ECS/Fargate). GCP is the demonstration vehicle precisely because it's the
*hardest* target — it needs both the conditional-write translation and keyless auth — so if it
passes there, MinIO/on-prem/AWS are comparatively easy.

The goal is **not** a rewrite. The backend already accepts an injectable SDK config and an
`S3Options` hook. The real work is: (1) resolve the one true incompatibility (conditional writes),
(2) make endpoint **and pluggable auth** configuration first-class and documented, and (3) stand
up a GCP conformance pipeline mirroring the AWS one.

## Deployment targets & auth (the portability matrix)

All of these run the **same** backend code; only configuration differs. None is privileged in the
code — GCP is just the one we wire into CI first.

| Target | Object store | Coordination | Auth options |
|---|---|---|---|
| **GCP** | GCS (S3-interop) | Cloud SQL (MySQL) | **Keyless: Workload Identity (bearer, S3)** *or* HMAC keys |
| **On-prem / anywhere** | MinIO, Ceph/RGW | MySQL on k8s | **Keyless: STS web-identity from k8s SA (S2)** *or* HMAC keys |
| **AWS** | S3 | Aurora (MySQL) | **Keyless: IRSA / IAM roles (S2, native)** *or* HMAC keys — unchanged |
| **Other** | Cloudflare R2, etc. | any MySQL | HMAC keys (no keyless S3 path today) |

Design rule: **keyless identity where the platform offers it; long-lived secrets as an equal,
supported choice everywhere.** Operators who are happy managing a secret should never be forced
into a federation setup, and operators who want zero secrets should never be forced to mint one.

## Background: what the "AWS" backend actually is

From `storage/aws/aws.go` (~1,600 LOC, verified against upstream `63f846b`):

| Concern | Implementation | AWS-specific? |
|---|---|---|
| Serve bundles/tiles/checkpoints | S3 object API (`GetObject`/`PutObject`/`ListObjectsV2`/`DeleteObjects`) | Protocol only |
| Write coordination | MySQL/Aurora tables: `SeqCoord`, `Seq`, `IntCoord`, `PubCoord`, `GCCoord` | No — plain MySQL |
| Credentials / client | `aws-sdk-go-v2` `config.LoadDefaultConfig` + `s3.NewFromConfig` | Yes, but injectable |

Key existing seams (already public, already portable):

- `Config.SDKConfig *aws.Config` — caller can supply any `aws.Config`, including a custom
  endpoint resolver and static credentials. (`storage/aws/aws.go:150`)
- `Config.S3Options func(*s3.Options)` — caller can mutate the S3 client options, e.g. set
  `BaseEndpoint`, force path-style addressing, or register middleware. (`storage/aws/aws.go:155`)
- **`objStore` interface** — object access (`getObject`/`setObject`/`setObjectIfNoneMatch`) now
  goes through an interface (`storage/aws/aws.go:110`), not the concrete S3 client directly. This
  is a *better* seam than middleware: we can supply a GCS-native `objStore` implementation
  (translated precondition + bearer auth) without touching the integration/coordination logic.
- MySQL is a DSN — Cloud SQL, self-hosted, Vitess, etc. all work unchanged.

The README already concedes the backend "may work" on other S3/MySQL-compatible stores, but
declares it **unsupported** and warns that out-of-AWS PRs are "unlikely to be accepted unless
shown to have no detrimental effect on AWS performance." → This is why the work lives in a
**fork** and is structured to be *additive* (no behavior change on the AWS path).

## The one true blocker: conditional create-if-absent writes 🧨

`s3Storage.setObjectIfNoneMatch` (`storage/aws/aws.go:1526`) writes tiles/checkpoints with the
S3 header **`If-None-Match: "*"`** to get atomic *create-only* semantics (and idempotency on
retry). This is the correctness-critical primitive that makes concurrent integrators safe.

**GCS over S3-interop does NOT honor `If-None-Match: "*"` on PUT.** Per the GCS XML API
reference, conditional create is expressed with the GCS-native header
**`x-goog-if-generation-match: 0`** ("perform the request only if the object does not currently
exist"). `If-None-Match` is documented only for GET/HEAD ETag matching.

Sources:
- GCS XML API reference headers: <https://docs.cloud.google.com/storage/docs/xml-api/reference-headers>
- GCS interoperability / S3 migration: <https://docs.cloud.google.com/storage/docs/interoperability>

### Consequence

Pointed naively at GCS, `setObjectIfNoneMatch` would degrade to an unconditional PUT — the
precondition is ignored, so the create-only guarantee is **silently lost**. That breaks the
safety the integrator relies on. Any "runs on GCS" claim must close this gap.

### Proposed fix: an S3-client middleware that translates the precondition

The `aws-sdk-go-v2` S3 client supports request middleware via `APIOptions` (reachable through
the existing `S3Options` hook). We register a finalize/serialize-step middleware that, when the
target is GCS, rewrites the outbound HTTP request:

- detects the `If-None-Match: *` header on `PutObject`,
- removes it, and sets `x-goog-if-generation-match: 0`,
- maps the GCS precondition-failure response (HTTP 412) back to the same
  `smithy.APIError` code path the existing code already handles (`PreconditionFailed`),
  so the idempotency branch (compare-existing-bytes) keeps working unchanged.

This keeps the translation **entirely outside** the core storage logic and **zero-cost on AWS**
(middleware only activates for the GCS endpoint), satisfying the "no detriment to AWS" rule.

**Alternative seam (possibly cleaner):** upstream now routes object access through an `objStore`
interface (`aws.go:110`). Rather than rewriting HTTP at the SDK layer, we can provide a GCS-native
`objStore` implementation that issues the correct precondition (and bearer auth, §2) directly,
while reusing the identical integration/coordination code. The Phase-0 spike should try both and
keep whichever is simpler to maintain.

> ⚠️ Spike required: confirm (a) GCS returns 412 with a smithy-classifiable code, and (b) path-style
> addressing + HMAC V4 signing interplay with the injected header. This is the riskiest assumption
> and should be proven first (see Phase 0).

## Goals / Non-goals

**Goals**
- **Run anywhere on the same code:** the backend runs unchanged on GCS+Cloud SQL, on
  **on-prem k8s + MinIO + MySQL**, on AWS S3+Aurora, and on other S3/MySQL-compatible stores —
  no vendor lock-in, no per-target code paths beyond config.
- **Operator-chosen auth, both first-class:** keyless Workload Identity *and* long-lived HMAC keys
  are fully supported; choosing secrets is never a degraded mode.
- A GCP conformance CI pipeline that mirrors `aws_integration_test.yml` end to end
  (build image → deploy ephemeral infra → run hammer → tear down), using Workload Identity — the
  proof that the hardest target works.
- All changes additive and gated; the AWS path is byte-for-byte unchanged at runtime.

**Non-goals**
- Replacing or merging with the *existing* `storage/gcp` backend (GCS + **Spanner**). That is a
  different design (Spanner coordination); this one keeps **MySQL** coordination and only swaps
  the object store. Naming must avoid confusion (see Open Questions).
- Upstreaming (for now). Revisit once the fork proves the approach and perf parity.
- **CI coverage for *every* target.** On-prem/MinIO and other S3 stores are **first-class supported
  targets**, not afterthoughts — but GCP is the only one we wire into *automated CI* initially
  (it's the hardest, so it subsumes the others). A MinIO-on-k8s CI lane is a welcome stretch goal,
  not a blocker. ("Supported" ≠ "in CI from day one".)

## Design

### 1. Configuration surface

Add a small, explicit config path for "S3-compatible, non-AWS" usage rather than making callers
hand-assemble an `aws.Config`:

- Endpoint (e.g. `https://storage.googleapis.com`), path-style toggle, region placeholder.
- **Pluggable auth provider (see §2):** one config-selected seam, three strategies — static HMAC
  keys, keyless STS/web-identity (AWS/MinIO/Ceph), or keyless bearer token (GCS). Keyless is a
  general capability, not GCP-only; no strategy is privileged in code.
- A `Flavor`/`Dialect` enum (e.g. `AWS` | `GCS`) that decides whether the precondition-translation
  and auth middleware are installed. Default `AWS` → no change.

This can be a thin constructor wrapper that produces the `SDKConfig` + `S3Options` the existing
`New(...)` already accepts — i.e. **no change to the core `New` signature**.

### 2. Authentication: one pluggable seam, keyless as a general feature

Auth is a **pluggable provider**, not a hard-coded mode. The backend selects a credential/auth
strategy from config; the integration and coordination code never sees the difference. **Keyless
is designed as a general capability, not a GCP special-case** — and it's feasible largely because
two of the three strategies already live in the AWS SDK's credential chain. Only the GCS bearer
path is bespoke.

**Strategy 1 — Static HMAC keys (works everywhere).** Long-lived access key + secret, SigV4. The
default today on AWS; works unchanged on MinIO, Ceph/RGW, Cloudflare R2, and GCS HMAC interop. The
zero-infrastructure choice; pair with a k8s Secret / Secret Manager / env. A first-class, fully
supported option — not a fallback.

**Strategy 2 — Keyless via STS web-identity → temporary SigV4 creds (the *general* keyless path).**
The S3-native way to be keyless; generalises across any store exposing an STS
`AssumeRoleWithWebIdentity` endpoint:

- **AWS:** EKS **IRSA** / instance / task roles — the SDK's default chain already exchanges the
  projected token (`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`) for temporary creds. **Zero new code.**
- **MinIO** and **Ceph/RGW:** both implement STS `AssumeRoleWithWebIdentity`. A pod's projected
  Kubernetes ServiceAccount JWT is exchanged at the store's STS endpoint for short-lived SigV4
  creds; the SDK's `stscreds.WebIdentityRoleProvider` does this once pointed at the store's STS
  endpoint.

So a single config shape — *(identity-token source, STS endpoint, role)* — covers keyless on AWS,
MinIO, and Ceph. This is mostly **SDK configuration**, not new signing code.

**Strategy 3 — Bearer token, no SigV4 (stores whose object API speaks OIDC directly).** Some stores
accept an OAuth/OIDC bearer token *instead of* SigV4. **GCS's XML API** is the motivating case: we
obtain a short-lived token from a `TokenSource` (ADC → Workload Identity on GKE/Cloud Run, WIF in
CI, or `gcloud` locally), **remove the SDK's SigV4 signer**, and attach `Authorization: Bearer
<token>`, refreshing as it nears expiry. This strategy is provider-specific by nature (it isn't S3
protocol), but the *seam* is generic: any future store that authenticates by bearer token plugs in
here with its own `TokenSource`.

**Unifying abstraction.** Strategies 2 and 3 both reduce to a **`TokenSource`** (ambient workload
identity — k8s projected SA token, cloud metadata, OIDC) plus an **`Authenticator`** that turns it
into request auth: either *exchange-for-SigV4* (S2) or *present-as-bearer* (S3). Strategy 1 is just
an `Authenticator` backed by static keys. **One interface, three implementations, chosen by config.**

| Provider | Keyless mechanism | Build effort |
|---|---|---|
| AWS (S3) | S2 — STS / IRSA, **native in SDK** | none — works today |
| MinIO | S2 — STS `AssumeRoleWithWebIdentity` | point SDK STS provider at MinIO endpoint |
| Ceph / RGW | S2 — STS `AssumeRoleWithWebIdentity` | same shape as MinIO |
| GCP (GCS) | S3 — bearer token (ADC / WIF) | the bespoke bit; gated by Phase-0 spike |
| Cloudflare R2 | — none (S3 access keys only) | use Strategy 1 |

**Honest limits:** R2 has no OIDC/STS path for its S3 API today, so it's Strategy-1-only — keyless
is "general where the protocol allows it," not literally universal. And Strategy 3 must clear the
Phase-0 spike (can we cleanly drop SigV4 in `aws-sdk-go-v2` and have GCS accept the bearer token).

> ⚠️ Spike scope (Phase 0): **(a)** GCS bearer-token path [Strategy 3]; **(b)** MinIO STS
> web-identity exchange [Strategy 2] from a fake k8s token → temporary SigV4 creds. Proving (a)+(b)
> validates the *general* keyless design end to end; Strategy 1 needs no proof. If (a) fails, GCP
> keyless falls back to GCS HMAC keys without affecting S2/S1 elsewhere.

### 3. Object-store compatibility

- Install the conditional-write middleware when `Dialect == GCS`.
- Force path-style addressing (GCS interop + dotted bucket names → virtual-host TLS issues).
- Verify `ListObjectsV2` + batch `DeleteObjects` (used by GC, `aws.go:1574`) behave on GCS;
  GCS supports these via XML API but pagination/markers should be smoke-tested.

### 4. Coordination store

No code change expected — point the MySQL DSN at Cloud SQL (MySQL 8.0). CI uses the Cloud SQL
Auth Proxy or a private IP from the runner/job. Validate schema bootstrap
(`Tessera`/`SeqCoord`/...) applies cleanly on Cloud SQL.

### 5. GCP conformance CI (the "port the CI" half)

Mirror `aws_integration_test.yml`, swapping each AWS primitive for its GCP analogue:

| AWS (today) | GCP (proposed) |
|---|---|
| OIDC → `aws-actions/configure-aws-credentials` | Workload Identity Federation → `google-github-actions/auth` |
| ECR (image registry) | Artifact Registry |
| ECS/Fargate task run (conformance + hammer) | Cloud Run jobs **or** GKE Job |
| Aurora MySQL | Cloud SQL (MySQL 8.0) |
| S3 bucket | GCS bucket (interop enabled) |
| HMAC access keys (static secret) | **WIF → short-lived OAuth bearer token** — no stored secret |
| Terragrunt `deployment/live/aws/conformance/ci` | new `deployment/live/gcp/conformance/interop/` |
| `cmd/conformance/aws/Dockerfile` | reuse, or `cmd/conformance/gcs/Dockerfile` |

New Terraform/Terragrunt modules under `deployment/modules/gcp/` for: GCS bucket + HMAC key,
Cloud SQL instance, Artifact Registry repo, Cloud Run/GKE job definitions, and the WIF service
account. A new workflow `gcp_interop_integration_test.yml` orchestrates build → apply → hammer →
destroy, matching the AWS one's lifecycle (including pre-destroy cleanup).

> Note: the repo already has `deployment/live/gcp/...` and `deployment/modules/gcp/gcs` for the
> *Spanner* backend. Keep the interop pipeline in a clearly separate path to avoid collision.

## Relationship to the Firestore backend plan

This fork already carries `FIRESTORE_IMPLEMENTATION_PLAN.md` — a plan for a *native*
`storage/firestore/` backend where Firestore is **both** the object store (documents for
tiles/bundles/checkpoints) **and** the coordinator (Firestore transactions replace MySQL). That's
a different philosophy, and the Workload-Identity requirement sharpens the contrast:

| | This doc: S3-interop + MySQL | Firestore backend |
|---|---|---|
| Code reuse | ~1,600 LOC of `storage/aws` largely as-is | New backend, roughly `aws.go`-sized |
| "Runs anywhere" | ✅ one backend → AWS, GCS, MinIO, R2, Ceph | ❌ GCP-only (Firestore) |
| Workload Identity | ⚠️ needs the bearer-token middleware spike | ✅ native — Firestore Go client uses ADC/IAM, **no secrets ever** |
| Coordination | proven MySQL transaction design | Firestore txns — new concurrency model to validate |
| Object-size limits | n/a (object store) | ⚠️ 1 MiB/doc — entry bundles may need chunking |

- If **"cloud-agnostic, runs anywhere"** is the priority → this S3-interop design.
- If **"cleanest GCP-native, secret-less"** is the priority → the Firestore backend wins on auth
  outright, but gives up portability.
- **Hybrid worth considering:** S3-interop object store for the portable serving path **+**
  Firestore (or Spanner) for coordination only. Keeps "runs anywhere" for reads while getting
  native, secret-less coordination on GCP — at the cost of two code paths.

These aren't mutually exclusive, but they shouldn't be pursued blindly in parallel. Recommend
deciding the coordination story before writing backend code.

## Phased plan

- **Phase 0 — De-risk (spike, throwaway):** Two proofs.
  **(1) Conditional write:** AWS SDK → real GCS bucket; prove `setObjectIfNoneMatch` via the
  translated `x-goog-if-generation-match: 0` header (first write OK; second gets 412 → idempotency
  branch).
  **(2) General keyless auth:** prove Strategy 3 (GCS bearer token from ADC, SigV4 stripped) *and*
  Strategy 2 (MinIO STS `AssumeRoleWithWebIdentity` from a k8s-style token → temporary SigV4 creds).
  MinIO runs locally, so S2 is cheap to prove first; it also validates the conditional write on a
  second S3 implementation. **Go/no-go gate for the whole approach.**
- **Phase 1 — Backend config + middleware:** Add the `Dialect`/constructor wrapper and the
  precondition-translation middleware. Unit tests + a GCS-backed integration test (build-tagged,
  skipped without creds). No change to AWS behavior.
- **Phase 2 — Terraform/Terragrunt GCP modules:** GCS+HMAC, Cloud SQL, Artifact Registry,
  Cloud Run/GKE job, WIF. Apply/destroy locally.
- **Phase 3 — CI workflow:** `gcp_interop_integration_test.yml` mirroring the AWS lifecycle;
  run the hammer to conformance-completion; ensure teardown is reliable.
- **Phase 4 — Docs + (optional) rename:** README for the interop backend; decide on package
  naming; write up perf observations vs AWS.

## Risks / open questions

1. **Conditional-write translation correctness** — the whole thing hinges on Phase 0. If GCS
   won't surface a clean 412 through the SDK, fallback is a thin object-store interface with a
   GCS-native code path (heavier; closer to forking `storage/aws`).
2. **Other silent S3-interop gaps** — multipart upload thresholds, `DeleteObjects` batch
   semantics, ListV2 pagination, ETag format assumptions. Smoke-test each.
3. **Compute choice** — Cloud Run jobs (simplest, serverless, matches Fargate ergonomics) vs GKE
   (closer to ECS networking model, more control). Lean Cloud Run jobs unless networking to
   Cloud SQL forces GKE.
4. **Naming / package identity** — `storage/aws` doing GCS is confusing, and `storage/gcp`
   already means GCS+Spanner. Options: keep `storage/aws` + dialect flag (least churn), or extract
   a shared `storage/s3` used by both an `aws` and `gcs-interop` thin wrapper. Fork can be bold.
5. **Cost & CI runtime** — Aurora bring-up is already the slow part on AWS; Cloud SQL has similar
   cold-start. Consider keeping a long-lived CI instance vs per-run create/destroy.
6. **Bearer-token auth vs the SDK** — replacing SigV4 with an OAuth bearer token inside
   `aws-sdk-go-v2` is the second-riskiest assumption after conditional writes. Both must clear
   Phase 0 together, since they're exercised by the same PUT path. If either fails on GCS, the
   Firestore-native path (below) starts to look more attractive precisely because it sidesteps
   both.
7. **Keyless generality has edges** — (a) **R2 (and possibly other S3 clones) have no STS/OIDC**,
   so keyless can't be universal; Strategy 1 must always remain a first-class option. (b) STS
   web-identity needs per-provider config of *token audience*, *role mapping*, and *STS endpoint* —
   easy to misconfigure; document a known-good MinIO + k8s recipe. (c) Token lifetime/refresh and
   clock skew must be handled in the `Authenticator` for both S2 and S3 (long-running integrators
   outlive a single token).

## Appendix: key code references

- `storage/aws/aws.go:110` — `objStore` interface (cleanest seam for a GCS-native implementation)
- `storage/aws/aws.go:150` — `Config.SDKConfig` injection point
- `storage/aws/aws.go:155` — `Config.S3Options` hook (SDK middleware entry point)
- `storage/aws/aws.go:179` — default `config.LoadDefaultConfig`
- `storage/aws/aws.go:1526` — `setObjectIfNoneMatch` impl; `IfNoneMatch:"*"` at `:1538`
- `storage/aws/aws.go:1574` — `ListObjectsV2` + `DeleteObjects` (GC path)
- Verified against upstream `63f846b`; the standalone `storage/mysql` backend was removed upstream,
  but `storage/aws` still embeds MySQL coordination (`SeqCoord`/`IntCoord`/... at `:947`+).
- `.github/workflows/aws_integration_test.yml` — the pipeline to mirror on GCP
- `deployment/live/aws/conformance/`, `deployment/modules/aws/` — Terragrunt to mirror
