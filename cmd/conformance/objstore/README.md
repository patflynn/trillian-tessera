# Conformance testing binary for object-store backends

This binary checks Tessera conformance using the `storage/objstore` backend over
any [gocloud.dev/blob](https://gocloud.dev/howto/blob/) object store coordinated by
MySQL. The object store is selected with a single provider-scoped bucket URL, so the
same binary serves every provider:

- **GCS:** `--blob_url=gs://BUCKET` (gcsblob driver; authenticates via Application
  Default Credentials / Workload Identity).
- **Amazon S3 / MinIO and other S3-compatible stores:** either pass a fully-specified
  `--blob_url=s3://BUCKET?endpoint=...&region=...&s3ForcePathStyle=true`, or use the
  convenience flags `--bucket` plus `--s3_endpoint`/`--s3_access_key`/`--s3_secret`
  to derive an `s3://` URL automatically.
- **Local/testing:** `--blob_url=file:///path` or `--blob_url=mem://`.

Write coordination always uses MySQL. Supply the DSN either directly with
`--mysql_uri` (e.g. pointing at a Cloud SQL Auth Proxy) or via the discrete
`--db_host`/`--db_port`/`--db_user`/`--db_password`/`--db_name` flags.

It is exercised against MinIO in the hermetic `minio-conformance` CI lane (via the
`--s3_*` convenience flags), against real GCS + Cloud SQL in the `conformance` lane
(via `--blob_url=gs://...` and `--mysql_uri`), and against real AWS (S3 + Aurora) for
AWS conformance.

See [the design doc](/docs/design/cloud-agnostic-storage.md) for the rationale.

If you want to try running it on AWS, please see the instructions in the
[README file in the /deployment/live/aws/codelab directory](/deployment/live/aws/codelab).
