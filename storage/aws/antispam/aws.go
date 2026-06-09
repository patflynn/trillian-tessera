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

// Package aws contains an AWS-based antispam implementation for Tessera.
//
// Deprecated: this implementation was never AWS-specific (it is plain MySQL),
// and now lives in
// [github.com/transparency-dev/tessera/storage/objstore/antispam]. This
// package remains as a compatibility wrapper re-exporting that implementation;
// new code should use it directly. This package will be removed in a future
// release.
package aws

import (
	"context"

	"github.com/transparency-dev/tessera/storage/objstore/antispam"
)

const (
	// DefaultMaxBatchSize is the default value for AntispamOpts.MaxBatchSize.
	//
	// Deprecated: use antispam.DefaultMaxBatchSize.
	DefaultMaxBatchSize = antispam.DefaultMaxBatchSize
	// DefaultPushbackThreshold is the default value for AntispamOpts.PushbackThreshold.
	//
	// Deprecated: use antispam.DefaultPushbackThreshold.
	DefaultPushbackThreshold = antispam.DefaultPushbackThreshold

	// SchemaCompatibilityVersion represents the expected version (e.g. layout & serialisation) of stored data.
	//
	// Deprecated: use antispam.SchemaCompatibilityVersion.
	SchemaCompatibilityVersion = antispam.SchemaCompatibilityVersion
)

// AntispamOpts allows configuration of some tunable options.
//
// Deprecated: use antispam.AntispamOpts.
type AntispamOpts = antispam.AntispamOpts

// AntispamStorage provides a MySQL-backed duplicate detection mechanism.
//
// Deprecated: use antispam.AntispamStorage.
type AntispamStorage = antispam.AntispamStorage

// NewAntispam returns an antispam driver which uses a MySQL table to maintain a mapping of
// previously seen entries and their assigned indices.
//
// Note that the storage for this mapping is entirely separate and unconnected to the storage used for
// maintaining the Merkle tree.
//
// This functionality is experimental!
//
// Deprecated: use antispam.NewAntispam.
func NewAntispam(ctx context.Context, dsn string, opts AntispamOpts) (*AntispamStorage, error) {
	return antispam.NewAntispam(ctx, dsn, opts)
}
