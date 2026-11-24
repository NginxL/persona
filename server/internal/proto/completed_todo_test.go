package proto

import (
	"encoding/json"
	"testing"
)

func TestCompletedTodoIsSparseAndCopyOnWrite(t *testing.T) {
	var original Session
	original.SetCompletedTodo(&TodoRef{ID: "done", Title: "Completed task"})
	copy := original
	copy.SetCompletedTodo(&TodoRef{ID: "other", Title: "Another completed task"})
	if original.CompletedTodo().ID != "done" || copy.CompletedTodo().ID != "other" || original.Todo != nil {
		t.Fatal("completed ownership changed another row or active membership")
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.CompletedTodo() == nil ||
		decoded.CompletedTodo().ID != "done" || decoded.Todo != nil {
		t.Fatalf("display-only ownership did not round trip: %s, %v", raw, err)
	}
	copy.SetCompletedTodo(nil)
	if copy.Hierarchy != nil || original.CompletedTodo().ID != "done" {
		t.Fatal("clearing the last sparse value left a carrier or changed the original")
	}
}

func TestCompletedTodoPreservesParentAgentMetadata(t *testing.T) {
	var original Session
	original.SetParentAgentID("parent")
	original.SetCompletedTodo(&TodoRef{ID: "done", Title: "Completed task"})
	withoutTodo := original
	withoutTodo.SetCompletedTodo(nil)
	if withoutTodo.Hierarchy == nil || withoutTodo.ParentAgentID() != "parent" {
		t.Fatal("clearing completed ownership discarded the independent parent agent")
	}
	withoutParent := original
	withoutParent.SetParentAgentID("")
	if withoutParent.CompletedTodo() == nil || withoutParent.CompletedTodo().ID != "done" ||
		original.ParentAgentID() != "parent" || original.CompletedTodo() == nil {
		t.Fatal("clearing parent agent metadata discarded completed ownership or changed the original")
	}
	checkWire(t, "completed ownership beside parent agent metadata", original)
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.ParentAgentID() != "parent" ||
		decoded.CompletedTodo() == nil || decoded.CompletedTodo().ID != "done" || decoded.Todo != nil {
		t.Fatalf("sparse metadata did not round trip independently: %s, %v", raw, err)
	}
}

func TestCompletedTodoPreservesDispatchCreationMetadata(t *testing.T) {
	var original Session
	original.SetCreationID("creation-1")
	original.SetCompletedTodo(&TodoRef{ID: "done", Title: "Completed task"})
	withoutTodo := original
	withoutTodo.SetCompletedTodo(nil)
	if withoutTodo.Hierarchy == nil || withoutTodo.CreationID() != "creation-1" {
		t.Fatal("clearing completed ownership discarded dispatch creation metadata")
	}
	withoutCreation := original
	withoutCreation.SetCreationID("")
	if withoutCreation.CompletedTodo() == nil || withoutCreation.CompletedTodo().ID != "done" ||
		original.CreationID() != "creation-1" || original.CompletedTodo() == nil {
		t.Fatal("clearing dispatch creation metadata discarded completed ownership or changed the original")
	}
	checkWire(t, "completed ownership beside dispatch creation metadata", original)
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.CreationID() != "creation-1" ||
		decoded.CompletedTodo() == nil || decoded.CompletedTodo().ID != "done" || decoded.Todo != nil {
		t.Fatalf("creation and completed ownership did not round trip independently: %s, %v", raw, err)
	}
}
