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

package objstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	_ "gocloud.dev/blob/s3blob"
)

// newFakeS3 starts a server that answers every request with one canned S3 error
// response, and returns the s3:// bucket URL addressing it.
//
// This drives getObject's error path through the real AWS SDK and the real
// s3blob driver: the SDK does its own status, header and XML body parsing, so
// the errors under test are the same values a live S3 or MinIO would produce.
// The request ID matters — see TestGetObjectNotFoundClassification.
func newFakeS3(t *testing.T, status int, code, requestID string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("x-amz-request-id", requestID)
		w.Header().Set("x-amz-id-2", "ZXhhbXBsZUhvc3RJRGV4YW1wbGVIb3N0SUQ=")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>injected by newFakeS3</Message><RequestId>%s</RequestId></Error>`, code, requestID)
	}))
	t.Cleanup(srv.Close)

	q := url.Values{
		"endpoint":         {srv.URL},
		"region":           {"us-east-1"},
		"s3ForcePathStyle": {"true"},
	}
	return "s3://test-bucket?" + q.Encode()
}

// TestGetObjectNotFoundClassification pins down which S3 failures getObject is
// allowed to report as os.ErrNotExist.
//
// Everything above getObject treats os.ErrNotExist as authoritative: getTiles
// substitutes a nil tile and ReadCheckpoint reports an empty log, which forces
// integration to start over. So classifying a transient or misconfiguration
// error as "absent" is not a lost error message, it is the log lying about its
// own contents.
//
// gcerrors.Code cannot be used for this. s3blob's ErrorCode asks whether the
// rendered OperationError contains the substring "301" before it looks at the
// actual API error code, and the rendered form embeds the request ID — so the
// "18C58E3010C7F943" case below is a plain 412 that gocloud reports as
// NotFound purely because of six characters of its request ID. That is not an
// exotic input: MinIO request IDs are monotonic hex timestamps, so they contain
// "301" in contiguous multi-minute runs, and this backend has a MinIO CI lane.
// The 403 and NoSuchBucket cases are the same failure in a different costume.
func TestGetObjectNotFoundClassification(t *testing.T) {
	ctx := context.Background()

	// The SDK insists on credentials before it will sign a request; the fake
	// server never checks them.
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	for _, test := range []struct {
		name        string
		status      int
		code        string
		requestID   string
		wantNotdErr bool
	}{
		{
			name:      "precondition failed, innocuous request ID",
			status:    http.StatusPreconditionFailed,
			code:      "PreconditionFailed",
			requestID: "AAAAAAAAAAAAAAAA",
		}, {
			// The regression case: identical to the above but for a request ID
			// containing "301".
			name:      "precondition failed, request ID containing 301",
			status:    http.StatusPreconditionFailed,
			code:      "PreconditionFailed",
			requestID: "18C58E3010C7F943",
		}, {
			// A missing bucket is a configuration error. Reporting it as an
			// absent object turns a misconfigured deployment into a log that
			// looks empty.
			name:      "no such bucket",
			status:    http.StatusNotFound,
			code:      "NoSuchBucket",
			requestID: "AAAAAAAAAAAAAAAA",
		}, {
			name:      "access denied",
			status:    http.StatusForbidden,
			code:      "AccessDenied",
			requestID: "AAAAAAAAAAAAAAAA",
		}, {
			// Proves the test is not vacuous: a genuine missing object must
			// still come back as os.ErrNotExist, or nothing above would work.
			name:        "no such key",
			status:      http.StatusNotFound,
			code:        "NoSuchKey",
			requestID:   "AAAAAAAAAAAAAAAA",
			wantNotdErr: true,
		}, {
			name:        "no such key, request ID containing 301",
			status:      http.StatusNotFound,
			code:        "NoSuchKey",
			requestID:   "18C58E3010C7F943",
			wantNotdErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			bkt, err := blob.OpenBucket(ctx, newFakeS3(t, test.status, test.code, test.requestID))
			if err != nil {
				t.Fatalf("blob.OpenBucket: %v", err)
			}
			t.Cleanup(func() { _ = bkt.Close() })

			// Recorded rather than asserted: gocloud's own verdict on the raw
			// driver error, which is what isNotFound exists to second-guess.
			// Compare the two 412 rows in the output to see the request ID
			// alone flip it between FailedPrecondition and NotFound.
			if _, rawErr := bkt.ReadAll(ctx, "tile/0/000"); rawErr != nil {
				t.Logf("injected %d %s (request ID %q): gcerrors.Code=%v", test.status, test.code, test.requestID, gcerrors.Code(rawErr))
			}

			_, err = newBlobStore(bkt, "").getObject(ctx, "tile/0/000")
			if err == nil {
				t.Fatalf("getObject returned a nil error, want the injected %s", test.code)
			}

			if got := errors.Is(err, os.ErrNotExist); got != test.wantNotdErr {
				t.Errorf("errors.Is(err, os.ErrNotExist) = %t, want %t for an injected %d %s; err = %v", got, test.wantNotdErr, test.status, test.code, err)
			}
		})
	}
}
