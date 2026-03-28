[![Go Reference](https://pkg.go.dev/badge/github.com/portablesql/psql-mysql.svg)](https://pkg.go.dev/github.com/portablesql/psql-mysql)

# psql-mysql

MySQL / MariaDB driver for [portablesql/psql](https://github.com/portablesql/psql).

## Installation

```bash
go get github.com/portablesql/psql-mysql
```

## Usage

Import with a blank identifier to register the driver automatically:

```go
import (
    "github.com/portablesql/psql"
    _ "github.com/portablesql/psql-mysql"
)

be, err := psql.New("user:password@tcp(localhost:3306)/mydb?charset=utf8mb4")
ctx := be.Plug(context.Background())
```

The DSN format follows the standard [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql#dsn-data-source-name) syntax.

## Features

- ANSI SQL mode with `NO_BACKSLASH_ESCAPES` enabled automatically
- Charset forced to `utf8mb4`
- `REPLACE INTO` and `INSERT IGNORE` for upsert/conflict handling
- `ON DUPLICATE KEY UPDATE` for `OnConflict().DoUpdate()`
- Full type support: `ENUM`, `SET`, `VECTOR`, `JSON` (stored as `LONGTEXT`)
- `DATETIME(6)` with microsecond precision
- Automatic table creation and schema migration
- `FULLTEXT` and `SPATIAL` index support
- Duplicate detection via error code 1062

## Underlying Driver

[github.com/go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) (pure Go, no CGO required).

## License

MIT - see [LICENSE](LICENSE).
