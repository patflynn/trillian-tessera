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

// Package aws contains an AWS-based storage implementation for Tessera.
//
// Deprecated: this backend has been generalised into the vendor-agnostic
// [github.com/transparency-dev/tessera/storage/objstore] package, which serves
// AWS S3 (and any other S3-compatible store, GCS, the local filesystem, ...)
// through the gocloud.dev/blob abstraction, with the same MySQL write
// coordination as before.
//
// This package remains as a compatibility wrapper: it preserves the previous
// API and behaviour by constructing an S3 client from the provided AWS SDK
// configuration exactly as it used to, and handing it to storage/objstore via
// the s3blob driver. Existing code continues to work unchanged, but new code
// should use storage/objstore directly. This package will be removed in a
// future release.
package aws

import (
	"context"
	"fmt"
	"net/http"

	"log/slog"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/transparency-dev/tessera"
	"github.com/transparency-dev/tessera/storage/objstore"
	"gocloud.dev/blob/s3blob"

	aaws "github.com/aws/aws-sdk-go-v2/aws"
)

// DefaultPushbackMaxOutstanding is the default value used if no override is
// provided via tessera options.
//
// Deprecated: use objstore.DefaultPushbackMaxOutstanding.
const DefaultPushbackMaxOutstanding = objstore.DefaultPushbackMaxOutstanding

// Storage is an AWS based storage implementation for Tessera.
//
// Deprecated: use objstore.Storage.
type Storage = objstore.Storage

// Appender is an implementation of the Tessera appender lifecycle contract.
//
// Deprecated: use objstore.Appender.
type Appender = objstore.Appender

// MigrationStorage implements the tessera.MigrationStorage lifecycle contract.
//
// Deprecated: use objstore.MigrationStorage.
type MigrationStorage = objstore.MigrationStorage

// Config holds AWS project and resource configuration for a storage instance.
//
// Deprecated: use objstore.Config, which takes an already-opened
// gocloud.dev/blob bucket (e.g. from s3blob.OpenBucket or blob.OpenBucket with
// the s3blob driver imported) in place of the AWS SDK configuration here.
type Config struct {
	// SDKConfig is an optional AWS config to use when configuring service clients, e.g. to
	// use non-AWS S3 or MySQL services.
	//
	// If nil, the value from config.LoadDefaultConfig() will be used - this is the only
	// supported configuration.
	SDKConfig *aaws.Config
	// S3Options is an optional function which can be used to configure the S3 library.
	// This is primarily useful when configuring the use of non-AWS S3 or MySQL services.
	//
	// If nil, the default options will be used - this is the only supported configuration.
	S3Options func(*s3.Options)
	// Bucket is the name of the S3 bucket to use for storing log state.
	Bucket string
	// BucketPrefix is an optional prefix to prepend to all log resource paths.
	// This can be used e.g. to store multiple logs in the same bucket.
	BucketPrefix string
	// DSN is the DSN of the MySQL instance to use.
	DSN string
	// Maximum connections to the MySQL database.
	MaxOpenConns int
	// Maximum idle database connections in the connection pool.
	MaxIdleConns int

	// HTTPClient will be used for other HTTP requests. If unset, Tessera will use the net/http DefaultClient.
	HTTPClient *http.Client
}

// New creates a new instance of the AWS based Storage.
//
// Storage instances created via this c'tor will participate in integrating newly sequenced entries into the log
// and periodically publishing a new checkpoint which commits to the state of the tree.
//
// Deprecated: use objstore.New with an s3blob-backed bucket. This wrapper
// builds the S3 client from cfg exactly as previous releases did and delegates
// to objstore.New.
func New(ctx context.Context, cfg Config) (tessera.Driver, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("bucket name must be provided")
	}
	if cfg.SDKConfig == nil {
		// We're running on AWS so use the SDK's default config which will handle credentials etc.
		sdkConfig, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load default AWS configuration: %v", err)
		}
		cfg.SDKConfig = &sdkConfig
	} else {
		slog.InfoContext(ctx, "Running in non-AWS mode - see storage/aws/README.md for more details. Here be dragons!")
	}
	optFns := []func(*s3.Options){}
	if cfg.S3Options != nil {
		optFns = append(optFns, cfg.S3Options)
	}
	client := s3.NewFromConfig(*cfg.SDKConfig, optFns...)

	bkt, err := s3blob.OpenBucket(ctx, client, cfg.Bucket, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open S3 bucket %q: %w", cfg.Bucket, err)
	}

	return objstore.New(ctx, objstore.Config{
		Bucket:       bkt,
		BucketPrefix: cfg.BucketPrefix,
		DSN:          cfg.DSN,
		MaxOpenConns: cfg.MaxOpenConns,
		MaxIdleConns: cfg.MaxIdleConns,
		HTTPClient:   cfg.HTTPClient,
	})
}
