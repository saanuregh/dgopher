package connection

import (
	"context"
	"time"

	"dgopher/internal/db"
)

// ReadQuery runs a read the app runs for the user, as a dashboard panel's
// or the AI chat's, on a session of its own, in a read-only transaction
// where the engine has one: the server itself refuses writes, as a read
// the app classified may still call a function that writes. release
// closes the cursor, rolls the transaction back and closes the session;
// call it once the rows are read. On an error there is nothing to release.
func ReadQuery(ctx context.Context, pool *db.DB, cfg db.Config, database, sql string, args ...any) (cursor *db.Cursor, release func(), err error) {
	sess, err := OpenSession(ctx, pool, database)
	if err != nil {
		return nil, nil, err
	}
	begin := ""
	if r, ok := db.SQL(cfg.Engine); ok {
		begin = r.ReadOnlyBeginSQL()
	}
	return readOn(ctx, sess, begin, sql, args...)
}

// readSession is what a read needs of a db.Session.
type readSession interface {
	Exec(ctx context.Context, query string, args ...any) (int64, error)
	Query(ctx context.Context, query string, args ...any) (*db.Cursor, error)
	Rollback(ctx context.Context) error
	Close() error
}

func readOn(ctx context.Context, sess readSession, begin, sql string, args ...any) (*db.Cursor, func(), error) {
	var cursor *db.Cursor
	release := func() {
		if cursor != nil {
			cursor.Close()
		}
		if begin != "" {
			// Even when ctx has ended: the session goes back to the pool.
			rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sess.Rollback(rctx)
		}
		sess.Close()
	}
	if begin != "" {
		if _, err := sess.Exec(ctx, begin); err != nil {
			sess.Close()
			return nil, nil, err
		}
	}
	cursor, err := sess.Query(ctx, sql, args...)
	if err != nil {
		release()
		return nil, nil, err
	}
	return cursor, release, nil
}
