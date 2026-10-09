package db

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
)

// DistinctValues reads up to limit values of a column, without NULL or
// repeats, as text an INSERT casts back to them: the values a column
// referring to it may take.
func DistinctValues(ctx context.Context, d *DB, schema, table string, col Column, limit int) ([]string, error) {
	name := d.Dialect.Quote(col.Name)
	rows, err := d.Catalog().QueryContext(ctx, "SELECT DISTINCT "+name+" FROM "+QualifiedName(d.Dialect, schema, table)+
		" WHERE "+name+" IS NOT NULL LIMIT "+strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	canonical := CanonicalType(d.Config.Engine, col.Type)
	var out []string
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		switch x := ValueText(v, canonical).(type) {
		case string:
			out = append(out, x)
		case []byte:
			out = append(out, string(x))
		}
	}
	return out, rows.Err()
}

// NextNumber is the first whole number past the highest value of a
// column of numbers, 1 when it has none: where new keys start.
func NextNumber(ctx context.Context, d *DB, schema, table, column string) (int64, error) {
	rows, err := d.Catalog().QueryContext(ctx, "SELECT MAX("+d.Dialect.Quote(column)+") FROM "+QualifiedName(d.Dialect, schema, table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var top any
	if rows.Next() {
		if err := rows.Scan(&top); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil || top == nil {
		return 1, err
	}
	// As text, exactly: a float loses the last digits of a large key.
	text, _ := comparableText(top, "DECIMAL")
	n, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, fmt.Errorf("the highest %s, %s, is not a number", column, text)
	}
	// Toward zero, then up one: the floor's next, but for a negative
	// fraction, whose floor is one below.
	next := new(big.Int).Quo(n.Num(), n.Denom())
	if n.Sign() >= 0 || n.IsInt() {
		next.Add(next, big.NewInt(1))
	}
	if !next.IsInt64() {
		return 0, fmt.Errorf("%s holds numbers past the largest a key continues from", column)
	}
	return next.Int64(), nil
}
