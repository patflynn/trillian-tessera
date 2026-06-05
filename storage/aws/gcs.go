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

// This file provides a native Google Cloud Storage (GCS) implementation of the
// objStore interface (see aws.go). It allows the MySQL-coordinated backend in
// this package to serve its object storage from GCS using the native GCS client
// (cloud.google.com/go/storage) rather than via S3-interoperability.
//
// The native client is used because it exposes the primitives this backend
// relies on cleanly:
//   - atomic create-if-absent via Conditions{DoesNotExist: true}, and
//   - native Application Default Credentials / Workload Identity auth (no keys).
//
// This is "Option A" in docs/design/cloud-agnostic-storage.md.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"

	gcs "cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/go-cmp/cmp"
	"github.com/transparency-dev/tessera/internal/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// gcsStore knows how to store and retrieve objects from Google Cloud Storage
// using the native GCS client.
type gcsStore struct {
	bucket       string
	bucketPrefix string
	gcsClient    *gcs.Client
}

// newGCSStore creates a gcsStore backed by the native GCS client.
//
// The client is constructed with storage.NewClient, so it resolves credentials
// via Application Default Credentials / Workload Identity automatically - no
// static keys are required. The variadic option.ClientOption arguments are
// passed through to the client, primarily so tests can point at a fake server.
func newGCSStore(ctx context.Context, bucket, bucketPrefix string, opts ...option.ClientOption) (*gcsStore, error) {
	c, err := gcs.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	return &gcsStore{
		bucket:       bucket,
		bucketPrefix: bucketPrefix,
		gcsClient:    c,
	}, nil
}

// objectName applies the optional bucketPrefix to an object name, matching the
// layout produced by s3Storage.
func (s *gcsStore) objectName(obj string) string {
	if s.bucketPrefix != "" {
		return path.Join(s.bucketPrefix, obj)
	}
	return obj
}

// getObject returns the data of the specified object, or an error.
//
// If the object does not exist, the returned error wraps a *types.NoSuchKey so
// that callers (which use errors.As against that type) behave identically to the
// S3 implementation.
func (s *gcsStore) getObject(ctx context.Context, obj string) ([]byte, error) {
	objName := s.objectName(obj)

	r, err := s.gcsClient.Bucket(s.bucket).Object(objName).NewReader(ctx)
	if err != nil {
		if errors.Is(err, gcs.ErrObjectNotExist) {
			// Mirror the s3Storage not-found shape so higher levels can detect
			// "not found" without caring which object store is in use.
			return nil, fmt.Errorf("getObject: object %q not found in bucket %q: %w", objName, s.bucket, &types.NoSuchKey{})
		}
		return nil, fmt.Errorf("getObject: failed to create reader for object %q in bucket %q: %w", objName, s.bucket, err)
	}
	defer func() { _ = r.Close() }()

	d, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("getObject: failed to read %q: %v", objName, err)
	}
	return d, nil
}

// setObject stores the provided data in the specified object.
func (s *gcsStore) setObject(ctx context.Context, objName string, data []byte, contType string, cacheControl string) error {
	name := s.objectName(objName)

	// Use a cancellable context so a failed write can abort the upload and
	// release the writer's resources (cancelling the context is the documented
	// replacement for the deprecated Writer.CloseWithError).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := s.gcsClient.Bucket(s.bucket).Object(name).NewWriter(ctx)
	w.ContentType = contType
	w.CacheControl = cacheControl

	if _, err := w.Write(data); err != nil {
		// Abort the upload so the writer's resources are released.
		cancel()
		return fmt.Errorf("failed to write object %q to bucket %q: %w", name, s.bucket, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to close writer for object %q in bucket %q: %w", name, s.bucket, err)
	}
	return nil
}

// setObjectIfNoneMatch stores data in the specified object gated by a
// create-if-absent condition.
//
// The write only succeeds if no object exists under this key already. If an
// object already exists, an error is returned *unless* the currently stored data
// is bit-for-bit identical to the data to-be-written, in which case the write is
// treated as idempotently successful. This mirrors s3Storage.setObjectIfNoneMatch.
func (s *gcsStore) setObjectIfNoneMatch(ctx context.Context, objName string, data []byte, contType string, cacheControl string) error {
	name := s.objectName(objName)

	// Use a separate cancellable context for the writer so a failed write can
	// abort the upload and release the writer's resources (cancelling the
	// context is the documented replacement for the deprecated
	// Writer.CloseWithError). The original ctx is kept intact for the
	// follow-up getObject read in the precondition-failed path below.
	writeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := s.gcsClient.Bucket(s.bucket).Object(name).If(gcs.Conditions{DoesNotExist: true}).NewWriter(writeCtx)
	w.ContentType = contType
	w.CacheControl = cacheControl

	// The precondition failure may surface on either Write or Close, so collect
	// the first error from the write/close sequence. On a write error abort the
	// writer by cancelling its context so its resources are released; otherwise
	// the (possibly precondition) error surfaces from Close.
	writeErr := func() error {
		if _, err := w.Write(data); err != nil {
			cancel()
			return err
		}
		return w.Close()
	}()
	if writeErr != nil {
		// If we run into a precondition failure error, check that the object
		// which exists contains the same content that we want to write.
		// If so, we can consider this write to be idempotently successful.
		var gErr *googleapi.Error
		if errors.As(writeErr, &gErr) && gErr.Code == http.StatusPreconditionFailed {
			existing, err := s.getObject(ctx, objName)
			if err != nil {
				return fmt.Errorf("failed to fetch existing content for %q: %v", name, err)
			}
			if !bytes.Equal(existing, data) {
				slog.ErrorContext(ctx, "Resource non-idempotent write", slog.String("objname", name), slog.String("diff", cmp.Diff(existing, data)))
				return fmt.Errorf("precondition failed: resource content for %q differs from data to-be-written", name)
			}

			slog.DebugContext(ctx, "setObjectIfNoneMatch: identical resource already exists. Continuing", slog.String("objname", name))
			return nil
		}

		return fmt.Errorf("failed to write object %q to bucket %q: %w", name, s.bucket, writeErr)
	}
	return nil
}

// deleteObjectsWithPrefix removes any objects with the provided prefix from GCS.
//
// GCS has no S3-style batch delete, so objects are listed via the objects
// iterator and removed individually.
func (s *gcsStore) deleteObjectsWithPrefix(ctx context.Context, objPrefix string) error {
	return otel.TraceErr(ctx, "tessera.storage.aws.deleteObject", tracer, func(ctx context.Context, span trace.Span) error {
		prefix := s.objectName(objPrefix)
		span.SetAttributes(objectPathKey.String(prefix))

		bkt := s.gcsClient.Bucket(s.bucket)
		it := bkt.Objects(ctx, &gcs.Query{Prefix: prefix})
		for {
			attrs, err := it.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to list objects with prefix %q: %v", prefix, err)
			}
			slog.DebugContext(ctx, "Deleting object", slog.String("key", attrs.Name))
			if err := bkt.Object(attrs.Name).Delete(ctx); err != nil {
				return fmt.Errorf("failed to delete object %q: %v", attrs.Name, err)
			}
		}
		return nil
	})
}
