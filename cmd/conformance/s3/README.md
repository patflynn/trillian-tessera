# Conformance testing binary for S3-compatible object stores

This binary checks Tessera conformance using the `storage/objstore` backend over
any S3-compatible object store (Amazon S3, MinIO, …) coordinated by MySQL. It is
exercised against MinIO in the hermetic `minio-conformance` CI lane, and against
real AWS (S3 + Aurora) for AWS conformance.

If you want to try running it on AWS, please see the instructions in the
[README file in the /deployment/live/aws/codelab directory](/deployment/live/aws/codelab).
