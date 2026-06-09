// Copyright 2025 The Tessera authors. All Rights Reserved.
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

package bloburl

import (
	"context"
	"net/url"
	"os"
	"testing"

	"gocloud.dev/blob"

	_ "gocloud.dev/blob/s3blob"
)

func TestDeriveS3PlainAWS(t *testing.T) {
	got, err := DeriveS3("my-bucket", "", "", "")
	if err != nil {
		t.Fatalf("DeriveS3: %v", err)
	}
	if want := "s3://my-bucket"; got != want {
		t.Errorf("DeriveS3 = %q, want %q", got, want)
	}
}

func TestDeriveS3EmptyBucket(t *testing.T) {
	if _, err := DeriveS3("", "", "", ""); err == nil {
		t.Errorf("DeriveS3 with empty bucket returned nil error, want error")
	}
}

func TestDeriveS3CustomEndpoint(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-2") // Pre-set so DeriveS3 must not clobber it.
	// Pre-set via t.Setenv so the values DeriveS3 writes are restored on cleanup.
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient")
	got, err := DeriveS3("tessera", "http://localhost:9000", "ak", "sk")
	if err != nil {
		t.Fatalf("DeriveS3: %v", err)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", got, err)
	}
	if u.Scheme != "s3" || u.Host != "tessera" {
		t.Errorf("derived URL %q, want s3://tessera?...", got)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"endpoint":         "http://localhost:9000",
		"s3ForcePathStyle": "true",
		"region":           defaultRegion,
	} {
		if g := q.Get(k); g != want {
			t.Errorf("query param %s = %q, want %q", k, g, want)
		}
	}

	// The static credentials must be exported into the AWS SDK chain, while a
	// pre-existing AWS_REGION must be left alone.
	for env, want := range map[string]string{
		"AWS_ACCESS_KEY_ID":     "ak",
		"AWS_SECRET_ACCESS_KEY": "sk",
		"AWS_REGION":            "eu-west-2",
	} {
		if g := os.Getenv(env); g != want {
			t.Errorf("%s = %q, want %q", env, g, want)
		}
	}
}

// TestDerivedURLOpens is a lightweight, hermetic check that a derived URL with
// a custom endpoint (the on-prem / MinIO / R2 path) parses and opens via the
// s3blob driver without contacting any network. Opening only constructs the
// client; no request is made until an object is accessed.
func TestDerivedURLOpens(t *testing.T) {
	t.Setenv("AWS_REGION", defaultRegion)
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient")
	rawURL, err := DeriveS3("example-bucket", "http://127.0.0.1:9000", "ak", "sk")
	if err != nil {
		t.Fatalf("DeriveS3: %v", err)
	}
	b, err := blob.OpenBucket(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("blob.OpenBucket(%q): %v", rawURL, err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("bucket.Close: %v", err)
	}
}
