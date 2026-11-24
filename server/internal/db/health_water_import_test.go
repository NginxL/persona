package db

import (
	"context"
	"testing"
)

func TestHealthIntakeImportTransaction(t *testing.T) {
	d, ctx := open(t), context.Background()
	a, b := intakeRow(1), intakeRow(2)
	b.Kind = "invalid"
	if n, err := d.HealthIntakeImport(ctx, []HealthIntakeRow{a, b}); err == nil || n != 0 {
		t.Fatalf("invalid batch: %d %v", n, err)
	}
	rows, err := d.HealthIntakeChanges(ctx, 0, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("partial batch committed: %+v %v", rows, err)
	}
	b.Kind = "water"
	if n, err := d.HealthIntakeImport(ctx, []HealthIntakeRow{a, b}); err != nil || n != 2 {
		t.Fatalf("batch: %d %v", n, err)
	}
	if _, _, err := d.HealthIntakeUndo(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := d.HealthIntakeImport(ctx, []HealthIntakeRow{a, b}); err != nil || n != 0 {
		t.Fatalf("retry: %d %v", n, err)
	}
	rows, err = d.HealthIntakeChanges(ctx, 0, 100)
	if err != nil || len(rows) != 2 || rows[1].DeletedAt == nil {
		t.Fatalf("tombstone: %+v %v", rows, err)
	}
}

func TestHealthIntakeFinishWaterImportAtomic(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.PutSetting(ctx, "health.intake", map[string]any{"before": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `CREATE FUNCTION reject_water_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.key = 'health.intake.water_import' THEN RAISE EXCEPTION 'synthetic receipt failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_water_receipt BEFORE INSERT ON settings FOR EACH ROW EXECUTE FUNCTION reject_water_receipt()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.pool.Exec(ctx, `DROP TRIGGER reject_water_receipt ON settings; DROP FUNCTION reject_water_receipt()`); err != nil {
			t.Error(err)
		}
	})
	if err := d.HealthIntakeFinishWaterImport(ctx, map[string]any{"after": true}, map[string]any{"last": "synthetic"}, map[string]any{"records": 2}); err == nil {
		t.Fatal("receipt failure ignored")
	}
	var c map[string]bool
	if ok, err := d.Setting(ctx, "health.intake", &c); err != nil || !ok || !c["before"] || c["after"] {
		t.Fatalf("settings partially published: %v %v", c, err)
	}
	for _, key := range []string{"health.intake.water_reminder", "health.intake.water_import"} {
		if ok, err := d.Setting(ctx, key, &c); err != nil || ok {
			t.Fatalf("partial write %s: %v %v", key, ok, err)
		}
	}
}
