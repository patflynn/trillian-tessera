// Copyright 2026 The Tessera authors. All Rights Reserved.
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

// gcs is a simple personality allowing to run conformance/compliance/performance
// tests against Google Cloud Storage backed by MySQL coordination.
//
// It is modelled closely on cmd/conformance/s3: write coordination is handled by
// MySQL exactly as the S3 backend does (same sequencer/integration schema); the
// only difference is the object store is selected with a gs:// bucket URL rather
// than an s3:// one. Both run through the same portable gocloud.dev/blob object
// store. The gcsblob driver authenticates using Application Default Credentials /
// Workload Identity, so no static keys are required.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"

	"github.com/transparency-dev/tessera"
	"github.com/transparency-dev/tessera/storage/objstore"
	"golang.org/x/mod/sumdb/note"
)

var (
	bucket          = flag.String("bucket", "", "GCS bucket to use for storing the log")
	bucketPrefix    = flag.String("bucket_prefix", "", "Optional prefix to prepend to all log resource paths in the bucket")
	mysqlURI        = flag.String("mysql_uri", "", "Full MySQL DSN used for write coordination, e.g. 'user@tcp(127.0.0.1:3306)/db?parseTime=true'")
	dbMaxConns      = flag.Int("db_max_conns", 0, "Maximum connections to the database, defaults to 0, i.e unlimited")
	dbMaxIdle       = flag.Int("db_max_idle_conns", 2, "Maximum idle database connections in the connection pool, defaults to 2")
	listen          = flag.String("listen", ":2024", "Address:port to listen on")
	signer          = flag.String("signer", "", "Note signer to use to sign checkpoints")
	publishInterval = flag.Duration("publish_interval", 3*time.Second, "How frequently to publish updated checkpoints")
	slogLevel       = flag.Int("slog_level", 0, "The cut-off threshold for structured logging. Default is 0 (INFO). See https://pkg.go.dev/log/slog#Level for other levels.")
	logFormat       = flag.String("log_format", "text", "The format of the logs: text or json.")

	additionalSigners = []string{}
)

func init() {
	flag.Func("additional_signer", "Additional note signer for checkpoints, may be specified multiple times", func(s string) error {
		additionalSigners = append(additionalSigners, s)
		return nil
	})
}

func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(*slogLevel)})
	default:
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(*slogLevel)})
	}
	slog.SetDefault(slog.New(handler))

	s, a := signerFromFlags()

	// Create our Tessera storage backend, selecting GCS via the portable blob
	// object store while keeping MySQL coordination (identical to the AWS backend).
	cfg := storageConfigFromFlags()
	driver, err := objstore.New(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create new GCS storage", slog.Any("error", err))
		os.Exit(1)
	}

	appender, shutdown, _, err := tessera.NewAppender(ctx, driver, tessera.NewAppendOptions().
		WithCheckpointSigner(s, a...).
		WithCheckpointInterval(*publishInterval).
		WithBatching(512, 300*time.Millisecond).
		WithPushback(10*4096))
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

	// Shut the server down cleanly when a termination signal is received.
	go func() {
		<-ctx.Done()
		slog.InfoContext(ctx, "Shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.ErrorContext(shutdownCtx, "Failed to shut down HTTP server cleanly", slog.Any("error", err))
		}
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		if err := shutdown(ctx); err != nil {
			slog.ErrorContext(ctx, "Failed to cleanly shutdown after ListenAndServe", slog.Any("error", err))
			os.Exit(1)
		}
		slog.ErrorContext(ctx, "ListenAndServe", slog.Any("error", err))
		os.Exit(1)
	}
}

// storageConfigFromFlags returns an objstore.Config struct populated with values
// provided via flags, configured to use the native GCS object store with MySQL
// coordination.
func storageConfigFromFlags() objstore.Config {
	ctx := context.Background()
	if *bucket == "" {
		slog.ErrorContext(ctx, "--bucket must be set")
		os.Exit(1)
	}
	if *mysqlURI == "" {
		slog.ErrorContext(ctx, "--mysql_uri must be set")
		os.Exit(1)
	}

	return objstore.Config{
		// Select GCS via the portable gocloud.dev/blob object store. The gcsblob
		// driver resolves credentials via ADC / Workload Identity, so no keys are
		// configured here.
		BlobURL:      "gs://" + *bucket,
		BucketPrefix: *bucketPrefix,
		// Coordination is plain MySQL, exactly as in the AWS backend.
		DSN:          *mysqlURI,
		MaxOpenConns: *dbMaxConns,
		MaxIdleConns: *dbMaxIdle,
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
