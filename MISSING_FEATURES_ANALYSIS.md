# iceberg-go Missing Features Analysis

**Date:** 2026-02-21
**Compared against:** Apache Iceberg Java reference implementation & spec

---

## Executive Summary

The Go implementation covers the core read path well and has recently added
write support, but significant gaps remain in delete handling, file format
breadth, table maintenance, and several spec features. The project is past
the proof-of-concept stage for **read-only workloads** but is still early for
**full read-write production use**, especially for workloads that depend on
merge-on-read deletes, compaction, or multi-engine interoperability.

### Maturity Spectrum

| Area | Maturity | Notes |
|------|----------|-------|
| Read path (scan, filter, project) | **Production-ready** | Partition/manifest/metrics pruning, Parquet row-group stats, position deletes |
| Schema & partition evolution | **Production-ready** | Add/drop/rename/reorder columns, type promotion, partition spec evolution |
| Write path (append) | **Usable** | Parquet-only, partitioned fanout, rolling files, manifest merging |
| Write path (overwrite/delete) | **Usable** | Copy-on-write only, filter-based file classification works |
| Catalog support | **Usable** | REST (best), Glue, Hive, SQL — all implement core interface |
| Table maintenance | **Early** | Expire snapshots & orphan cleanup exist; no compaction |
| Delete files (MoR) | **Not implemented** | Equality deletes error at scan time; no delete file writing |
| Views | **Early** | REST catalog only; no Glue/Hive support |
| Encryption | **Not implemented** | Only the key-metadata field exists in manifests |
| Statistics (Puffin) | **Not implemented** | Data structures defined, no reader/writer |

---

## 1. Write Path Gaps

### 1.1 Parquet-Only File Format Support

**Severity: High**

Only Parquet is implemented for both reading and writing. The `FileFormat`
interface in `table/internal/interfaces.go:76-91` returns `nil` for Avro and
ORC:

```go
func GetFileFormat(format iceberg.FileFormat) FileFormat {
    switch format {
    case iceberg.ParquetFile:
        return parquetFormat{}
    default:
        return nil  // Avro and ORC unsupported
    }
}
```

**Impact:** Tables written by Spark/Flink/Trino using Avro or ORC data files
cannot be read or written by iceberg-go. Avro is the default format for some
engines.

### 1.2 No Delete File Writing (Position or Equality)

**Severity: High**

The `Transaction` validates that only data files can be added
(`transaction.go:490`):

```go
if df.ContentType() != iceberg.EntryContentData {
    return nil, fmt.Errorf("adding files other than data files is not yet implemented: ...")
}
```

There is **no API** to produce position delete files or equality delete files.
All deletes use the **copy-on-write** strategy: files are fully rewritten with
non-matching rows preserved.

**Impact:** Copy-on-write is the only delete mode. This works but is
significantly more expensive for targeted deletes (e.g., GDPR row removal)
because entire data files must be rewritten even for single-row deletes.

### 1.3 No Sort-Order Aware Writing

**Severity: Medium**

`SortOrderID` is a field in `WriteTask` (`table/writer.go:40`) but is **never
populated** — always zero. No pre-sorting of records occurs before writing.

**Impact:** Engines that depend on sorted data files for efficient range queries
or merge joins cannot benefit from iceberg-go-written data. Data locality
within files is uncontrolled.

### 1.4 No Metrics Configuration for Writing

**Severity: Medium**

The properties `write.metadata.metrics.default` and
`write.metadata.metrics.column.*` are defined (`table/properties.go:36-38`)
but the writer always collects full metrics with `truncate(16)`. There is no
way to configure per-column metrics modes (none, counts, truncate(N), full).

---

## 2. Read Path Gaps

### 2.1 No Equality Delete Support

**Severity: High**

Scanning explicitly errors on equality deletes (`table/scanner.go:408`):

```go
return errors.New("iceberg-go does not yet support equality deletes")
```

Position deletes are fully supported (concurrent reading, per-file matching,
row filtering via Arrow compute).

**Impact:** Any table written with merge-on-read equality deletes by Spark,
Flink, or Trino will fail to scan in iceberg-go.

### 2.2 No Metadata Tables

**Severity: Medium**

The Iceberg spec defines system tables (`history`, `snapshots`, `manifests`,
`files`, `partitions`, `all_data_files`, `all_manifests`, etc.). None of these
are implemented. Metadata is accessible via the `Metadata` interface but not
as scannable Arrow tables.

**Impact:** Users cannot inspect table state programmatically (e.g., "list all
files", "show snapshot history") through a standard query interface. They must
use the raw Go API.

### 2.3 No Incremental Scan

**Severity: Medium**

There is no API to scan changes between two snapshots (added/deleted files
between snapshot A and B). Time travel (`WithSnapshotID`, `WithSnapshotAsOf`)
loads a full snapshot, not a delta.

**Impact:** CDC (Change Data Capture) use cases and incremental processing
pipelines cannot efficiently detect what changed.

---

## 3. Merge-on-Read (V2 Deletes) — Not Implemented

**Severity: Critical for V2 interop**

This is the gap you likely noticed. The entire merge-on-read pipeline is
missing:

| Component | Status |
|-----------|--------|
| Read position delete files | Implemented |
| Read equality delete files | **Not implemented** (errors) |
| Write position delete files | **Not implemented** |
| Write equality delete files | **Not implemented** |
| Deletion vectors (V3) | **Not implemented** |
| `write.delete.mode = merge-on-read` | Errors at `transaction.go:971` |

The `WriteDeleteModeKey` property exists but only `copy-on-write` is accepted:

```go
if writeDeleteMode != WriteModeCopyOnWrite {
    return fmt.Errorf("'%s' is set to '%s' but only '%s' is currently supported",
        WriteDeleteModeKey, writeDeleteMode, WriteModeCopyOnWrite)
}
```

---

## 4. Table Maintenance Gaps

### 4.1 No Data Compaction / Rewrite Data Files

**Severity: High**

Java's `RewriteDataFilesAction` (bin-packing, sort-based compaction, z-order)
has no equivalent. `ReplaceDataFiles` exists but operates on explicit
file paths — there is no automatic detection of small files, no bin-packing
action, and no z-order/Hilbert optimization.

### 4.2 No Rewrite Manifests

**Severity: Medium**

Manifest merging exists as part of the append path
(`manifestMergeManager` in `snapshot_producers.go:252-380`), but there is no
standalone "rewrite manifests" action for retroactive cleanup.

### 4.3 Expire Snapshots — Implemented

`Transaction.ExpireSnapshots()` (`transaction.go:201-302`) is implemented with
`WithRetainLast`, `WithOlderThan`, `WithPostCommit` options. This handles
snapshot expiration and optional file deletion.

### 4.4 Orphan File Cleanup — Implemented

`Table.DeleteOrphanFiles()` (`table/orphan_cleanup.go:164-226`) is
implemented with configurable location, age threshold, dry-run mode,
concurrency, and scheme/authority equivalence. However, statistics files are
not tracked (`orphan_cleanup.go:243`):

```go
// TODO: Add statistics files support once iceberg-go exposes statisticsFiles()
```

---

## 5. Statistics & Puffin Files

**Severity: Medium**

Data structures exist (`table/statistics.go:61-87`):

```go
type StatisticsFile struct { ... }
type BlobMetadata struct { ... }
type PartitionStatisticsFile struct { ... }
```

These are correctly serialized/deserialized in table metadata. However:

- **No Puffin file reader** — cannot read NDV sketches or other blob stats
- **No Puffin file writer** — cannot produce statistics during compaction
- **No theta-sketch integration** — `BlobTypeApacheDatasketchesThetaV1` is
  defined as a constant but unused

**Impact:** Query engines that rely on NDV (number of distinct values) for
cost-based optimization cannot get these stats from iceberg-go tables.

---

## 6. Encryption

**Severity: Low (spec feature, rarely used)**

Only the `KeyMetadata` field exists in manifest entries
(`internal/avro_schemas.go:276`). No encryption key management, no encrypted
file reading/writing, no encryption configuration.

---

## 7. View Support Gaps

| Catalog | CreateView | LoadView | UpdateView | DropView | ListViews |
|---------|-----------|----------|-----------|----------|-----------|
| REST    | Yes | Yes | Yes | Yes | Yes |
| SQL     | Yes (partial) | Yes | **No** | Yes | Yes |
| Glue    | **No** | **No** | **No** | **No** | **No** |
| Hive    | **No** | **No** | **No** | **No** | **No** |

The `view` package has metadata, builder, requirements, and updates
implemented. But catalog support is limited to REST (full) and SQL (partial,
missing `UpdateView`). Glue and Hive catalogs have zero view support.

---

## 8. Catalog Gaps

### 8.1 No Multi-Table Transactions

No catalog supports atomic commits across multiple tables. Each
`CommitTable()` operates on a single table.

### 8.2 No `PurgeTable` in Interface

Only the REST catalog has `PurgeTable()` (`catalog/rest/rest.go:869`). Other
catalogs only support `DropTable` which doesn't clean up data files.

### 8.3 No DynamoDB Catalog

The catalog type `DynamoDB` is defined as a constant
(`catalog/catalog.go`) but has no implementation.

### 8.4 Hive Catalog: No Hierarchical Namespaces

`ListNamespaces()` with a parent rejects with:
```go
return nil, errors.New("hierarchical namespace is not supported")
```

### 8.5 No Catalog-Level Retry / Conflict Resolution

When `CommitTable` fails due to concurrent modification (409 Conflict from
REST, version mismatch from SQL/Glue), there is no built-in retry loop. The
caller must implement their own retry logic. Java's `BaseMetastoreCatalog`
has built-in retry with configurable backoff.

---

## 9. Expression / Predicate Gaps

### 9.1 No Residual Evaluation

After partition pruning, the original filter should be simplified to a
"residual" that omits already-satisfied partition predicates. This
optimization is not implemented — the full filter is always evaluated against
every row.

### 9.2 No Expression Serialization

Expressions cannot be serialized to/from JSON or any wire format. They are
always constructed programmatically. Java supports JSON serialization for
engine integration.

---

## 10. Format V3 / Row Lineage

V3 support is partially implemented:

- **Metadata format version 3** — reads and writes correctly
- **Row lineage tracking** (`FirstRowID`, `AddedRows` in snapshots) —
  implemented in `snapshot_producers.go:706-748`
- **`ManifestListWriterV3`** — implemented for row ID tracking
- **Deletion vectors** — `BlobTypeDeletionVectorV1` constant defined but
  **not implemented**

---

## 11. Append / Overwrite — What's Actually There

Since you mentioned noticing something about Append or Overwrite, here is
precisely what exists:

### Append Operations

| Method | Description |
|--------|-------------|
| `Transaction.Append()` | Writes Arrow RecordReader as new data files |
| `Transaction.AppendTable()` | Wraps `Append()` for `arrow.Table` |
| `Transaction.AddFiles()` | Adds existing Parquet files by path (reads metadata) |
| `Transaction.AddDataFiles()` | Adds pre-built `DataFile` objects (no file I/O) |

Append uses either `fastAppendFiles` (no manifest merging) or
`mergeAppendFiles` (with manifest merging) depending on the
`commit.manifest-merge.enabled` property.

### Overwrite Operations

| Method | Description |
|--------|-------------|
| `Transaction.Overwrite()` | Filter-based overwrite with RecordReader |
| `Transaction.OverwriteTable()` | Wraps `Overwrite()` for `arrow.Table` |
| `Transaction.Delete()` | Filter-based delete (copy-on-write only) |
| `Transaction.ReplaceDataFiles()` | Replace files by path |
| `Transaction.ReplaceDataFilesWithDataFiles()` | Replace using DataFile objects |

### What's Missing from Append/Overwrite

1. **No conflict detection on Overwrite** — Java's `OverwriteFiles` validates
   that no new files were added to the overwritten partition between plan and
   commit. iceberg-go does not perform this validation; it only uses
   `AssertRefSnapshotID("main", ...)` which catches any change, not
   partition-scoped conflicts.

2. **No `ReplacePartitions`** — Java has a separate `ReplacePartitions` API
   that atomically replaces all files in specific partitions. iceberg-go's
   `Overwrite` with `AlwaysTrue` replaces everything; there is no
   partition-scoped replacement.

3. **No dynamic partition overwrite** — Spark's
   `spark.sql.sources.partitionOverwriteMode=dynamic` behavior has no
   equivalent.

4. **`ReplaceDataFiles` uses `OpOverwrite` instead of `OpReplace`** — As
   noted in `transaction.go:342-344`:
   ```go
   // TODO: technically, this could be a REPLACE operation but we aren't performing
   // any validation here that there are no changes to the underlying data.
   ```
   A REPLACE operation should validate data equivalence, but this is skipped.

5. **No `FastAppend` vs `AppendFiles` distinction at the user API level** —
   Java exposes both. In iceberg-go, the choice between fast-append and
   merge-append is made automatically based on the
   `commit.manifest-merge.enabled` property, which is reasonable but less
   flexible.

---

## 12. Summary: Top Priority Missing Features

Ranked by impact on production usability:

| Priority | Feature | Effort Estimate |
|----------|---------|----------------|
| **P0** | Equality delete reading | Medium — scanner already handles position deletes |
| **P0** | Avro/ORC data file reading | Large — need full format implementations |
| **P1** | Delete file writing (position + equality) | Large — new write path needed |
| **P1** | Data compaction (rewrite data files) | Large — detection + rewrite logic |
| **P1** | Metadata tables | Medium — expose existing data as Arrow tables |
| **P2** | Incremental scan | Medium — diff manifest lists between snapshots |
| **P2** | Sort-order aware writing | Medium — pre-sort + split logic |
| **P2** | Puffin file reader/writer | Medium — binary format + theta sketches |
| **P2** | Partition-scoped overwrite | Small-Medium — extend existing overwrite |
| **P3** | View support in Glue/Hive | Medium — per-catalog implementation |
| **P3** | DynamoDB catalog | Medium — new catalog implementation |
| **P3** | Encryption | Large — full encryption framework |
| **P3** | Expression serialization | Small — JSON marshaling |
