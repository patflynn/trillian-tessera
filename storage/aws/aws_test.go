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
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	aaws "github.com/aws/aws-sdk-go-v2/aws"
)

// TestNewWithCustomSDKConfig exercises the legacy construction path with an
// explicit SDKConfig and S3Options, as users of non-AWS S3 services (e.g.
// MinIO) configure it. Construction is hermetic: no request is made until the
// log is used.
func TestNewWithCustomSDKConfig(t *testing.T) {
	ctx := context.Background()

	sdkConfig := aaws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("access", "secret", ""),
	}
	s3Options := func(o *s3.Options) {
		o.BaseEndpoint = aaws.String("http://127.0.0.1:9000")
		o.UsePathStyle = true
	}

	driver, err := New(ctx, Config{
		SDKConfig: &sdkConfig,
		S3Options: s3Options,
		Bucket:    "test-bucket",
		DSN:       "user:pass@tcp(127.0.0.1:3306)/tessera",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if driver == nil {
		t.Fatal("New returned nil driver")
	}
	// The driver must be the objstore Storage the wrapper delegates to.
	if _, ok := driver.(*Storage); !ok {
		t.Errorf("New returned %T, want *Storage", driver)
	}
}

// TestNewRequiresBucket verifies that a missing bucket name is reported at
// construction time rather than first use.
func TestNewRequiresBucket(t *testing.T) {
	sdkConfig := aaws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("access", "secret", ""),
	}
	if _, err := New(context.Background(), Config{SDKConfig: &sdkConfig}); err == nil {
		t.Error("New with empty Bucket returned nil error, want error")
	}
}
