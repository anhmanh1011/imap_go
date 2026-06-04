package tgbot

import (
	"path/filepath"
	"testing"
)

func newTestState(t *testing.T) *State {
	t.Helper()
	st, err := NewState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStateInsertHasComplete(t *testing.T) {
	st := newTestState(t)

	has, err := st.Has(100)
	if err != nil || has {
		t.Fatalf("Has(100) before insert = %v, %v; want false, nil", has, err)
	}

	if err := st.Insert(100, 555); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	has, err = st.Has(100)
	if err != nil || !has {
		t.Fatalf("Has(100) after insert = %v, %v; want true, nil", has, err)
	}

	if err := st.Complete(100, 8420, 312); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if has, _ := st.Has(100); !has {
		t.Fatal("Has(100) after Complete = false; want true")
	}
}

func TestStateIncompleteIDs(t *testing.T) {
	st := newTestState(t)
	st.Insert(100, 1)
	st.Insert(200, 1)
	st.Complete(200, 10, 5)
	st.Insert(300, 1)
	st.MarkError(300)

	ids, err := st.IncompleteIDs()
	if err != nil {
		t.Fatalf("IncompleteIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != 100 {
		t.Fatalf("IncompleteIDs = %v; want [100]", ids)
	}
}

func TestStateDeleteIncomplete(t *testing.T) {
	st := newTestState(t)
	st.Insert(1, 1)      // incomplete
	st.Insert(2, 1)
	st.Complete(2, 5, 5) // complete
	n, err := st.DeleteIncomplete()
	if err != nil {
		t.Fatalf("DeleteIncomplete: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	if has, _ := st.Has(1); has {
		t.Fatal("msg 1 should be gone")
	}
	if has, _ := st.Has(2); !has {
		t.Fatal("msg 2 should remain")
	}
}
