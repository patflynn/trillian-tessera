# Firestore Storage Backend for Trillian Tessera

This document describes the implementation of a Firestore-based storage backend for Trillian Tessera.

## Overview

This implementation leverages Google Firestore for both storage and transactional operations, providing an alternative to the GCS/Spanner implementation. Firestore offers:

- Document-based storage with nested collections
- Strong consistency for single-document reads/writes
- Transactions for multi-document operations
- Automatic scaling with no capacity management required
- Low operational overhead

## Document Structure

The Firestore implementation uses the following collection structure:

```
logs/
├── {log_name}/
│   ├── tiles/
│   │   └── {path}
│   ├── entries/
│   │   └── {path}
│   ├── checkpoints/
│   │   └── latest
│   └── metadata/
│       ├── version
│       └── sequence
```

Where:
- `logs/{log_name}` - Collection group for each log
- `tiles/{path}` - Documents containing Merkle tree tiles
- `entries/{path}` - Documents containing entry bundles
- `checkpoints/latest` - Document with the most recent checkpoint
- `metadata/version` - Document with schema version information
- `metadata/sequence` - Document for sequence number coordination

## Life of a Leaf

1. Leaves are submitted via a call to the storage's `Add` function.
2. The storage implementation batches these entries and uses Firestore transactions to:
   - Read the current sequence number from the `sequence` document
   - Write the batch to the `entries` collection with appropriate sequence numbers
   - Update the sequence number document atomically
3. Newly sequenced entries are integrated into the Merkle tree:
   - Using Firestore transactions to ensure consistency
   - Writing tile documents to the `tiles` collection
   - Updating the checkpoint document with the latest tree state
4. Checkpoints are published at configured intervals, updating the `latest` document

## Performance Considerations

- Document size limits (1 MiB) are managed by appropriate entry batching
- Firestore's billing model is based on reads/writes/deletes, so operation counts are optimized
- Caching is implemented for frequently accessed documents
- Batch operations are used for multi-document writes

## Configuration

The Firestore backend can be configured with the following options:

- Project ID
- Collection prefix (optional)
- Batch size for writes
- Integration interval
- Checkpoint publishing interval