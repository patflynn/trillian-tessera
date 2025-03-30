// Copyright 2023 Google LLC. All Rights Reserved.
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

// Package firestore provides a Google Firestore implementation of a tessera.Driver.
package firestore

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/transparency-dev/trillian-tessera"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// mockFirestoreClient sets up a test client that talks to the Firestore emulator.
// For real tests this would use the Firestore emulator, but for now it just 
// tests that the config and initialization work correctly.
func setupTestFirestore(t *testing.T) (*firestore.Client, func()) {
	t.Skip("Skipping Firestore tests until emulator setup is complete")
	
	// This would connect to the emulator in a real test
	ctx := context.Background()
	conn, err := grpc.Dial("localhost:8080", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial emulator: %v", err)
	}
	
	client, err := firestore.NewClient(ctx, "test-project", option.WithGRPCConn(conn))
	if err != nil {
		conn.Close()
		t.Fatalf("Failed to create client: %v", err)
	}
	
	cleanup := func() {
		client.Close()
		conn.Close()
	}
	
	return client, cleanup
}

func TestNew(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		wantErr     bool
		checkConfig func(*testing.T, Config)
	}{
		{
			name: "Default config",
			config: Config{
				Project: "test-project",
			},
			checkConfig: func(t *testing.T, c Config) {
				if c.CollectionPrefix != logCollectionPrefix {
					t.Errorf("CollectionPrefix: got %q, want %q", c.CollectionPrefix, logCollectionPrefix)
				}
				if c.CheckpointFrequency != defaultCheckpointFrequency {
					t.Errorf("CheckpointFrequency: got %v, want %v", c.CheckpointFrequency, defaultCheckpointFrequency)
				}
				if c.IntegrateFrequency != defaultIntegrateFrequency {
					t.Errorf("IntegrateFrequency: got %v, want %v", c.IntegrateFrequency, defaultIntegrateFrequency)
				}
				if c.MaxBatchSize != defaultMaxBatchSize {
					t.Errorf("MaxBatchSize: got %d, want %d", c.MaxBatchSize, defaultMaxBatchSize)
				}
			},
		},
		{
			name: "Custom config",
			config: Config{
				Project:             "test-project",
				CollectionPrefix:    "custom-logs",
				CheckpointFrequency: 5 * time.Minute,
				IntegrateFrequency:  2 * time.Minute,
				MaxBatchSize:        500,
			},
			checkConfig: func(t *testing.T, c Config) {
				if c.CollectionPrefix != "custom-logs" {
					t.Errorf("CollectionPrefix: got %q, want %q", c.CollectionPrefix, "custom-logs")
				}
				if c.CheckpointFrequency != 5*time.Minute {
					t.Errorf("CheckpointFrequency: got %v, want %v", c.CheckpointFrequency, 5*time.Minute)
				}
				if c.IntegrateFrequency != 2*time.Minute {
					t.Errorf("IntegrateFrequency: got %v, want %v", c.IntegrateFrequency, 2*time.Minute)
				}
				if c.MaxBatchSize != 500 {
					t.Errorf("MaxBatchSize: got %d, want %d", c.MaxBatchSize, 500)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			driver, err := New(ctx, test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil {
				return
			}

			storage, ok := driver.(*Storage)
			if !ok {
				t.Fatalf("New() returned %T, want *Storage", driver)
			}

			if storage.config.Project != test.config.Project {
				t.Errorf("Project: got %q, want %q", storage.config.Project, test.config.Project)
			}

			if test.checkConfig != nil {
				test.checkConfig(t, storage.config)
			}
		})
	}
}

func TestInitializeLog(t *testing.T) {
	t.Skip("Skipping Firestore initialization test until emulator setup is complete")
	
	client, cleanup := setupTestFirestore(t)
	defer cleanup()
	
	ctx := context.Background()
	logPath := "logs/test-log"
	
	// Initialize the log
	if err := initializeLog(ctx, client, logPath); err != nil {
		t.Fatalf("initializeLog() error = %v", err)
	}
	
	// Check that the version document was created
	versionRef := client.Collection(logPath).Collection(metadataCollectionName).Doc(versionDocName)
	doc, err := versionRef.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get version document: %v", err)
	}
	
	versionData, err := doc.DataAt("version")
	if err != nil {
		t.Fatalf("Failed to extract version data: %v", err)
	}
	
	version, ok := versionData.(int64)
	if !ok {
		t.Fatalf("Version data is not an integer")
	}
	
	if int(version) != requiredVersion {
		t.Errorf("Version: got %d, want %d", version, requiredVersion)
	}
}

func TestAppenderCreation(t *testing.T) {
	t.Skip("Skipping Firestore appender test until emulator setup is complete")
	
	ctx := context.Background()
	config := Config{
		Project: "test-project",
	}
	
	driver, err := New(ctx, config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	
	appender, err := driver.Appender(ctx, tessera.AppenderOptions{
		LogID: "test-log",
	})
	if err != nil {
		t.Fatalf("Appender() error = %v", err)
	}
	defer appender.Close()
	
	// In a real test, we would add entries and verify they were properly stored
}