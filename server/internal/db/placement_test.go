package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPlacementHistorySurvivesBindAndResume(t *testing.T) {
	d, ctx := open(t), context.Background()
	p := PlacementRow{ID: "first", Machine: "box", TaskID: "task", At: time.Now().UTC(), Decision: json.RawMessage(`{"policy_version":1,"choice":"box"}`)}
	if err := d.PutPlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.SessionID = "agent"
	if err := d.PutPlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ID = "resume"
	if err := d.PutPlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM agent_placements WHERE task_id=$1 AND machine=$2 AND session_id=$3 AND decision->>'choice'=$2`, p.TaskID, p.Machine, p.SessionID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("persisted %d placements, %v", count, err)
	}
}
