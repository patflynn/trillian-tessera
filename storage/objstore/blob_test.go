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

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"slices"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gocloud.dev/blob"
)

// blobURLs returns the set of in-process blob driver URLs to exercise. Both
// memblob and fileblob honor WriterOptions.IfNotExist, which the
// setObjectIfNoneMatch idempotency contract depends on, and both are fully
// hermetic (no network, no credentials).
func blobURLs(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"mem":  "mem://",
		"file": "file://" + t.TempDir(),
	}
}

// newTestBlobStore opens a blobStore against the given driver URL via
// newBlobStore (exercising the URL-based construction path).
func newTestBlobStore(t *testing.T, rawURL, bucketPrefix string) *blobStore {
	t.Helper()
	s, err := newBlobStore(context.Background(), rawURL, bucketPrefix)
	if err != nil {
		t.Fatalf("newBlobStore(%q): %v", rawURL, err)
	}
	t.Cleanup(func() { _ = s.bucket.Close() })
	return s
}

func TestBlobGetObjectRoundtrip(t *testing.T) {
	ctx := context.Background()
	for name, rawURL := range blobURLs(t) {
		t.Run(name, func(t *testing.T) {
			s := newTestBlobStore(t, rawURL, "")

			want := []byte("hello world")
			if err := s.setObject(ctx, "some/object", want, "text/plain", "no-cache"); err != nil {
				t.Fatalf("setObject: %v", err)
			}

			got, err := s.getObject(ctx, "some/object")
			if err != nil {
				t.Fatalf("getObject: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("getObject returned %q, want %q", got, want)
			}
		})
	}
}

func TestBlobGetObjectNotFound(t *testing.T) {
	ctx := context.Background()
	for name, rawURL := range blobURLs(t) {
		t.Run(name, func(t *testing.T) {
			s := newTestBlobStore(t, rawURL, "")

			_, err := s.getObject(ctx, "does/not/exist")
			if err == nil {
				t.Fatalf("getObject of missing key returned nil error, want not-found")
			}
			// Callers detect "not found" via errors.As against *types.NoSuchKey,
			// so the blob implementation must mirror that shape.
			var nske *types.NoSuchKey
			if !errors.As(err, &nske) {
				t.Errorf("getObject error = %v, want one wrapping *types.NoSuchKey", err)
			}
		})
	}
}

func TestBlobSetObjectIfNoneMatch(t *testing.T) {
	ctx := context.Background()
	for name, rawURL := range blobURLs(t) {
		t.Run(name, func(t *testing.T) {
			s := newTestBlobStore(t, rawURL, "")

			data := []byte("first write")

			// First write must succeed.
			if err := s.setObjectIfNoneMatch(ctx, "cond/object", data, "text/plain", "no-cache"); err != nil {
				t.Fatalf("first setObjectIfNoneMatch: %v", err)
			}

			// Second write with identical bytes is idempotent: it must return nil.
			if err := s.setObjectIfNoneMatch(ctx, "cond/object", data, "text/plain", "no-cache"); err != nil {
				t.Errorf("idempotent setObjectIfNoneMatch with identical data returned %v, want nil", err)
			}

			// Second write with different bytes must return a non-nil error.
			if err := s.setObjectIfNoneMatch(ctx, "cond/object", []byte("different"), "text/plain", "no-cache"); err == nil {
				t.Errorf("setObjectIfNoneMatch with differing data returned nil, want error")
			}

			// The originally written data must be unchanged.
			got, err := s.getObject(ctx, "cond/object")
			if err != nil {
				t.Fatalf("getObject: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("object content = %q, want unchanged %q", got, data)
			}
		})
	}
}

func TestBlobDeleteObjectsWithPrefix(t *testing.T) {
	ctx := context.Background()
	for name, rawURL := range blobURLs(t) {
		t.Run(name, func(t *testing.T) {
			s := newTestBlobStore(t, rawURL, "")

			toDelete := []string{"tile/0/000", "tile/0/001", "tile/1/000"}
			toKeep := []string{"checkpoint", "entries/000"}
			for _, k := range append(append([]string{}, toDelete...), toKeep...) {
				if err := s.setObject(ctx, k, []byte(k), "application/octet-stream", "no-cache"); err != nil {
					t.Fatalf("setObject(%q): %v", k, err)
				}
			}

			if err := s.deleteObjectsWithPrefix(ctx, "tile/"); err != nil {
				t.Fatalf("deleteObjectsWithPrefix: %v", err)
			}

			for _, k := range toDelete {
				if _, err := s.getObject(ctx, k); err == nil {
					t.Errorf("object %q still present after prefix delete", k)
				}
			}
			for _, k := range toKeep {
				if _, err := s.getObject(ctx, k); err != nil {
					t.Errorf("object %q outside prefix was removed: %v", k, err)
				}
			}
		})
	}
}

// TestBlobBucketPrefix verifies that the bucketPrefix is honored consistently
// across writes, reads and prefix deletes so that the on-disk layout matches the
// S3 and GCS implementations.
func TestBlobBucketPrefix(t *testing.T) {
	ctx := context.Background()
	const prefix = "logs/my-log"
	for name, rawURL := range blobURLs(t) {
		t.Run(name, func(t *testing.T) {
			s := newTestBlobStore(t, rawURL, prefix)

			if err := s.setObject(ctx, "tile/0/000", []byte("a"), "application/octet-stream", "no-cache"); err != nil {
				t.Fatalf("setObject: %v", err)
			}

			// The object must physically live under the bucket prefix.
			names := objectNamesInBlobBucket(t, s)
			want := []string{"logs/my-log/tile/0/000"}
			if !slices.Equal(names, want) {
				t.Errorf("stored object names = %v, want %v", names, want)
			}

			// Reads and prefix deletes go through the same prefixing and must work.
			if _, err := s.getObject(ctx, "tile/0/000"); err != nil {
				t.Errorf("getObject through prefix: %v", err)
			}
			if err := s.deleteObjectsWithPrefix(ctx, "tile/"); err != nil {
				t.Fatalf("deleteObjectsWithPrefix: %v", err)
			}
			if _, err := s.getObject(ctx, "tile/0/000"); err == nil {
				t.Errorf("object still present after prefix delete")
			}
		})
	}
}

// TestBlobOpenBucketParsesS3URL is a lightweight, hermetic check that an
// S3-style URL (the on-prem / MinIO / R2 path) parses and opens without
// contacting any network. It exercises the s3blob driver registration and URL
// option handling without requiring credentials or a live endpoint.
func TestBlobOpenBucketParsesS3URL(t *testing.T) {
	ctx := context.Background()
	// A syntactically valid S3 URL with a custom endpoint. Opening only
	// constructs the client; no request is made until an object is accessed.
	rawURL := "s3://example-bucket?" + url.Values{
		"endpoint":         {"http://127.0.0.1:9000"},
		"region":           {"us-east-1"},
		"s3ForcePathStyle": {"true"},
	}.Encode()
	s, err := newBlobStore(ctx, rawURL, "")
	if err != nil {
		t.Fatalf("newBlobStore(%q): %v", rawURL, err)
	}
	if err := s.bucket.Close(); err != nil {
		t.Errorf("bucket.Close: %v", err)
	}
}

// objectNamesInBlobBucket lists all object names in the store's bucket directly
// via the underlying *blob.Bucket.
func objectNamesInBlobBucket(t *testing.T, s *blobStore) []string {
	t.Helper()
	ctx := context.Background()
	it := s.bucket.List(&blob.ListOptions{})
	var names []string
	for {
		obj, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("listing objects: %v", err)
		}
		names = append(names, obj.Key)
	}
	sort.Strings(names)
	return names
}
