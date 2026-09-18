package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	model "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&model.ScenarioRun{})
}

func (r *Repo) CreateOrUpdate(ctx context.Context, run *model.ScenarioRun) error {
	if run == nil || !model.IsKnownStatus(run.Status) {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.ScenarioRun
		err := tx.Where(
			"tenant_id = ? AND scenario_id = ? AND idempotency_key = ?",
			run.TenantID, run.ScenarioID, run.IdempotencyKey,
		).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(run).Error
		}
		if err != nil {
			return err
		}
		if existing.TargetFingerprint != run.TargetFingerprint {
			return errs.ErrConflict
		}
		run.ID = existing.ID
		run.CreatedAt = existing.CreatedAt
		return tx.Model(&existing).Select(
			"incident_id", "pool_manifest_id", "target", "target_fingerprint",
			"alert_fingerprint", "status", "expires_at", "updated_at",
		).Updates(run).Error
	})
}

func (r *Repo) GetByIdempotencyKey(ctx context.Context, tenantID uint64, scenarioID, key string) (*model.ScenarioRun, error) {
	var run model.ScenarioRun
	err := r.db.WithContext(ctx).Where(
		"tenant_id = ? AND scenario_id = ? AND idempotency_key = ?",
		tenantID, scenarioID, key,
	).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *Repo) UpdateStatus(ctx context.Context, id uint64, status string, mutation func(*model.ScenarioRun) error) error {
	if !model.IsKnownStatus(status) {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.ScenarioRun
		if err := tx.First(&run, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return err
		}
		before := run.TargetFingerprint
		run.Status = status
		if mutation != nil {
			if err := mutation(&run); err != nil {
				return err
			}
		}
		if !model.IsKnownStatus(run.Status) || run.TargetFingerprint != before {
			return errs.ErrConflict
		}
		return tx.Save(&run).Error
	})
}
