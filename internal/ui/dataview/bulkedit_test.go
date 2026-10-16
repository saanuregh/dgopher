package dataview

import (
	"testing"
)

func TestSetNullOverBlock(t *testing.T) {
	tt, g, src := selectionGrid(t)
	chooseBlock(tt, g, []int{1, 2, 3}, 1, 2)
	g.setSelectedNull(src)
	for r := 1; r <= 3; r++ {
		for c := 1; c <= 2; c++ {
			if v, ok := g.edits.updates[r][c]; !ok || v != nil {
				t.Fatalf("row %d col %d = %v, %v; want NULL", r, c, v, ok)
			}
		}
	}
	if len(g.edits.updates) != 3 || len(g.edits.updates[0]) != 0 || len(g.edits.updates[4]) != 0 {
		t.Fatalf("updates %v outside the block", g.edits.updates)
	}
}

func TestRevertOverBlock(t *testing.T) {
	tt, g, src := selectionGrid(t)
	chooseBlock(tt, g, []int{0, 1, 2, 3, 4}, 0, 2)
	g.setSelectedNull(src)
	chooseBlock(tt, g, []int{1, 2}, 1, 2)
	g.revertSelectedCells(src)
	for r := 1; r <= 2; r++ {
		if len(g.edits.updates[r]) != 1 {
			t.Fatalf("row %d after Revert Cells: %v; want only id", r, g.edits.updates[r])
		}
	}
	g.edits.deleted[4] = true
	chooseBlock(tt, g, []int{3, 4}, 0, 0)
	g.revertSelectedRows(src)
	if len(g.edits.updates[3]) != 0 || len(g.edits.updates[4]) != 0 || g.edits.deleted[4] {
		t.Fatalf("Revert Rows left %v, deleted %v", g.edits.updates, g.edits.deleted)
	}
	if len(g.edits.updates[0]) != 3 {
		t.Fatalf("row 0 reverted: %v", g.edits.updates[0])
	}
}

func TestBulkEditOneUndoStep(t *testing.T) {
	tt, g, src := selectionGrid(t)
	chooseBlock(tt, g, []int{0, 1, 2}, 0, 2)
	before := len(g.undoStack)
	g.setSelectedNull(src)
	if len(g.undoStack) != before+1 {
		t.Fatalf("undo steps %d; want %d", len(g.undoStack), before+1)
	}
	g.undo()
	if len(g.edits.updates) != 0 {
		t.Fatalf("undo left %v", g.edits.updates)
	}
	g.redo()
	if len(g.edits.updates) != 3 {
		t.Fatalf("redo gave %v", g.edits.updates)
	}
	g.revertSelectedRows(src)
	if len(g.undoStack) != before+2 {
		t.Fatalf("undo steps %d after Revert Rows; want %d", len(g.undoStack), before+2)
	}
}

func TestBulkEditSkipsReadOnly(t *testing.T) {
	tt, g, src := selectionGrid(t)
	g.readOnlyCols = map[int]string{1: "computed"}
	g.edits.deleted[2] = true
	chooseBlock(tt, g, []int{1, 2, 3}, 0, 2)
	g.setSelectedNull(src)
	g.setSelectedDefault(src)
	for r, u := range g.edits.updates {
		if _, ok := u[1]; ok || r == 2 {
			t.Fatalf("edited read-only or deleted cell: row %d %v", r, u)
		}
	}
	if len(g.edits.updates[1]) != 2 || len(g.edits.updates[3]) != 2 {
		t.Fatalf("updates %v; want id and note of rows 1 and 3", g.edits.updates)
	}
}
