[![Go Reference](https://pkg.go.dev/badge/github.com/portablesql/psql-mysql.svg)](https://pkg.go.dev/github.com/portablesql/psql-mysql)
[![Tests](https://github.com/portablesql/psql-mysql/actions/workflows/test.yml/badge.svg)](https://github.com/portablesql/psql-mysql/actions/workflows/test.yml)

# psql-mysql

MySQL / MariaDB driver for [portablesql/psql](https://github.com/portablesql/psql), built on
[go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) (pure Go, no CGO).

## Installation

```bash
go get github.com/portablesql/psql github.com/portablesql/psql-mysql
```

Requires Go 1.24 or later.

## Usage

The package registers its dialect and backend factory in `init()`. Import it with a blank
identifier and `psql.New(dsn)` auto-detects MySQL DSNs:

```go
import (
    "context"

    "github.com/portablesql/psql"
    _ "github.com/portablesql/psql-mysql"
)

be, err := psql.New("user:password@tcp(localhost:3306)/mydb?parseTime=true")
if err != nil {
    return err
}
ctx := be.Plug(context.Background())
```

### DSN format

`MatchDSN` accepts any DSN that [`mysql.ParseDSN`](https://pkg.go.dev/github.com/go-sql-driver/mysql#ParseDSN)
understands, i.e. the go-sql-driver/mysql syntax:

```
[user[:password]@][net[(addr)]]/dbname[?param=value&...]
```

Examples that are detected:

```
root:test@tcp(127.0.0.1:3306)/psql_test
user@unix(/tmp/mysql.sock)/db?parseTime=true
/db
```

URL-style strings (`mysql://...`, `postgres://...`), `:memory:` and file names are rejected so
that they can be picked up by the other drivers.

### Explicit construction

```go
import (
    "github.com/go-sql-driver/mysql"
    psqlmysql "github.com/portablesql/psql-mysql"
)

cfg, err := mysql.ParseDSN("user:password@tcp(localhost:3306)/mydb?parseTime=true")
be, err := psqlmysql.New(cfg)     // func New(cfg *mysql.Config) (*psql.Backend, error)
err = psqlmysql.InitCfg(cfg)      // same, but stores the backend in psql.DefaultBackend
```

`New` does not modify the config you pass in: it works on a clone whose `Params` are merged
with the defaults below. It opens the pool, runs `SHOW VARIABLES LIKE 'version%'` as a
connectivity check (logged at debug level) and applies `psql.WithPoolDefaults`
(128 max open, 32 idle, 3 minute max lifetime).

### Connection defaults

Only two parameters are added, and only when the caller did not set them. Everything else in
`cfg.Params` (and all other `mysql.Config` fields such as `tls`, `parseTime`, `loc`, timeouts)
is kept as is.

| Parameter  | Default (`DefaultCharset` / `DefaultSQLMode`) | Why |
|------------|-----------------------------------------------|-----|
| `charset`  | `utf8mb4`                                     | full Unicode |
| `sql_mode` | `'ANSI,NO_BACKSLASH_ESCAPES'`                 | `ANSI` enables double-quoted identifiers, which psql relies on; `NO_BACKSLASH_ESCAPES` makes string escaping portable |

A session `sql_mode` **replaces** the server default. With the default above, strict mode is
off for psql connections even if the server enables it globally. To keep strict mode (or any
other mode), set `sql_mode` yourself and include `ANSI` and `NO_BACKSLASH_ESCAPES`, which psql
requires:

```go
cfg.Params = map[string]string{
    "sql_mode": "'ANSI,NO_BACKSLASH_ESCAPES,STRICT_TRANS_TABLES'",
}
```

The same works through the DSN: `...?sql_mode=%27ANSI,NO_BACKSLASH_ESCAPES,STRICT_TRANS_TABLES%27`.

## Product detection

`New` reads `SHOW VARIABLES LIKE 'version%'` (also the initial connectivity check) and records
`version` and `version_comment` with `psql.WithServerVersion`. When they mention MariaDB the
backend's `Variant()` is `psql.VariantMariaDB`, otherwise `psql.VariantMySQL`. The dialect
implements `psql.VariantAware`, so `be.Supports(...)` answers per product:

| Feature | MySQL | MariaDB |
|---------|-------|---------|
| `FeatureAdvisoryLocks`, `FeatureCTE`, `FeatureJSON`, `FeatureFullText`, `FeatureIdentityColumns` | yes | yes |
| `FeatureReturning` | no | yes (`INSERT`/`REPLACE` and `DELETE` only) |
| `FeatureDistinctOn`, `FeatureListenNotify`, `FeatureAsOfSystemTime`, `FeatureRowTTL`, `FeatureVectors`, `FeatureBulkCopy` | no | no |

The dialect deliberately implements neither `psql.ReturningRenderer` nor
`psql.ReturningStatements`: a dialect has no backend and cannot tell the products apart, so the
core decides from the detected variant, which yields exactly MariaDB's `INSERT`/`DELETE ...
RETURNING` support and refuses `RETURNING` on MySQL with an error wrapping
`psql.ErrNotSupported`. As a consequence `psql.Insert` never uses `RETURNING` on this engine and
reads generated keys with `LastInsertId` on both products.

## Dialect behavior

- Placeholders are `?`; `Limit(offset, count)` renders `LIMIT count OFFSET offset`.
- `Replace` renders `REPLACE INTO`, `InsertIgnore` renders `INSERT IGNORE INTO`, and the query
  builder's `OnConflict().DoUpdate()` renders `ON DUPLICATE KEY UPDATE`.
- `time.Time` arguments are sent as UTC `2006-01-02 15:04:05.999999`; the zero time becomes
  `0000-00-00 00:00:00.000000`.
- `CILike` renders a plain `LIKE` (MySQL collations are case-insensitive by default);
  `FindInSet` renders `FIND_IN_SET`; `DateAdd`/`DateSub` render `INTERVAL` arithmetic.

### Type mapping (`SqlType`)

| Struct tag | Column type |
|------------|-------------|
| `type=enum,values=a,b` / `type=set,values=a,b` | `enum('a','b')` / `set('a','b')` (single quotes in values are doubled) |
| `type=VECTOR,size=N` | `vector(N)` (`vector` without size) |
| any other `type=T,size=N` | `T(N)`; `T` alone when no size is given |
| `type=DATETIME` (magic type) | `DATETIME(6)` |
| `type=JSON` (magic type) | `LONGTEXT` with `format=json` |

Column definitions honor `null=0/1` (`NOT NULL` / `NULL`), `default=...` (`default=\N` gives
`DEFAULT NULL`) and `collation=...` (`COLLATE ...`). A field declared with the `autoinc`
attribute (`ID uint64 \`sql:",key=PRIMARY,autoinc"\``) renders `AUTO_INCREMENT` after the
nullability (a `default` attribute is dropped on such a column):

```sql
"ID" bigint(20) NOT NULL AUTO_INCREMENT
```

### Keys and indexes

All keys are created inline in `CREATE TABLE` and added with `ALTER TABLE ... ADD`:
`PRIMARY KEY`, `UNIQUE INDEX name`, `INDEX name`, `FULLTEXT INDEX name` and
`SPATIAL INDEX name`. `VECTOR` keys are not rendered on MySQL. `GIN` and `GIST` keys are
PostgreSQL index methods and are skipped with a warning (`psql:check:skip_index`), in both the
`CREATE TABLE` and the `ALTER TABLE` paths; so are keys declared with an `expression` attribute
only (no column list), which MySQL's inline syntax cannot express.

A `FULLTEXT` key on the columns searched with `psql.FullText` is required for
`MATCH ... AGAINST` to work; the schema check creates it on new and existing tables.

### Schema check

On first use of a table (unless the backend was created with `psql.WithSchemaCheck(false)`,
in which case call `be.CheckStructure` yourself) the driver looks the table up in
`information_schema.tables`:

- missing table: `CREATE TABLE` with all columns and keys;
- existing table: columns are compared with `SHOW FIELDS` and keys with `SHOW INDEX`. Columns
  whose type, nullability or default differ are `MODIFY`'d, missing columns and keys are
  `ADD`ed in a single `ALTER TABLE`. An `autoinc` field whose column lacks the
  `auto_increment` extra is `MODIFY`'d to add it; an existing `AUTO_INCREMENT` column on a field
  without the attribute is tolerated and never downgraded. Columns and keys that exist in the
  database but not in the struct are logged as warnings and never dropped.

A table declared with `psql.Name \`sql:"name,check=0"\`` is never modified.

## Error classification

`psql.ErrorNumber(err)` returns the MySQL error number found anywhere in the error tree
(including wrapped and joined errors), `0` for `nil` and `0xffff` when no MySQL error is present.

- `psql.IsNotExist(err)` is true for errors 1049 (unknown database), 1051 (unknown table),
  1054 (unknown column), 1091 (can't DROP), 1109 (unknown table in ...), 1146 (table doesn't
  exist) and 1176 (key doesn't exist).
- `psql.IsDuplicate(err)` is true for error 1062 (duplicate entry).
- `psql.IsRetryable(err)` is true for errors 1213 (deadlock) and 1205 (lock wait timeout);
  `psql.Tx` and `psql.TxWithOptions` retry the transaction on these.

## Named locks

The dialect implements `psql.LockRenderer` with `GET_LOCK` / `RELEASE_LOCK`, so
`psql.NamedLock` and `psql.WithNamedLock` work on MySQL and MariaDB:

| call | statement |
|------|-----------|
| acquire, timeout `0` (wait) | `SELECT GET_LOCK(?, -1)` |
| acquire, timeout `< 0` (try) | `SELECT GET_LOCK(?, 0)` |
| acquire, timeout `> 0` | `SELECT GET_LOCK(?, <seconds, rounded up>)` |
| release | `SELECT RELEASE_LOCK(?)` |

`GET_LOCK` returns 1 on success and 0 when the wait times out (`psql.ErrLockTimeout`); `NULL`
(an error) is reported as a query error. Locks belong to the session, survive transactions and
are re-entrant on the same connection; `psql.NamedLock` therefore pins a connection until
`release` is called.

## Testing

Unit tests need no database server:

```bash
go test ./...
```

Integration tests live in [portablesql/psql-test](https://github.com/portablesql/psql-test).
Check out `psql`, `psql-mysql` and `psql-test` side by side, tie them together with a
workspace and point `PSQL_TEST_DSN` at a MySQL server:

```bash
go work init ./psql ./psql-mysql ./psql-test
cd psql-test
PSQL_TEST_DSN="root:test@tcp(127.0.0.1:3306)/psql_test" go test -race -count=1 ./...
```

CI runs exactly this against `mysql:8`.

## License

MIT, same as [psql](https://github.com/portablesql/psql/blob/master/LICENSE).
