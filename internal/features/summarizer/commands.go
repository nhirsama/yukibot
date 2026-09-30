package summarizer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SummaryHelp is the /summary command help. It must not start with a slash.
const SummaryHelp = `消息总结命令:
/summary list - 列出总结规则
/summary show <id> - 查看规则详情
/summary add <source> <destination> [时间窗] - 添加规则
/summary set <id> <source> <destination> [时间窗] - 更新规则
/summary run <id> [时间窗] - 立即生成并发送总结
/summary enable <id> - 启用规则
/summary disable <id> - 停用规则
/summary remove <id> - 删除规则
模型配置:
/summary model show
/summary model set <provider> <model> [-api-key <key>] [-base-url <url>]
/summary model tune <input_tokens> <output_tokens> <temperature> <timeout> <retries> [concurrency]
/summary model clear
总结提示词:
/summary prompt list
/summary prompt show
/summary prompt use <focused|decisions|technical|digest>
/summary prompt custom <自定义偏好>
/summary prompt clear
时间窗支持 30m、6h、1d, 默认 1d, 最大 30d。
source/destination 支持数字 ID、@用户名和 Telegram 公开链接。
群组话题支持 -100群组ID/话题ID、https://t.me/c/内部ID/话题ID,
以及 https://t.me/公开群用户名/话题ID。目标可以是私聊、频道、群组或群组话题。`

// ControlCommand is one control-plane slash command.
// Fields mirror the kernel command so the composition root can copy them.
type ControlCommand struct {
	Name         string
	RawArguments string
	ChatID       int64
	MessageID    int
	ActorID      *int64
	Outgoing     bool
}

// CommandResult is the reply text for one command.
type CommandResult struct {
	Text string
}

// CommandBackend is the summarizer surface used by /summary.
// *SummarizerService implements it. Tests can substitute a fake.
type CommandBackend interface {
	GetModelConfig(ctx context.Context) (*SummaryModelConfig, error)
	ConfigureModel(ctx context.Context, provider, model string, apiKey, baseURL *string) (SummaryModelConfig, error)
	TuneModel(ctx context.Context, inputTokenLimit, outputTokenLimit int, temperature float64, timeout time.Duration, maxRetries int, maxConcurrency *int) (SummaryModelConfig, error)
	SetPromptPreset(ctx context.Context, preset string) (SummaryModelConfig, error)
	SetCustomPrompt(ctx context.Context, prompt string) (SummaryModelConfig, error)
	ClearModelConfig(ctx context.Context) (bool, error)
	ListRules(ctx context.Context) ([]SummaryRule, error)
	GetRule(ctx context.Context, ruleID int) (SummaryRule, error)
	AddRule(ctx context.Context, sourceReference, destinationReference string, windowSeconds *int) (SummaryRule, error)
	ReplaceRule(ctx context.Context, ruleID int, sourceReference, destinationReference string, windowSeconds *int) (SummaryRule, error)
	RunRule(ctx context.Context, ruleID int, windowSeconds *int) (SummaryExecution, error)
	SetEnabled(ctx context.Context, ruleID int, enabled bool) (SummaryRule, error)
	RemoveRule(ctx context.Context, ruleID int) error
}

// SummarizerCommands serves the /summary control command.
type SummarizerCommands struct {
	backend CommandBackend
}

// NewSummarizerCommands returns the /summary handler.
func NewSummarizerCommands(backend CommandBackend) *SummarizerCommands {
	return &SummarizerCommands{backend: backend}
}

var summaryDuration = regexp.MustCompile(`^([1-9][0-9]*)([mhd])$`)

// Handle runs one /summary invocation. User errors become reply text.
func (c *SummarizerCommands) Handle(ctx context.Context, command ControlCommand) (CommandResult, error) {
	arguments, err := splitShell(command.RawArguments)
	if err != nil {
		return CommandResult{Text: "Invalid arguments: " + err.Error()}, nil
	}
	if len(arguments) == 0 || (len(arguments) == 1 && arguments[0] == "help") {
		return CommandResult{Text: SummaryHelp}, nil
	}
	text, err := c.dispatch(ctx, arguments)
	if err != nil {
		if isReportedCommandError(err) {
			return CommandResult{Text: err.Error()}, nil
		}
		return CommandResult{}, err
	}
	if text == "" {
		return CommandResult{Text: SummaryHelp}, nil
	}
	return CommandResult{Text: text}, nil
}

func (c *SummarizerCommands) dispatch(ctx context.Context, arguments []string) (string, error) {
	if len(arguments) == 2 && arguments[0] == "model" && arguments[1] == "show" {
		config, err := c.backend.GetModelConfig(ctx)
		if err != nil {
			return "", err
		}
		if config == nil {
			return "Summary model is not configured.", nil
		}
		return formatModelConfig(*config), nil
	}
	if len(arguments) == 2 && arguments[0] == "model" && arguments[1] == "clear" {
		if _, err := c.backend.ClearModelConfig(ctx); err != nil {
			return "", err
		}
		return "Summary model configuration is cleared.", nil
	}
	if len(arguments) >= 4 && arguments[0] == "model" && arguments[1] == "set" {
		options, err := namedOptions(arguments[4:], map[string]struct{}{"-api-key": {}, "-base-url": {}})
		if err != nil {
			return "", err
		}
		config, err := c.backend.ConfigureModel(ctx, arguments[2], arguments[3], optionPtr(options, "-api-key"), optionPtr(options, "-base-url"))
		if err != nil {
			return "", err
		}
		return "Summary model is configured: " + qualifiedModel(config) + ".", nil
	}
	if len(arguments) >= 2 && arguments[0] == "model" && arguments[1] == "tune" && (len(arguments) == 7 || len(arguments) == 8) {
		inputLimit, err := parseCommandInt(arguments[2])
		if err != nil {
			return "", err
		}
		outputLimit, err := parseCommandInt(arguments[3])
		if err != nil {
			return "", err
		}
		temperature, err := parseCommandFloat(arguments[4])
		if err != nil {
			return "", err
		}
		timeoutSeconds, err := parseCommandFloat(arguments[5])
		if err != nil {
			return "", err
		}
		retries, err := parseCommandInt(arguments[6])
		if err != nil {
			return "", err
		}
		var concurrency *int
		if len(arguments) == 8 {
			value, err := parseCommandInt(arguments[7])
			if err != nil {
				return "", err
			}
			concurrency = &value
		}
		config, err := c.backend.TuneModel(ctx, inputLimit, outputLimit, temperature, time.Duration(timeoutSeconds*float64(time.Second)), retries, concurrency)
		if err != nil {
			return "", err
		}
		return formatModelConfig(config), nil
	}
	if len(arguments) == 2 && arguments[0] == "prompt" && arguments[1] == "list" {
		lines := make([]string, 0, len(PresetOrder))
		for _, preset := range PresetOrder {
			definition := PresetDefinitions[preset]
			lines = append(lines, string(preset)+": "+definition.Description)
		}
		return strings.Join(lines, "\n"), nil
	}
	if len(arguments) == 2 && arguments[0] == "prompt" && arguments[1] == "show" {
		config, err := c.backend.GetModelConfig(ctx)
		if err != nil {
			return "", err
		}
		if config == nil {
			return "", &SummarizerError{Msg: "消息总结模型未配置, 请先配置模型。"}
		}
		return formatPromptConfig(*config), nil
	}
	if len(arguments) == 3 && arguments[0] == "prompt" && arguments[1] == "use" {
		config, err := c.backend.SetPromptPreset(ctx, arguments[2])
		if err != nil {
			return "", err
		}
		return formatPromptConfig(config), nil
	}
	if len(arguments) >= 3 && arguments[0] == "prompt" && arguments[1] == "custom" {
		config, err := c.backend.SetCustomPrompt(ctx, strings.Join(arguments[2:], " "))
		if err != nil {
			return "", err
		}
		return formatPromptConfig(config), nil
	}
	if len(arguments) == 2 && arguments[0] == "prompt" && arguments[1] == "clear" {
		config, err := c.backend.SetPromptPreset(ctx, string(PromptFocused))
		if err != nil {
			return "", err
		}
		return formatPromptConfig(config), nil
	}
	if len(arguments) == 1 && arguments[0] == "list" {
		rules, err := c.backend.ListRules(ctx)
		if err != nil {
			return "", err
		}
		if len(rules) == 0 {
			return "No summary rules.", nil
		}
		lines := make([]string, len(rules))
		for i, rule := range rules {
			lines[i] = ruleSummary(rule)
		}
		return strings.Join(lines, "\n"), nil
	}
	if len(arguments) == 2 && arguments[0] == "show" {
		ruleID, err := parseCommandInt(arguments[1])
		if err != nil {
			return "", err
		}
		rule, err := c.backend.GetRule(ctx, ruleID)
		if err != nil {
			return "", err
		}
		return ruleDetails(rule), nil
	}
	if arguments[0] == "add" && len(arguments) >= 3 && len(arguments) <= 4 {
		var window *int
		if len(arguments) == 4 {
			seconds, err := parseWindow(arguments[3])
			if err != nil {
				return "", err
			}
			window = &seconds
		}
		rule, err := c.backend.AddRule(ctx, arguments[1], arguments[2], window)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Summary rule %d is configured.", rule.ID), nil
	}
	if arguments[0] == "set" && len(arguments) >= 4 && len(arguments) <= 5 {
		ruleID, err := parseCommandInt(arguments[1])
		if err != nil {
			return "", err
		}
		var window *int
		if len(arguments) == 5 {
			seconds, err := parseWindow(arguments[4])
			if err != nil {
				return "", err
			}
			window = &seconds
		}
		rule, err := c.backend.ReplaceRule(ctx, ruleID, arguments[2], arguments[3], window)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Summary rule %d is updated.", rule.ID), nil
	}
	if arguments[0] == "run" && len(arguments) >= 2 && len(arguments) <= 3 {
		ruleID, err := parseCommandInt(arguments[1])
		if err != nil {
			return "", err
		}
		var window *int
		if len(arguments) == 3 {
			seconds, err := parseWindow(arguments[2])
			if err != nil {
				return "", err
			}
			window = &seconds
		}
		execution, err := c.backend.RunRule(ctx, ruleID, window)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("总结已发送: 规则 %d, 消息 %d, 主题 %d, 发送 %d 条。", execution.Rule.ID, execution.MessageCount, execution.TopicCount, len(execution.SentMessages)), nil
	}
	if len(arguments) == 2 && (arguments[0] == "enable" || arguments[0] == "disable") {
		ruleID, err := parseCommandInt(arguments[1])
		if err != nil {
			return "", err
		}
		enabled := arguments[0] == "enable"
		rule, err := c.backend.SetEnabled(ctx, ruleID, enabled)
		if err != nil {
			return "", err
		}
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		return fmt.Sprintf("Summary rule %d is %s.", rule.ID, state), nil
	}
	if len(arguments) == 2 && arguments[0] == "remove" {
		ruleID, err := parseCommandInt(arguments[1])
		if err != nil {
			return "", err
		}
		if err := c.backend.RemoveRule(ctx, ruleID); err != nil {
			return "", err
		}
		return fmt.Sprintf("Summary rule %d is removed.", ruleID), nil
	}
	return "", nil
}

func isReportedCommandError(err error) bool {
	var value *ValueError
	if errors.As(err, &value) {
		return true
	}
	var summary *SummarizerError
	return errors.As(err, &summary)
}

func parseWindow(value string) (int, error) {
	match := summaryDuration.FindStringSubmatch(casefold(value))
	if match == nil {
		return 0, &ValueError{Msg: "时间窗格式应为 30m、6h 或 1d"}
	}
	amount, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, &ValueError{Msg: "时间窗必须在 1 分钟到 30 天之间"}
	}
	var multiplier int64
	switch match[2] {
	case "m":
		multiplier = 60
	case "h":
		multiplier = 3600
	default:
		multiplier = 86400
	}
	seconds := amount * multiplier
	if seconds < 60 || seconds > 30*86400 {
		return 0, &ValueError{Msg: "时间窗必须在 1 分钟到 30 天之间"}
	}
	return int(seconds), nil
}

func parseCommandInt(token string) (int, error) {
	value, err := strconv.Atoi(token)
	if err != nil {
		return 0, &ValueError{Msg: "invalid literal for int() with base 10: '" + token + "'"}
	}
	return value, nil
}

func parseCommandFloat(token string) (float64, error) {
	value, err := strconv.ParseFloat(token, 64)
	if err != nil {
		return 0, &ValueError{Msg: "could not convert string to float: '" + token + "'"}
	}
	return value, nil
}

func namedOptions(arguments []string, allowed map[string]struct{}) (map[string]string, error) {
	if len(arguments)%2 != 0 {
		return nil, &ValueError{Msg: "模型选项必须使用 -参数 值 的格式"}
	}
	parsed := map[string]string{}
	for index := 0; index < len(arguments); index += 2 {
		name := normalizeOptionName(arguments[index])
		value := arguments[index+1]
		if _, ok := allowed[name]; !ok {
			return nil, &ValueError{Msg: "未知模型选项: " + name}
		}
		if _, ok := parsed[name]; ok {
			return nil, &ValueError{Msg: "模型选项不能重复: " + name}
		}
		if value == "" || strings.HasPrefix(value, "--") {
			return nil, &ValueError{Msg: "模型选项缺少值: " + name}
		}
		parsed[name] = value
	}
	return parsed, nil
}

func normalizeOptionName(value string) string {
	normalized := strings.NewReplacer("\u2014", "-", "\u2013", "-", "\u2212", "-").Replace(value)
	if strings.HasPrefix(normalized, "-") {
		return "-" + strings.TrimLeft(normalized, "-")
	}
	return normalized
}

func optionPtr(options map[string]string, name string) *string {
	value, ok := options[name]
	if !ok {
		return nil
	}
	return &value
}

func ruleSummary(rule SummaryRule) string {
	state := "disabled"
	if rule.Enabled {
		state = "enabled"
	}
	return fmt.Sprintf("%d: %s -> %s (%s, %s)", rule.ID, formatEndpoint(rule.Source), formatEndpoint(rule.Destination), formatWindow(rule.WindowSeconds), state)
}

func ruleDetails(rule SummaryRule) string {
	return strings.Join([]string{
		ruleSummary(rule),
		"source chat id: " + strconv.FormatInt(rule.Source.ChatID, 10),
		"source topic id: " + formatTopic(rule.Source.TopicID),
		"destination chat id: " + strconv.FormatInt(rule.Destination.ChatID, 10),
		"destination topic id: " + formatTopic(rule.Destination.TopicID),
	}, "\n")
}

func formatEndpoint(endpoint SummaryEndpoint) string {
	chat := strconv.FormatInt(endpoint.ChatID, 10)
	if endpoint.Username != "" {
		chat = "@" + endpoint.Username
	}
	if endpoint.TopicID != 0 {
		return chat + "/" + strconv.Itoa(endpoint.TopicID)
	}
	return chat
}

func formatTopic(topicID int) string {
	if topicID == 0 {
		return "none"
	}
	return strconv.Itoa(topicID)
}

func formatWindow(seconds int) string {
	if seconds%86400 == 0 {
		return strconv.Itoa(seconds/86400) + "d"
	}
	if seconds%3600 == 0 {
		return strconv.Itoa(seconds/3600) + "h"
	}
	return strconv.Itoa(seconds/60) + "m"
}

func formatModelConfig(config SummaryModelConfig) string {
	apiKey := "not configured"
	if config.APIKey != "" {
		apiKey = "configured"
	}
	baseURL := "provider default"
	if config.BaseURL != "" {
		baseURL = config.BaseURL
	}
	activePrompt := string(config.PromptPreset)
	if config.CustomPrompt != "" {
		activePrompt = "custom"
	}
	return strings.Join([]string{
		"provider: " + config.Provider,
		"model: " + config.Model,
		"API key: " + apiKey,
		"base URL: " + baseURL,
		"input tokens: " + strconv.Itoa(config.InputTokenLimit),
		"output tokens: " + strconv.Itoa(config.OutputTokenLimit),
		"temperature: " + pythonG(config.Temperature),
		"timeout: " + pythonG(float64(config.Timeout)/float64(time.Second)) + "s",
		"retries: " + strconv.Itoa(config.MaxRetries),
		"concurrency: " + strconv.Itoa(config.MaxConcurrency),
		"prompt: " + activePrompt,
	}, "\n")
}

func formatPromptConfig(config SummaryModelConfig) string {
	if config.CustomPrompt != "" {
		return strings.Join([]string{
			"prompt: custom",
			"custom prompt: " + config.CustomPrompt,
		}, "\n")
	}
	definition := PresetDefinitions[config.PromptPreset]
	return strings.Join([]string{
		"prompt: " + string(config.PromptPreset),
		"description: " + definition.Description,
	}, "\n")
}

func qualifiedModel(config SummaryModelConfig) string {
	prefix := config.Provider + "/"
	if strings.HasPrefix(casefold(config.Model), casefold(prefix)) {
		return config.Model
	}
	return prefix + config.Model
}

func pythonG(value float64) string {
	return strconv.FormatFloat(value, 'g', 6, 64)
}
