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

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/fsouza/fake-gcs-server/fakestorage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const testGCSBucket = "test-bucket"

// newTestGCSStore starts an in-process fake GCS server and returns a gcsStore
// wired to it via newGCSStore (exercising the option.ClientOption injection
// path). The test is fully hermetic: no network access and no GCP credentials
// are required.
func newTestGCSStore(t *testing.T, bucketPrefix string) *gcsStore {
	t.Helper()

	srv, err := fakestorage.NewServerWithOptions(fakestorage.Options{})
	if err != nil {
		t.Fatalf("failed to start fake GCS server: %v", err)
	}
	t.Cleanup(srv.Stop)
	srv.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: testGCSBucket})

	store, err := newGCSStore(context.Background(), testGCSBucket, bucketPrefix, option.WithHTTPClient(srv.HTTPClient()))
	if err != nil {
		t.Fatalf("newGCSStore: %v", err)
	}
	return store
}

func TestGCSGetObjectRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := newTestGCSStore(t, "")

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
}

func TestGCSGetObjectNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestGCSStore(t, "")

	_, err := s.getObject(ctx, "does/not/exist")
	if err == nil {
		t.Fatalf("getObject of missing key returned nil error, want not-found")
	}
	// Callers detect "not found" via errors.As against *types.NoSuchKey, so the
	// GCS implementation must mirror that shape.
	var nske *types.NoSuchKey
	if !errors.As(err, &nske) {
		t.Errorf("getObject error = %v, want one wrapping *types.NoSuchKey", err)
	}
}

func TestGCSSetObjectIfNoneMatch(t *testing.T) {
	ctx := context.Background()
	s := newTestGCSStore(t, "")

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
}

func TestGCSDeleteObjectsWithPrefix(t *testing.T) {
	ctx := context.Background()
	s := newTestGCSStore(t, "")

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
}

// TestGCSBucketPrefix verifies that the bucketPrefix is honored consistently
// across writes, reads and prefix deletes so that the on-disk layout matches the
// S3 implementation.
func TestGCSBucketPrefix(t *testing.T) {
	ctx := context.Background()
	const prefix = "logs/my-log"
	s := newTestGCSStore(t, prefix)

	if err := s.setObject(ctx, "tile/0/000", []byte("a"), "application/octet-stream", "no-cache"); err != nil {
		t.Fatalf("setObject: %v", err)
	}

	// The object must physically live under the bucket prefix.
	names := objectNamesInBucket(t, s, prefix)
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
}

// objectNamesInBucket lists all object names in the store's bucket directly via
// the underlying GCS client.
func objectNamesInBucket(t *testing.T, s *gcsStore, prefix string) []string {
	t.Helper()
	ctx := context.Background()
	it := s.gcsClient.Bucket(s.bucket).Objects(ctx, nil)
	var names []string
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatalf("listing objects: %v", err)
		}
		names = append(names, attrs.Name)
	}
	sort.Strings(names)
	return names
}
