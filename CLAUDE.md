# CLAUDE.md

This file provides guidance for AI assistants working with the `iceberg-go` codebase.

## Project Overview

`iceberg-go` is the official Go implementation of the [Apache Iceberg table specification](https://iceberg.apache.org/spec/). It is an Apache Software Foundation project licensed under Apache License 2.0.

**Module path:** `github.com/apache/iceberg-go`
**Go version:** 1.23+ (CI tests against 1.23 and 1.24)

## Build & Test Commands

Always use the Makefile so commands stay in sync with CI.

```shell
make test              # Run all unit tests (go test -v ./...)
make lint              # Run golangci-lint (v2.8.0, --timeout=10m)
make lint-install      # Install the correct golangci-lint version
```

### Integration Tests (require Docker)

```shell
make integration-setup     # Start Docker Compose services
make integration-test      # Run all integration test suites
make integration-scanner   # Scanner tests only
make integration-io        # IO tests only
make integration-rest      # REST catalog tests only
make integration-spark     # Spark integration tests only
make integration-hive      # Hive catalog tests only
```

Integration tests use the `//go:build integration` build tag and are run with `-tags=integration`.

## Repository Structure

```
/                         Root package (github.com/apache/iceberg-go)
                          Core types: Schema, Type, PartitionSpec, Manifest,
                          transforms, expressions, literals, predicates, visitors
├── catalog/              Catalog interface + implementations
│   ├── catalog.go        Catalog interface definition
│   ├── registry.go       Catalog type registry (self-registering pattern)
│   ├── glue/             AWS Glue catalog
│   ├── hive/             Hive Metastore catalog
│   ├── rest/             REST catalog (Iceberg REST spec)
│   ├── sql/              SQL-backed catalog (via uptrace/bun)
│   └── internal/         Shared catalog utilities
├── table/                Table operations
│   ├── table.go          Table struct and core operations
│   ├── metadata.go       Table metadata (v1/v2/v3)
│   ├── snapshots.go      Snapshot types
│   ├── snapshot_producers.go  Snapshot creation (append, overwrite, delete)
│   ├── transaction.go    Transaction support
│   ├── scanner.go        Table scanning / plan files
│   ├── arrow_scanner.go  Arrow-based scan (returns arrow.Record batches)
│   ├── arrow_utils.go    Arrow ↔ Iceberg type conversion
│   ├── writer.go         Data file writer
│   ├── rolling_data_writer.go    Rolling data file writer
│   ├── partitioned_fanout_writer.go  Partitioned writing
│   ├── updates.go        Table update operations
│   ├── update_schema.go  Schema evolution
│   ├── update_spec.go    Partition spec evolution
│   ├── requirements.go   Table commit requirements
│   ├── evaluators.go     Row-level expression evaluation
│   ├── substrait/        Substrait expression support
│   └── internal/         Internal table utilities
├── view/                 View support (metadata, updates, requirements)
├── io/                   FileSystem IO abstraction
│   ├── io.go             IO, ReadFileIO, WriteFileIO interfaces
│   ├── s3.go             S3 filesystem
│   ├── gcs.go            GCS filesystem
│   ├── azure.go          Azure Blob Storage
│   ├── local.go          Local filesystem
│   └── blob.go           gocloud.dev blob abstraction
├── cmd/iceberg/          CLI tool (similar to pyiceberg CLI)
├── config/               YAML config file handling (.iceberg-go.yaml)
├── utils/                AWS context utilities
├── internal/             Internal shared utilities (avro schemas, mock FS, helpers)
│   └── recipe/           Docker Compose setup for integration tests
├── dev/                  Release tooling and license checking
│   ├── check-license     Apache RAT license header checker
│   └── release/          Release scripts
└── website/              Documentation website source
```

## Key Interfaces and Types

### Root Package (`iceberg`)

- **`Type`** - Interface for all Iceberg types (primitives + nested)
- **`Schema`** - Immutable table schema with lazy-populated lookup maps
- **`NestedField`** - Schema field with ID, name, type, required flag
- **`PartitionSpec`** - Partition specification
- **`Properties`** - `map[string]string` with helper methods (`Get`, `GetBool`, `GetInt`)
- **`NameMapping`** - Column name-to-ID mapping
- **`Transform`** - Partition transforms (identity, bucket, truncate, etc.)
- **`BooleanExpression`** / **`UnboundExpression`** - Filter expressions

### `catalog` Package

- **`Catalog`** - Primary interface: `CreateTable`, `LoadTable`, `ListTables`, `DropTable`, `RenameTable`, namespace operations
- **`Type`** - Catalog type constants: `REST`, `Hive`, `Glue`, `DynamoDB`, `SQL`
- Catalog implementations self-register via blank imports: `import _ "github.com/apache/iceberg-go/catalog/rest"`

### `table` Package

- **`Table`** - Main table struct with metadata, identifier, filesystem
- **`Metadata`** - Table metadata interface (v1/v2/v3 implementations)
- **`Snapshot`** - Table snapshot with manifest list
- **`Identifier`** - `[]string` type alias for table identifiers
- **`CatalogIO`** - Interface for catalog-backed table operations
- **`SortOrder`** - Sort order specification

### `io` Package

- **`IO`** - Minimal filesystem interface (`Open`, `Remove`)
- **`ReadFileIO`** - Adds `ReadFile` for optimized reads
- **`WriteFileIO`** - Adds `Create` for creating writers

## Code Conventions

### License Headers

Every source file must include the Apache License 2.0 header. CI runs `dev/check-license` (Apache RAT) to verify. The header format:

```go
// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.
```

### Formatting and Style

- **Formatter:** `gofmt`, `gofumpt`, `goimports` (enforced by golangci-lint)
- Use `any` instead of `interface{}` (auto-rewrite rule in `.golangci.yml`)
- Blank line before return statements (enforced by `nlreturn` linter)
- Use `fmt.Sprintf` and friends efficiently (`perfsprint` linter)
- Errors checked except in test files (`errcheck` linter)
- No `fieldalignment` or `shadow` govet checks

### Error Handling

- Sentinel errors defined as package-level `var` using `errors.New()` or `fmt.Errorf`
- Root errors in `errors.go`: `ErrInvalidTypeString`, `ErrNotImplemented`, `ErrInvalidArgument`, etc.
- Catalog errors in `catalog/catalog.go`: `ErrNoSuchTable`, `ErrNoSuchNamespace`, etc.
- Use `fmt.Errorf("context: %w", err)` for wrapping

### Testing Patterns

- Test files follow `*_test.go` naming convention
- Integration test files use `*_integration_test.go` with `//go:build integration` tag
- Internal tests use `_internal_test.go` suffix (same package testing)
- Uses `github.com/stretchr/testify` for assertions (`assert`, `require`)
- Uses `github.com/google/go-cmp` for deep comparisons
- Benchmark tests use `*_bench_test.go` suffix
- Integration tests use `testcontainers-go` for Docker-based test infrastructure

### Code Generation

- `stringer` tool used for enum string methods: `//go:generate stringer -type=Operation -linecomment`
- Generated files have `// Code generated by ...; DO NOT EDIT.` header (e.g., `operation_string.go`)

### Import Aliasing

Common patterns seen throughout the codebase:

```go
icebergio "github.com/apache/iceberg-go/io"
iceinternal "github.com/apache/iceberg-go/internal"
tblutils "github.com/apache/iceberg-go/table/internal"
```

### Functional Options Pattern

- Used for configurable operations: `CreateTableOpt`, `CreateViewOpt`
- Options are function types that modify config structs

## CI Workflows

- **go-ci.yml** - Lint + unit tests on Go 1.23/1.24 across ubuntu/windows/macos
- **go-integration.yml** - Integration tests with Docker Compose on ubuntu
- **license_check.yml** - Apache RAT license header verification
- **codeql.yml** - GitHub CodeQL security scanning
- **audit-and-verify.yml** - Dependency auditing
- **labeler.yml** - Automatic PR labeling
- **rc.yml** - Release candidate workflow

## Pre-commit Hooks

The project uses `pre-commit` with golangci-lint (v2.8.0) configured to run `--fix` on Go files.

## Reference Implementation

The **Java implementation** ([apache/iceberg](https://github.com/apache/iceberg)) is the reference implementation of the Iceberg spec. It is feature-complete and should be the primary source of inspiration when implementing new features or understanding expected behavior. The Python (`pyiceberg`) and Go (`iceberg-go`) implementations lag behind in feature coverage, so when in doubt, look at how Java does it.

## Important Notes

- Data is read via Apache Arrow (`arrow.Record` batches or `arrow.Table`)
- Manifest files use Avro serialization (via `hamba/avro`)
- The project supports Iceberg table format v1, v2, and v3 (row-lineage)
- Catalog implementations use `go:linkname` for shared internal utilities
- The `internal` package is not importable outside the module
