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

// Package bloburl derives gocloud.dev/blob bucket URLs from the S3-style
// command line flags shared by the objstore-backed binaries.
//
// The storage/objstore package itself takes an already-opened *blob.Bucket, so
// it is the binaries in /cmd which decide both which blob drivers are compiled
// in (via blank imports) and how a bucket URL is assembled from flags. This
// package holds the latter so the conformance and migration binaries derive
// URLs identically.
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
// Without an endpoint (real AWS), a plain s3://BUCKET URL is returned and the
// region and credentials are resolved by the AWS SDK's default chain. With a
// custom endpoint (e.g. MinIO, Ceph/RGW), the endpoint, region and path-style
// addressing are encoded as query parameters, and any non-empty static
// accessKeyID/secretAccessKey are exported into the AWS SDK credential chain
// via this process's environment so the s3blob driver picks them up - the
// driver deliberately does not accept credentials in the URL. Credential vars
// are only set when the corresponding values are non-empty so ambient AWS
// credentials are never clobbered, and AWS_REGION is only defaulted when it
// isn't already configured.
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
	if _, ok := os.LookupEnv("AWS_REGION"); !ok {
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
		"region":           {defaultRegion},
	}
	return "s3://" + bucket + "?" + q.Encode(), nil
}
