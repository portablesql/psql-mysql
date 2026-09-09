package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/portablesql/psql"
)

// compile-time checks of the optional interfaces the dialect implements
var (
	_ psql.VariantAware     = mysqlDialect{}
	_ psql.RetryableChecker = mysqlDialect{}
	_ psql.LockRenderer     = mysqlDialect{}
)

// The dialect deliberately implements neither psql.ReturningRenderer nor
// psql.ReturningStatements: a dialect has no backend, so it cannot tell
// MySQL from MariaDB, and the core falls back to the detected variant
// (MariaDB: INSERT and DELETE, MySQL: none) when both are absent.
func TestNoReturningRenderer(t *testing.T) {
	var d any = mysqlDialect{}
	if _, ok := d.(psql.ReturningRenderer); ok {
		t.Error("mysqlDialect must not implement ReturningRenderer (it would hide the variant)")
	}
	if _, ok := d.(psql.ReturningStatements); ok {
		t.Error("mysqlDialect must not implement ReturningStatements")
	}
}

func TestDetectVariant(t *testing.T) {
	cases := map[string]psql.Variant{
		"8.0.36 MySQL Community Server - GPL": psql.VariantMySQL,
		"8.4.0":                               psql.VariantMySQL,
		"10.11.6-MariaDB-1:10.11.6+maria~ubu2204 mariadb.org binary distribution": psql.VariantMariaDB,
		"11.4.2-MariaDB MariaDB Server":                                           psql.VariantMariaDB,
		"":                                                                        psql.VariantMySQL,
	}
	for in, want := range cases {
		if got := detectVariant(in); got != want {
			t.Errorf("detectVariant(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSupportsFeature(t *testing.T) {
	my, maria := psql.VariantMySQL, psql.VariantMariaDB
	if d.SupportsFeature(my, psql.FeatureReturning) || !d.SupportsFeature(maria, psql.FeatureReturning) {
		t.Error("RETURNING must be MariaDB-only")
	}
	for _, f := range []string{psql.FeatureAdvisoryLocks, psql.FeatureCTE, psql.FeatureJSON, psql.FeatureFullText, psql.FeatureIdentityColumns} {
		if !d.SupportsFeature(my, f) || !d.SupportsFeature(maria, f) {
			t.Errorf("%s must be supported on both products", f)
		}
	}
	for _, f := range []string{psql.FeatureDistinctOn, psql.FeatureListenNotify, psql.FeatureAsOfSystemTime, psql.FeatureRowTTL, psql.FeatureVectors, psql.FeatureBulkCopy, "nope"} {
		if d.SupportsFeature(my, f) || d.SupportsFeature(maria, f) {
			t.Errorf("%s must not be supported", f)
		}
	}
	be := psql.NewBackend(psql.EngineMySQL, nil, psql.WithVariant(maria), psql.WithServerVersion("11.4.2-MariaDB"))
	if !be.Supports(psql.FeatureReturning) || be.ServerVersion() != "11.4.2-MariaDB" {
		t.Error("Backend.Supports does not consult the dialect")
	}
	if be = psql.NewBackend(psql.EngineMySQL, nil); be.Variant() != my || be.Supports(psql.FeatureReturning) {
		t.Error("default variant must be MySQL")
	}
}

// RETURNING rendering follows the detected variant through the core.
func TestReturningByVariant(t *testing.T) {
	maria := psql.NewBackend(psql.EngineMySQL, nil, psql.WithVariant(psql.VariantMariaDB)).Plug(context.Background())
	my := psql.NewBackend(psql.EngineMySQL, nil).Plug(context.Background())

	ins := func() *psql.QueryBuilder {
		return psql.B().Insert().Table("t").Set(map[string]any{"a": 1}).Returning("id")
	}
	upd := func() *psql.QueryBuilder {
		return psql.B().Update("t").Set(map[string]any{"a": 1}).Where(map[string]any{"id": 1}).Returning("id")
	}
	del := func() *psql.QueryBuilder {
		return psql.B().Delete().From("t").Where(map[string]any{"id": 1}).Returning("id")
	}

	if q, err := ins().Render(maria); err != nil || !strings.HasSuffix(q, `RETURNING "id"`) {
		t.Errorf("MariaDB INSERT RETURNING = %q, %v", q, err)
	}
	if q, err := del().Render(maria); err != nil || !strings.HasSuffix(q, `RETURNING "id"`) {
		t.Errorf("MariaDB DELETE RETURNING = %q, %v", q, err)
	}
	if _, err := upd().Render(maria); !errors.Is(err, psql.ErrNotSupported) {
		t.Errorf("MariaDB UPDATE RETURNING: err = %v, want ErrNotSupported", err)
	}
	for name, q := range map[string]*psql.QueryBuilder{"INSERT": ins(), "UPDATE": upd(), "DELETE": del()} {
		if _, err := q.Render(my); !errors.Is(err, psql.ErrNotSupported) {
			t.Errorf("MySQL %s RETURNING: err = %v, want ErrNotSupported", name, err)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
	waitTimeout := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	dup := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	for _, err := range []error{
		deadlock, waitTimeout,
		fmt.Errorf("tx: %w", deadlock),
		&psql.Error{Query: "UPDATE", Err: waitTimeout},
		errors.Join(errors.New("ctx"), &psql.Error{Query: "COMMIT", Err: deadlock}),
	} {
		if !d.IsRetryable(err) || !psql.IsRetryable(err) {
			t.Errorf("IsRetryable(%v) = false", err)
		}
	}
	for _, err := range []error{nil, dup, errors.New("plain"), &psql.Error{Query: "X", Err: dup}} {
		if d.IsRetryable(err) {
			t.Errorf("IsRetryable(%v) = true", err)
		}
	}
}

func TestLockSQL(t *testing.T) {
	cases := []struct {
		timeout time.Duration
		wait    int64
	}{
		{0, -1},
		{-1, 0},
		{-time.Hour, 0},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{time.Millisecond, 1},
		{time.Minute, 60},
	}
	for _, c := range cases {
		q, args, err := d.AcquireLockSQL("job", c.timeout)
		if err != nil {
			t.Fatal(err)
		}
		if q != "SELECT GET_LOCK(?, ?)" {
			t.Errorf("AcquireLockSQL(%s) = %q", c.timeout, q)
		}
		if len(args) != 2 || args[0] != "job" || args[1] != c.wait {
			t.Errorf("AcquireLockSQL(%s) args = %v, want [job %d]", c.timeout, args, c.wait)
		}
	}
	q, args, err := d.ReleaseLockSQL("job")
	if err != nil || q != "SELECT RELEASE_LOCK(?)" || len(args) != 1 || args[0] != "job" {
		t.Errorf("ReleaseLockSQL = %q, %v, %v", q, args, err)
	}
}

func TestFieldDefAutoInc(t *testing.T) {
	attrs := map[string]string{"null": "0", "autoinc": "1"}
	want := `"ID" bigint(20) NOT NULL AUTO_INCREMENT`
	if got := d.FieldDef("ID", "bigint(20)", false, attrs); got != want {
		t.Errorf("FieldDef(autoinc) = %q, want %q", got, want)
	}
	if got := d.FieldDefAlter("ID", "bigint(20)", false, attrs); got != want {
		t.Errorf("FieldDefAlter(autoinc) = %q, want %q", got, want)
	}
	attrs["default"] = "0"
	if got := d.FieldDef("ID", "bigint(20)", false, attrs); got != want {
		t.Errorf("FieldDef(autoinc+default) = %q, want %q", got, want)
	}
	if got := d.FieldDef("ID", "bigint(20)", false, map[string]string{"null": "0", "autoinc": "0"}); got != `"ID" bigint(20) NOT NULL` {
		t.Errorf("FieldDef(autoinc=0) = %q", got)
	}

	type autoItem struct {
		psql.Name `sql:"auto_item"`
		ID        uint64 `sql:",key=PRIMARY,autoinc"`
		Label     string `sql:",type=VARCHAR,size=32"`
	}
	be := psql.NewBackend(psql.EngineMySQL, nil)
	if got, want := psql.Table[autoItem]().AllFields()[0].DefString(be), `"ID" bigint(20) NOT NULL AUTO_INCREMENT`; got != want {
		t.Errorf("DefString = %q, want %q", got, want)
	}
	stmt, err := createTableSQL(be, psql.Table[autoItem]())
	if err != nil {
		t.Fatal(err)
	}
	if want := `CREATE TABLE "auto_item" ("ID" bigint(20) NOT NULL AUTO_INCREMENT, "Label" varchar(32), PRIMARY KEY ("ID"))`; stmt != want {
		t.Errorf("CREATE TABLE = %q, want %q", stmt, want)
	}
}

func TestGinGistSkipped(t *testing.T) {
	gin := &psql.StructKey{Key: "g", Typ: psql.KeyGIN, Fields: []string{"Data"}}
	gist := &psql.StructKey{Key: "s", Typ: psql.KeyGIST, Fields: []string{"Range"}}
	// a key declared with expression= only has no Fields: never render INDEX "e"()
	expr := &psql.StructKey{Key: "e", Typ: psql.KeyIndex, Attrs: map[string]string{"expression": "lower({Name})"}}
	noFields := &psql.StructKey{Key: "n", Typ: psql.KeyUnique}
	for _, k := range []*psql.StructKey{gin, gist, expr, noFields} {
		if got := d.InlineKeyDef(k, "t"); got != "" {
			t.Errorf("InlineKeyDef(%s) = %q, want empty", k.Key, got)
		}
		if got := d.KeyDef(k, "t"); got != "" {
			t.Errorf("KeyDef(%s) = %q, want empty", k.Key, got)
		}
	}

	type ginItem struct {
		psql.Name `sql:"gin_item"`
		ID        int64    `sql:",key=PRIMARY"`
		Data      string   `sql:",import=JSON"`
		DataIdx   psql.Key `sql:",type=GIN,fields='Data'"`
		LowerIdx  psql.Key `sql:",expression=\"lower({Data})\""`
	}
	be := psql.NewBackend(psql.EngineMySQL, nil)
	stmt, err := createTableSQL(be, psql.Table[ginItem]())
	if err != nil {
		t.Fatal(err)
	}
	if want := `CREATE TABLE "gin_item" ("ID" bigint(21) NOT NULL, "Data" longtext, PRIMARY KEY ("ID"))`; stmt != want {
		t.Errorf("CREATE TABLE = %q, want %q", stmt, want)
	}
}
