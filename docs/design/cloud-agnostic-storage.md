# Design: Run-anywhere object-storage backend (cloud + on-prem)

**Status:** Draft (rev 2) · **Owner:** patflynn · **Scope:** personal fork experiment (`patflynn/trillian-tessera`)

**Chosen direction (2026-06-05):** Keep `storage/aws`'s MySQL coordination + integration logic;
make the **object store a pluggable implementation behind the existing `objStore` interface**.
Ship a **native-GCS-client** implementation first (GCS is required short-term), then evaluate a
single `gocloud.dev/blob`-backed implementation for true run-anywhere. Firestore-native backend
deferred (plan preserved on the `firestore-plan` branch).

> **Rev 2 changes the core mechanism.** Rev 1 proposed pointing the AWS S3 client at GCS via
> S3-interoperability and translating headers + auth. Investigation showed that path is the
> *worst-of-both-worlds*: it's a configuration nobody runs in production **and** the one that needs
> the most bespoke work (header translation, SigV4-stripping bearer auth, and it still hits GCS
> gaps in batch-delete / chunked-PUT / HMAC org-policy). The native GCS client removes all of that.
> S3-interop is retained **only** for genuinely S3-native stores (AWS, MinIO, Ceph/RGW, R2).

## Summary

Tessera's `storage/aws` backend has two separable halves:

1. **Object storage** — get/put/conditional-put/delete of bundles, tiles, checkpoints. Already
   abstracted behind an unexported **`objStore` interface** (4 methods).
2. **Write coordination** — sequencing/integration/publish/GC, on **MySQL** (`SeqCoord`, `Seq`,
   `IntCoord`, `PubCoord`, `GCCoord`), using only standard SQL.

The coordination half is **already portable** (any MySQL: Cloud SQL, self-hosted, Vitess) and
needs no code change. The only thing tying the backend to AWS is the object-store implementation —
so the design is: **swap the `objStore` implementation per provider, keep everything else.**

The hard part is one primitive: **atomic create-if-absent** (`setObjectIfNoneMatch`), which makes
concurrent integrators safe. The whole client/library question reduces to *"which object client
exposes a reliable create-if-absent on this provider?"* — answered in §3.

**Two principles still drive the design:**

1. **Run anywhere, no lock-in** — same coordination + integration code on managed cloud and fully
   on-prem (k8s + MinIO + MySQL).
2. **Auth is the operator's choice** — keyless where the platform offers it (GCS: native Workload
   Identity; AWS/MinIO/Ceph: STS web-identity), and long-lived keys as a **first-class** option,
   never a degraded fallback.

## Deployment targets & auth (the portability matrix)

Same coordination + integration code everywhere; only the `objStore` impl and config differ.

| Target | Object store (impl) | Coordination | Auth |
|---|---|---|---|
| **GCP** | GCS via **native client** (or `gocloud` gcsblob) | Cloud SQL (MySQL) | **Native ADC / Workload Identity — zero secrets** |
| **On-prem / anywhere** | MinIO, Ceph/RGW via **S3 client** (or `gocloud` s3blob) | MySQL on k8s | STS web-identity from k8s SA *or* HMAC keys |
| **AWS** | S3 via existing client | Aurora (MySQL) | IAM role / IRSA *or* HMAC keys — **unchanged** |
| **Other** | Cloudflare R2 via S3 client | any MySQL | HMAC/API-token keys (no STS/OIDC path) |

Design rule: **native keyless identity where the platform offers it; long-lived secrets an equal,
supported choice everywhere.**

## Background: what the "AWS" backend actually is

From `storage/aws/aws.go` (~1,600 LOC at upstream `63f846b`; cite by symbol, not line — the file
churns, e.g. +457 lines in one recent sync):

| Concern | Implementation | AWS-specific? |
|---|---|---|
| Object access | `objStore` interface → `s3Storage` (aws-sdk-go-v2) | **Interface: no.** Impl: yes |
| Write coordination | MySQL tables, standard SQL (`FOR UPDATE`, `INSERT IGNORE`) | No — plain MySQL |
| Client construction | `config.LoadDefaultConfig` + `s3.NewFromConfig` | Yes, but injectable |

The reusable seam — **`objStore`** (`type objStore interface` in `aws.go`):

```go
type objStore interface {
    getObject(ctx, obj) ([]byte, error)
    setObject(ctx, obj, data, contentType, cacheControl) error
    setObjectIfNoneMatch(ctx, obj, data, contentType, cacheControl) error   // ← the hard one
    deleteObjectsWithPrefix(ctx, prefix) error
}
```

**Important constraint (corrects rev 1's "clean seam" claim):** `objStore`, its methods, the
`s3Storage` impl, *and* the MySQL `sequencer`/integration code are all **unexported**, inside
package `aws`. Consequences:

- A new `objStore` impl **must live in package `aws`** (or upstream must export a shared package).
- Selecting it still requires a **small edit to the constructor** to branch on a `Dialect`. So this
  is *not* zero-diff — it's "one new file + one constructor branch." That's the realistic minimum,
  and the right target given the rebase tax below.

The README declares non-AWS use **unsupported** and says such PRs are "unlikely to be accepted
unless shown to have no detrimental effect on AWS performance." → permanent fork; **minimize the
diff to existing files** so rebases against a fast-moving upstream stay cheap.

## The core primitive: atomic create-if-absent

`setObjectIfNoneMatch` writes tiles/bundles/checkpoints with create-only semantics (and treats an
existing **byte-identical** object as idempotent success). The current S3 impl uses
`If-None-Match: "*"` and keys idempotency off `smithy.APIError.ErrorCode() == "PreconditionFailed"`.

How each candidate object client provides this primitive:

| Provider | Native primitive | Notes |
|---|---|---|
| AWS S3 / MinIO / Ceph / R2 | `If-None-Match: "*"` | Works on the existing S3 impl today |
| **GCS (native client)** | `obj.If(storage.Conditions{DoesNotExist:true})` → 412 | ✅ clean; **not** available via S3-interop |
| **`gocloud.dev/blob`** | `WriterOptions.IfNotExist=true` → `gcerrors.PreconditionFailed` | ✅ portable across gcsblob/s3blob/etc. |

> ⚠️ **GCS over S3-interop does NOT honor `If-None-Match:"*"`** (it uses `x-goog-if-generation-match: 0`).
> This was rev 1's central blocker — now **avoided** by not using S3-interop for GCS.

## 3. Object-store client strategy (the heart of rev 2)

The `objStore` interface is the portability seam. Three ways to fill it; we recommend A now, B next.

### Option A — Per-provider native impls (recommended to start; GCS now) ✅
- **AWS / MinIO / Ceph / R2:** keep the existing `s3Storage` (aws-sdk-go-v2). Unchanged.
- **GCS:** new `gcsStore` implementing `objStore` with `cloud.google.com/go/storage`:
  - `setObjectIfNoneMatch` → `obj.If(storage.Conditions{DoesNotExist:true}).NewWriter`; on
    `*googleapi.Error` code 412, fall back to byte-compare for idempotency (mirrors current logic).
  - `deleteObjectsWithPrefix` → objects iterator + per-object delete (sidesteps S3 batch-delete).
  - Auth → `storage.NewClient(ctx)` uses **ADC / Workload Identity** automatically. **No HMAC, no
    bearer hacking, no SigV4 stripping.**
- **Why first:** GCS is required short-term, and this is the *smallest, lowest-risk* way to get a
  correct, secret-less GCS backend. It deletes rev 1's three GCS-only blockers outright.
- **Cost:** a GCS-specific file lives in package `aws` (naming smell — see Open Questions) and adds
  the `cloud.google.com/go/storage` dependency to that build.

### Option B — Unify on `gocloud.dev/blob` (evaluate next; the run-anywhere endgame)
- One `objStore` impl over `*blob.Bucket`; pick driver by URL (`gs://`, `s3://`, `file://`, `mem://`).
  `WriterOptions.IfNotExist` gives the create-if-absent primitive uniformly; auth is each driver's
  native chain (gcsblob → ADC/WIF; s3blob → SDK chain incl. STS/HMAC).
- **Upside:** genuinely one implementation for GCS + AWS + MinIO + Ceph + R2 + local; Google-maintained.
- **Must verify before adopting (spike):** that `IfNotExist` is honored by **s3blob against MinIO/R2/Ceph**
  and by **gcsblob**, and that error semantics + cache-control/content-type pass through. The Go CDK
  docs explicitly warn "driver support varies."
- **Trade-off:** new dependency; less low-level control than the raw SDKs.

### Option C — S3-interop-to-GCS + middleware (rev 1; **rejected**)
Rejected because it is maximal-effort for a non-production config and still must solve, on GCS:
batch-delete incompatibility, chunked-PUT-vs-SigV4, HMAC org-policy blocks, and brittle error-code
mapping (all detailed in Risks). Documented here so the rejection is on record.

## 4. Authentication

With Option A/B, auth is **much simpler than rev 1** because GCS uses its native client.

- **GCS — native ADC / Workload Identity (zero secrets).** `storage.NewClient` resolves WIF on
  GKE/Cloud Run, WIF federation in GitHub Actions, or `gcloud` locally. No HMAC, no bearer middleware.
- **AWS / MinIO / Ceph — pick one, both first-class:**
  - *Static HMAC keys* (SigV4): the existing default; works everywhere S3 does.
  - *Keyless STS web-identity:* exchange a k8s projected ServiceAccount JWT at the store's STS
    `AssumeRoleWithWebIdentity` endpoint for temporary SigV4 creds. **Native in the SDK on AWS
    (IRSA — zero new code)**; for MinIO/Ceph, point `stscreds.WebIdentityRoleProvider` at their STS
    endpoint. Config shape: *(token source, STS endpoint, role)*.
- **R2 / other:** API-token / access-key only (no OIDC/STS) — Strategy-1 only. Honest limit:
  keyless is "general where the protocol allows it," not universal.

The bearer-token-over-S3 approach from rev 1 is **no longer needed** — it only existed to force
S3-interop onto GCS, which we now avoid. (Kept in git history for reference.)

## 5. Coordination store (unchanged — verified)

No code change. The schema is vanilla MySQL (`CREATE TABLE … IF NOT EXISTS`, `INSERT IGNORE`,
`SELECT … FOR UPDATE`); no Aurora-specific SQL. Cloud SQL (MySQL 8.0), self-hosted, or on-prem
MySQL accept it as-is. CI connects via the Cloud SQL Auth Proxy or private IP.

**Strategic caveat (S2):** coordination remains a single MySQL serialization point
(`SELECT … FOR UPDATE`). That is the throughput ceiling the *native* `storage/gcp` backend uses
Spanner to avoid. "Run anywhere" inherits the MySQL coordination ceiling — acceptable for the
portability goal, but a known limit, not a cloud-native scaling story.

## 6. CI plan — GCS first, MinIO-on-GKE second

Per the substrate decision (do both; GCS is required short-term):

- **CI lane 1 (now): GCS + Cloud SQL on GCP**, auth via Workload Identity Federation. Exercises the
  native-GCS `objStore`. Mirror `aws_integration_test.yml`'s lifecycle (build → deploy ephemeral
  infra → run hammer → tear down) with GCP analogues.
- **CI lane 2 (next): MinIO-on-GKE + Cloud SQL.** Cheapest, most faithful proof of the *portable
  S3 path* and the on-prem story; no GCS quirks. Add once lane 1 is green.

| AWS today | GCP analogue |
|---|---|
| OIDC → `configure-aws-credentials` | WIF → `google-github-actions/auth` |
| ECR | Artifact Registry |
| ECS/Fargate task (conformance + hammer) | Cloud Run jobs **or** GKE Job |
| Aurora MySQL | Cloud SQL (MySQL 8.0) |
| S3 bucket | GCS bucket (lane 1) / MinIO on GKE (lane 2) |
| Terragrunt `live/aws/conformance/ci` | new `live/gcp/conformance/{gcs,minio}/` |

New Terraform/Terragrunt under `deployment/modules/gcp/`: GCS bucket + IAM (no HMAC needed),
Cloud SQL, Artifact Registry, Cloud Run/GKE job, WIF service account. The conformance **binary**
must construct the new `Dialect` config — so expect a new/extended `cmd/conformance/gcs/main.go`,
not just a Dockerfile reuse.

> The repo already has `deployment/live/gcp/...` + `deployment/modules/gcp/gcs` for the *Spanner*
> backend. Keep this pipeline in a clearly separate path to avoid collision.

## Relationship to existing GCP work

- **`storage/gcp` (native GCS + Spanner)** already exists. This design is deliberately different:
  native GCS object store **+ MySQL** coordination, reusing `storage/aws`'s sequencer/integration.
  The point is portability of the *coordination + integration* code across providers, not a second
  Spanner backend. (If MySQL-coordination scaling becomes the bottleneck, the Spanner backend is
  the answer — see S2.)
- **Firestore plan** (`firestore-plan` branch): a fully native, GCP-only backend (Firestore as both
  store and coordinator; native WIF; 1 MiB/doc bundle-chunking concern). Deferred. If "run
  anywhere" stops being the priority, that's the cleaner *GCP-only* answer.

## Goals / Non-goals

**Goals**
- Same coordination + integration code on GCS+Cloud SQL, on-prem k8s+MinIO+MySQL, and AWS S3+Aurora.
- A correct, **secret-less GCS** backend via the native client (short-term necessity).
- GCP conformance CI (lane 1), then MinIO-on-GKE CI (lane 2).
- **Strictly additive within `storage/aws`:** new file(s) for the GCS impl + one constructor branch.
  The AWS path is byte-for-byte unchanged at runtime. **No package rename** in the fork (rename is
  an upstream-only concern; renaming maximizes rebase pain — see S3).

**Non-goals**
- Replacing/merging the existing `storage/gcp` (Spanner) backend.
- Upstreaming (for now) — revisit after the fork proves it; expect to carry a permanent diff.
- CI for *every* target from day one (MinIO lane is lane 2, not a blocker). "Supported" ≠ "in CI".

## Phased plan

- **Phase 0 — De-risk spike (throwaway).** Prove the **native-GCS `objStore`** end-to-end against a
  real GCS bucket with **Workload Identity** (confirm the target project's org policy permits the
  intended auth — `constraints/storage.restrictAuthTypes` etc.):
  1. `setObjectIfNoneMatch` via `Conditions{DoesNotExist:true}` — first write OK; second → 412 →
     byte-compare idempotency branch.
  2. `deleteObjectsWithPrefix` via iterator + per-object delete.
  3. list/iterate semantics for any prefix scans.
  4. auth: client works from ADC/WIF with **no static key**.
  Optionally spike **Option B** (`gocloud.dev/blob` `IfNotExist` on gcsblob **and** s3blob/MinIO) to
  decide whether to converge. **Go/no-go gate.**
- **Phase 1 — GCS objStore in-tree.** Add `gcsStore` (package `aws`) + a `Dialect` branch in the
  constructor. Unit tests + a GCS integration test (build-tagged, skipped without creds). AWS path
  unchanged.
- **Phase 2 — GCP infra (Terraform/Terragrunt):** GCS + IAM (no HMAC), Cloud SQL, Artifact Registry,
  Cloud Run/GKE job, WIF SA. Apply/destroy locally.
- **Phase 3 — CI lane 1:** `gcp_gcs_integration_test.yml` mirroring the AWS lifecycle; hammer to
  conformance; reliable teardown.
- **Phase 4 — Run-anywhere:** CI lane 2 (MinIO-on-GKE); evaluate adopting Option B to unify impls.
- **Phase 5 — Docs + naming:** README; revisit package naming (upstream/long-term only).

## Risks / open questions

1. **🔴 Native-GCS objStore correctness** — the spike must confirm create-if-absent 412 handling,
   per-object delete, list semantics, and content-type/cache-control parity with the S3 impl.
2. **🟠 Org policy on the GCP project** — `constraints/storage.restrictAuthTypes` can block HMAC
   (and this is a locked-down corporate GCP context — web search was already org-policy-blocked).
   Native-client + WIF sidesteps HMAC entirely, but confirm WIF/SA permissions exist before Phase 0.
3. **🟠 `gocloud.dev/blob` driver parity (Option B)** — verify `IfNotExist` on s3blob/MinIO/R2 and
   gcsblob, plus error + metadata passthrough, before betting the unification on it.
4. **🟠 Unexported reuse / rebase tax** — `objStore`, `sequencer`, integration are unexported in
   package `aws`; the GCS impl must live there and add one constructor branch. Keep the diff tiny;
   upstream churns `aws.go` heavily (+457 lines in one sync).
5. **🟠 Naming smell** — a GCS-native file inside `storage/aws` is confusing, and `storage/gcp`
   already means GCS+Spanner. Tolerate in the fork (minimal diff); only resolve via rename if/when
   upstreaming.
6. **🟠 MySQL coordination ceiling (S2)** — single `FOR UPDATE` serialization point; fine for the
   portability goal, not a scaling story. Spanner backend remains the answer if that bites.
7. **🟡 Compute choice** — Cloud Run jobs (simplest, Fargate-like) vs GKE Job (closer to ECS
   networking; may be needed for Cloud SQL connectivity). Lean Cloud Run jobs.
8. **🟡 CI cost/runtime** — Cloud SQL bring-up is slow (as Aurora is today); consider a long-lived
   CI instance vs per-run create/destroy.
9. **🟡 Rejected S3-interop-to-GCS landmines (for the record)** — batch-delete format mismatch,
   "chunked transfer encoding + V4 signatures can't be used simultaneously" on GCS, unverified
   `list-type=2`, and `If-None-Match:"*"` ignored. All avoided by Option A/B; listed so Option C
   stays rejected for the right reasons.

## Appendix: key code references (by symbol — line numbers rot)

- `objStore` interface + `s3Storage` impl — `storage/aws/aws.go`
- `setObjectIfNoneMatch` — the create-if-absent primitive; idempotency keys off
  `smithy.APIError.ErrorCode() == "PreconditionFailed"` (must be re-implemented for the GCS 412 path)
- `deleteObjectsWithPrefix` — GC path; currently S3 `ListObjectsV2` + batch `DeleteObjects`
- `Config.SDKConfig` / `Config.S3Options` — injection points (still used by the S3 impl)
- MySQL coordination — `SeqCoord`/`Seq`/`IntCoord`/`PubCoord`/`GCCoord`, standard SQL
- Native GCS client — `cloud.google.com/go/storage`: `ObjectHandle.If(Conditions{DoesNotExist:true})`,
  `NewWriter`, errors via `*googleapi.Error` (Code 412)
- Go CDK — `gocloud.dev/blob`: `WriterOptions.IfNotExist`, `gcerrors.PreconditionFailed`
- CI to mirror — `.github/workflows/aws_integration_test.yml`; `deployment/{live,modules}/aws/`
- Verified against upstream `63f846b`; standalone `storage/mysql` removed upstream, but
  `storage/aws` still embeds MySQL coordination.
