# Firestore Backend Implementation Plan for Trillian Tessera

## Overview

This document outlines the plan to implement a Google Firestore storage backend for Trillian Tessera. The implementation will follow the patterns established by existing storage backends while leveraging Firestore's unique capabilities.

## Implementation Progress

### Completed Tasks

- ✅ Created directory structure at `storage/firestore/` following similar patterns to `storage/gcp/`
- ✅ Created README.md with overview and usage instructions
- ✅ Created basic Firestore client wrapper and configuration
- ✅ Implemented `Driver` interface with `Appender` and `MigrationWriter` methods
- ✅ Created `Storage` struct to manage Firestore connection and operations
- ✅ Defined document schema for tiles, entry bundles, and checkpoints
- ✅ Defined document path structure for efficient retrieval
- ✅ Implemented version checking and initialization
- ✅ Implemented basic read operations (tiles, entries, checkpoints)
- ✅ Implemented scaffolding for write operations
- ✅ Implemented basic concurrency control structure (locks, background jobs)
- ✅ Fixed build issues and made tests pass
- ✅ Implemented `StreamEntries` functionality using the storage helper

### Remaining Tasks

#### Core Operations Completion

- Implement proper sequencing logic in the `sequenceEntries` method
- Complete entry integration logic in the `integrateNewEntries` method
- Complete checkpoint publishing in the `publishCheckpoint` method
- Complete migration writer functionality (`AddEntry` and `AddEntryHashes`)

#### Concurrency and Transactions

- Implement proper Firestore transactions for atomic operations
- Optimize batch operations for performance
- Complete thread-safety implementation for all operations

#### Testing

- Create comprehensive unit tests for all operations
- Set up Firestore emulator for integration tests
- Create performance benchmarks
- Test compatibility with existing Trillian Tessera operations

#### Documentation

- Update documentation with implementation details
- Document performance characteristics
- Add usage examples

## Design Considerations

### Firestore Document Structure

The current implementation uses the following document structure:

```
logs/
├── {log_id}/
│   ├── tiles/
│   │   └── {tile_path}
│   ├── entries/
│   │   └── {entry_bundle_path}
│   ├── checkpoints/
│   │   └── latest
│   └── metadata/
│       ├── version
│       └── sequence
```

Where:
- Document paths follow the Trillian Tessera layout conventions
- Each document contains its data and metadata (creation time, etc.)
- Metadata documents track schema version and sequence information

### Current Implementation Details

- Document creation and retrieval uses the Firestore Document API
- Background goroutines handle integration and checkpoint publication
- The storage queue is used for batching entries
- Locking is used to ensure thread safety

### Performance Optimizations Planned

- Use Firestore batching for multi-document operations
- Implement caching for frequently accessed tiles
- Consider document size limits (1 MiB) for entry bundles
- Use composite indexes for efficient querying

### Security Considerations

- Use IAM for authentication and authorization
- Implement proper error handling for permission issues
- Consider encryption options for sensitive data

## Next Steps

1. Complete the sequencing logic for adding entries
2. Implement integration of new entries into the Merkle tree
3. Implement checkpoint publication
4. Complete migration writer functionality
5. Expand test coverage
6. Finalize documentation

## References

- [Firestore Documentation](https://firebase.google.com/docs/firestore)
- Existing Trillian Tessera backends:
  - `storage/gcp/`
  - `storage/aws/`
  - `storage/mysql/`
  - `storage/posix/`