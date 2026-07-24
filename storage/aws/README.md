# Tessera on Amazon Web Services (deprecated package)

> [!WARNING]
> **Deprecated:** this package has been generalised into the vendor-agnostic
> [`storage/objstore`](../objstore/) backend, which serves AWS S3 (and any other
> S3-compatible store, GCS, the local filesystem, ...) through the
> [`gocloud.dev/blob`](https://gocloud.dev/howto/blob/) abstraction, with the
> same MySQL write coordination as before.
>
> `storage/aws` remains as a thin compatibility wrapper: the previous `Config`
> (including `SDKConfig` and `S3Options`) keeps working exactly as it did, and
> is used to construct the S3 client handed to the s3blob driver. New code
> should use `storage/objstore` directly; this package will be removed in a
> future release.

Migrating is mechanical:

```go
// Before:
driver, err := aws.New(ctx, aws.Config{
	Bucket: "my-bucket",
	DSN:    dsn,
})

// After:
import (
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/s3blob" // s3:// driver; auth via the AWS SDK chain.
)

bkt, err := blob.OpenBucket(ctx, "s3://my-bucket")
// handle err; the bucket must stay open for the lifetime of the storage.
driver, err := objstore.New(ctx, objstore.Config{
	Bucket: bkt,
	DSN:    dsn,
})
```

If you were using `SDKConfig`/`S3Options` to point at a non-AWS S3 service, the
equivalents are query parameters on the bucket URL (e.g.
`s3://my-bucket?endpoint=...&s3ForcePathStyle=true&region=...`), or open the
bucket with `s3blob.OpenBucket` and your own `*s3.Client` for full control.

For the design and operational documentation previously found here, see the
[`storage/objstore` README](../objstore/README.md), which describes the same
S3 + MySQL (e.g. Aurora) deployment.
