package mysql

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/portablesql/psql"
)

var d = mysqlDialect{}

func TestExportArg(t *testing.T) {
	loc := time.FixedZone("JST", 9*3600)
	in := time.Date(2024, 3, 5, 12, 30, 0, 123456789, loc)
	if got := d.ExportArg(in); got != "2024-03-05 03:30:00.123456" {
		t.Errorf("ExportArg(time) = %q", got)
	}
	if got := d.ExportArg(time.Time{}); got != "0000-00-00 00:00:00.000000" {
		t.Errorf("ExportArg(zero) = %q", got)
	}
	var nilTime *time.Time
	if got := d.ExportArg(nilTime); got != nil {
		t.Errorf("ExportArg(nil *time.Time) = %v", got)
	}
	if got := d.ExportArg(&in); got != "2024-03-05 03:30:00.123456" {
		t.Errorf("ExportArg(*time.Time) = %v", got)
	}
	b := []byte{1, 2}
	if got, ok := d.ExportArg(b).([]byte); !ok || string(got) != string(b) {
		t.Errorf("ExportArg([]byte) = %v", got)
	}
	var nilStr *string
	if got := d.ExportArg(nilStr); got != nil {
		t.Errorf("ExportArg(nil *string) = %v", got)
	}
	v := psql.Vector{1, 2}
	if got, want := d.ExportArg(v), psql.DefaultExportArg(v); !reflect.DeepEqual(got, want) {
		t.Errorf("ExportArg(Vector) = %v, want %v", got, want)
	}
	if got := d.ExportArg(nil); got != nil {
		t.Errorf("ExportArg(nil) = %v", got)
	}
}

func TestLimitOffset(t *testing.T) {
	if got := d.LimitOffset(20, 10); got != "LIMIT 10 OFFSET 20" {
		t.Errorf("LimitOffset(20, 10) = %q", got)
	}
	if d.Placeholder(3) != "?" {
		t.Error("Placeholder should be ?")
	}
}

func TestSqlType(t *testing.T) {
	cases := []struct {
		base  string
		attrs map[string]string
		want  string
	}{
		{"enum", map[string]string{"values": "a,b"}, "enum('a','b')"},
		{"set", map[string]string{"values": "x,y,z"}, "set('x','y','z')"},
		{"enum", map[string]string{"values": "it's,ok"}, "enum('it''s','ok')"},
		{"enum", nil, ""},
		{"vector", map[string]string{"size": "3"}, "vector(3)"},
		{"vector", nil, "vector"},
		{"varchar", map[string]string{"size": "128"}, "varchar(128)"},
		{"bigint", nil, "bigint"},
		{"datetime", map[string]string{"size": "6"}, "datetime(6)"},
	}
	for _, c := range cases {
		if got := d.SqlType(c.base, c.attrs); got != c.want {
			t.Errorf("SqlType(%q, %v) = %q, want %q", c.base, c.attrs, got, c.want)
		}
	}
}

func TestFieldDef(t *testing.T) {
	cases := []struct {
		attrs map[string]string
		want  string
	}{
		{map[string]string{}, `"col" varchar(10)`},
		{map[string]string{"null": "0"}, `"col" varchar(10) NOT NULL`},
		{map[string]string{"null": "1"}, `"col" varchar(10) NULL`},
		{map[string]string{"null": "?"}, ``},
		{map[string]string{"default": "\\N"}, `"col" varchar(10) DEFAULT NULL`},
		{map[string]string{"null": "0", "default": "x"}, `"col" varchar(10) NOT NULL DEFAULT 'x'`},
		{map[string]string{"collation": "utf8mb4_bin"}, `"col" varchar(10) COLLATE utf8mb4_bin`},
	}
	for _, c := range cases {
		if got := d.FieldDef("col", "varchar(10)", false, c.attrs); got != c.want {
			t.Errorf("FieldDef(%v) = %q, want %q", c.attrs, got, c.want)
		}
		if got := d.FieldDefAlter("col", "varchar(10)", false, c.attrs); got != c.want {
			t.Errorf("FieldDefAlter(%v) = %q, want %q", c.attrs, got, c.want)
		}
	}
}

func TestKeyRendering(t *testing.T) {
	primary := &psql.StructKey{Key: "PRIMARY", Typ: psql.KeyPrimary, Fields: []string{"id"}}
	unique := &psql.StructKey{Key: "u", Typ: psql.KeyUnique, Fields: []string{"a", "b"}}
	index := &psql.StructKey{Key: "i", Typ: psql.KeyIndex, Fields: []string{"x"}}
	fulltext := &psql.StructKey{Key: "f", Typ: psql.KeyFulltext, Fields: []string{"body"}}
	spatial := &psql.StructKey{Key: "s", Typ: psql.KeySpatial, Fields: []string{"geo"}}
	vector := &psql.StructKey{Key: "v", Typ: psql.KeyVector, Fields: []string{"emb"}}

	cases := []struct {
		k    *psql.StructKey
		want string
	}{
		{primary, `PRIMARY KEY ("id")`},
		{unique, `UNIQUE INDEX "u"("a", "b")`},
		{index, `INDEX "i"("x")`},
		{fulltext, `FULLTEXT INDEX "f"("body")`},
		{spatial, `SPATIAL INDEX "s"("geo")`},
		{vector, ``},
	}
	for _, c := range cases {
		if got := d.InlineKeyDef(c.k, "t"); got != c.want {
			t.Errorf("InlineKeyDef(%s) = %q, want %q", c.k.Key, got, c.want)
		}
		if got := d.KeyDef(c.k, "t"); got != c.want {
			t.Errorf("KeyDef(%s) = %q, want %q", c.k.Key, got, c.want)
		}
		if got := d.CreateIndex(c.k, "t"); got != "" {
			t.Errorf("CreateIndex(%s) = %q, want empty (all keys inline)", c.k.Key, got)
		}
	}
}

func TestUpsertRendering(t *testing.T) {
	if got, want := d.ReplaceSQL("t", `"a","b"`, "?,?", nil, nil), `REPLACE INTO "t" ("a","b") VALUES (?,?)`; got != want {
		t.Errorf("ReplaceSQL = %q, want %q", got, want)
	}
	if got, want := d.InsertIgnoreSQL("t", `"a"`, "?"), `INSERT IGNORE INTO "t" ("a") VALUES (?)`; got != want {
		t.Errorf("InsertIgnoreSQL = %q, want %q", got, want)
	}
}

func TestMatchDSN(t *testing.T) {
	f := mysqlFactory{}
	for _, dsn := range []string{
		"root:test@tcp(127.0.0.1:3306)/psql_test",
		"user@unix(/tmp/mysql.sock)/db?parseTime=true",
		"/db",
	} {
		if !f.MatchDSN(dsn) {
			t.Errorf("MatchDSN(%q) = false, want true", dsn)
		}
	}
	for _, dsn := range []string{
		":memory:",
		"foo.db",
		"file:test.db",
		"postgres://user@localhost/db",
		"postgresql://user:pw@localhost:5432/db?sslmode=disable",
		"mysql://user@tcp(localhost)/db",
		"host=localhost dbname=x",
	} {
		if f.MatchDSN(dsn) {
			t.Errorf("MatchDSN(%q) = true, want false", dsn)
		}
	}
}

func TestErrors(t *testing.T) {
	dup := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'x' for key 'PRIMARY'"}
	noTable := &mysql.MySQLError{Number: 1146, Message: "Table 'db.t' doesn't exist"}
	other := &mysql.MySQLError{Number: 1064, Message: "syntax error"}

	if got := d.ErrorNumber(nil); got != 0 {
		t.Errorf("ErrorNumber(nil) = %d", got)
	}
	if got := d.ErrorNumber(errors.New("plain")); got != 0xffff {
		t.Errorf("ErrorNumber(plain) = %d", got)
	}
	if got := d.ErrorNumber(dup); got != 1062 {
		t.Errorf("ErrorNumber(dup) = %d", got)
	}
	if got := d.ErrorNumber(fmt.Errorf("wrapped: %w", noTable)); got != 1146 {
		t.Errorf("ErrorNumber(wrapped) = %d", got)
	}
	if got := d.ErrorNumber(&psql.Error{Query: "SELECT 1", Err: other}); got != 1064 {
		t.Errorf("ErrorNumber(psql.Error) = %d", got)
	}
	if got := d.ErrorNumber(errors.Join(errors.New("ctx"), fmt.Errorf("x: %w", dup))); got != 1062 {
		t.Errorf("ErrorNumber(joined) = %d", got)
	}

	if !d.IsDuplicate(dup) || !d.IsDuplicate(errors.Join(errors.New("a"), dup)) {
		t.Error("IsDuplicate failed to detect 1062")
	}
	if d.IsDuplicate(noTable) || d.IsDuplicate(nil) || d.IsDuplicate(errors.New("x")) {
		t.Error("IsDuplicate false positive")
	}

	if !d.IsNotExist(noTable) || !d.IsNotExist(fmt.Errorf("w: %w", &mysql.MySQLError{Number: 1054})) {
		t.Error("IsNotExist failed to detect missing table/column")
	}
	if d.IsNotExist(dup) || d.IsNotExist(nil) || d.IsNotExist(errors.New("x")) {
		t.Error("IsNotExist false positive")
	}
}

func TestMergeParams(t *testing.T) {
	got := mergeParams(nil)
	if got["charset"] != DefaultCharset || got["sql_mode"] != DefaultSQLMode || len(got) != 2 {
		t.Errorf("mergeParams(nil) = %v", got)
	}

	in := map[string]string{
		"tls":       "custom",
		"parseTime": "true",
		"loc":       "UTC",
		"timeout":   "5s",
		"sql_mode":  "'ANSI,NO_BACKSLASH_ESCAPES,STRICT_TRANS_TABLES'",
	}
	got = mergeParams(in)
	for k, v := range in {
		if got[k] != v {
			t.Errorf("caller param %s = %q, want %q", k, got[k], v)
		}
	}
	if got["charset"] != DefaultCharset {
		t.Errorf("charset not defaulted: %q", got["charset"])
	}
	if got["sql_mode"] != in["sql_mode"] {
		t.Errorf("caller sql_mode overridden: %q", got["sql_mode"])
	}
	if len(in) != 5 {
		t.Error("mergeParams modified its input")
	}

	got = mergeParams(map[string]string{"charset": "latin1"})
	if got["charset"] != "latin1" {
		t.Errorf("caller charset overridden: %q", got["charset"])
	}
}

func TestNewDoesNotMutateConfig(t *testing.T) {
	cfg := mysql.NewConfig()
	cfg.User = "u"
	cfg.Net = "tcp"
	cfg.Addr = "127.0.0.1:1" // nothing listens here
	cfg.DBName = "db"
	cfg.Timeout = 200 * time.Millisecond
	cfg.Params = map[string]string{"parseTime": "true"}

	be, err := New(cfg)
	if err == nil {
		be.DB().Close()
		t.Skip("something is listening on 127.0.0.1:1")
	}
	if !strings.Contains(err.Error(), "SHOW VARIABLES") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(cfg.Params) != 1 || cfg.Params["parseTime"] != "true" {
		t.Errorf("New modified the caller's config: %v", cfg.Params)
	}

	// and the DSN that would be used carries both the caller's and default params
	merged := cfg.Clone()
	merged.Params = mergeParams(cfg.Params)
	dsn := merged.FormatDSN()
	for _, want := range []string{"parseTime=true", "charset=utf8mb4", "sql_mode="} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q lacks %q", dsn, want)
		}
	}
}
