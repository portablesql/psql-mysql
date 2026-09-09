package mysql

import (
	"strings"
	"testing"

	"github.com/portablesql/psql"
)

type fbVecTable struct {
	psql.Name `sql:"fb_vec"`
	ID        uint64      `sql:",key=PRIMARY"`
	Emb       psql.Vector `sql:",type=VECTOR,size=3"`
	VecIdx    psql.Key    `sql:"emb_idx,type=VECTOR,fields='Emb'"`
}

type fbUntyped struct {
	psql.Name `sql:"fb_untyped"`
	ID        uint64 `sql:",key=PRIMARY"`
	Label     string
}

// Keys MySQL cannot express (VECTOR) are skipped instead of producing an empty
// definition, and a column without a resolvable type is an error.
func TestCreateTableSQL(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	stmt, err := createTableSQL(be, psql.Table[fbVecTable]())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stmt, ", )") || strings.Contains(stmt, ",)") {
		t.Fatalf("empty key definition rendered: %s", stmt)
	}
	if _, err := createTableSQL(be, psql.Table[fbUntyped]()); err == nil {
		t.Fatal("untyped string column must be reported")
	}
}
