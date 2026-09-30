package summarizer

import (
	"context"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// RuleRepository stores summary rules.
type RuleRepository interface {
	ListAll(ctx context.Context) ([]SummaryRule, error)
	AddAuto(ctx context.Context, draft SummaryRuleDraft) (SummaryRule, error)
	Replace(ctx context.Context, rule SummaryRule) error
	Remove(ctx context.Context, ruleID int) (bool, error)
}

// RunRepository stores successful summary runs.
type RunRepository interface {
	Save(ctx context.Context, run SummaryRun) error
}

// ModelConfigRepository stores the single summary model configuration.
type ModelConfigRepository interface {
	GetModelConfig(ctx context.Context) (*SummaryModelConfig, error)
	SaveModelConfig(ctx context.Context, config SummaryModelConfig) error
	ClearModelConfig(ctx context.Context) (bool, error)
}

// Telegram is the chat gateway implemented by the composition root.
// A nil limit means no fetch cap.
type Telegram interface {
	ResolveEndpoint(ctx context.Context, reference string) (SummaryEndpoint, error)
	FetchRecent(ctx context.Context, source SummaryEndpoint, since time.Time, limit *int) (FetchedSummaryMessages, error)
	SendText(ctx context.Context, destination SummaryEndpoint, text string) (contracts.MessageRef, error)
}

// Generator produces one structured summary document from a prompt pair.
type Generator interface {
	Reset(ctx context.Context) error
	Generate(ctx context.Context, config SummaryModelConfig, systemPrompt, userPrompt string) (SummaryDocument, error)
}
