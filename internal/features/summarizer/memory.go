package summarizer

import (
	"context"
	"sort"
	"sync"
)

// MemoryRepository is an in-memory rule, run, and model-config store for tests
// and for composition before a database adapter is attached.
// Rules, Runs, and ModelConfig are the storage itself and may be seeded by tests
// before methods run.
type MemoryRepository struct {
	mu          sync.Mutex
	Rules       map[int]SummaryRule
	Runs        []SummaryRun
	ModelConfig *SummaryModelConfig
}

// NewMemoryRepository returns an empty store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{Rules: map[int]SummaryRule{}}
}

// ListAll returns rules ordered by id.
func (m *MemoryRepository) ListAll(ctx context.Context) ([]SummaryRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]int, 0, len(m.Rules))
	for id := range m.Rules {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]SummaryRule, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.Rules[id])
	}
	return out, nil
}

// AddAuto assigns the next positive id and stores the draft.
func (m *MemoryRepository) AddAuto(ctx context.Context, draft SummaryRuleDraft) (SummaryRule, error) {
	if err := ctx.Err(); err != nil {
		return SummaryRule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Rules == nil {
		m.Rules = map[int]SummaryRule{}
	}
	next := 0
	for id := range m.Rules {
		if id > next {
			next = id
		}
	}
	next++
	rule, err := draft.Bind(next)
	if err != nil {
		return SummaryRule{}, err
	}
	m.Rules[rule.ID] = rule
	return rule, nil
}

// Replace updates an existing rule or returns ErrRuleMissing.
func (m *MemoryRepository) Replace(ctx context.Context, rule SummaryRule) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Rules == nil {
		m.Rules = map[int]SummaryRule{}
	}
	if _, ok := m.Rules[rule.ID]; !ok {
		return ErrRuleMissing
	}
	m.Rules[rule.ID] = rule
	return nil
}

// Remove deletes a rule and reports whether it existed.
func (m *MemoryRepository) Remove(ctx context.Context, ruleID int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Rules[ruleID]; !ok {
		return false, nil
	}
	delete(m.Rules, ruleID)
	return true, nil
}

// Save appends a successful run.
func (m *MemoryRepository) Save(ctx context.Context, run SummaryRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Runs = append(m.Runs, run)
	return nil
}

// GetModelConfig returns a copy of the current config, or nil when unset.
func (m *MemoryRepository) GetModelConfig(ctx context.Context) (*SummaryModelConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ModelConfig == nil {
		return nil, nil
	}
	cfg := *m.ModelConfig
	return &cfg, nil
}

// SaveModelConfig replaces the single model configuration.
func (m *MemoryRepository) SaveModelConfig(ctx context.Context, config SummaryModelConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := config
	m.ModelConfig = &cfg
	return nil
}

// ClearModelConfig removes the configuration and reports whether one existed.
func (m *MemoryRepository) ClearModelConfig(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existed := m.ModelConfig != nil
	m.ModelConfig = nil
	return existed, nil
}
