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

package objstore

// This file implements the objStore interface (see objstore.go) on top of the
// gocloud.dev/blob abstraction, serving GCS, any S3-compatible store, the local
// filesystem, or an in-memory bucket from a single implementation.
//
// The caller opens and owns the *blob.Bucket (see Config.Bucket). Its driver
// must support WriterOptions.IfNotExist, which backs the atomic create-if-absent
// writes that make concurrent integrators safe.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"

	"github.com/google/go-cmp/cmp"
	"github.com/transparency-dev/tessera/internal/otel"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

// blobStore stores and retrieves objects via the gocloud.dev/blob abstraction.
type blobStore struct {
	bucket       *blob.Bucket
	bucketPrefix string
}

// newBlobStore returns a blobStore wrapping the provided open bucket.
// The caller retains ownership and must close it when done.
func newBlobStore(bucket *blob.Bucket, bucketPrefix string) *blobStore {
	return &blobStore{
		bucket:       bucket,
		bucketPrefix: bucketPrefix,
	}
}

// objectName applies the optional bucketPrefix to an object name.
func (s *blobStore) objectName(obj string) string {
	if s.bucketPrefix != "" {
		return path.Join(s.bucketPrefix, obj)
	}
	return obj
}

// getObject returns the object's data. A missing object yields an error wrapping
// os.ErrNotExist so callers can detect "not found" via errors.Is.
func (s *blobStore) getObject(ctx context.Context, obj string) ([]byte, error) {
	objName := s.objectName(obj)

	d, err := s.bucket.ReadAll(ctx, objName)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, fmt.Errorf("getObject: object %q not found: %w", objName, os.ErrNotExist)
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

// setObjectIfNoneMatch writes the object only if the key does not already exist.
// If it does exist, this errors unless the stored data is identical, in which
// case the write is treated as idempotently successful.
func (s *blobStore) setObjectIfNoneMatch(ctx context.Context, objName string, data []byte, contType string, cacheControl string) error {
	name := s.objectName(objName)

	err := s.bucket.WriteAll(ctx, name, data, &blob.WriterOptions{
		IfNotExist:   true,
		ContentType:  contType,
		CacheControl: cacheControl,
	})
	if err != nil {
		// On precondition failure, treat as success if the existing object
		// already holds identical content.
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

// deleteObjectsWithPrefix removes all objects with the provided prefix, listing
// and deleting them individually to avoid provider-specific batch-delete semantics.
func (s *blobStore) deleteObjectsWithPrefix(ctx context.Context, objPrefix string) error {
	return otel.TraceErr(ctx, "tessera.storage.objstore.deleteObject", tracer, func(ctx context.Context, span trace.Span) error {
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
