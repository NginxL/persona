package db

import (
	"context"
	"testing"
	"time"
)

func TestTodoResponsible(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)

	sessionResp := &TodoResponsible{
		Kind:      ResponsibleSession,
		Name:      "ops-tmux-main",
		MachineID: "host-a",
		SessionID: "sess-1",
	}
	if err := d.CreateTodo(ctx, TodoRow{
		ID:          "todo_resp_create",
		Title:       "Watch canary deploy",
		Responsible: sessionResp,
		CreatedAt:   base,
		UpdatedAt:   base,
	}); err != nil {
		t.Fatalf("CreateTodo with responsible: %v", err)
	}
	if err := d.CreateTodo(ctx, TodoRow{
		ID:        "todo_resp_plain",
		Title:     "Plain task",
		CreatedAt: base,
		UpdatedAt: base,
	}); err != nil {
		t.Fatalf("CreateTodo without responsible: %v", err)
	}

	got, err := d.GetTodo(ctx, "todo_resp_create")
	if err != nil {
		t.Fatalf("GetTodo: %v", err)
	}
	if got.Responsible == nil || *got.Responsible != *sessionResp {
		t.Fatalf("GetTodo responsible = %+v, want %+v", got.Responsible, sessionResp)
	}
	plain, err := d.GetTodo(ctx, "todo_resp_plain")
	if err != nil {
		t.Fatalf("GetTodo plain: %v", err)
	}
	if plain.Responsible != nil {
		t.Fatalf("plain responsible = %+v, want nil", plain.Responsible)
	}

	rows, err := d.ListTodos(ctx)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	byID := map[string]TodoRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if byID["todo_resp_create"].Responsible == nil || *byID["todo_resp_create"].Responsible != *sessionResp {
		t.Fatalf("ListTodos responsible = %+v, want %+v", byID["todo_resp_create"].Responsible, sessionResp)
	}
	if byID["todo_resp_plain"].Responsible != nil {
		t.Fatalf("ListTodos plain responsible = %+v, want nil", byID["todo_resp_plain"].Responsible)
	}

	// EditTodo without SetResponsible leaves responsible untouched.
	title := "Watch canary deploy v2"
	if row, err := d.EditTodo(ctx, "todo_resp_create", TodoEdit{Title: &title}, base.Add(time.Minute)); err != nil || row == nil {
		t.Fatalf("EditTodo title only: row=%+v err=%v", row, err)
	}
	got, err = d.GetTodo(ctx, "todo_resp_create")
	if err != nil {
		t.Fatalf("GetTodo after title edit: %v", err)
	}
	if got.Title != title || got.Responsible == nil || *got.Responsible != *sessionResp {
		t.Fatalf("after title edit = %+v", got)
	}

	// EditTodo with SetResponsible updates to a person responsible party.
	personResp := &TodoResponsible{Kind: ResponsiblePerson, Name: "Alex"}
	if row, err := d.EditTodo(ctx, "todo_resp_create", TodoEdit{SetResponsible: true, Responsible: personResp}, base.Add(2*time.Minute)); err != nil || row == nil {
		t.Fatalf("EditTodo set person responsible: row=%+v err=%v", row, err)
	}
	got, err = d.GetTodo(ctx, "todo_resp_create")
	if err != nil {
		t.Fatalf("GetTodo after set person: %v", err)
	}
	if got.Responsible == nil || *got.Responsible != *personResp {
		t.Fatalf("after set person = %+v, want %+v", got.Responsible, personResp)
	}

	// EditTodo with SetResponsible and nil clears responsible cleanly.
	if row, err := d.EditTodo(ctx, "todo_resp_create", TodoEdit{SetResponsible: true, Responsible: nil}, base.Add(3*time.Minute)); err != nil || row == nil {
		t.Fatalf("EditTodo clear responsible: row=%+v err=%v", row, err)
	}
	got, err = d.GetTodo(ctx, "todo_resp_create")
	if err != nil {
		t.Fatalf("GetTodo after clear: %v", err)
	}
	if got.Responsible != nil {
		t.Fatalf("after clear responsible = %+v, want nil", got.Responsible)
	}

	// Database CHECK constraint rejects malformed JSON shapes.
	invalid := []string{
		`{}`,
		`[]`,
		`"ops"`,
		`{"kind":"agent","name":"bot"}`,
		`{"kind":"session","name":""}`,
		`{"kind":"person","name":"   "}`,
		`{"kind":"session","name":"ops","machine_id":123}`,
		`{"kind":"session","name":"ops","session_id":true}`,
	}
	for _, raw := range invalid {
		if _, err := d.pool.Exec(ctx, `UPDATE todos SET responsible = $2::jsonb WHERE id = $1`, "todo_resp_plain", raw); err == nil {
			t.Fatalf("expected todos_responsible_valid CHECK to reject %s", raw)
		}
	}
}
