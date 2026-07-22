window.BENCHMARK_DATA = {
  "lastUpdate": 1784749716574,
  "repoUrl": "https://github.com/patflynn/trillian-tessera",
  "entries": {
    "Benchmark": [
      {
        "commit": {
          "author": {
            "email": "rogerng@google.com",
            "name": "Roger Ng",
            "username": "roger2hk"
          },
          "committer": {
            "email": "noreply@github.com",
            "name": "GitHub",
            "username": "web-flow"
          },
          "distinct": true,
          "id": "0baca5b8e0c76c683a2fe3af59c9a0bc193afe9a",
          "message": "Support HTTP/2 in MTC Mirror (#1070)",
          "timestamp": "2026-07-22T11:07:59+01:00",
          "tree_id": "773304f8f4ab38e9c7382edad7b7fae412ea9fe0",
          "url": "https://github.com/patflynn/trillian-tessera/commit/0baca5b8e0c76c683a2fe3af59c9a0bc193afe9a"
        },
        "date": 1784749716257,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkDedup (github.com/transparency-dev/tessera)",
            "value": 5801133,
            "unit": "ns/op\t  907091 B/op\t   18845 allocs/op",
            "extra": "219 times\n4 procs"
          },
          {
            "name": "BenchmarkDedup (github.com/transparency-dev/tessera) - ns/op",
            "value": 5801133,
            "unit": "ns/op",
            "extra": "219 times\n4 procs"
          },
          {
            "name": "BenchmarkDedup (github.com/transparency-dev/tessera) - B/op",
            "value": 907091,
            "unit": "B/op",
            "extra": "219 times\n4 procs"
          },
          {
            "name": "BenchmarkDedup (github.com/transparency-dev/tessera) - allocs/op",
            "value": 18845,
            "unit": "allocs/op",
            "extra": "219 times\n4 procs"
          },
          {
            "name": "BenchmarkAwait (github.com/transparency-dev/tessera)",
            "value": 1540911,
            "unit": "ns/op\t    1210 B/op\t      20 allocs/op",
            "extra": "763 times\n4 procs"
          },
          {
            "name": "BenchmarkAwait (github.com/transparency-dev/tessera) - ns/op",
            "value": 1540911,
            "unit": "ns/op",
            "extra": "763 times\n4 procs"
          },
          {
            "name": "BenchmarkAwait (github.com/transparency-dev/tessera) - B/op",
            "value": 1210,
            "unit": "B/op",
            "extra": "763 times\n4 procs"
          },
          {
            "name": "BenchmarkAwait (github.com/transparency-dev/tessera) - allocs/op",
            "value": 20,
            "unit": "allocs/op",
            "extra": "763 times\n4 procs"
          },
          {
            "name": "BenchmarkWitnessGroupSatisfaction (github.com/transparency-dev/tessera)",
            "value": 241063,
            "unit": "ns/op\t    3697 B/op\t      46 allocs/op",
            "extra": "4567 times\n4 procs"
          },
          {
            "name": "BenchmarkWitnessGroupSatisfaction (github.com/transparency-dev/tessera) - ns/op",
            "value": 241063,
            "unit": "ns/op",
            "extra": "4567 times\n4 procs"
          },
          {
            "name": "BenchmarkWitnessGroupSatisfaction (github.com/transparency-dev/tessera) - B/op",
            "value": 3697,
            "unit": "B/op",
            "extra": "4567 times\n4 procs"
          },
          {
            "name": "BenchmarkWitnessGroupSatisfaction (github.com/transparency-dev/tessera) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "4567 times\n4 procs"
          },
          {
            "name": "BenchmarkLeafBundle_UnmarshalText (github.com/transparency-dev/tessera/api)",
            "value": 3240,
            "unit": "ns/op\t    6528 B/op\t       1 allocs/op",
            "extra": "376786 times\n4 procs"
          },
          {
            "name": "BenchmarkLeafBundle_UnmarshalText (github.com/transparency-dev/tessera/api) - ns/op",
            "value": 3240,
            "unit": "ns/op",
            "extra": "376786 times\n4 procs"
          },
          {
            "name": "BenchmarkLeafBundle_UnmarshalText (github.com/transparency-dev/tessera/api) - B/op",
            "value": 6528,
            "unit": "B/op",
            "extra": "376786 times\n4 procs"
          },
          {
            "name": "BenchmarkLeafBundle_UnmarshalText (github.com/transparency-dev/tessera/api) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "376786 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/WarmCache (github.com/transparency-dev/tessera/client)",
            "value": 76273,
            "unit": "ns/op\t   15397 B/op\t     180 allocs/op",
            "extra": "15741 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/WarmCache (github.com/transparency-dev/tessera/client) - ns/op",
            "value": 76273,
            "unit": "ns/op",
            "extra": "15741 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/WarmCache (github.com/transparency-dev/tessera/client) - B/op",
            "value": 15397,
            "unit": "B/op",
            "extra": "15741 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/WarmCache (github.com/transparency-dev/tessera/client) - allocs/op",
            "value": 180,
            "unit": "allocs/op",
            "extra": "15741 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/ColdCache (github.com/transparency-dev/tessera/client)",
            "value": 526875,
            "unit": "ns/op\t  689107 B/op\t    3555 allocs/op",
            "extra": "2264 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/ColdCache (github.com/transparency-dev/tessera/client) - ns/op",
            "value": 526875,
            "unit": "ns/op",
            "extra": "2264 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/ColdCache (github.com/transparency-dev/tessera/client) - B/op",
            "value": 689107,
            "unit": "B/op",
            "extra": "2264 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/InclusionProof/ColdCache (github.com/transparency-dev/tessera/client) - allocs/op",
            "value": 3555,
            "unit": "allocs/op",
            "extra": "2264 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/WarmCache (github.com/transparency-dev/tessera/client)",
            "value": 57122,
            "unit": "ns/op\t   15414 B/op\t     180 allocs/op",
            "extra": "20511 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/WarmCache (github.com/transparency-dev/tessera/client) - ns/op",
            "value": 57122,
            "unit": "ns/op",
            "extra": "20511 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/WarmCache (github.com/transparency-dev/tessera/client) - B/op",
            "value": 15414,
            "unit": "B/op",
            "extra": "20511 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/WarmCache (github.com/transparency-dev/tessera/client) - allocs/op",
            "value": 180,
            "unit": "allocs/op",
            "extra": "20511 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/ColdCache (github.com/transparency-dev/tessera/client)",
            "value": 519954,
            "unit": "ns/op\t  688012 B/op\t    3548 allocs/op",
            "extra": "2348 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/ColdCache (github.com/transparency-dev/tessera/client) - ns/op",
            "value": 519954,
            "unit": "ns/op",
            "extra": "2348 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/ColdCache (github.com/transparency-dev/tessera/client) - B/op",
            "value": 688012,
            "unit": "B/op",
            "extra": "2348 times\n4 procs"
          },
          {
            "name": "BenchmarkProofBuilder/ConsistencyProof/ColdCache (github.com/transparency-dev/tessera/client) - allocs/op",
            "value": 3548,
            "unit": "allocs/op",
            "extra": "2348 times\n4 procs"
          },
          {
            "name": "BenchmarkCheckpointUnsafe (github.com/transparency-dev/tessera/internal/parse)",
            "value": 227.3,
            "unit": "ns/op\t     224 B/op\t       5 allocs/op",
            "extra": "5372628 times\n4 procs"
          },
          {
            "name": "BenchmarkCheckpointUnsafe (github.com/transparency-dev/tessera/internal/parse) - ns/op",
            "value": 227.3,
            "unit": "ns/op",
            "extra": "5372628 times\n4 procs"
          },
          {
            "name": "BenchmarkCheckpointUnsafe (github.com/transparency-dev/tessera/internal/parse) - B/op",
            "value": 224,
            "unit": "B/op",
            "extra": "5372628 times\n4 procs"
          },
          {
            "name": "BenchmarkCheckpointUnsafe (github.com/transparency-dev/tessera/internal/parse) - allocs/op",
            "value": 5,
            "unit": "allocs/op",
            "extra": "5372628 times\n4 procs"
          },
          {
            "name": "BenchmarkBuildRequestBody (github.com/transparency-dev/tessera/internal/witness)",
            "value": 1423,
            "unit": "ns/op\t    2080 B/op\t       7 allocs/op",
            "extra": "774146 times\n4 procs"
          },
          {
            "name": "BenchmarkBuildRequestBody (github.com/transparency-dev/tessera/internal/witness) - ns/op",
            "value": 1423,
            "unit": "ns/op",
            "extra": "774146 times\n4 procs"
          },
          {
            "name": "BenchmarkBuildRequestBody (github.com/transparency-dev/tessera/internal/witness) - B/op",
            "value": 2080,
            "unit": "B/op",
            "extra": "774146 times\n4 procs"
          },
          {
            "name": "BenchmarkBuildRequestBody (github.com/transparency-dev/tessera/internal/witness) - allocs/op",
            "value": 7,
            "unit": "allocs/op",
            "extra": "774146 times\n4 procs"
          },
          {
            "name": "BenchmarkIntegrate (github.com/transparency-dev/tessera/storage/internal)",
            "value": 325515,
            "unit": "ns/op\t  274671 B/op\t    3079 allocs/op",
            "extra": "4108 times\n4 procs"
          },
          {
            "name": "BenchmarkIntegrate (github.com/transparency-dev/tessera/storage/internal) - ns/op",
            "value": 325515,
            "unit": "ns/op",
            "extra": "4108 times\n4 procs"
          },
          {
            "name": "BenchmarkIntegrate (github.com/transparency-dev/tessera/storage/internal) - B/op",
            "value": 274671,
            "unit": "B/op",
            "extra": "4108 times\n4 procs"
          },
          {
            "name": "BenchmarkIntegrate (github.com/transparency-dev/tessera/storage/internal) - allocs/op",
            "value": 3079,
            "unit": "allocs/op",
            "extra": "4108 times\n4 procs"
          },
          {
            "name": "BenchmarkQueue (github.com/transparency-dev/tessera/storage/internal)",
            "value": 958320,
            "unit": "ns/op\t  540384 B/op\t   14405 allocs/op",
            "extra": "1274 times\n4 procs"
          },
          {
            "name": "BenchmarkQueue (github.com/transparency-dev/tessera/storage/internal) - ns/op",
            "value": 958320,
            "unit": "ns/op",
            "extra": "1274 times\n4 procs"
          },
          {
            "name": "BenchmarkQueue (github.com/transparency-dev/tessera/storage/internal) - B/op",
            "value": 540384,
            "unit": "B/op",
            "extra": "1274 times\n4 procs"
          },
          {
            "name": "BenchmarkQueue (github.com/transparency-dev/tessera/storage/internal) - allocs/op",
            "value": 14405,
            "unit": "allocs/op",
            "extra": "1274 times\n4 procs"
          }
        ]
      }
    ]
  }
}