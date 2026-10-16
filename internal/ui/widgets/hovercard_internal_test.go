package widgets

import "testing"

// Keys come first, then foreign keys, then the rest in their order.
func TestOrderColumns(t *testing.T) {
	got := orderColumns([]CardColumn{{Name: "note"}, {Name: "customer_id", Ref: "customers"}, {Name: "id", Key: true}, {Name: "at"}})
	want := []string{"id", "customer_id", "note", "at"}
	for i, col := range got {
		if col.Name != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}
