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
	"google.golang.org/api/iterator"
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
	
	// First, make sure we have the raw entry data
	rawEntries := make([][]byte, len(entries))
	for i, e := range entries {
		rawEntries[i] = e.Data()
	}
	
	// Assign sequence numbers to the entries
	// We need to use a transaction to ensure consecutive sequence numbers
	sequenceRef := a.client.Doc(fmt.Sprintf("%s/%s/%s", a.logPath, metadataCollectionName, sequenceDocName))
	
	var startSeq uint64
	err := a.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Get the current sequence number
		doc, err := tx.Get(sequenceRef)
		if err != nil {
			return fmt.Errorf("failed to get sequence document: %v", err)
		}
		
		// Extract the next sequence number
		nextData, err := doc.DataAt("next")
		if err != nil {
			return fmt.Errorf("failed to extract next sequence number: %v", err)
		}
		
		nextInt, ok := nextData.(int64)
		if !ok {
			return fmt.Errorf("next sequence number is not an integer")
		}
		startSeq = uint64(nextInt)
		
		// Store entries in batches collection
		batchRef := a.client.Doc(fmt.Sprintf("%s/batches/%d", a.logPath, startSeq))
		
		// Convert entries to a serializable format and compute their bundle data
		entriesData := make([]map[string]interface{}, len(entries))
		for i, e := range entries {
			seq := startSeq + uint64(i)
			bundleData := e.MarshalBundleData(seq)
			entriesData[i] = map[string]interface{}{
				"seq":        seq,
				"data":       e.Data(),
				"bundleData": bundleData,
				"leafHash":   e.LeafHash(),
			}
		}
		
		// Store the batch
		err = tx.Set(batchRef, map[string]interface{}{
			"entries":  entriesData,
			"count":    len(entries),
			"start":    startSeq,
			"end":      startSeq + uint64(len(entries)) - 1,
			"created":  time.Now(),
			"consumed": false, // Flag to indicate if this batch has been consumed by the integrator
		})
		if err != nil {
			return fmt.Errorf("failed to store entry batch: %v", err)
		}
		
		// Update the sequence number for the next batch
		newNext := startSeq + uint64(len(entries))
		err = tx.Set(sequenceRef, map[string]interface{}{
			"next":    int64(newNext),
			"updated": time.Now(),
		})
		if err != nil {
			return fmt.Errorf("failed to update sequence number: %v", err)
		}
		
		return nil
	})
	
	if err != nil {
		return fmt.Errorf("sequencing transaction failed: %v", err)
	}
	
	// At this point, we have successfully assigned sequence numbers to the entries
	// The integration is handled by the background goroutine in integrateLoop
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
	// We need to find unprocessed batches and integrate them into the tree
	// First, get the current tree size to know where to start integrating from
	treeSize, err := a.reader.IntegratedSize(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current tree size: %v", err)
	}
	
	// Query for batches that haven't been consumed yet and where the start seq is >= treeSize
	batchesRef := a.client.Collection(fmt.Sprintf("%s/batches", a.logPath))
	
	// Get unconsumed batches ordered by start sequence number
	query := batchesRef.Where("consumed", "==", false).
		Where("start", ">=", treeSize).
		OrderBy("start", firestore.Asc).
		Limit(10) // Process a reasonable number of batches at a time
	
	iter := query.Documents(ctx)
	defer iter.Stop()
	
	batches := make([]map[string]interface{}, 0)
	batchRefs := make([]*firestore.DocumentRef, 0)
	
	// Collect the batches to process
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("error iterating batches: %v", err)
		}
		
		data := doc.Data()
		batches = append(batches, data)
		batchRefs = append(batchRefs, doc.Ref)
	}
	
	if len(batches) == 0 {
		// No batches to process
		return nil
	}
	
	// Process the batches in order and prepare the sequenced entries
	var sequencedEntries []storage.SequencedEntry
	batchSeqs := make([]uint64, len(batches))
	
	for i, batch := range batches {
		entriesData, ok := batch["entries"].([]interface{})
		if !ok {
			return fmt.Errorf("batch entries not in expected format")
		}
		
		// Store the batch start seq for later
		batchSeqs[i], _ = batch["start"].(uint64)
		
		for _, entryData := range entriesData {
			entry, ok := entryData.(map[string]interface{})
			if !ok {
				return fmt.Errorf("entry data not in expected format")
			}
			
			// Extract the bundle data and leaf hash
			bundleData, ok := entry["bundleData"].([]byte)
			if !ok {
				return fmt.Errorf("bundle data not in expected format")
			}
			
			leafHash, ok := entry["leafHash"].([]byte)
			if !ok {
				return fmt.Errorf("leaf hash not in expected format")
			}
			
			sequencedEntries = append(sequencedEntries, storage.SequencedEntry{
				BundleData: bundleData,
				LeafHash:   leafHash,
			})
		}
	}
	
	// No entries to process
	if len(sequencedEntries) == 0 {
		return nil
	}
	
	// Now we need to integrate the entries into the Merkle tree
	// We'll use the storage.Integrate helper here if available
	
	// Extract the leaf hashes for integration
	leafHashes := make([][]byte, len(sequencedEntries))
	for i, entry := range sequencedEntries {
		leafHashes[i] = entry.LeafHash
	}
	
	// The integration would start at the current tree size
	// TODO: For a more complete implementation, we'd need to implement the tree integration
	// logic here using the storage.Integrate function or similar mechanism
	
	// Mark the batches as consumed in a transaction
	err = a.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		for _, batchRef := range batchRefs {
			err := tx.Update(batchRef, []firestore.Update{
				{Path: "consumed", Value: true},
				{Path: "integrated_at", Value: time.Now()},
			})
			if err != nil {
				return fmt.Errorf("failed to mark batch as consumed: %v", err)
			}
		}
		return nil
	})
	
	if err != nil {
		return fmt.Errorf("failed to update batch status: %v", err)
	}
	
	// Trigger a checkpoint update after integration
	// This is a signal for the checkpoint publisher to create a new checkpoint
	select {
	case a.cpUpdated <- struct{}{}:
	default:
	}
	
	return nil
}

// publishCheckpoint creates and stores a new checkpoint.
func (a *appender) publishCheckpoint(ctx context.Context) error {
	// Get the current tree size and root hash
	treeSize, err := a.reader.IntegratedSize(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current tree size: %v", err)
	}
	
	// If the tree is empty, there's nothing to checkpoint
	if treeSize == 0 {
		return nil
	}
	
	// Get the current checkpoint to check if we need to update
	currentCP, err := a.reader.ReadCheckpoint(ctx)
	if err != nil && err != os.ErrNotExist {
		return fmt.Errorf("failed to read current checkpoint: %v", err)
	}
	
	var currentSize uint64
	if err != os.ErrNotExist && currentCP != nil {
		_, currentSize, _, err = parse.CheckpointUnsafe(currentCP)
		if err != nil {
			return fmt.Errorf("failed to parse current checkpoint: %v", err)
		}
		
		// Skip if there's nothing new to checkpoint
		if currentSize >= treeSize {
			return nil
		}
	}
	
	// To compute the current root hash, we'd normally read the appropriate tile
	// Since we don't have a full implementation of the tree integration yet,
	// we'll use a placeholder for now
	// In a full implementation, this would come from the integration step
	rootHash := []byte("placeholder-root-hash")
	
	// Create a new checkpoint
	cp := log.Checkpoint{
		Origin: "Firestore",
		Size:   treeSize,
		Hash:   rootHash,
	}
	
	// Marshal the checkpoint
	cpBytes := cp.Marshal()
	
	// Store the checkpoint
	err = a.store.setCheckpoint(ctx, cpBytes)
	if err != nil {
		return fmt.Errorf("failed to set checkpoint: %v", err)
	}
	
	// Notify listeners about the checkpoint update
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
		"next":    int64(0),
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