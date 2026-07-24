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

// objstore-migrate migrates a tlog-tiles compliant log into a Tessera log
// backed by any gocloud.dev/blob object store with MySQL coordination.
//
// The object store is selected by a single bucket URL via --blob_url (e.g.
// gs://BUCKET, s3://BUCKET?endpoint=...&region=..., file:///path). When
// --blob_url is unset, an s3:// URL is derived from --bucket and the
// --s3_endpoint/--s3_access_key/--s3_secret flags.
//
// Coordination uses MySQL: the DSN is either --mysql_uri verbatim or assembled
// from the --db_host/--db_port/--db_user/--db_password/--db_name flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"

	"log/slog"

	"github.com/go-sql-driver/mysql"
	"github.com/transparency-dev/tessera"
	"github.com/transparency-dev/tessera/client"
	"github.com/transparency-dev/tessera/cmd/internal/bloburl"
	"github.com/transparency-dev/tessera/internal/parse"
	"github.com/transparency-dev/tessera/storage/objstore"
	"gocloud.dev/blob"

	// Register the blob drivers this binary supports; each binds its URL scheme
	// (gs://, s3://, file://) on import. The library is driver-agnostic.
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
)

var (
	bucket            = flag.String("bucket", "", "Bucket to use for storing log")
	blobURL           = flag.String("blob_url", "", "Optional explicit provider-scoped bucket URL for the object store, e.g. gs://BUCKET, s3://BUCKET?endpoint=...&s3ForcePathStyle=true&region=..., file:///path. If unset, a URL is derived from --bucket and the --s3_* flags. Auth is each driver's native credential chain.")
	mysqlURI          = flag.String("mysql_uri", "", "Full MySQL DSN used for write coordination, e.g. 'user@tcp(127.0.0.1:3306)/db?parseTime=true'. If set, it is used directly; otherwise the DSN is built from the --db_* flags.")
	dbName            = flag.String("db_name", "", "AuroraDB name")
	dbHost            = flag.String("db_host", "", "AuroraDB host")
	dbPort            = flag.Int("db_port", 3306, "AuroraDB port")
	dbUser            = flag.String("db_user", "", "AuroraDB user")
	dbPassword        = flag.String("db_password", "", "AuroraDB user")
	dbMaxConns        = flag.Int("db_max_conns", 0, "Maximum connections to the database, defaults to 0, i.e unlimited")
	dbMaxIdle         = flag.Int("db_max_idle_conns", 2, "Maximum idle database connections in the connection pool, defaults to 2")
	s3Endpoint        = flag.String("s3_endpoint", "", "Endpoint for custom non-AWS S3 service")
	s3AccessKeyID     = flag.String("s3_access_key", "", "Access key ID for custom non-AWS S3 service")
	s3SecretAccessKey = flag.String("s3_secret", "", "Secret access key for custom non-AWS S3 service")

	sourceURL  = flag.String("source_url", "", "Base URL for the source log.")
	numWorkers = flag.Uint("num_workers", 30, "Number of migration worker goroutines.")
	slogLevel  = flag.Int("slog_level", 0, "The cut-off threshold for structured logging. Default is 0 (INFO). See https://pkg.go.dev/log/slog#Level for other levels.")
)

func main() {
	flag.Parse()
	ctx := context.Background()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(*slogLevel)})))

	if *sourceURL == "" {
		slog.ErrorContext(ctx, "Missing parameter: --source_url")
		os.Exit(1)
	}
	srcURL, err := url.Parse(*sourceURL)
	if err != nil {
		slog.ErrorContext(ctx, "Invalid --source_url", slog.String("param", *sourceURL), slog.Any("error", err))
		os.Exit(1)
	}
	src, err := client.NewHTTPFetcher(srcURL, nil)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create HTTP fetcher", slog.Any("error", err))
		os.Exit(1)
	}
	sourceCP, err := src.ReadCheckpoint(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "fetch initial source checkpoint", slog.Any("error", err))
		os.Exit(1)
	}
	// TODO(mhutchinson): parse this safely.
	_, sourceSize, sourceRoot, err := parse.CheckpointUnsafe(sourceCP)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to parse checkpoint", slog.Any("error", err))
		os.Exit(1)
	}

	// Create our Tessera storage backend:
	cfg := storageConfigFromFlags(ctx)
	defer func() {
		if err := cfg.Bucket.Close(); err != nil {
			slog.WarnContext(ctx, "Failed to close blob bucket", slog.Any("error", err))
		}
	}()
	driver, err := objstore.New(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create new storage", slog.Any("error", err))
		os.Exit(1)
	}
	opts := tessera.NewMigrationOptions()

	m, err := tessera.NewMigrationTarget(ctx, driver, opts)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create MigrationTarget", slog.Any("error", err))
		os.Exit(1)
	}

	slog.InfoContext(ctx, "Starting Migrate() with workers=, sourceSize=, migrating from", slog.Any("numworkers", *numWorkers), slog.Uint64("sourcesize", sourceSize), slog.String("sourceurl", *sourceURL))
	if err := m.Migrate(context.Background(), *numWorkers, sourceSize, sourceRoot, src.ReadEntryBundle); err != nil {
		slog.ErrorContext(ctx, "Migrate failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// storageConfigFromFlags returns an objstore.Config populated from flags. The
// caller must Close the returned Config's *blob.Bucket when done.
func storageConfigFromFlags(ctx context.Context) objstore.Config {
	if *blobURL == "" && *bucket == "" {
		slog.ErrorContext(ctx, "either --bucket or --blob_url must be set")
		os.Exit(1)
	}

	u := blobURLFromFlags(ctx)
	bkt, err := blob.OpenBucket(ctx, u)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to open blob bucket", slog.String("url", u), slog.Any("error", err))
		os.Exit(1)
	}

	return objstore.Config{
		Bucket:       bkt,
		DSN:          dsnFromFlags(ctx),
		MaxOpenConns: *dbMaxConns,
		MaxIdleConns: *dbMaxIdle,
	}
}

// dsnFromFlags returns the MySQL DSN used for write coordination.
//
// If --mysql_uri is set it is used verbatim (e.g. a Cloud SQL proxy connection).
// Otherwise the DSN is assembled from the discrete --db_* flags.
func dsnFromFlags(ctx context.Context) string {
	if *mysqlURI != "" {
		return *mysqlURI
	}

	if *dbName == "" {
		slog.ErrorContext(ctx, "--db_name must be set (or pass --mysql_uri)")
		os.Exit(1)
	}
	if *dbHost == "" {
		slog.ErrorContext(ctx, "--db_host must be set (or pass --mysql_uri)")
		os.Exit(1)
	}
	if *dbPort == 0 {
		slog.ErrorContext(ctx, "--db_port must be set (or pass --mysql_uri)")
		os.Exit(1)
	}
	if *dbUser == "" {
		slog.ErrorContext(ctx, "--db_user must be set (or pass --mysql_uri)")
		os.Exit(1)
	}
	// Empty password isn't an option with AuroraDB MySQL.
	if *dbPassword == "" {
		slog.ErrorContext(ctx, "--db_password must be set (or pass --mysql_uri)")
		os.Exit(1)
	}

	c := mysql.Config{
		User:                    *dbUser,
		Passwd:                  *dbPassword,
		Net:                     "tcp",
		Addr:                    fmt.Sprintf("%s:%d", *dbHost, *dbPort),
		DBName:                  *dbName,
		AllowCleartextPasswords: true,
		AllowNativePasswords:    true,
	}
	return c.FormatDSN()
}

// blobURLFromFlags derives the gocloud.dev/blob bucket URL for the object store.
//
// --blob_url is used verbatim if set; otherwise an s3:// URL is built from
// --bucket and the --s3_* flags via bloburl.DeriveS3, which also exports any
// static credentials into the AWS SDK chain for the s3blob driver.
func blobURLFromFlags(ctx context.Context) string {
	if *blobURL != "" {
		return *blobURL
	}

	u, err := bloburl.DeriveS3(*bucket, *s3Endpoint, *s3AccessKeyID, *s3SecretAccessKey)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to derive blob URL from --bucket/--s3_* flags", slog.Any("error", err))
		os.Exit(1)
	}
	return u
}
