package connection

import (
	"context"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
)

// signaturesKey is what LoadSignatures marks its loading by.
type signaturesKey struct{}

// LoadSignatures reads the server's built-in function signatures once, then calls then; the
// dialect's own table (sqltext.BuiltinSignatures) when the server has none or fails.
func LoadSignatures(r Runner, cn *Conn, then func(map[string][]db.Signature)) {
	if cn.Signatures != nil {
		if then != nil {
			then(cn.Signatures)
		}
		return
	}
	key := signaturesKey{}
	if cn.Loading[key] {
		return
	}
	cn.Loading[key] = true
	poolOf := cn.PoolFor("") // read on the main thread
	engine := cn.Config.Engine
	r.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var sigs []db.Signature
		var err error
		if reader, ok := db.DialectOf(engine).(db.SignatureReader); ok {
			var d *db.DB
			if d, err = poolOf(ctx); err == nil {
				sigs, err = reader.Signatures(ctx, d.Catalog())
			}
		}
		if err != nil || len(sigs) == 0 {
			sigs = sqltext.BuiltinSignatures(db.LexDialect(engine)) // a failed read falls back silently
		}
		byName := map[string][]db.Signature{}
		for _, s := range sigs {
			name := strings.ToLower(s.Name)
			byName[name] = append(byName[name], s)
		}
		return func() {
			delete(cn.Loading, key)
			cn.Signatures = byName
			if then != nil {
				then(byName)
			}
		}
	})
}
