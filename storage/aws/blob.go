// Copyright 2024 The Tessera authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

// This file provides a portable, provider-agnostic implementation of the
// objStore interface (see aws.go) backed by the Go CDK blob abstraction
// (gocloud.dev/blob). A single blobStore can serve object storage from GCS, any
// S3-compatible store (AWS S3, MinIO, Ceph/RGW, Cloudflare R2), the local
// filesystem, or an in-memory bucket, selected purely by the bucket URL.
//
// The Go CDK is used because it exposes the primitives this backend relies on
// uniformly across drivers:
//   - atomic create-if-absent via WriterOptions.IfNotExist, and
//   - each driver's native credential chain (gcsblob -> ADC/Workload Identity;
//     s3blob -> AWS SDK chain incl. STS/HMAC) - no keys in code.
//
// This is "Option B" in docs/design/cloud-agnostic-storage.md. It is purely
// additive: the s3Storage and gcsStore implementations are unchanged and remain
// the default.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/go-cmp/cmp"
	"github.com/transparency-dev/tessera/internal/otel"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	// Register the blob drivers we support. Each registers itself for its URL
	// scheme (gs://, s3://, file://, mem://) on import.
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
)

// blobStore knows how to store and retrieve objects from any storage provider
// supported by the Go CDK blob abstraction (gocloud.dev/blob).
type blobStore struct {
	bucket       *blob.Bucket
	bucketPrefix string
}

// newBlobStore opens a *blob.Bucket from the provided provider-scoped URL and
// returns a blobStore wrapping it.
//
// The URL selects both the driver and the bucket, e.g:
//   - gs://my-bucket
//   - s3://my-bucket?endpoint=...&s3ForcePathStyle=true&region=...
//   - file:///var/lib/tessera
//   - mem://
//
// Authentication is resolved by each driver's native chain (gcsblob via ADC /
// Workload Identity; s3blob via the AWS SDK chain including STS/HMAC), so no
// static keys are required or accepted here.
func newBlobStore(ctx context.Context, url, bucketPrefix string) (*blobStore, error) {
	b, err := blob.OpenBucket(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to open blob bucket %q: %w", url, err)
	}
	return &blobStore{
		bucket:       b,
		bucketPrefix: bucketPrefix,
	}, nil
}

// objectName applies the optional bucketPrefix to an object name, matching the
// layout produced by s3Storage and gcsStore.
func (s *blobStore) objectName(obj string) string {
	if s.bucketPrefix != "" {
		return path.Join(s.bucketPrefix, obj)
	}
	return obj
}

// getObject returns the data of the specified object, or an error.
//
// If the object does not exist, the returned error wraps a *types.NoSuchKey so
// that callers (which use errors.As against that type) behave identically to the
// S3 and GCS implementations.
func (s *blobStore) getObject(ctx context.Context, obj string) ([]byte, error) {
	objName := s.objectName(obj)

	d, err := s.bucket.ReadAll(ctx, objName)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			// Mirror the s3Storage/gcsStore not-found shape so higher levels can
			// detect "not found" without caring which object store is in use.
			return nil, fmt.Errorf("getObject: object %q not found: %w", objName, &types.NoSuchKey{})
		}
		return nil, fmt.Errorf("getObject: failed to read object %q: %w", objName, err)
	}
	return d, nil
}

// setObject stores the provided data in the specified object.
func (s *blobStore) setObject(ctx context.Context, objName string, data []byte, contType string, cacheControl string) error {
	name := s.objectName(objName)

	if err := s.bucket.WriteAll(ctx, name, data, &blob.WriterOptions{
		ContentType:  contType,
		CacheControl: cacheControl,
	}); err != nil {
		return fmt.Errorf("failed to write object %q: %w", name, err)
	}
	return nil
}

// setObjectIfNoneMatch stores data in the specified object gated by a
// create-if-absent condition.
//
// The write only succeeds if no object exists under this key already. If an
// object already exists, an error is returned *unless* the currently stored data
// is bit-for-bit identical to the data to-be-written, in which case the write is
// treated as idempotently successful. This mirrors s3Storage/gcsStore.
func (s *blobStore) setObjectIfNoneMatch(ctx context.Context, objName string, data []byte, contType string, cacheControl string) error {
	name := s.objectName(objName)

	err := s.bucket.WriteAll(ctx, name, data, &blob.WriterOptions{
		IfNotExist:   true,
		ContentType:  contType,
		CacheControl: cacheControl,
	})
	if err != nil {
		// If we run into a precondition failure error, check that the object
		// which exists contains the same content that we want to write.
		// If so, we can consider this write to be idempotently successful.
		if gcerrors.Code(err) == gcerrors.FailedPrecondition {
			existing, gErr := s.getObject(ctx, objName)
			if gErr != nil {
				return fmt.Errorf("failed to fetch existing content for %q: %w", name, gErr)
			}
			if !bytes.Equal(existing, data) {
				slog.ErrorContext(ctx, "Resource non-idempotent write", slog.String("objname", name), slog.String("diff", cmp.Diff(existing, data)))
				return fmt.Errorf("precondition failed: resource content for %q differs from data to-be-written", name)
			}

			slog.DebugContext(ctx, "setObjectIfNoneMatch: identical resource already exists. Continuing", slog.String("objname", name))
			return nil
		}

		return fmt.Errorf("failed to write object %q: %w", name, err)
	}
	return nil
}

// deleteObjectsWithPrefix removes any objects with the provided prefix.
//
// Objects are listed via the blob iterator and removed individually, sidestepping
// any provider-specific batch-delete semantics (mirroring gcsStore).
func (s *blobStore) deleteObjectsWithPrefix(ctx context.Context, objPrefix string) error {
	return otel.TraceErr(ctx, "tessera.storage.aws.deleteObject", tracer, func(ctx context.Context, span trace.Span) error {
		prefix := s.objectName(objPrefix)
		span.SetAttributes(objectPathKey.String(prefix))

		it := s.bucket.List(&blob.ListOptions{Prefix: prefix})
		for {
			obj, err := it.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to list objects with prefix %q: %w", prefix, err)
			}
			slog.DebugContext(ctx, "Deleting object", slog.String("key", obj.Key))
			if err := s.bucket.Delete(ctx, obj.Key); err != nil {
				return fmt.Errorf("failed to delete object %q: %w", obj.Key, err)
			}
		}
		return nil
	})
}
