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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/transparency-dev/merkle/compact"
	"github.com/transparency-dev/merkle/rfc6962"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/transparency-dev/trillian-tessera"
	"github.com/transparency-dev/trillian-tessera/api/layout"
	"github.com/transparency-dev/trillian-tessera/internal/witness"
	storagehelpers "github.com/transparency-dev/trillian-tessera/storage/internal"
)

// This is the required storage schema version.
// If breaking changes to the storage schema are made, then:
// 1. Increment this number
// 2. Add migration code to New that will run if the storage version is old.
const requiredVersion = 1

// These constants define the paths/collections used in the Firestore document structure.
const (
	logCollectionPrefix     = "logs"
	tileCollectionName      = "tiles"
	entryCollectionName     = "entries"
	checkpointCollectionName = "checkpoints"
	metadataCollectionName  = "metadata"
	versionDocName          = "version"
	sequenceDocName         = "sequence"
	latestCheckpointDocName = "latest"
)

// Default operational parameters
const (
	defaultCheckpointFrequency = 10 * time.Second
	defaultIntegrateFrequency  = 1 * time.Second
	defaultMaxBatchSize        = 1000
	defaultRetryCount          = 5
	defaultRetryDelay          = 500 * time.Millisecond
	defaultMaxRetryDelay       = 5 * time.Second
)

// Config holds the configuration for a firestore storage implementation.
type Config struct {
	// Project is the GCP project ID for Firestore.
	Project string
	// The root collection prefix for logs (defaults to "logs").
	CollectionPrefix string
	// How frequently to create checkpoint files.
	CheckpointFrequency time.Duration
	// How frequently to check for new entries to integrate.
	IntegrateFrequency time.Duration
	// Maximum batch size for entry operations.
	MaxBatchSize int
}

// Storage is an implementation of tessera.Driver backed by Firestore.
type Storage struct {
	config Config
}

// logReader implements tessera.LogReader for a Firestore-backed log.
type logReader struct {
	client   *firestore.Client
	logPath  string
	hasher   compact.HashFn
	tileSize uint64
}

// logResourceStore manages the storage of tiles, entries, and checkpoints.
type logResourceStore struct {
	client  *firestore.Client
	logPath string
}

// firestoreCoordinator manages sequencing and coordination for a log.
type firestoreCoordinator struct {
	client    *firestore.Client
	logPath   string
	mu        sync.Mutex
	nextSeq   uint64
	batchSize int
}

// appender implements the append lifecycle for a Firestore-backed log.
type appender struct {
	ctx       context.Context
	client    *firestore.Client
	logPath   string
	reader    *logReader
	coords    *firestoreCoordinator
	store     *logResourceStore
	integrate *storagehelpers.Integrate
	
	cpFreq    time.Duration
	intFreq   time.Duration
	maxBatch  int
	
	// For background goroutines
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// migrationStorage implements tessera.MigrationLogWriter for a Firestore-backed log.
type migrationStorage struct {
	client   *firestore.Client
	logPath  string
	store    *logResourceStore
	coords   *firestoreCoordinator
}

// New creates a new tessera.Driver which reads from and writes to Firestore.
func New(ctx context.Context, c Config) (tessera.Driver, error) {
	if c.CollectionPrefix == "" {
		c.CollectionPrefix = logCollectionPrefix
	}
	if c.CheckpointFrequency == 0 {
		c.CheckpointFrequency = defaultCheckpointFrequency
	}
	if c.IntegrateFrequency == 0 {
		c.IntegrateFrequency = defaultIntegrateFrequency
	}
	if c.MaxBatchSize == 0 {
		c.MaxBatchSize = defaultMaxBatchSize
	}
	
	return &Storage{config: c}, nil
}

// Appender returns a tessera.Appender instance for the specified log.
func (s *Storage) Appender(ctx context.Context, opts tessera.AppenderOptions) (tessera.Appender, error) {
	client, err := firestore.NewClient(ctx, s.config.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firestore client: %v", err)
	}
	
	logPath := fmt.Sprintf("%s/%s", s.config.CollectionPrefix, opts.LogID)
	
	// Initialize the log if needed
	if err := ensureVersion(ctx, client, logPath); err != nil {
		return nil, fmt.Errorf("failed to ensure version: %v", err)
	}
	
	reader := &logReader{
		client:   client,
		logPath:  logPath,
		hasher:   rfc6962.DefaultHasher.HashLeaf,
		tileSize: layout.TileSize,
	}
	
	store := &logResourceStore{
		client:  client,
		logPath: logPath,
	}
	
	coords := &firestoreCoordinator{
		client:    client,
		logPath:   logPath,
		batchSize: s.config.MaxBatchSize,
	}
	
	// Create the integration helper
	integrate := &storagehelpers.Integrate{
		Hasher:   rfc6962.DefaultHasher,
		TileSize: layout.TileSize,
		Reader:   reader,
		SetTile: func(ctx context.Context, path string, data []byte) error {
			return store.setTile(ctx, path, data)
		},
	}
	
	// Create a cancelable context for the background goroutines
	appCtx, cancel := context.WithCancel(ctx)
	
	a := &appender{
		ctx:       appCtx,
		client:    client,
		logPath:   logPath,
		reader:    reader,
		coords:    coords,
		store:     store,
		integrate: integrate,
		cpFreq:    s.config.CheckpointFrequency,
		intFreq:   s.config.IntegrateFrequency,
		maxBatch:  s.config.MaxBatchSize,
		cancel:    cancel,
	}
	
	// Start background tasks
	a.wg.Add(2)
	go a.integrateLoop()
	go a.checkpointLoop()
	
	return a, nil
}

// MigrationWriter creates a new MigrationLogWriter for the specified log.
func (s *Storage) MigrationWriter(ctx context.Context, opts tessera.MigrationOptions) (tessera.MigrationLogWriter, error) {
	client, err := firestore.NewClient(ctx, s.config.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firestore client: %v", err)
	}
	
	logPath := fmt.Sprintf("%s/%s", s.config.CollectionPrefix, opts.LogID)
	
	// Initialize the log if needed
	if err := ensureVersion(ctx, client, logPath); err != nil {
		return nil, fmt.Errorf("failed to ensure version: %v", err)
	}
	
	store := &logResourceStore{
		client:  client,
		logPath: logPath,
	}
	
	coords := &firestoreCoordinator{
		client:    client,
		logPath:   logPath,
		batchSize: s.config.MaxBatchSize,
	}
	
	return &migrationStorage{
		client:   client,
		logPath:  logPath,
		store:    store,
		coords:   coords,
	}, nil
}

// ReadCheckpoint implements tessera.LogReader.
func (r *logReader) ReadCheckpoint(ctx context.Context) ([]byte, error) {
	docRef := r.client.Collection(r.logPath).Collection(checkpointCollectionName).Doc(latestCheckpointDocName)
	
	doc, err := docRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, tessera.ErrNoDataFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint: %v", err)
	}
	
	data, err := doc.DataAt("data")
	if err != nil {
		return nil, fmt.Errorf("failed to extract checkpoint data: %v", err)
	}
	
	checkpointData, ok := data.([]byte)
	if !ok {
		return nil, fmt.Errorf("checkpoint data is not of type []byte")
	}
	
	return checkpointData, nil
}

// ReadTile implements tessera.LogReader.
func (r *logReader) ReadTile(ctx context.Context, path string) ([]byte, error) {
	docRef := r.client.Collection(r.logPath).Collection(tileCollectionName).Doc(path)
	
	doc, err := docRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, tessera.ErrNoDataFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read tile: %v", err)
	}
	
	data, err := doc.DataAt("data")
	if err != nil {
		return nil, fmt.Errorf("failed to extract tile data: %v", err)
	}
	
	tileData, ok := data.([]byte)
	if !ok {
		return nil, fmt.Errorf("tile data is not of type []byte")
	}
	
	return tileData, nil
}

// ReadEntryBundle implements tessera.LogReader.
func (r *logReader) ReadEntryBundle(ctx context.Context, path string) ([]byte, error) {
	docRef := r.client.Collection(r.logPath).Collection(entryCollectionName).Doc(path)
	
	doc, err := docRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, tessera.ErrNoDataFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read entry bundle: %v", err)
	}
	
	data, err := doc.DataAt("data")
	if err != nil {
		return nil, fmt.Errorf("failed to extract entry data: %v", err)
	}
	
	entryData, ok := data.([]byte)
	if !ok {
		return nil, fmt.Errorf("entry data is not of type []byte")
	}
	
	return entryData, nil
}

// IntegratedSize implements tessera.LogReader.
func (r *logReader) IntegratedSize(ctx context.Context) (uint64, error) {
	checkpointBytes, err := r.ReadCheckpoint(ctx)
	if err == tessera.ErrNoDataFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read checkpoint: %v", err)
	}
	
	cp, err := witness.ParseCheckpoint(checkpointBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to parse checkpoint: %v", err)
	}
	
	return cp.Size, nil
}

// StreamEntries implements tessera.LogReader.
func (r *logReader) StreamEntries(ctx context.Context, start, count uint64, prefetch bool) (<-chan tessera.EntryChunk, error) {
	if count == 0 {
		return nil, nil
	}
	
	chunks := make(chan tessera.EntryChunk)
	
	go func() {
		defer close(chunks)
		
		// TODO: Implement streaming logic for entries
		// This is placeholder code
		chunks <- tessera.EntryChunk{
			Entries: nil,
			Err:     errors.New("StreamEntries not yet implemented"),
		}
	}()
	
	return chunks, nil
}

// setTile writes a tile to the store.
func (s *logResourceStore) setTile(ctx context.Context, path string, data []byte) error {
	docRef := s.client.Collection(s.logPath).Collection(tileCollectionName).Doc(path)
	
	_, err := docRef.Set(ctx, map[string]interface{}{
		"data": data,
		"created": time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to write tile: %v", err)
	}
	
	return nil
}

// setEntryBundle writes an entry bundle to the store.
func (s *logResourceStore) setEntryBundle(ctx context.Context, path string, data []byte) error {
	docRef := s.client.Collection(s.logPath).Collection(entryCollectionName).Doc(path)
	
	_, err := docRef.Set(ctx, map[string]interface{}{
		"data": data,
		"created": time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to write entry bundle: %v", err)
	}
	
	return nil
}

// setCheckpoint writes a checkpoint to the store.
func (s *logResourceStore) setCheckpoint(ctx context.Context, data []byte) error {
	docRef := s.client.Collection(s.logPath).Collection(checkpointCollectionName).Doc(latestCheckpointDocName)
	
	_, err := docRef.Set(ctx, map[string]interface{}{
		"data": data,
		"created": time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to write checkpoint: %v", err)
	}
	
	return nil
}

// Add implements tessera.Appender.
func (a *appender) Add(ctx context.Context, entries [][]byte) ([]uint64, error) {
	return a.coords.sequenceEntries(ctx, entries)
}

// integrateLoop continuously checks for new entries to integrate into the tree.
func (a *appender) integrateLoop() {
	defer a.wg.Done()
	
	ticker := time.NewTicker(a.intFreq)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			if err := a.integrateNewEntries(a.ctx); err != nil {
				// Log error but continue
				fmt.Printf("Failed to integrate entries: %v\n", err)
			}
		case <-a.ctx.Done():
			return
		}
	}
}

// checkpointLoop periodically publishes a new checkpoint.
func (a *appender) checkpointLoop() {
	defer a.wg.Done()
	
	ticker := time.NewTicker(a.cpFreq)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			if err := a.publishCheckpoint(a.ctx); err != nil {
				// Log error but continue
				fmt.Printf("Failed to publish checkpoint: %v\n", err)
			}
		case <-a.ctx.Done():
			return
		}
	}
}

// integrateNewEntries processes any new entries that haven't been integrated yet.
func (a *appender) integrateNewEntries(ctx context.Context) error {
	// TODO: Implement integration logic
	return nil
}

// publishCheckpoint creates and stores a new checkpoint.
func (a *appender) publishCheckpoint(ctx context.Context) error {
	// TODO: Implement checkpoint publication
	return nil
}

// Close implements tessera.Appender.
func (a *appender) Close() error {
	a.cancel()
	a.wg.Wait()
	return a.client.Close()
}

// sequenceEntries assigns sequence numbers to the provided entries and stores them.
func (c *firestoreCoordinator) sequenceEntries(ctx context.Context, entries [][]byte) ([]uint64, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	
	// TODO: Implement sequencing logic using Firestore transactions
	return nil, errors.New("sequenceEntries not yet implemented")
}

// AddEntry implements tessera.MigrationLogWriter.
func (m *migrationStorage) AddEntry(ctx context.Context, idx uint64, entry []byte) error {
	// TODO: Implement migration writer AddEntry
	return errors.New("AddEntry not yet implemented")
}

// Close implements tessera.MigrationLogWriter.
func (m *migrationStorage) Close() error {
	return m.client.Close()
}

// ensureVersion checks and initializes the log version.
func ensureVersion(ctx context.Context, client *firestore.Client, logPath string) error {
	versionRef := client.Collection(logPath).Collection(metadataCollectionName).Doc(versionDocName)
	
	// Check if version doc exists
	doc, err := versionRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		// Initialize the log
		return initializeLog(ctx, client, logPath)
	}
	if err != nil {
		return fmt.Errorf("failed to check version: %v", err)
	}
	
	// Check version
	versionData, err := doc.DataAt("version")
	if err != nil {
		return fmt.Errorf("failed to extract version data: %v", err)
	}
	
	version, ok := versionData.(int64)
	if !ok {
		return fmt.Errorf("version data is not an integer")
	}
	
	if int(version) != requiredVersion {
		return fmt.Errorf("version mismatch: got %d, want %d", version, requiredVersion)
	}
	
	return nil
}

// initializeLog sets up initial documents for a new log.
func initializeLog(ctx context.Context, client *firestore.Client, logPath string) error {
	batch := client.Batch()
	
	// Create version document
	versionRef := client.Collection(logPath).Collection(metadataCollectionName).Doc(versionDocName)
	batch.Set(versionRef, map[string]interface{}{
		"version": requiredVersion,
		"created": time.Now(),
	})
	
	// Create sequence document
	seqRef := client.Collection(logPath).Collection(metadataCollectionName).Doc(sequenceDocName)
	batch.Set(seqRef, map[string]interface{}{
		"next": 0,
		"updated": time.Now(),
	})
	
	// Create empty checkpoint document
	cpRef := client.Collection(logPath).Collection(checkpointCollectionName).Doc(latestCheckpointDocName)
	emptyCheckpoint := witness.Checkpoint{
		Origin: "Firestore",
		Size:   0,
	}
	cpBytes, err := emptyCheckpoint.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal empty checkpoint: %v", err)
	}
	
	batch.Set(cpRef, map[string]interface{}{
		"data": cpBytes,
		"created": time.Now(),
	})
	
	// Commit the batch
	_, err = batch.Commit(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize log: %v", err)
	}
	
	return nil
}