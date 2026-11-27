package safety

import (
	"fmt"

	"dgopher/internal/db"
	"dgopher/internal/mongosh"
)

// ReviewMongo applies the policy to a MongoDB shell statement, as
// ReviewRedis does to a Redis command: a read-only connection refuses
// writes, a risky statement asks everywhere and wants the connection's
// name typed on production, and any other write asks on production.
func ReviewMongo(cfg *db.Config, op mongosh.Op) Verdict {
	c := mongosh.Classify(op)
	v := Verdict{Writes: c.Writes}
	if cfg.ReadOnly && c.Writes {
		v.Blocked = fmt.Sprintf("%s is read-only: %s writes. Edit the connection to allow writes.", cfg.Name, c.Command)
		return v
	}
	if c.Risk != "" {
		v.Confirm = true
		v.Reasons = append(v.Reasons, c.Risk)
		if cfg.Env == db.Production {
			v.TypeName = true
		}
		return v
	}
	if cfg.Env == db.Production && c.Writes {
		v.Confirm = true
		v.Reasons = append(v.Reasons, c.Command+" changes data on production")
	}
	return v
}
