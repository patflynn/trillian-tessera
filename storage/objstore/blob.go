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
// gocloud.dev/blob abstraction, serving GCS or any S3-compatible store from a
// single implementation.
//
// The caller opens and owns the *blob.Bucket (see Config.Bucket). Its driver
// must implement WriterOptions.IfNotExist as a genuinely atomic
// create-if-absent, which is what keeps concurrent integrators from forking the
// log. Only gcsblob (via the x-goog-if-generation-match precondition) and
// s3blob (via If-None-Match) do so: both push the check to the server, which
// arbitrates it. memblob is atomic within a process and is fine for tests.
//
// fileblob is NOT safe here: it implements IfNotExist as an os.Stat followed by
// an os.Rename, guarded by a mutex that is allocated per writer and therefore
// serialises nothing, and POSIX rename silently replaces the destination. Two
// concurrent writers both "succeed" and one payload is lost, with no error
// returned to either. Do not use it with more than one writer.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"

	gcs "cloud.google.com/go/storage"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/google/go-cmp/cmp"
	"github.com/transparency-dev/tessera/internal/otel"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"google.golang.org/api/googleapi"
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
		if isNotFound(err) {
			return nil, fmt.Errorf("getObject: object %q not found: %w", objName, os.ErrNotExist)
		}
		return nil, fmt.Errorf("getObject: failed to read object %q: %w", objName, err)
	}
	return d, nil
}

// isNotFound reports whether err means "this object does not exist", as opposed
// to any other failure.
//
// Getting this wrong is not a cosmetic problem. Callers convert a true result
// into os.ErrNotExist, and the layers above treat that as authoritative:
// getTiles returns a nil tile, and ReadCheckpoint reports an empty log and
// triggers a forced integration. Misreporting a transient 503 or a
// misconfigured 403 as "absent" therefore corrupts the log's view of itself.
//
// gcerrors.Code is not a sound classifier for this. gocloud's own drivers fold
// unrelated failures into gcerrors.NotFound:
//
//   - s3blob checks strings.Contains(operationError.Error(), "301") before it
//     looks at the real API error code, and the rendered OperationError embeds
//     the request ID. Any error whose request ID happens to contain "301" — the
//     MinIO IDs are monotonic hex timestamps, so these arrive in contiguous
//     runs — is reclassified as NoSuchBucket, hence NotFound.
//   - s3blob maps NoSuchBucket to NotFound, so a misnamed bucket looks like an
//     empty log rather than a configuration error.
//   - gcsblob maps HTTP 403 to NotFound outright.
//
// So we require corroboration from a typed, provider-specific error before
// believing it, and only consult gcerrors when the error carries no such type.
// That does reintroduce provider-specific knowledge — and the GCS and S3
// SDKs — into the otherwise portable layer, and it means a driver we have no
// special case for gets gocloud's classification unchanged. That is the
// deliberate price of not regressing error semantics relative to the typed
// checks in the storage/aws implementation this replaced; a wrong "not found"
// is far more damaging here than a wrong "some other error".
func isNotFound(err error) bool {
	if err == nil {
		return false
	}

	// GCS returns a sentinel for a genuinely missing object.
	if errors.Is(err, gcs.ErrObjectNotExist) {
		return true
	}
	// Any other GCS API error carries its own status; trust that over gcerrors,
	// which would turn a 403 into NotFound.
	var gapiErr *googleapi.Error
	if errors.As(err, &gapiErr) {
		return gapiErr.Code == http.StatusNotFound
	}

	// S3: prefer the modelled error types.
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	// Then the wire error code, for the responses the SDK does not model.
	// NoSuchBucket is deliberately absent: a missing bucket is a configuration
	// error, not an absent object.
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() != "" {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		default:
			return false
		}
	}
	// Then the HTTP status, for responses with no usable error code at all.
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode() == http.StatusNotFound
	}

	// No typed provider error: drivers such as memblob signal only through
	// gocloud's code, and for those it is the best evidence available.
	return gcerrors.Code(err) == gcerrors.NotFound
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
//
// This is the primitive that keeps concurrent integrators from forking the log,
// so it must not depend on classifying the write error. It deliberately does
// not: on any failure it reads the object back and lets the stored bytes decide.
// Tessera only ever writes a given tile or bundle with one value, so finding our
// own bytes already there means some other integrator did our work and the write
// is idempotently complete, whatever the driver called the error. Anything else
// — different bytes, or a readback that fails — is reported, with the original
// write error preserved for diagnosis.
func (s *blobStore) setObjectIfNoneMatch(ctx context.Context, objName string, data []byte, contType string, cacheControl string) error {
	name := s.objectName(objName)

	err := s.bucket.WriteAll(ctx, name, data, &blob.WriterOptions{
		IfNotExist:   true,
		ContentType:  contType,
		CacheControl: cacheControl,
	})
	if err == nil {
		return nil
	}

	existing, gErr := s.getObject(ctx, objName)
	if gErr != nil {
		return fmt.Errorf("failed to write object %q: %w (readback to check for an idempotent rewrite also failed: %v)", name, err, gErr)
	}
	if !bytes.Equal(existing, data) {
		slog.ErrorContext(ctx, "Resource non-idempotent write", slog.String("objname", name), slog.String("diff", cmp.Diff(existing, data)))
		return fmt.Errorf("failed to write object %q: %w (stored content differs from the data to-be-written)", name, err)
	}

	slog.DebugContext(ctx, "setObjectIfNoneMatch: identical resource already exists. Continuing", slog.String("objname", name))
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
