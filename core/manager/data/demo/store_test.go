package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	alertmodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/alert"
	model "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func run(fingerprint string) *model.ScenarioRun {
	return &model.ScenarioRun{
		TenantID: 1, ScenarioID: "pg-pool-exhaustion", IdempotencyKey: "demo-key-0001",
		IncidentID: 10, Target: "pg:pool-fixture", TargetFingerprint: fingerprint,
		Status: model.ScenarioStatusStarting, ExpiresAt: time.Now().Add(time.Minute).UTC(),
	}
}

func TestScenarioStoreIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := NewRepo(newTestDB(t))
	first := run("0123456789abcdef")
	if err := repo.CreateOrUpdate(ctx, first); err != nil {
		t.Fatalf("first create: %v", err)
	}
	second := run("0123456789abcdef")
	second.IncidentID = 99
	second.PoolManifestID = "manifest"
	if err := repo.CreateOrUpdate(ctx, second); err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent IDs differ: %d != %d", second.ID, first.ID)
	}
	var count int64
	if err := repo.db.Model(&model.ScenarioRun{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("count = %d err = %v", count, err)
	}
}

func TestScenarioStoreUpdatesAlertAndManifest(t *testing.T) {
	ctx := context.Background()
	repo := NewRepo(newTestDB(t))
	created := run("aaaaaaaaaaaaaaaa")
	if err := repo.CreateOrUpdate(ctx, created); err != nil {
		t.Fatal(err)
	}
	created.AlertFingerprint = "bbbbbbbbbbbbbbbb"
	created.PoolManifestID = "pool-manifest-1"
	created.Status = model.ScenarioStatusAwaitingAlert
	if err := repo.CreateOrUpdate(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByIdempotencyKey(ctx, 1, created.ScenarioID, created.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.AlertFingerprint != "bbbbbbbbbbbbbbbb" || got.PoolManifestID != "pool-manifest-1" || got.Status != model.ScenarioStatusAwaitingAlert {
		t.Fatalf("updated run = %+v", got)
	}
}

func TestScenarioStoreRejectsFingerprintMismatch(t *testing.T) {
	ctx := context.Background()
	repo := NewRepo(newTestDB(t))
	if err := repo.CreateOrUpdate(ctx, run("cccccccccccccccc")); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateOrUpdate(ctx, run("dddddddddddddddd")); !errorsIsConflict(err) {
		t.Fatalf("mismatch err = %v", err)
	}
	if err := repo.UpdateStatus(ctx, 1, model.ScenarioStatusAwaitingAlert, func(r *model.ScenarioRun) error {
		r.TargetFingerprint = "eeeeeeeeeeeeeeee"
		return nil
	}); !errorsIsConflict(err) {
		t.Fatalf("mutation mismatch err = %v", err)
	}
}

func TestScenarioStatusAndEventUpdateIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := db.AutoMigrate(&alertmodel.Event{}); err != nil {
		t.Fatalf("migrate events: %v", err)
	}
	repo := NewRepo(db)
	created := run("aaaaaaaaaaaaaaaa")
	if err := repo.CreateOrUpdate(ctx, created); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`CREATE TRIGGER fail_event_insert BEFORE INSERT ON alert_events ` +
			`BEGIN SELECT RAISE(ABORT, 'event insert failed'); END`,
	).Error; err != nil {
		t.Fatal(err)
	}
	event := &alertmodel.Event{
		IncidentID: created.IncidentID, EventType: model.ScenarioStatusAwaitingAlert,
		StatusAfter: "open", Severity: "critical", SnapshotJSON: "{}", Reason: "test",
	}
	if err := repo.UpdateStatusWithEvent(
		ctx, created.ID, model.ScenarioStatusAwaitingAlert, event, model.ScenarioStatusStarting,
	); err == nil {
		t.Fatal("expected event insertion failure")
	}
	got, err := repo.GetByIdempotencyKey(ctx, 1, created.ScenarioID, created.IdempotencyKey)
	if err != nil || got.Status != model.ScenarioStatusStarting {
		t.Fatalf("rolled-back run = %+v err = %v", got, err)
	}
	var count int64
	if err := db.Model(&alertmodel.Event{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("event count = %d err = %v", count, err)
	}

	if err := db.Exec(`DROP TRIGGER fail_event_insert`).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatusWithEvent(
		ctx, created.ID, model.ScenarioStatusAwaitingAlert, event, model.ScenarioStatusStarting,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&alertmodel.Event{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("successful event count = %d err = %v", count, err)
	}
}

func errorsIsConflict(err error) bool { return errors.Is(err, errs.ErrConflict) }
