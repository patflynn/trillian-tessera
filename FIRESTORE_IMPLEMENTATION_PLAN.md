# Firestore Backend Implementation Plan for Trillian Tessera

## Overview

This document outlines the plan to implement a Google Firestore storage backend for Trillian Tessera. The implementation will follow the patterns established by existing storage backends while leveraging Firestore's unique capabilities.

## Implementation Tasks

### 1. Setup Infrastructure and Basics

- Create directory structure at `storage/firestore/` following similar patterns to `storage/gcp/`
- Create README.md with overview and usage instructions
- Create basic Firestore client wrapper and configuration

### 2. Core Storage Implementation

- Implement `Driver` interface with `Appender` and `MigrationWriter` methods
- Create `Storage` struct to manage Firestore connection and operations
- Implement document schema for tiles, entry bundles, and checkpoints
- Define document path structure for efficient retrieval
- Implement version checking and initialization

### 3. Implement Core Operations

- Implement tile reading/writing operations
- Implement entry bundle reading/writing
- Implement checkpoint reading/writing
- Implement streaming entries functionality
- Integrate with Merkle tree operations

### 4. Concurrency and Transactions

- Implement concurrency control using Firestore transactions
- Handle optimistic concurrency for checkpoint updates
- Ensure thread-safety for all operations

### 5. Testing

- Create unit tests for all Firestore operations
- Implement integration tests against Firestore emulator
- Create benchmarks to measure performance
- Verify compatibility with existing Trillian Tessera operations

### 6. Documentation

- Document Firestore schema design
- Create performance comparison with other backends
- Document configuration options and best practices
- Add usage examples

### 7. Tooling and Integration

- Add Firestore backend to appropriate CLI tools
- Create migration utilities for existing backends to Firestore
- Implement conformance test for Firestore

## Design Considerations

### Firestore Document Structure

```
collections/
├── log_name/
│   ├── tiles/
│   │   └── {path}/
│   │       └── {tile_data}
│   ├── entries/
│   │   └── {path}/
│   │       └── {entry_data}
│   └── checkpoints/
│       └── {checkpoint_data}
```

### Performance Optimizations

- Use Firestore batching for multi-document operations
- Implement caching for frequently accessed tiles
- Consider document size limits (1 MiB) for entry bundles
- Use composite indexes for efficient querying

### Security Considerations

- Use IAM for authentication and authorization
- Implement proper error handling for permission issues
- Consider encryption options for sensitive data

## Implementation Order

1. Basic infrastructure and client setup
2. Core document operations (read/write)
3. Integration with tree operations
4. Concurrency and transactions
5. Testing and validation
6. Documentation and examples

## Estimated Timeline

- Infrastructure and basic operations: 2-3 days
- Core implementation: 1 week
- Testing and validation: 3-4 days
- Documentation and examples: 1-2 days

## References

- [Firestore Documentation](https://firebase.google.com/docs/firestore)
- Existing Trillian Tessera backends:
  - `storage/gcp/`
  - `storage/aws/`
  - `storage/mysql/`
  - `storage/posix/`