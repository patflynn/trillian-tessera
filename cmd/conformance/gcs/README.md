# Conformance testing binary for GCS + MySQL

This binary is a conformance/compliance/performance personality for
**Google Cloud Storage with MySQL coordination**.

It reuses the `storage/aws` backend's MySQL sequencing/integration code unchanged
and selects GCS through the portable gocloud.dev/blob object store
(`storage/aws/blob.go`) via `aws.Config{BlobURL: "gs://BUCKET"}`. In other words
it is identical to [`cmd/conformance/aws`](../aws/) except the object store is
selected with a `gs://` bucket URL (gcsblob driver) instead of an `s3://` one.
See [the design doc](/docs/design/cloud-agnostic-storage.md) for the rationale.

Like the other conformance personalities, it exposes a `POST /add` endpoint that
appends the request body to the log and returns the assigned index. Reads are
served directly from the GCS bucket (the standard tlog-tiles layout).

## Auth

- **GCS:** the client uses Application Default Credentials / Workload Identity
  automatically (`storage.NewClient`), so no keys are required.
- **MySQL:** pass a complete DSN via `--mysql_uri`. When using Cloud SQL with IAM
  database authentication, run the Cloud SQL Auth Proxy with `--auto-iam-authn`
  and point `--mysql_uri` at the proxy, e.g.
  `user@tcp(127.0.0.1:3306)/db?parseTime=true`.

## Flags

| Flag | Description |
|---|---|
| `--bucket` | GCS bucket to use for storing the log (required) |
| `--mysql_uri` | Full MySQL DSN used for write coordination (required) |
| `--signer` | Note signer used to sign checkpoints |
| `--listen` | Address:port to listen on (default `:2024`) |
| `--bucket_prefix` | Optional prefix prepended to all log resource paths |
| `--additional_signer` | Additional checkpoint signer (repeatable) |
| `--publish_interval` | How frequently to publish updated checkpoints |

## CI

The [`gcp_gcs_conformance`](/.github/workflows/gcp_gcs_conformance.yml) GitHub
Actions workflow runs this binary against a real GCS bucket and Cloud SQL (MySQL)
instance on every PR, driving load with the [hammer](/internal/hammer/).
