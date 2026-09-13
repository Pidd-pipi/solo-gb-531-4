package repository

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"time"
)

type RectificationItemRepository interface {
	Create(context.Context, *model.RectificationItem) error
	GetByID(context.Context, uint) (model.RectificationItem, error)
	FindByFingerprint(context.Context, string) (model.RectificationItem, error)
	List(context.Context, dto.RectificationQuery) ([]model.RectificationItem, int64, error)
	Update(context.Context, uint, map[string]any) error
	Transition(context.Context, uint, []string, string, map[string]any) (bool, error)
	CreateBinding(context.Context, *model.RectificationBinding) error
	FindActiveBindings(context.Context, []uint) ([]model.RectificationBinding, error)
	CountByState(context.Context) (map[string]int64, error)
	WithTx(context.Context, func(RectificationItemRepository) error) error
}

type rectificationItemRepository struct{ db *gorm.DB }

func NewRectificationItemRepository(db *gorm.DB) RectificationItemRepository {
	return &rectificationItemRepository{db: db}
}

func (r *rectificationItemRepository) WithTx(ctx context.Context, fn func(RectificationItemRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&rectificationItemRepository{db: tx})
	})
}

func (r *rectificationItemRepository) Create(ctx context.Context, item *model.RectificationItem) error {
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		return fmt.Errorf("create rectification item: %w", err)
	}
	return nil
}

func (r *rectificationItemRepository) GetByID(ctx context.Context, id uint) (model.RectificationItem, error) {
	var item model.RectificationItem
	if err := r.db.WithContext(ctx).Preload("Scenario").Preload("Bindings").First(&item, id).Error; err != nil {
		return model.RectificationItem{}, fmt.Errorf("find rectification item %d: %w", id, err)
	}
	return item, nil
}

func (r *rectificationItemRepository) FindByFingerprint(ctx context.Context, fingerprint string) (model.RectificationItem, error) {
	var item model.RectificationItem
	if err := r.db.WithContext(ctx).Where("gap_fingerprint = ?", fingerprint).First(&item).Error; err != nil {
		return model.RectificationItem{}, fmt.Errorf("find rectification item by fingerprint: %w", err)
	}
	return item, nil
}

func (r *rectificationItemRepository) List(ctx context.Context, query dto.RectificationQuery) ([]model.RectificationItem, int64, error) {
	base := r.db.WithContext(ctx).Model(&model.RectificationItem{})
	if query.ScenarioID != 0 {
		base = base.Where("scenario_id = ?", query.ScenarioID)
	}
	if query.State != "" {
		base = base.Where("state = ?", query.State)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count rectification items: %w", err)
	}
	var items []model.RectificationItem
	offset := (query.Page - 1) * query.PageSize
	if err := base.Preload("Scenario").Preload("Bindings").
		Order("CASE state WHEN 'pending' THEN 0 WHEN 'in_progress' THEN 1 WHEN 'pending_review' THEN 2 WHEN 'completed' THEN 3 ELSE 4 END, updated_at DESC, id DESC").
		Limit(query.PageSize).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list rectification items: %w", err)
	}
	return items, total, nil
}

func (r *rectificationItemRepository) Update(ctx context.Context, id uint, updates map[string]any) error {
	values := make(map[string]any, len(updates)+1)
	for key, value := range updates {
		values[key] = value
	}
	values["updated_at"] = time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.RectificationItem{}).
		Where("id = ? AND state IN ?", id, []string{"pending", "in_progress", "pending_review"}).
		Updates(values)
	if result.Error != nil {
		return fmt.Errorf("update rectification item %d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *rectificationItemRepository) Transition(
	ctx context.Context,
	id uint,
	from []string,
	to string,
	updates map[string]any,
) (bool, error) {
	values := make(map[string]any, len(updates)+2)
	for key, value := range updates {
		values[key] = value
	}
	values["state"] = to
	values["updated_at"] = time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.RectificationItem{}).
		Where("id = ? AND state IN ?", id, from).Updates(values)
	if result.Error != nil {
		return false, fmt.Errorf("transition rectification item %d: %w", id, result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *rectificationItemRepository) CreateBinding(ctx context.Context, binding *model.RectificationBinding) error {
	if err := r.db.WithContext(ctx).Create(binding).Error; err != nil {
		return fmt.Errorf("create rectification binding: %w", err)
	}
	return nil
}

func (r *rectificationItemRepository) FindActiveBindings(ctx context.Context, safeguardIDs []uint) ([]model.RectificationBinding, error) {
	if len(safeguardIDs) == 0 {
		return nil, nil
	}
	var bindings []model.RectificationBinding
	if err := r.db.WithContext(ctx).Model(&model.RectificationBinding{}).
		Joins("JOIN rectification_items ON rectification_items.id = rectification_bindings.item_id").
		Where("rectification_bindings.safeguard_id IN ?", safeguardIDs).
		Where("rectification_items.state != ?", "voided").
		Find(&bindings).Error; err != nil {
		return nil, fmt.Errorf("find active bindings for safeguards: %w", err)
	}
	return bindings, nil
}

func (r *rectificationItemRepository) CountByState(ctx context.Context) (map[string]int64, error) {
	type row struct {
		State string
		Total int64
	}
	var rows []row
	if err := r.db.WithContext(ctx).Model(&model.RectificationItem{}).
		Select("state, COUNT(*) AS total").Group("state").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count rectification items by state: %w", err)
	}
	counts := make(map[string]int64, len(rows))
	for _, entry := range rows {
		counts[entry.State] = entry.Total
	}
	return counts, nil
}
