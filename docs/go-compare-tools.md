# Go comparison, schema and export tools

The Go server handles schema diff preparation and SQL plans, data comparison and sync plans, table structure builders, SQLite rebuild preview/apply, administrative SQL builders, grid conditions and searches, in-memory SQL export, documentation snapshots/HTML export, and EXPLAIN SQL/plan retrieval.

SQLite, MySQL and PostgreSQL are the implemented native SQL dialects for comparison and schema tools. Other database drivers remain separate capabilities; having an Agent connection does not establish SQL-builder compatibility with these three engines.

## Verified behavior

- Real SQLite data comparison statements produce the expected target rows, with quoted text, JSON, binary cells and generated-column omission. Duplicate comparison keys fail explicitly.
- SQLite rebuild preserves stored data, STRICT, CHECK/UNIQUE constraints, generated columns, expression/partial indexes, triggers and AUTOINCREMENT counters after deleted high IDs. A stale preview is rejected. A failed data copy rolls back the original schema and data. The database manager reserves writes, checks the schema revision again and validates foreign keys inside the rebuild transaction.
- Schema synchronization drops changed foreign keys before referenced tables and orders table creation by dependencies. MySQL/PostgreSQL table-level foreign keys are added after creation, including composite columns and PostgreSQL deferral metadata. Native snake_case sequence/owner metadata and overloaded routine names are accepted. Routine CREATE headers are qualified to the target schema.
- Export preserves nullable type/extra positions, binary/JSON literals and generated-column omission, rejects SQL expressions supplied as qualified table names, and adds PostgreSQL array casts/identity overrides. MySQL/PostGIS spatial constructors include SRID metadata.
- Documentation export escapes HTML content and applies annotation precedence through the shared annotations implementation. SQLite EXPLAIN retrieval returns a real database plan. ANALYZE is restricted to the existing conservative read-only SQL subset.
- The optional PostgreSQL catalog integration test is skipped when `DBX_TEST_POSTGRES_DSN` is absent. This environment did not provide a live MySQL/PostgreSQL server, so their generated SQL/catalog queries have not been certified against live servers here.

Run the focused backend checks with:

```sh
go test -mod=mod -race ./internal/server -run 'TestSQLiteStructure|TestSQLExport|TestPostgresSQLExport|TestSchema|TestExplainPlan|TestAdvancedStructure|TestDataComparison|TestDocs|TestMarketplace' -count=1
go test -mod=mod ./internal/database -run 'TestSQLiteConstraint|TestPostgresPartition|TestLivePostgresAdvanced' -count=1
```

## Scope limits

The schema engine is not a complete replacement for every Rust migration option. Automatic table rename detection, arbitrary permission synchronization, batch/shard planning, expression/operator-class index editing, complete PostgreSQL identity edits and MySQL ENUM/SET column reconstruction remain unimplemented. Inline foreign keys require explicit table-level foreign key DDL for dependency-safe sync. Routine bodies are preserved verbatim and can retain source-schema references.

SQLite changes involving renamed/dropped constrained columns or dependent triggers/views fail closed when the dependency cannot be safely reconstructed. Documentation HTML is a searchable standalone report; it does not embed the original application's full diagram/navigation viewer. Data comparisons exceeding the configured row budget are explicitly marked partial, with no claim that an ordered prefix is a representative sample. Inverse DDL cannot restore lost rows/converted values, and MySQL DDL does not provide atomic rollback.

In-memory database SQL exports are bounded by supplied rows and retain explicit partial-data warnings. Full export type coverage, multi-schema dependency identity and table-level MySQL AUTO_INCREMENT-option normalization remain separate follow-up work. SQL reference analysis was not implemented in this change; the existing frontend semantics can be adapted without claiming full server AST parity.

## Plugin marketplace

The Go marketplace stores custom/enterprise repositories, loads the official catalog with a fallback URL, resolves relative artifacts, selects exact-platform or universal packages, checks bounded downloads, SHA-256/size and package signing identity, and delegates installation to the shared verified ZIP installer. The local fixture verifies a real signed package install and protects an existing installation when the catalog hash is wrong.
