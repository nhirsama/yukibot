package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nhirsama/yukibot/internal/features/summarizer"
	"github.com/nhirsama/yukibot/internal/storage/dbsql"
)

// Repository is the PostgreSQL adapter for summary rules, runs, and model config.
type Repository struct {
	q *dbsql.Queries
}

var (
	_ summarizer.RuleRepository        = (*Repository)(nil)
	_ summarizer.RunRepository         = (*Repository)(nil)
	_ summarizer.ModelConfigRepository = (*Repository)(nil)
)

// NewRepository binds the repositories to a sqlc database handle.
func NewRepository(db dbsql.DBTX) *Repository {
	return &Repository{q: dbsql.New(db)}
}

// ListAll returns rules ordered by id.
func (r *Repository) ListAll(ctx context.Context) ([]summarizer.SummaryRule, error) {
	rows, err := r.q.ListSummaryRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]summarizer.SummaryRule, 0, len(rows))
	for _, row := range rows {
		rule, err := ruleFrom(row)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

// AddAuto inserts a draft and returns it with the allocated id.
func (r *Repository) AddAuto(ctx context.Context, draft summarizer.SummaryRuleDraft) (summarizer.SummaryRule, error) {
	id, err := r.q.InsertSummaryRule(ctx, dbsql.InsertSummaryRuleParams{
		SourceChatID:        draft.Source.ChatID,
		SourceTopicID:       nullTopic(draft.Source.TopicID),
		SourceUsername:      nullString(draft.Source.Username),
		DestinationChatID:   draft.Destination.ChatID,
		DestinationTopicID:  nullTopic(draft.Destination.TopicID),
		DestinationUsername: nullString(draft.Destination.Username),
		WindowSeconds:       int32(draft.WindowSeconds),
		Enabled:             draft.Enabled,
	})
	if err != nil {
		return summarizer.SummaryRule{}, err
	}
	if id <= 0 || int64(int(id)) != id {
		return summarizer.SummaryRule{}, errors.New("database did not allocate a summary rule ID")
	}
	return draft.Bind(int(id))
}

// Replace updates an existing rule or returns ErrRuleMissing.
func (r *Repository) Replace(ctx context.Context, rule summarizer.SummaryRule) error {
	rows, err := r.q.UpdateSummaryRule(ctx, dbsql.UpdateSummaryRuleParams{
		ID:                  int64(rule.ID),
		SourceChatID:        rule.Source.ChatID,
		SourceTopicID:       nullTopic(rule.Source.TopicID),
		SourceUsername:      nullString(rule.Source.Username),
		DestinationChatID:   rule.Destination.ChatID,
		DestinationTopicID:  nullTopic(rule.Destination.TopicID),
		DestinationUsername: nullString(rule.Destination.Username),
		WindowSeconds:       int32(rule.WindowSeconds),
		Enabled:             rule.Enabled,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return summarizer.ErrRuleMissing
	}
	return nil
}

// Remove deletes a rule and reports whether it existed.
func (r *Repository) Remove(ctx context.Context, ruleID int) (bool, error) {
	rows, err := r.q.DeleteSummaryRule(ctx, int64(ruleID))
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// Save inserts one successful summary run.
func (r *Repository) Save(ctx context.Context, run summarizer.SummaryRun) error {
	document, err := summarizer.DocumentJSON(run.Document)
	if err != nil {
		return err
	}
	return r.q.InsertSummaryRun(ctx, dbsql.InsertSummaryRunParams{
		RuleID:         int64(run.RuleID),
		StartedAt:      timestamptz(run.StartedAt),
		CompletedAt:    timestamptz(run.CompletedAt),
		FirstMessageID: int64(run.FirstMessageID),
		LastMessageID:  int64(run.LastMessageID),
		MessageCount:   int32(run.MessageCount),
		Provider:       run.Provider,
		Model:          run.Model,
		PromptVersion:  int32(run.PromptVersion),
		OutputJson:     document,
	})
}

// GetModelConfig returns the singleton config, or nil when it is unset.
// Empty API key, base URL, and custom prompt are stored as NULL and read back empty.
func (r *Repository) GetModelConfig(ctx context.Context) (*summarizer.SummaryModelConfig, error) {
	row, err := r.q.GetSummaryModelConfig(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	config := summarizer.SummaryModelConfig{
		Provider:         row.Provider,
		Model:            row.Model,
		APIKey:           deref(row.ApiKey),
		BaseURL:          deref(row.BaseUrl),
		InputTokenLimit:  int(row.InputTokenLimit),
		OutputTokenLimit: int(row.OutputTokenLimit),
		Temperature:      row.Temperature,
		Timeout:          time.Duration(row.Timeout * float64(time.Second)),
		MaxRetries:       int(row.MaxRetries),
		PromptPreset:     summarizer.SummaryPromptPreset(row.PromptPreset),
		CustomPrompt:     deref(row.CustomPrompt),
		MaxConcurrency:   int(row.MaxConcurrency),
	}
	return &config, nil
}

// SaveModelConfig upserts the singleton row.
func (r *Repository) SaveModelConfig(ctx context.Context, config summarizer.SummaryModelConfig) error {
	seconds := float64(config.Timeout) / float64(time.Second)
	return r.q.UpsertSummaryModelConfig(ctx, dbsql.UpsertSummaryModelConfigParams{
		Provider:         config.Provider,
		Model:            config.Model,
		ApiKey:           nullString(config.APIKey),
		BaseUrl:          nullString(config.BaseURL),
		InputTokenLimit:  int32(config.InputTokenLimit),
		OutputTokenLimit: int32(config.OutputTokenLimit),
		Temperature:      config.Temperature,
		Timeout:          seconds,
		MaxRetries:       int32(config.MaxRetries),
		PromptPreset:     string(config.PromptPreset),
		CustomPrompt:     nullString(config.CustomPrompt),
		MaxConcurrency:   int32(config.MaxConcurrency),
	})
}

// ClearModelConfig deletes the singleton row and reports whether it existed.
func (r *Repository) ClearModelConfig(ctx context.Context) (bool, error) {
	rows, err := r.q.ClearSummaryModelConfig(ctx)
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func ruleFrom(row dbsql.ListSummaryRulesRow) (summarizer.SummaryRule, error) {
	source, err := endpointFrom(row.SourceChatID, row.SourceTopicID, row.SourceUsername)
	if err != nil {
		return summarizer.SummaryRule{}, err
	}
	destination, err := endpointFrom(row.DestinationChatID, row.DestinationTopicID, row.DestinationUsername)
	if err != nil {
		return summarizer.SummaryRule{}, err
	}
	if int64(int(row.ID)) != row.ID {
		return summarizer.SummaryRule{}, errors.New("summary rule id does not fit")
	}
	return summarizer.NewSummaryRule(int(row.ID), source, destination, int(row.WindowSeconds), row.Enabled)
}

func endpointFrom(chatID int64, topic *int64, username *string) (summarizer.SummaryEndpoint, error) {
	var topicID *int
	if topic != nil {
		value := int(*topic)
		topicID = &value
	}
	var name *string
	if username != nil && strings.TrimSpace(*username) != "" {
		name = username
	}
	return summarizer.NewSummaryEndpoint(chatID, topicID, name)
}

func nullTopic(topicID int) *int64 {
	if topicID <= 0 {
		return nil
	}
	value := int64(topicID)
	return &value
}

func nullString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func timestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
