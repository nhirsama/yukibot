package summarizer

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestSpecMatchesSummaryHelp(t *testing.T) {
	spec := Spec()
	if spec.Name != "/summary" || spec.Summary != "生成并发送消息总结" || spec.HelpText != SummaryHelp {
		t.Fatalf("%+v", spec)
	}
	if strings.HasPrefix(SummaryHelp, "/") || strings.HasSuffix(SummaryHelp, "\n") {
		t.Fatal("help must not start with a slash or end with a newline")
	}
}

func TestCommandsAcceptTopicDestinationAndDuration(t *testing.T) {
	service := newFakeCommandBackend()
	commands := NewSummarizerCommands(service)
	added := commandText(t, commands, "add -1001 https://t.me/c/2001/42 6h")
	run := commandText(t, commands, "run 1 30m")
	if added != "Summary rule 1 is configured." {
		t.Fatal(added)
	}
	if run != "总结已发送: 规则 1, 消息 8, 主题 2, 发送 1 条。" {
		t.Fatal(run)
	}
	want := [][]any{
		{"add", "-1001", "https://t.me/c/2001/42", 21600},
		{"run", 1, 1800},
	}
	if !reflect.DeepEqual(service.calls, want) {
		t.Fatalf("%#v", service.calls)
	}
}

func TestCommandsRenderRulesAndReturnDomainErrors(t *testing.T) {
	service := newFakeCommandBackend()
	commands := NewSummarizerCommands(service)
	listing := commandText(t, commands, "list")
	details := commandText(t, commands, "show 1")
	service.err = &SummarizerError{Msg: "模型未配置"}
	failed := commandText(t, commands, "run 1")
	if listing != "1: @source -> @target/42 (6h, enabled)" {
		t.Fatal(listing)
	}
	if !strings.Contains(details, "destination topic id: 42") {
		t.Fatal(details)
	}
	if failed != "模型未配置" {
		t.Fatal(failed)
	}
	if commandText(t, commands, "add only-one") != SummaryHelp {
		t.Fatal("bad add should show help")
	}
}

func TestModelConfigurationCommandsArePersistedAndRedacted(t *testing.T) {
	service := newFakeCommandBackend()
	commands := NewSummarizerCommands(service)
	configured := commandText(t, commands, "model set openai gpt-test -api-key top-secret -base-url https://models.example/v1")
	shown := commandText(t, commands, "model show")
	tuned := commandText(t, commands, "model tune 16000 2000 0.2 60 3")
	cleared := commandText(t, commands, "model clear")
	if configured != "Summary model is configured: openai/gpt-test." {
		t.Fatal(configured)
	}
	if !strings.Contains(shown, "API key: configured") || strings.Contains(shown, "top-secret") {
		t.Fatal(shown)
	}
	if !strings.Contains(tuned, "input tokens: 16000") {
		t.Fatal(tuned)
	}
	if cleared != "Summary model configuration is cleared." {
		t.Fatal(cleared)
	}
	want := [][]any{
		{"model-set", "openai", "gpt-test", "top-secret", "https://models.example/v1"},
		{"model-tune", 16000, 2000, 0.2, 60 * time.Second, 3, nil},
		{"model-clear"},
	}
	if !reflect.DeepEqual(service.calls, want) {
		t.Fatalf("%#v", service.calls)
	}
}

func TestPromptCommandsListSelectCustomizeAndClear(t *testing.T) {
	service := newFakeCommandBackend()
	config, err := NewSummaryModelConfig("openai", "gpt-test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.model = &config
	commands := NewSummarizerCommands(service)
	presets := commandText(t, commands, "prompt list")
	selected := commandText(t, commands, "prompt use technical")
	customized := commandText(t, commands, "prompt custom 只保留发布版本、故障根因和验证结果")
	shown := commandText(t, commands, "prompt show")
	cleared := commandText(t, commands, "prompt clear")
	for _, name := range []string{"focused", "decisions", "technical", "digest"} {
		if !strings.Contains(presets, name) {
			t.Fatal(presets)
		}
	}
	if !strings.Contains(selected, "prompt: technical") || !strings.Contains(customized, "prompt: custom") {
		t.Fatalf("selected %s customized %s", selected, customized)
	}
	if !strings.Contains(shown, "故障根因") || !strings.Contains(cleared, "prompt: focused") {
		t.Fatalf("shown %s cleared %s", shown, cleared)
	}
	want := [][]any{
		{"prompt-use", "technical"},
		{"prompt-custom", "只保留发布版本、故障根因和验证结果"},
		{"prompt-use", "focused"},
	}
	if !reflect.DeepEqual(service.calls, want) {
		t.Fatalf("%#v", service.calls)
	}
}

func TestModelTuneAcceptsAndDisplaysConcurrency(t *testing.T) {
	service := newFakeCommandBackend()
	config, err := NewSummaryModelConfig("openai", "gpt-test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.model = &config
	commands := NewSummarizerCommands(service)
	tuned := commandText(t, commands, "model tune 16000 2000 0.2 60 3 5")
	if !strings.Contains(tuned, "concurrency: 5") {
		t.Fatal(tuned)
	}
	want := [][]any{{"model-tune", 16000, 2000, 0.2, 60 * time.Second, 3, 5}}
	if !reflect.DeepEqual(service.calls, want) {
		t.Fatalf("%#v", service.calls)
	}
}

func TestModelConfigurationAcceptsTelegramTypographicDashes(t *testing.T) {
	service := newFakeCommandBackend()
	commands := NewSummarizerCommands(service)
	configured := commandText(t, commands, "model set openai gpt-test \u2014api-key top-secret \u2013base-url https://models.example/v1")
	if configured != "Summary model is configured: openai/gpt-test." {
		t.Fatal(configured)
	}
	if service.model == nil || service.model.APIKey != "top-secret" || service.model.BaseURL != "https://models.example/v1" {
		t.Fatalf("%+v", service.model)
	}
}

func TestModelConfigurationDoesNotDuplicateProviderPrefix(t *testing.T) {
	service := newFakeCommandBackend()
	commands := NewSummarizerCommands(service)
	configured := commandText(t, commands, "model set apiarc apiarc/deepseek-v4-flash-free -api-key top-secret -base-url https://apiarc.ai/v1")
	if configured != "Summary model is configured: apiarc/deepseek-v4-flash-free." {
		t.Fatal(configured)
	}
}

func TestCommandShellAndWindowErrors(t *testing.T) {
	commands := NewSummarizerCommands(newFakeCommandBackend())
	if got := commandText(t, commands, "add \"open"); got != "Invalid arguments: No closing quotation" {
		t.Fatal(got)
	}
	if got := commandText(t, commands, "add foo\\"); got != "Invalid arguments: No escaped character" {
		t.Fatal(got)
	}
	if got := commandText(t, commands, "add a b 0m"); got != "时间窗格式应为 30m、6h 或 1d" {
		t.Fatal(got)
	}
	if got := commandText(t, commands, "add a b 31d"); got != "时间窗必须在 1 分钟到 30 天之间" {
		t.Fatal(got)
	}
	if got := commandText(t, commands, "model set openai gpt -api-key"); got != "模型选项必须使用 -参数 值 的格式" {
		t.Fatal(got)
	}
}

func commandText(t *testing.T, commands *SummarizerCommands, arguments string) string {
	t.Helper()
	result, err := commands.Handle(context.Background(), ControlCommand{Name: "/summary", RawArguments: arguments, ChatID: -100, MessageID: 1, Outgoing: true})
	if err != nil {
		t.Fatal(err)
	}
	return result.Text
}

type fakeCommandBackend struct {
	rule  SummaryRule
	calls [][]any
	err   error
	model *SummaryModelConfig
}

func newFakeCommandBackend() *fakeCommandBackend {
	topic := 42
	sourceName := "source"
	targetName := "target"
	rule, err := NewSummaryRule(1, mustEndpoint(-1001, nil, &sourceName), mustEndpoint(-1002, &topic, &targetName), 21600, true)
	if err != nil {
		panic(err)
	}
	return &fakeCommandBackend{rule: rule}
}

func (f *fakeCommandBackend) GetModelConfig(context.Context) (*SummaryModelConfig, error) {
	if f.model == nil {
		return nil, nil
	}
	config := *f.model
	return &config, nil
}

func (f *fakeCommandBackend) ConfigureModel(_ context.Context, provider, model string, apiKey, baseURL *string) (SummaryModelConfig, error) {
	f.calls = append(f.calls, []any{"model-set", provider, model, anyString(apiKey), anyString(baseURL)})
	config, err := NewSummaryModelConfig(provider, model, apiKey, baseURL)
	if err != nil {
		return SummaryModelConfig{}, err
	}
	f.model = &config
	return config, nil
}

func (f *fakeCommandBackend) TuneModel(_ context.Context, inputTokenLimit, outputTokenLimit int, temperature float64, timeout time.Duration, maxRetries int, maxConcurrency *int) (SummaryModelConfig, error) {
	var concurrency any
	if maxConcurrency != nil {
		concurrency = *maxConcurrency
	}
	f.calls = append(f.calls, []any{"model-tune", inputTokenLimit, outputTokenLimit, temperature, timeout, maxRetries, concurrency})
	if f.model == nil {
		return SummaryModelConfig{}, &SummaryModelUnavailableError{Msg: "消息总结模型未配置, 请使用 /summary model set 配置。"}
	}
	config := *f.model
	config.InputTokenLimit = inputTokenLimit
	config.OutputTokenLimit = outputTokenLimit
	config.Temperature = temperature
	config.Timeout = timeout
	config.MaxRetries = maxRetries
	if maxConcurrency != nil {
		config.MaxConcurrency = *maxConcurrency
	}
	f.model = &config
	return config, nil
}

func (f *fakeCommandBackend) SetPromptPreset(_ context.Context, preset string) (SummaryModelConfig, error) {
	f.calls = append(f.calls, []any{"prompt-use", preset})
	if f.model == nil {
		return SummaryModelConfig{}, &SummaryModelUnavailableError{Msg: "消息总结模型未配置, 请使用 /summary model set 配置。"}
	}
	config := *f.model
	config.PromptPreset = SummaryPromptPreset(preset)
	config.CustomPrompt = ""
	f.model = &config
	return config, nil
}

func (f *fakeCommandBackend) SetCustomPrompt(_ context.Context, prompt string) (SummaryModelConfig, error) {
	f.calls = append(f.calls, []any{"prompt-custom", prompt})
	if f.model == nil {
		return SummaryModelConfig{}, &SummaryModelUnavailableError{Msg: "消息总结模型未配置, 请使用 /summary model set 配置。"}
	}
	config := *f.model
	config.CustomPrompt = prompt
	f.model = &config
	return config, nil
}

func (f *fakeCommandBackend) ClearModelConfig(context.Context) (bool, error) {
	f.calls = append(f.calls, []any{"model-clear"})
	existed := f.model != nil
	f.model = nil
	return existed, nil
}

func (f *fakeCommandBackend) ListRules(context.Context) ([]SummaryRule, error) {
	return []SummaryRule{f.rule}, nil
}

func (f *fakeCommandBackend) GetRule(context.Context, int) (SummaryRule, error) {
	return f.rule, nil
}

func (f *fakeCommandBackend) AddRule(_ context.Context, source, destination string, windowSeconds *int) (SummaryRule, error) {
	f.calls = append(f.calls, []any{"add", source, destination, anyInt(windowSeconds)})
	return f.rule, nil
}

func (f *fakeCommandBackend) ReplaceRule(_ context.Context, ruleID int, source, destination string, windowSeconds *int) (SummaryRule, error) {
	f.calls = append(f.calls, []any{"set", ruleID, source, destination, anyInt(windowSeconds)})
	return f.rule, nil
}

func (f *fakeCommandBackend) RunRule(_ context.Context, ruleID int, windowSeconds *int) (SummaryExecution, error) {
	f.calls = append(f.calls, []any{"run", ruleID, anyInt(windowSeconds)})
	if f.err != nil {
		return SummaryExecution{}, f.err
	}
	return SummaryExecution{Rule: f.rule, MessageCount: 8, TopicCount: 2, SentMessages: []contracts.MessageRef{{ChatID: -1002, MessageID: 50}}}, nil
}

func (f *fakeCommandBackend) SetEnabled(_ context.Context, ruleID int, enabled bool) (SummaryRule, error) {
	f.calls = append(f.calls, []any{"enabled", ruleID, enabled})
	return f.rule, nil
}

func (f *fakeCommandBackend) RemoveRule(_ context.Context, ruleID int) error {
	f.calls = append(f.calls, []any{"remove", ruleID})
	return nil
}

func anyString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func anyInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
