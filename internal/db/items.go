package db

import (
	"context"
	"fmt"
	"strings"
)

// ItemKind is a kind of schema object besides tables and views.
type ItemKind string

const (
	ItemFunction   ItemKind = "function"
	ItemProcedure  ItemKind = "procedure"
	ItemTrigger    ItemKind = "trigger"
	ItemSequence   ItemKind = "sequence"
	ItemType       ItemKind = "type"
	ItemExtension  ItemKind = "extension"
	ItemEvent      ItemKind = "event"
	ItemProjection ItemKind = "projection"
	// ItemPartition is a partition of a PostgreSQL table, shown under the
	// table rather than with the schema's.
	ItemPartition ItemKind = "partition"
)

// ItemKinds are the kinds a schema's navigator shows as folders, in
// their order.
func ItemKinds() []ItemKind {
	return []ItemKind{ItemFunction, ItemProcedure, ItemTrigger, ItemSequence, ItemType, ItemExtension, ItemEvent, ItemProjection}
}

// Plural names a kind's folder.
func (k ItemKind) Plural() string {
	label := strings.ToUpper(string(k[:1])) + string(k[1:])
	return label + "s"
}

// Item is an object of a schema besides a table or a view: a routine, a
// trigger, a sequence, a type and the like.
type Item struct {
	Schema string
	Name   string
	Kind   ItemKind
	// Table is the table a trigger, a projection or a partition is of.
	Table string
	// Detail says more in a few words: a routine's arguments, a type's
	// sort, a partition's bounds.
	Detail string
	// ID is what tells the item apart from others of its name, as
	// overloaded functions: what ItemDDL finds it by.
	ID string
}

// Label names the item, with its arguments or its table.
func (it Item) Label() string {
	switch {
	case it.Kind == ItemFunction || it.Kind == ItemProcedure:
		return it.Name + "(" + it.Detail + ")"
	case it.Table != "" && it.Kind != ItemPartition:
		return it.Name + " on " + it.Table
	}
	return it.Name
}

// scanItems reads items from a statement giving kind, name, table, detail
// and ID, in that order.
func scanItems(ctx context.Context, q Querier, schema, query string, args ...any) ([]Item, error) {
	var out []Item
	err := scanRows(ctx, q, query, args, func(scan func(...any) error) error {
		it := Item{Schema: schema}
		var kind string
		if err := scan(&kind, &it.Name, &it.Table, &it.Detail, &it.ID); err != nil {
			return err
		}
		it.Kind = ItemKind(kind)
		out = append(out, it)
		return nil
	})
	return out, err
}

// errNoDefinition is the error of an item kind a dialect cannot write the
// definition of.
func errNoDefinition(it Item) error {
	return fmt.Errorf("DGopher cannot write the definition of the %s %s", it.Kind, it.Name)
}
