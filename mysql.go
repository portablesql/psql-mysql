// Package mysql registers the MySQL/MariaDB dialect and backend factory for psql.
//
// Import this package with a blank identifier to enable MySQL support:
//
//	import _ "github.com/portablesql/psql-mysql"
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/portablesql/psql"
)

func init() {
	psql.RegisterDialect(psql.EngineMySQL, mysqlDialect{})
	psql.RegisterBackendFactory(&mysqlFactory{})

	// Register engine-specific magic types
	psql.DefineMagicTypeEngine(psql.EngineMySQL, "DATETIME", "type=DATETIME,size=6")
	psql.DefineMagicTypeEngine(psql.EngineMySQL, "JSON", "type=LONGTEXT,format=json")
}

// mysqlDialect implements psql.Dialect and optional interfaces for MySQL.
type mysqlDialect struct{}

func (mysqlDialect) Placeholder(_ int) string { return "?" }

// LimitOffset renders "LIMIT count OFFSET offset". The arguments are
// (offset, count), matching psql's QueryBuilder.Limit(offset, count).
//
// Deprecated: the core renders LIMIT/OFFSET itself and no longer calls this.
func (mysqlDialect) LimitOffset(offset, count int) string {
	return "LIMIT " + strconv.Itoa(count) + " OFFSET " + strconv.Itoa(offset)
}

func (mysqlDialect) ExportArg(v any) any {
	switch val := v.(type) {
	case time.Time:
		if val.IsZero() {
			return "0000-00-00 00:00:00.000000"
		}
		return val.UTC().Format("2006-01-02 15:04:05.999999")
	case *time.Time:
		if val == nil {
			return nil
		}
		return val.UTC().Format("2006-01-02 15:04:05.999999")
	}
	return psql.DefaultExportArg(v)
}

// TypeMapper implementation

func (mysqlDialect) SqlType(baseType string, attrs map[string]string) string {
	switch baseType {
	case "enum", "set":
		if myvals, ok := attrs["values"]; ok {
			l := strings.Split(myvals, ",")
			for i, v := range l {
				// NO_BACKSLASH_ESCAPES is set, so quotes are doubled
				l[i] = strings.ReplaceAll(v, "'", "''")
			}
			return baseType + "('" + strings.Join(l, "','") + "')"
		}
		return ""
	case "vector":
		if mysize, ok := attrs["size"]; ok {
			return "vector(" + mysize + ")"
		}
		return "vector"
	default:
		if mysize, ok := attrs["size"]; ok {
			return baseType + "(" + mysize + ")"
		}
		return baseType
	}
}

// isAutoInc reports whether the resolved attributes declare an
// auto-increment column (the core stores the autoinc attribute as autoinc=1).
func isAutoInc(attrs map[string]string) bool {
	switch attrs["autoinc"] {
	case "1", "true":
		return true
	}
	return false
}

// FieldDef renders a column definition. An autoinc column (see
// psql.StructField.IsAutoInc) gets the AUTO_INCREMENT attribute:
//
//	"ID" bigint(20) NOT NULL AUTO_INCREMENT
//
// A default attribute is ignored on such a column (MySQL rejects both).
func (mysqlDialect) FieldDef(column, sqlType string, nullable bool, attrs map[string]string) string {
	mydef := psql.QuoteName(column) + " " + sqlType
	autoinc := isAutoInc(attrs)

	if null, ok := attrs["null"]; ok {
		switch null {
		case "0", "false":
			mydef += " NOT NULL"
		case "1", "true":
			mydef += " NULL"
		default:
			return ""
		}
	}
	if def, ok := attrs["default"]; ok && !autoinc {
		if def == "\\N" {
			mydef += " DEFAULT NULL"
		} else {
			mydef += " DEFAULT " + psql.Escape(def)
		}
	}
	if autoinc {
		mydef += " AUTO_INCREMENT"
	}

	if mycol, ok := attrs["collation"]; ok {
		mydef += " COLLATE " + mycol
	}

	return mydef
}

func (d mysqlDialect) FieldDefAlter(column, sqlType string, nullable bool, attrs map[string]string) string {
	return d.FieldDef(column, sqlType, nullable, attrs)
}

// KeyRenderer implementation

func (mysqlDialect) KeyDef(k *psql.StructKey, tableName string) string {
	return inlineKeyDef(k)
}

func (mysqlDialect) InlineKeyDef(k *psql.StructKey, tableName string) string {
	return inlineKeyDef(k)
}

func (mysqlDialect) CreateIndex(k *psql.StructKey, tableName string) string {
	return "" // MySQL creates all keys inline
}

// inlineKeyDef renders the inline key definition used both in CREATE TABLE
// and in ALTER TABLE ... ADD. Keys without a column list (declared with an
// expression attribute only) and GIN/GiST keys have no MySQL rendering and
// are skipped with a warning; VECTOR keys are skipped silently.
func inlineKeyDef(k *psql.StructKey) string {
	s := &strings.Builder{}

	if len(k.Fields) == 0 {
		slog.Warn(fmt.Sprintf("[psql:check] key %s has no columns (expression indexes are not rendered on MySQL) and is skipped", k.Key),
			"event", "psql:check:skip_index", "psql.key", k.Key)
		return ""
	}

	switch k.Typ {
	case psql.KeyPrimary:
		s.WriteString("PRIMARY KEY ")
	case psql.KeyUnique:
		s.WriteString("UNIQUE INDEX ")
		s.WriteString(psql.QuoteName(k.Key))
	case psql.KeyIndex:
		s.WriteString("INDEX ")
		s.WriteString(psql.QuoteName(k.Key))
	case psql.KeyFulltext:
		s.WriteString("FULLTEXT INDEX ")
		s.WriteString(psql.QuoteName(k.Key))
	case psql.KeySpatial:
		s.WriteString("SPATIAL INDEX ")
		s.WriteString(psql.QuoteName(k.Key))
	case psql.KeyGIN, psql.KeyGIST:
		// PostgreSQL index methods: no MySQL equivalent, the key is skipped
		slog.Warn(fmt.Sprintf("[psql:check] key %s (GIN/GiST) is PostgreSQL-only and is skipped on MySQL", k.Key),
			"event", "psql:check:skip_index", "psql.key", k.Key)
		return ""
	case psql.KeyVector:
		return ""
	default:
		return ""
	}

	s.WriteByte('(')
	for n, f := range k.Fields {
		if n > 0 {
			s.WriteString(", ")
		}
		s.WriteString(psql.QuoteName(f))
	}
	s.WriteByte(')')
	return s.String()
}

// UpsertRenderer implementation

func (mysqlDialect) ReplaceSQL(tableName, fldStr, placeholders string, mainKey *psql.StructKey, fields []*psql.StructField) string {
	return "REPLACE INTO " + psql.QuoteName(tableName) + " (" + fldStr + ") VALUES (" + placeholders + ")"
}

func (mysqlDialect) InsertIgnoreSQL(tableName, fldStr, placeholders string) string {
	return "INSERT IGNORE INTO " + psql.QuoteName(tableName) + " (" + fldStr + ") VALUES (" + placeholders + ")"
}

// ErrorClassifier implementation

// ErrorNumber returns the MySQL error number found anywhere in the error tree
// (including joined errors), 0 for a nil error and 0xffff when no MySQL error
// is present.
func (mysqlDialect) ErrorNumber(err error) uint16 {
	if err == nil {
		return 0
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0xffff
}

// IsNotExist reports whether err is a MySQL error about a missing database,
// table, column or key.
func (d mysqlDialect) IsNotExist(err error) bool {
	switch d.ErrorNumber(err) {
	case 1049, // Unknown database
		1051, // Unknown table
		1054, // Unknown column
		1091, // Can't DROP; check that column/key exists
		1109, // Unknown table in ...
		1146, // Table doesn't exist
		1176: // Key doesn't exist in table
		return true
	}
	return false
}

// DuplicateChecker implementation

func (d mysqlDialect) IsDuplicate(err error) bool {
	return d.ErrorNumber(err) == 1062
}

// VariantAware implementation

// SupportsFeature implements psql.VariantAware. MySQL and MariaDB share
// named locks (GET_LOCK), CTEs, JSON functions, full-text search and
// AUTO_INCREMENT columns; RETURNING exists on MariaDB only (INSERT and
// DELETE, see the README). DISTINCT ON, LISTEN/NOTIFY, AS OF SYSTEM TIME,
// row TTL, vector distance operators and native bulk loading are not
// available.
func (mysqlDialect) SupportsFeature(v psql.Variant, feature string) bool {
	switch feature {
	case psql.FeatureReturning:
		return v == psql.VariantMariaDB
	case psql.FeatureAdvisoryLocks, psql.FeatureCTE, psql.FeatureJSON,
		psql.FeatureFullText, psql.FeatureIdentityColumns:
		return true
	default:
		return false
	}
}

// RetryableChecker implementation

// IsRetryable implements psql.RetryableChecker: a deadlock (error 1213) or
// a lock wait timeout (error 1205) anywhere in the error tree marks the
// transaction as worth retrying from the beginning.
func (d mysqlDialect) IsRetryable(err error) bool {
	switch d.ErrorNumber(err) {
	case 1213, 1205:
		return true
	}
	return false
}

// LockRenderer implementation

// AcquireLockSQL implements psql.LockRenderer with GET_LOCK, whose locks
// belong to the session and survive transactions:
//
//	SELECT GET_LOCK(?, ?)
//
// The second argument is the wait in seconds: -1 (wait forever) for a zero
// timeout, 0 (try once) for a negative one, otherwise the timeout rounded up
// to whole seconds. GET_LOCK returns 1 on success, 0 on timeout and NULL on
// error, which psql.NamedLock interprets.
func (mysqlDialect) AcquireLockSQL(name string, timeout time.Duration) (string, []any, error) {
	var wait int64
	switch {
	case timeout == 0:
		wait = -1
	case timeout < 0:
		wait = 0
	default:
		wait = int64(math.Ceil(timeout.Seconds()))
	}
	return "SELECT GET_LOCK(?, ?)", []any{name, wait}, nil
}

// ReleaseLockSQL implements psql.LockRenderer: SELECT RELEASE_LOCK(?).
func (mysqlDialect) ReleaseLockSQL(name string) (string, []any, error) {
	return "SELECT RELEASE_LOCK(?)", []any{name}, nil
}

// SchemaChecker implementation

func (mysqlDialect) CheckStructure(ctx context.Context, be *psql.Backend, tv psql.TableView) error {
	return checkStructureMySQL(ctx, be, tv)
}

// mysqlFactory implements psql.BackendFactory for MySQL DSNs.
type mysqlFactory struct{}

// MatchDSN reports whether dsn is a go-sql-driver/mysql DSN
// ("[user[:password]@][net[(addr)]]/dbname[?param=value]"). URL-style DSNs
// such as "postgres://..." and bare file names are rejected.
func (mysqlFactory) MatchDSN(dsn string) bool {
	if hasURLScheme(dsn) {
		return false
	}
	_, err := mysql.ParseDSN(dsn)
	return err == nil
}

// hasURLScheme reports whether s starts with "scheme://".
func hasURLScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for n, c := range s[:i] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case n > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

func (mysqlFactory) CreateBackend(dsn string) (*psql.Backend, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	return New(cfg)
}

// DefaultCharset is the "charset" connection parameter used when the caller's
// config does not set one.
const DefaultCharset = "utf8mb4"

// DefaultSQLMode is the "sql_mode" connection parameter used when the caller's
// config does not set one. ANSI enables double-quoted identifiers, which psql
// relies on, and NO_BACKSLASH_ESCAPES makes string escaping portable.
const DefaultSQLMode = "'ANSI,NO_BACKSLASH_ESCAPES'"

// mergeParams returns a copy of params with charset and sql_mode filled in
// when absent. The caller's values are never overridden.
func mergeParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params)+2)
	for k, v := range params {
		out[k] = v
	}
	if _, ok := out["charset"]; !ok {
		out["charset"] = DefaultCharset
	}
	if _, ok := out["sql_mode"]; !ok {
		out["sql_mode"] = DefaultSQLMode
	}
	return out
}

// New creates a psql.Backend connected to a MySQL database using the given
// mysql.Config and configures connection pooling.
//
// The config is not modified: a copy is used, in which cfg.Params is merged
// with the defaults. Every parameter set by the caller (tls, parseTime, loc,
// timeouts, ...) is kept as is; only "charset" (default [DefaultCharset]) and
// "sql_mode" (default [DefaultSQLMode]) are added when absent.
//
// Note that a session sql_mode replaces the server default, so when the
// default is used, strict mode is off for this connection even if the server
// enables it globally. To keep strict mode, set sql_mode yourself and include
// ANSI and NO_BACKSLASH_ESCAPES, which psql requires:
//
//	cfg.Params = map[string]string{
//		"sql_mode": "'ANSI,NO_BACKSLASH_ESCAPES,STRICT_TRANS_TABLES'",
//	}
func New(cfg *mysql.Config) (*psql.Backend, error) {
	cfg = cfg.Clone()
	cfg.Params = mergeParams(cfg.Params)

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	version, err := logServerVersion(db)
	if err != nil {
		db.Close()
		return nil, err
	}

	be := psql.NewBackend(psql.EngineMySQL, db, psql.WithPoolDefaults,
		psql.WithServerVersion(version),
		psql.WithVariant(detectVariant(version)),
	)
	return be, nil
}

// logServerVersion logs the server version variables at debug level and
// returns the server version, "version" followed by "version_comment" when
// present (e.g. "8.0.36 MySQL Community Server - GPL" or
// "11.4.2-MariaDB MariaDB Server"). It also serves as the initial
// connectivity check.
func logServerVersion(db *sql.DB) (string, error) {
	res, err := db.Query("SHOW VARIABLES LIKE 'version%'")
	if err != nil {
		return "", fmt.Errorf("SHOW VARIABLES failed: %w", err)
	}
	defer res.Close()

	var version, comment string
	for res.Next() {
		var k, v string
		if err := res.Scan(&k, &v); err != nil {
			return "", fmt.Errorf("SHOW VARIABLES scan failed: %w", err)
		}
		switch k {
		case "version":
			version = v
		case "version_comment":
			comment = v
		}
		slog.Debug(fmt.Sprintf("[mysql] %s = %s", k, v), "event", "psql:init:dbvar", "psql.dbvar", k)
	}
	if err := res.Err(); err != nil {
		return "", fmt.Errorf("SHOW VARIABLES failed: %w", err)
	}
	if comment != "" {
		version += " " + comment
	}
	return version, nil
}

// detectVariant identifies the product from the server version string:
// MariaDB reports "10.11.6-MariaDB..." (and "MariaDB Server" as
// version_comment), anything else is Oracle MySQL.
func detectVariant(version string) psql.Variant {
	if strings.Contains(strings.ToLower(version), "mariadb") {
		return psql.VariantMariaDB
	}
	return psql.VariantMySQL
}

// InitCfg creates a new MySQL Backend from the given config and sets it as psql.DefaultBackend.
func InitCfg(cfg *mysql.Config) error {
	be, err := New(cfg)
	if err != nil {
		return err
	}
	psql.DefaultBackend = be
	return nil
}
