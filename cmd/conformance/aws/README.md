# Conformance testing binary for AWS (deprecated storage/aws API)

This binary is primarily intended to be used for checking Tessera conformance on AWS.

It is deliberately kept as the **unmodified pre-refactor personality**: it builds
against the deprecated [`storage/aws`](/storage/aws/) compatibility wrapper using the
original `aws.Config` API (`SDKConfig`, `S3Options`, `Bucket`, ...). Running it through
the same conformance hammer as [`cmd/conformance/objstore`](../objstore/) proves that
existing `storage/aws` users get unchanged behaviour from the wrapper. New deployments
should use `cmd/conformance/objstore` and `storage/objstore` directly.

If you want to try running it yourself, please see the instructions in the
[README file in the /deployment/live/aws/codelab directory](/deployment/live/aws/codelab).
