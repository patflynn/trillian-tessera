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
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/transparency-dev/formats/log"
	"github.com/transparency-dev/trillian-tessera"
	"github.com/transparency-dev/trillian-tessera/api/layout"
	"github.com/transparency-dev/trillian-tessera/internal/parse"
	storage "github.com/transparency-dev/trillian-tessera/storage/internal"
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

// TileID represents a unique tile identifier within a log.
type TileID struct {
	Level uint64
	Index uint64
}

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
	queue     *storage.Queue
	
	cpFreq    time.Duration
	intFreq   time.Duration
	maxBatch  int
	
	// For background goroutines
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	
	// Channel that gets signaled when the checkpoint has been updated
	cpUpdated chan struct{}
}

// migrationStorage implements a migration writer for a Firestore-backed log.
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
func (s *Storage) Appender(ctx context.Context, opts *tessera.AppendOptions) (*tessera.Appender, tessera.LogReader, error) {
	client, err := firestore.NewClient(ctx, s.config.Project)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Firestore client: %v", err)
	}
	
	logID := "default" // This is a placeholder - we'd need to get a real log ID from somewhere
	logPath := fmt.Sprintf("%s/%s", s.config.CollectionPrefix, logID)
	
	// Initialize the log if needed
	if err := ensureVersion(ctx, client, logPath); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("failed to ensure version: %v", err)
	}
	
	reader := &logReader{
		client:   client,
		logPath:  logPath,
		tileSize: layout.TileWidth,
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
	
	// Create a cancelable context for the background goroutines
	appCtx, cancel := context.WithCancel(ctx)
	
	a := &appender{
		ctx:       appCtx,
		client:    client,
		logPath:   logPath,
		reader:    reader,
		coords:    coords,
		store:     store,
		cpFreq:    s.config.CheckpointFrequency,
		intFreq:   s.config.IntegrateFrequency,
		maxBatch:  s.config.MaxBatchSize,
		cancel:    cancel,
		cpUpdated: make(chan struct{}),
	}
	
	a.queue = storage.NewQueue(ctx, opts.BatchMaxAge(), opts.BatchMaxSize(), a.addEntries)
	
	// Create the actual tessera.Appender struct
	appender := &tessera.Appender{
		Add: a.add,
	}
	
	// Start background tasks
	a.wg.Add(2)
	go a.integrateLoop()
	go a.checkpointLoop()
	
	return appender, reader, nil
}

// MigrationWriter creates a new migration writer for the specified log.
func (s *Storage) MigrationWriter(ctx context.Context, opts *tessera.AppendOptions) (interface{}, error) {
	client, err := firestore.NewClient(ctx, s.config.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firestore client: %v", err)
	}
	
	logID := "default" // This is a placeholder - we'd need to get a real log ID from somewhere
	logPath := fmt.Sprintf("%s/%s", s.config.CollectionPrefix, logID)
	
	// Initialize the log if needed
	if err := ensureVersion(ctx, client, logPath); err != nil {
		client.Close()
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
	docRef := r.client.Doc(fmt.Sprintf("%s/%s/%s", r.logPath, checkpointCollectionName, latestCheckpointDocName))
	
	doc, err := docRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, os.ErrNotExist
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
func (r *logReader) ReadTile(ctx context.Context, l, i uint64, p uint8) ([]byte, error) {
	path := layout.TilePath(l, i, p)
	docRef := r.client.Doc(fmt.Sprintf("%s/%s/%s", r.logPath, tileCollectionName, path))
	
	doc, err := docRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, os.ErrNotExist
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
func (r *logReader) ReadEntryBundle(ctx context.Context, i uint64, p uint8) ([]byte, error) {
	path := layout.EntriesPath(i, p)
	docRef := r.client.Doc(fmt.Sprintf("%s/%s/%s", r.logPath, entryCollectionName, path))
	
	doc, err := docRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, os.ErrNotExist
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
	if err == os.ErrNotExist {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read checkpoint: %v", err)
	}
	
	_, size, _, err := parse.CheckpointUnsafe(checkpointBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to parse checkpoint: %v", err)
	}
	
	return size, nil
}

// StreamEntries implements tessera.LogReader.
func (r *logReader) StreamEntries(ctx context.Context, fromEntry uint64) (next func() (ri layout.RangeInfo, bundle []byte, err error), cancel func()) {
	// Use StreamAdaptor from storage package
	getBundle := func(ctx context.Context, i uint64, p uint8) ([]byte, error) {
		return r.ReadEntryBundle(ctx, i, p)
	}
	
	return storage.StreamAdaptor(ctx, 10, r.IntegratedSize, getBundle, fromEntry)
}

// setTile writes a tile to the store.
func (s *logResourceStore) setTile(ctx context.Context, l, i uint64, p uint8, data []byte) error {
	path := layout.TilePath(l, i, p)
	docRef := s.client.Doc(fmt.Sprintf("%s/%s/%s", s.logPath, tileCollectionName, path))
	
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
func (s *logResourceStore) setEntryBundle(ctx context.Context, i uint64, p uint8, data []byte) error {
	path := layout.EntriesPath(i, p)
	docRef := s.client.Doc(fmt.Sprintf("%s/%s/%s", s.logPath, entryCollectionName, path))
	
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
	docRef := s.client.Doc(fmt.Sprintf("%s/%s/%s", s.logPath, checkpointCollectionName, latestCheckpointDocName))
	
	_, err := docRef.Set(ctx, map[string]interface{}{
		"data": data,
		"created": time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to write checkpoint: %v", err)
	}
	
	return nil
}

// add is the implementation of tessera.AddFn
func (a *appender) add(ctx context.Context, entry *tessera.Entry) tessera.IndexFuture {
	return a.queue.Add(ctx, entry)
}

// addEntries is called by the queue when there's a batch of entries ready for sequencing
func (a *appender) addEntries(ctx context.Context, entries []*tessera.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	
	// TODO: Implement entry sequencing and integration
	return nil
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
	// TODO: Implement integration logic for new entries
	return nil
}

// publishCheckpoint creates and stores a new checkpoint.
func (a *appender) publishCheckpoint(ctx context.Context) error {
	// TODO: Implement checkpoint publication
	select {
	case a.cpUpdated <- struct{}{}:
	default:
	}
	return nil
}

// AddEntry implements migration writer.
func (m *migrationStorage) AddEntry(ctx context.Context, idx uint64, entry []byte) error {
	// TODO: Implement migration writer AddEntry
	return errors.New("AddEntry not yet implemented")
}

// AddEntryHashes implements migration writer.
func (m *migrationStorage) AddEntryHashes(ctx context.Context, start uint64, leafHashes [][]byte) error {
	// TODO: Implement migration writer AddEntryHashes
	return errors.New("AddEntryHashes not yet implemented")
}

// Close implements closer.
func (m *migrationStorage) Close() error {
	return m.client.Close()
}

// ensureVersion checks and initializes the log version.
func ensureVersion(ctx context.Context, client *firestore.Client, logPath string) error {
	versionRef := client.Doc(fmt.Sprintf("%s/%s/%s", logPath, metadataCollectionName, versionDocName))
	
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
	versionRef := client.Doc(fmt.Sprintf("%s/%s/%s", logPath, metadataCollectionName, versionDocName))
	batch.Set(versionRef, map[string]interface{}{
		"version": requiredVersion,
		"created": time.Now(),
	})
	
	// Create sequence document
	seqRef := client.Doc(fmt.Sprintf("%s/%s/%s", logPath, metadataCollectionName, sequenceDocName))
	batch.Set(seqRef, map[string]interface{}{
		"next": 0,
		"updated": time.Now(),
	})
	
	// Create empty checkpoint document
	cpRef := client.Doc(fmt.Sprintf("%s/%s/%s", logPath, checkpointCollectionName, latestCheckpointDocName))
	emptyCheckpoint := log.Checkpoint{
		Origin: "Firestore",
		Size:   0,
	}
	cpBytes := emptyCheckpoint.Marshal()
	
	batch.Set(cpRef, map[string]interface{}{
		"data": cpBytes,
		"created": time.Now(),
	})
	
	// Commit the batch
	_, err := batch.Commit(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize log: %v", err)
	}
	
	return nil
}