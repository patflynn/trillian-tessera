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

// Package bloburl derives gocloud.dev/blob bucket URLs from the S3-style command
// line flags shared by the objstore-backed binaries, so the conformance and
// migration binaries derive URLs identically.
package bloburl

import (
	"fmt"
	"net/url"
	"os"
)

// defaultRegion is used for custom S3-compatible endpoints (e.g. MinIO) where
// no meaningful region exists but the AWS SDK requires one to be set.
const defaultRegion = "us-east-1"

// DeriveS3 returns the gocloud.dev/blob s3:// bucket URL for the given bucket
// and optional custom endpoint.
//
// Without an endpoint (real AWS), it returns a plain s3://BUCKET URL, leaving
// region and credentials to the AWS SDK's default chain. With a custom endpoint
// (e.g. MinIO), it encodes the endpoint, region, and path-style addressing as
// query parameters and exports any non-empty accessKeyID/secretAccessKey into
// the environment for the s3blob driver, which does not accept credentials in
// the URL. Only non-empty values are set, so ambient credentials aren't
// clobbered. The URL region matches AWS_REGION when set, else defaultRegion
// (also exported so URL and environment agree).
//
// Because it mutates process-global state via os.Setenv, call it exactly once,
// early in start-up, before spawning goroutines or child processes. It is not
// safe to call twice with different credentials, nor concurrently.
func DeriveS3(bucket, endpoint, accessKeyID, secretAccessKey string) (string, error) {
	if bucket == "" {
		return "", fmt.Errorf("bucket must be provided")
	}
	if endpoint == "" {
		return "s3://" + bucket, nil
	}

	envVars := make(map[string]string)
	if accessKeyID != "" {
		envVars["AWS_ACCESS_KEY_ID"] = accessKeyID
	}
	if secretAccessKey != "" {
		envVars["AWS_SECRET_ACCESS_KEY"] = secretAccessKey
	}
	// Honour AWS_REGION if set so the URL region matches what SigV4 signs for;
	// otherwise use defaultRegion and export it so URL and environment agree.
	region := defaultRegion
	if r, ok := os.LookupEnv("AWS_REGION"); ok && r != "" {
		region = r
	} else {
		envVars["AWS_REGION"] = defaultRegion
	}
	for k, v := range envVars {
		if err := os.Setenv(k, v); err != nil {
			return "", fmt.Errorf("failed to set %s: %w", k, err)
		}
	}

	q := url.Values{
		"endpoint":         {endpoint},
		"s3ForcePathStyle": {"true"},
		"region":           {region},
	}
	return "s3://" + bucket + "?" + q.Encode(), nil
}
