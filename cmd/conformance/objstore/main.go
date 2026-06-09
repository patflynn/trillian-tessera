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

// objstore is a generic personality for running conformance/compliance/performance
// tests against any gocloud.dev/blob object store backed by MySQL coordination.
//
// The object store is selected with a single provider-scoped bucket URL passed via
// --blob_url (e.g. gs://BUCKET, s3://BUCKET?endpoint=...&region=..., file:///path,
// mem://). For convenience, when --blob_url is unset an s3:// URL is derived from
// --bucket and the --s3_endpoint/--s3_access_key/--s3_secret flags, which is handy
// for S3-compatible stores such as Amazon S3 or MinIO.
//
// Write coordination always uses MySQL. The DSN can either be supplied directly via
// --mysql_uri (e.g. for a Cloud SQL proxy connection) or assembled from the discrete
// --db_host/--db_port/--db_user/--db_password/--db_name flags.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"log/slog"

	"github.com/go-sql-driver/mysql"
	"github.com/transparency-dev/tessera"
	"github.com/transparency-dev/tessera/storage/objstore"
	antispamstore "github.com/transparency-dev/tessera/storage/objstore/antispam"
	"golang.org/x/mod/sumdb/note"
)

var (
	bucket            = flag.String("bucket", "", "Bucket to use for storing log")
	bucketPrefix      = flag.String("bucket_prefix", "", "Optional prefix to prepend to all log resource paths in the bucket")
	mysqlURI          = flag.String("mysql_uri", "", "Full MySQL DSN used for write coordination, e.g. 'user@tcp(127.0.0.1:3306)/db?parseTime=true'. If set, it is used directly; otherwise the DSN is built from the --db_* flags.")
	dbName            = flag.String("db_name", "", "AuroraDB name for the log DB")
	dbHost            = flag.String("db_host", "", "AuroraDB host")
	dbPort            = flag.Int("db_port", 3306, "AuroraDB port")
	dbUser            = flag.String("db_user", "", "AuroraDB user")
	dbPassword        = flag.String("db_password", "", "AuroraDB user")
	dbMaxConns        = flag.Int("db_max_conns", 0, "Maximum connections to the database, defaults to 0, i.e unlimited")
	dbMaxIdle         = flag.Int("db_max_idle_conns", 2, "Maximum idle database connections in the connection pool, defaults to 2")
	s3Endpoint        = flag.String("s3_endpoint", "", "Endpoint for custom non-AWS S3 service")
	s3AccessKeyID     = flag.String("s3_access_key", "", "Access key ID for custom non-AWS S3 service")
	s3SecretAccessKey = flag.String("s3_secret", "", "Secret access key for custom non-AWS S3 service")

	blobURL = flag.String("blob_url", "", "Optional explicit provider-scoped bucket URL for the object store, e.g. gs://BUCKET, s3://BUCKET?endpoint=...&s3ForcePathStyle=true&region=..., file:///path, mem://. If unset, a URL is derived from --bucket and the --s3_* flags. Auth is each driver's native credential chain.")

	listen            = flag.String("listen", ":2024", "Address:port to listen on")
	signer            = flag.String("signer", "", "Note signer to use to sign checkpoints")
	publishInterval   = flag.Duration("publish_interval", 3*time.Second, "How frequently to publish updated checkpoints")
	traceFraction     = flag.Float64("trace_fraction", 0, "Fraction of open-telemetry span traces to sample")
	slogLevel         = flag.Int("slog_level", 0, "The cut-off threshold for structured logging. Default is 0 (INFO). See https://pkg.go.dev/log/slog#Level for other levels.")
	logFormat         = flag.String("log_format", "text", "The format of the logs: text or json.")
	additionalSigners = []string{}

	antispamEnable = flag.Bool("antispam", false, "EXPERIMENTAL: Set to true to enable persistent antispam storage")
	antispamDb     = flag.String("antispam_db_name", "", "AuroraDB name for the antispam DB")
)

func init() {
	flag.Func("additional_signer", "Additional note signer for checkpoints, may be specified multiple times", func(s string) error {
		additionalSigners = append(additionalSigners, s)
		return nil
	})
}

func main() {
	flag.Parse()
	ctx := context.Background()
	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(*slogLevel)})
	default:
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(*slogLevel)})
	}
	slog.SetDefault(slog.New(handler))

	shutdownOTel := initOTel(ctx, *traceFraction)
	defer shutdownOTel(ctx)
	s, a := signerFromFlags()

	// Create our Tessera storage backend:
	cfg := storageConfigFromFlags()
	driver, err := objstore.New(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create new storage", slog.Any("error", err))
		os.Exit(1)
	}
	var antispam tessera.Antispam
	// Persistent antispam is currently experimental, so there's no documentation yet!
	if *antispamEnable {
		asOpts := antispamstore.AntispamOpts{} // Use defaults
		antispam, err = antispamstore.NewAntispam(ctx, antispamMysqlConfig().FormatDSN(), asOpts)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to create new antispam storage", slog.Any("error", err))
			os.Exit(1)
		}
	}
	appender, shutdown, _, err := tessera.NewAppender(ctx, driver, tessera.NewAppendOptions().
		WithCheckpointSigner(s, a...).
		WithCheckpointInterval(*publishInterval).
		WithBatching(512, 300*time.Millisecond).
		WithPushback(10*4096).
		WithAntispam(tessera.DefaultAntispamInMemorySize, antispam))
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create new appender", slog.Any("error", err))
		os.Exit(1)
	}

	// Expose a HTTP handler for the conformance test writes.
	// This should accept arbitrary bytes POSTed to /add, and return an ascii
	// decimal representation of the index assigned to the entry.
	http.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		idx, err := appender.Add(r.Context(), tessera.NewEntry(b))()
		if err != nil {
			if errors.Is(err, tessera.ErrPushback) {
				w.Header().Add("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		// Write out the assigned index
		_, _ = fmt.Fprintf(w, "%d", idx.Index)
	})

	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              *listen,
		Handler:           http.DefaultServeMux,
		Protocols:         &protocols,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		if err := shutdown(ctx); err != nil {
			slog.ErrorContext(ctx, "Failed to cleanly shutdown after ListenAndServe", slog.Any("error", err))
			os.Exit(1)
		}
		slog.ErrorContext(ctx, "ListenAndServe", slog.Any("error", err))
		os.Exit(1)
	}
}

// storageConfigFromFlags returns an objstore.Config struct populated with values
// provided via flags.
func storageConfigFromFlags() objstore.Config {
	ctx := context.Background()
	// The object store is identified by a single blob URL. It can be provided
	// explicitly via --blob_url, otherwise it's derived from --bucket and the
	// --s3_* flags, so --bucket is only required when --blob_url is unset.
	if *blobURL == "" && *bucket == "" {
		slog.ErrorContext(ctx, "either --bucket or --blob_url must be set")
		os.Exit(1)
	}

	return objstore.Config{
		BlobURL:      blobURLFromFlags(ctx),
		BucketPrefix: *bucketPrefix,
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
	if *dbPort < 1 || *dbPort > 65535 {
		slog.ErrorContext(ctx, "--db_port must be a valid port number between 1 and 65535")
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
// If --blob_url is set it is used verbatim (allowing gs://, file://, mem://, or a
// fully-specified s3:// URL). Otherwise an s3:// URL is built from --bucket and
// the --s3_* flags:
//   - with a custom --s3_endpoint (e.g. MinIO), the endpoint, region and
//     path-style addressing are encoded as query parameters, and the static
//     --s3_access_key / --s3_secret credentials are exported into the AWS SDK
//     credential chain (via environment variables) so the s3blob driver picks
//     them up.
//   - without --s3_endpoint (real AWS), a plain s3://BUCKET URL is used and the
//     region and credentials are resolved by the AWS SDK's default chain.
func blobURLFromFlags(ctx context.Context) string {
	if *blobURL != "" {
		return *blobURL
	}

	if *s3Endpoint == "" {
		// Real AWS: region and credentials come from the AWS SDK default chain.
		return "s3://" + *bucket
	}

	// Custom S3-compatible endpoint (e.g. MinIO). The s3blob driver reads
	// credentials from the AWS SDK chain rather than the URL, so export the
	// static credentials into the environment for it to discover. Only set the
	// credential vars when the corresponding flags are non-empty so we don't
	// clobber any ambient AWS credentials, and only default AWS_REGION when it
	// isn't already configured.
	const defaultRegion = "us-east-1"
	envVars := make(map[string]string)
	if *s3AccessKeyID != "" {
		envVars["AWS_ACCESS_KEY_ID"] = *s3AccessKeyID
	}
	if *s3SecretAccessKey != "" {
		envVars["AWS_SECRET_ACCESS_KEY"] = *s3SecretAccessKey
	}
	if _, ok := os.LookupEnv("AWS_REGION"); !ok {
		envVars["AWS_REGION"] = defaultRegion
	}
	for k, v := range envVars {
		if err := os.Setenv(k, v); err != nil {
			slog.ErrorContext(ctx, "failed to set AWS credential env var", slog.String("var", k), slog.Any("error", err))
			os.Exit(1)
		}
	}

	q := url.Values{
		"endpoint":         {*s3Endpoint},
		"s3ForcePathStyle": {"true"},
		"region":           {defaultRegion},
	}
	return "s3://" + *bucket + "?" + q.Encode()
}

func antispamMysqlConfig() *mysql.Config {
	ctx := context.Background()
	if *antispamDb == "" {
		slog.ErrorContext(ctx, "--antispam_db_name must be set")
		os.Exit(1)
	}
	if *dbHost == "" {
		slog.ErrorContext(ctx, "--db_host must be set")
		os.Exit(1)
	}
	if *dbPort == 0 {
		slog.ErrorContext(ctx, "--db_port must be set")
		os.Exit(1)
	}
	if *dbUser == "" {
		slog.ErrorContext(ctx, "--db_user must be set")
		os.Exit(1)
	}
	// Empty password isn't an option with AuroraDB MySQL.
	if *dbPassword == "" {
		slog.ErrorContext(ctx, "--db_password must be set")
		os.Exit(1)
	}

	return &mysql.Config{
		User:                    *dbUser,
		Passwd:                  *dbPassword,
		Net:                     "tcp",
		Addr:                    fmt.Sprintf("%s:%d", *dbHost, *dbPort),
		DBName:                  *antispamDb,
		AllowCleartextPasswords: true,
		AllowNativePasswords:    true,
	}
}

func signerFromFlags() (note.Signer, []note.Signer) {
	s, err := note.NewSigner(*signer)
	if err != nil {
		slog.ErrorContext(context.Background(), "Failed to create new signer", slog.Any("error", err))
		os.Exit(1)
	}

	var a []note.Signer
	for _, as := range additionalSigners {
		s, err := note.NewSigner(as)
		if err != nil {
			slog.ErrorContext(context.Background(), "Failed to create additional signer", slog.Any("error", err))
			os.Exit(1)
		}
		a = append(a, s)
	}

	return s, a
}
