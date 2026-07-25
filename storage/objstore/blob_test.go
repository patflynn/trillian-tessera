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
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"

	"gocloud.dev/blob"

	// The library registers no blob drivers; tests import the ones they use.
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/memblob"
)

// blobURLs returns the hermetic in-process blob driver URLs to exercise with
// tests that do not depend on create-if-absent being atomic. fileblob is
// included here and only here: it is fine for plain reads and writes, but its
// WriterOptions.IfNotExist is an unsynchronised stat-then-rename that lets
// concurrent writers overwrite one another (see the comment in blob.go), so it
// must not be used to claim conditional-write coverage.
func blobURLs(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"mem":  "mem://",
		"file": "file://" + t.TempDir(),
	}
}

// atomicBlobURLs returns the in-process blob driver URLs whose
// WriterOptions.IfNotExist really is an atomic create-if-absent, and which may
// therefore be used to exercise setObjectIfNoneMatch. memblob arbitrates every
// write under one bucket-wide lock, matching what gcsblob and s3blob get from
// the server; those two are covered by the conformance CI lanes rather than
// here, since they need a real backend.
func atomicBlobURLs(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"mem": "mem://",
	}
}

// newTestBlobStore opens a bucket for the given driver URL and wraps it in a blobStore.
func newTestBlobStore(t *testing.T, rawURL, bucketPrefix string) *blobStore {
	t.Helper()
	b, err := blob.OpenBucket(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("blob.OpenBucket(%q): %v", rawURL, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return newBlobStore(b, bucketPrefix)
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
			// Callers detect not-found via errors.Is(err, os.ErrNotExist).
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("getObject error = %v, want one wrapping os.ErrNotExist", err)
			}
		})
	}
}

func TestBlobSetObjectIfNoneMatch(t *testing.T) {
	ctx := context.Background()
	for name, rawURL := range atomicBlobURLs(t) {
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

// TestBlobSetObjectIfNoneMatchConcurrent is the test that actually decides
// whether a driver may back this storage. setObjectIfNoneMatch is the only
// thing stopping two integrators from writing different tiles to the same
// coordinate and forking the log, so "exactly one writer wins" has to hold
// under real contention, not just when the calls happen to be sequential.
//
// Every writer offers distinct bytes, so a driver that silently ignores the
// precondition cannot pass by accident: it will either report more than one
// success or leave behind bytes that no successful writer wrote. Pointing this
// at fileblob reports several simultaneous winners, which is why fileblob is
// not in atomicBlobURLs.
func TestBlobSetObjectIfNoneMatchConcurrent(t *testing.T) {
	ctx := context.Background()
	const (
		writers = 32
		key     = "cond/contended-object"
	)

	for name, rawURL := range atomicBlobURLs(t) {
		t.Run(name, func(t *testing.T) {
			s := newTestBlobStore(t, rawURL, "")

			payloads := make([][]byte, writers)
			for i := range payloads {
				payloads[i] = []byte(fmt.Sprintf("payload written by writer %02d", i))
			}

			errs := make([]error, writers)
			start := make(chan struct{})
			wg := sync.WaitGroup{}
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start // Line all the writers up so they collide.
					errs[i] = s.setObjectIfNoneMatch(ctx, key, payloads[i], "text/plain", "no-cache")
				}()
			}
			close(start)
			wg.Wait()

			var winners []int
			for i, err := range errs {
				if err == nil {
					winners = append(winners, i)
				}
			}
			if len(winners) != 1 {
				t.Fatalf("setObjectIfNoneMatch succeeded for %d of %d concurrent writers (%v), want exactly 1: the driver is not honouring IfNotExist atomically", len(winners), writers, winners)
			}

			// The bytes left in the store must be the winner's, not some other
			// writer's that overwrote them after losing.
			got, err := s.getObject(ctx, key)
			if err != nil {
				t.Fatalf("getObject: %v", err)
			}
			if want := payloads[winners[0]]; !bytes.Equal(got, want) {
				t.Errorf("stored content = %q, want the winning writer's %q", got, want)
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

// TestBlobBucketPrefix verifies bucketPrefix is honored consistently across
// writes, reads, and prefix deletes.
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
