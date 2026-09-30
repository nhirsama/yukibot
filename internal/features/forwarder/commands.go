package forwarder

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// RouteHelp is the /route command help. It must not start with a slash.
const RouteHelp = `转发路由命令:
/route list - 列出全部路由
/route show <id> - 查看路由详情
/route add <source> <destination> [选项] - 添加路由并自动分配 ID
/route set <id> <source> <destination> [选项] - 更新路由
/route enable <id> - 启用路由
/route disable <id> - 停用路由
/route remove <id> - 删除路由
/route check - 刷新频道名称、链接并检查当前账号加入状态
/route rebuild - 按启用路由重建当前账号的频道和群组
/route rebuild --all - 同时包含已停用路由
/route rebuild status - 查看当前重建进度
/route rebuild cancel - 取消当前重建
source/destination 可使用数字 ID、@username、公开链接或私有邀请链接; 话题在引用末尾加 /话题ID
选项: [forward|copy] [--poll <间隔>]
默认自动加入源频道并实时接收; --poll 5m 表示不自动加入, 每 5 分钟拉取一次。
私有邀请链接会先加入对应聊天; 其他方式配置的目标群必须已加入。
轮询从配置后的新消息开始且不跟踪编辑/删除。
默认使用 forward; 目标为论坛且目标引用未指定话题时, 自动创建与来源同名的话题。`

var (
	pollDuration  = regexp.MustCompile(`^([1-9][0-9]*)([mhd]?)$`)
	numericTopic  = regexp.MustCompile(`^(-?[1-9][0-9]*)/([1-9][0-9]*)$`)
	usernameTopic = regexp.MustCompile(`^(@[^/\s]+)/([1-9][0-9]*)$`)
	telegramHosts = map[string]struct{}{
		"t.me": {}, "telegram.me": {}, "www.t.me": {}, "www.telegram.me": {},
	}
)

type endpointRef struct {
	chat     string
	topic    int
	hasTopic bool
}

// ForwarderCommands serves the /route control command.
type ForwarderCommands struct {
	service  *ForwarderManagementService
	recovery *MembershipRecoveryService
}

// NewForwarderCommands returns the /route handler. Recovery may be nil.
func NewForwarderCommands(service *ForwarderManagementService, recovery *MembershipRecoveryService) (*ForwarderCommands, error) {
	if service == nil {
		return nil, valueErr("management service is required")
	}
	return &ForwarderCommands{service: service, recovery: recovery}, nil
}

// Handle runs one /route invocation. User errors become reply text.
func (c *ForwarderCommands) Handle(ctx context.Context, command ControlCommand) (CommandResult, error) {
	arguments, err := splitShell(command.RawArguments)
	if err != nil {
		return CommandResult{Text: "Invalid arguments: " + err.Error()}, nil
	}
	if len(arguments) == 0 || (len(arguments) == 1 && arguments[0] == "help") {
		return CommandResult{Text: RouteHelp}, nil
	}
	text, err := c.dispatch(ctx, arguments)
	if err != nil {
		if isReportedCommandError(err) {
			return CommandResult{Text: commandErrorText(err)}, nil
		}
		return CommandResult{}, err
	}
	if text == "" {
		return CommandResult{Text: RouteHelp}, nil
	}
	return CommandResult{Text: text}, nil
}

func (c *ForwarderCommands) dispatch(ctx context.Context, arguments []string) (string, error) {
	if len(arguments) == 1 && arguments[0] == "list" {
		routes, err := c.service.ListRoutes(ctx)
		if err != nil {
			return "", err
		}
		if len(routes) == 0 {
			return "No forwarding routes.", nil
		}
		lines := make([]string, len(routes))
		for i, route := range routes {
			source, destination := c.service.RouteTitles(route)
			lines[i] = routeSummary(route, source, destination)
		}
		return strings.Join(lines, "\n"), nil
	}
	if len(arguments) == 2 && arguments[0] == "show" {
		routeID, err := parseRouteID(arguments[1])
		if err != nil {
			return "", err
		}
		route, err := c.service.GetRoute(ctx, routeID)
		if err != nil {
			return "", err
		}
		source, destination := c.service.RouteTitles(route)
		return routeDetails(route, source, destination), nil
	}
	if arguments[0] == "add" && len(arguments) >= 3 {
		draft, identities, err := parseRouteDraft(ctx, arguments[1:], c.service)
		if err != nil {
			return "", err
		}
		route, err := c.service.AddGeneratedRoute(ctx, draft)
		if err != nil {
			return "", err
		}
		if err := c.service.RememberChatAccesses(ctx, identities); err != nil {
			return "", err
		}
		return "Route " + strconv.Itoa(route.ID) + " is configured.", nil
	}
	if arguments[0] == "set" && len(arguments) >= 4 {
		routeID, err := parseRouteID(arguments[1])
		if err != nil {
			return "", err
		}
		draft, identities, err := parseRouteDraft(ctx, arguments[2:], c.service)
		if err != nil {
			return "", err
		}
		route, err := draft.Bind(routeID)
		if err != nil {
			return "", err
		}
		if _, err := c.service.ReplaceRoute(ctx, route); err != nil {
			return "", err
		}
		if err := c.service.RememberChatAccesses(ctx, identities); err != nil {
			return "", err
		}
		return "Route " + strconv.Itoa(route.ID) + " is updated.", nil
	}
	if len(arguments) == 2 && (arguments[0] == "enable" || arguments[0] == "disable") {
		routeID, err := parseRouteID(arguments[1])
		if err != nil {
			return "", err
		}
		route, err := c.service.SetEnabled(ctx, routeID, arguments[0] == "enable")
		if err != nil {
			return "", err
		}
		state := "disabled"
		if route.Enabled {
			state = "enabled"
		}
		return "Route " + strconv.Itoa(route.ID) + " is " + state + ".", nil
	}
	if len(arguments) == 2 && arguments[0] == "remove" {
		routeID, err := parseRouteID(arguments[1])
		if err != nil {
			return "", err
		}
		if err := c.service.RemoveRoute(ctx, routeID); err != nil {
			return "", err
		}
		return "Route " + strconv.Itoa(routeID) + " is removed.", nil
	}
	if len(arguments) == 1 && arguments[0] == "check" && c.recovery != nil {
		report, err := c.recovery.Check(ctx, false)
		if err != nil {
			return "", err
		}
		return membershipReportText(report), nil
	}
	if len(arguments) > 0 && arguments[0] == "rebuild" && c.recovery != nil {
		if len(arguments) == 2 && arguments[1] == "status" {
			return rebuildProgressText(c.recovery.Progress()), nil
		}
		if len(arguments) == 2 && arguments[1] == "cancel" {
			if c.recovery.Cancel() {
				return "当前重建已取消。", nil
			}
			return "当前没有重建任务。", nil
		}
		if len(arguments) == 1 || (len(arguments) == 2 && arguments[1] == "--all") {
			report, err := c.recovery.Rebuild(ctx, len(arguments) == 2)
			if err != nil {
				return "", err
			}
			return rebuildStartedText(report), nil
		}
	}
	return "", nil
}

func parseRouteDraft(ctx context.Context, arguments []string, service *ForwarderManagementService) (RouteDraft, []ChatIdentity, error) {
	positional, pollEvery, err := extractPollOption(arguments)
	if err != nil {
		return RouteDraft{}, nil, err
	}
	if len(positional) < 2 || len(positional) > 3 {
		return RouteDraft{}, nil, valueErr("路由参数数量不正确")
	}
	mode := ForwardModeForward
	if len(positional) >= 3 {
		mode, err = parseForwardMode(positional[2])
		if err != nil {
			return RouteDraft{}, nil, err
		}
	}
	sourceRef, err := endpointReference(positional[0])
	if err != nil {
		return RouteDraft{}, nil, err
	}
	destinationRef, err := endpointReference(positional[1])
	if err != nil {
		return RouteDraft{}, nil, err
	}
	if pollEvery > 0 && isPrivateInvite(sourceRef.chat) {
		return RouteDraft{}, nil, valueErr("轮询源不能使用私有邀请链接, 请改用实时模式")
	}
	sourceIdentity, err := service.ResolveChat(ctx, sourceRef.chat)
	if err != nil {
		return RouteDraft{}, nil, err
	}
	destinationIdentity, err := service.ResolveChat(ctx, destinationRef.chat)
	if err != nil {
		return RouteDraft{}, nil, err
	}
	sourceCfg := SourceConfig{Username: sourceIdentity.Username}
	if sourceRef.hasTopic {
		topic := sourceRef.topic
		sourceCfg.TopicID = &topic
	}
	if pollEvery > 0 {
		sourceCfg.Polled = true
		sourceCfg.PollEvery = pollEvery
	}
	source, err := NewSourceEndpoint(sourceIdentity.ChatID, sourceCfg)
	if err != nil {
		return RouteDraft{}, nil, err
	}
	destinationCfg := DestinationConfig{Username: destinationIdentity.Username}
	if destinationRef.hasTopic {
		topic := destinationRef.topic
		destinationCfg.TopicID = &topic
	}
	destination, err := NewDestinationEndpoint(destinationIdentity.ChatID, destinationCfg)
	if err != nil {
		return RouteDraft{}, nil, err
	}
	draft := NewRouteDraft(source, destination)
	draft.Mode = mode
	return draft, []ChatIdentity{sourceIdentity, destinationIdentity}, nil
}

func extractPollOption(arguments []string) ([]string, time.Duration, error) {
	positional := make([]string, 0, len(arguments))
	var pollValue string
	seen := false
	for index := 0; index < len(arguments); index++ {
		value := arguments[index]
		if value == "--poll" {
			if seen || index+1 >= len(arguments) {
				return nil, 0, valueErr("--poll 必须且只能指定一次间隔")
			}
			pollValue = arguments[index+1]
			seen = true
			index++
			continue
		}
		if strings.HasPrefix(value, "--poll=") {
			if seen {
				return nil, 0, valueErr("--poll 必须且只能指定一次间隔")
			}
			pollValue = strings.TrimPrefix(value, "--poll=")
			seen = true
			continue
		}
		if strings.HasPrefix(value, "--") {
			return nil, 0, valueErr("未知选项: " + value)
		}
		positional = append(positional, value)
	}
	if !seen {
		return positional, 0, nil
	}
	delay, err := pollSeconds(pollValue)
	if err != nil {
		return nil, 0, err
	}
	return positional, delay, nil
}

func pollSeconds(value string) (time.Duration, error) {
	match := pollDuration.FindStringSubmatch(strings.ToLower(value))
	if match == nil {
		return 0, valueErr("轮询间隔格式应为 5m、2h 或 1d")
	}
	amount, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, valueErr("轮询间隔格式应为 5m、2h 或 1d")
	}
	multiplier := 60
	switch match[2] {
	case "", "m":
		multiplier = 60
	case "h":
		multiplier = 3600
	case "d":
		multiplier = 86400
	}
	return time.Duration(amount*multiplier) * time.Second, nil
}

func isPrivateInvite(reference string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reference))
	return strings.Contains(normalized, "t.me/+") ||
		strings.Contains(normalized, "telegram.me/+") ||
		strings.Contains(normalized, "/joinchat/") ||
		strings.HasPrefix(normalized, "tg://join?")
}

func endpointReference(value string) (endpointRef, error) {
	reference := strings.TrimSpace(value)
	if match := numericTopic.FindStringSubmatch(reference); match != nil {
		topic, err := strconv.Atoi(match[2])
		if err != nil {
			return endpointRef{}, err
		}
		return endpointRef{chat: match[1], topic: topic, hasTopic: true}, nil
	}
	if match := usernameTopic.FindStringSubmatch(reference); match != nil {
		topic, err := strconv.Atoi(match[2])
		if err != nil {
			return endpointRef{}, err
		}
		return endpointRef{chat: match[1], topic: topic, hasTopic: true}, nil
	}
	parsed, err := url.Parse(reference)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !telegramHost(parsed.Host) {
		return endpointRef{chat: reference}, nil
	}
	parts := make([]string, 0)
	for _, part := range strings.Split(parsed.Path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 && parts[0] == "s" {
		parts = parts[1:]
	}
	if len(parts) > 0 && parts[0] == "c" {
		if len(parts) != 3 || !allDigits(parts[1]) || !allDigits(parts[2]) {
			return endpointRef{}, valueErr("Telegram 私有群话题链接格式不正确")
		}
		topic, err := strconv.Atoi(parts[2])
		if err != nil {
			return endpointRef{}, err
		}
		return endpointRef{chat: "-100" + parts[1], topic: topic, hasTopic: true}, nil
	}
	if len(parts) == 2 && !strings.HasPrefix(parts[0], "+") && parts[0] != "joinchat" && allDigits(parts[1]) {
		topic, err := strconv.Atoi(parts[1])
		if err != nil {
			return endpointRef{}, err
		}
		return endpointRef{chat: "@" + parts[0], topic: topic, hasTopic: true}, nil
	}
	return endpointRef{chat: reference}, nil
}

func telegramHost(host string) bool {
	_, ok := telegramHosts[strings.ToLower(host)]
	return ok
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseRouteID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil {
		return 0, valueErr("invalid literal for int() with base 10: '" + value + "'")
	}
	return id, nil
}

func parseForwardMode(value string) (ForwardMode, error) {
	switch ForwardMode(value) {
	case ForwardModeForward, ForwardModeCopy:
		return ForwardMode(value), nil
	default:
		return "", valueErr("'" + value + "' is not a valid ForwardMode")
	}
}

func routeSummary(route Route, sourceTitle, destinationTitle string) string {
	state := "disabled"
	if route.Enabled {
		state = "enabled"
	}
	access := ""
	if route.Source.IsPolled() {
		access = ", poll=" + formatPollDuration(route.Source.PollEvery)
	}
	source := formatEndpoint(route.Source.ChatID, route.Source.HasTopic, route.Source.TopicID, route.Source.Username, sourceTitle)
	destination := formatEndpoint(route.Destination.ChatID, route.Destination.HasTopic, route.Destination.TopicID, route.Destination.Username, destinationTitle)
	return strconv.Itoa(route.ID) + ": " + source + " -> " + destination + " (" + string(route.Mode) + ", " + state + access + ")"
}

func routeDetails(route Route, sourceTitle, destinationTitle string) string {
	keywords := strings.Join(route.Filter.Keywords, ", ")
	if keywords == "" {
		keywords = "none"
	}
	lines := []string{
		routeSummary(route, sourceTitle, destinationTitle),
		"keywords: " + keywords,
		"allowed content: " + joinContent(route.Filter.Allowed, "all"),
		"blocked content: " + joinContent(route.Filter.Blocked, "none"),
		"service messages: " + strconv.FormatBool(route.Filter.IncludeService),
		"fallback to copy: " + strconv.FormatBool(route.FallbackToCopy),
		"source chat id: " + strconv.FormatInt(route.Source.ChatID, 10),
		"destination chat id: " + strconv.FormatInt(route.Destination.ChatID, 10),
	}
	return strings.Join(lines, "\n")
}

func joinContent(types []ContentType, empty string) string {
	if len(types) == 0 {
		return empty
	}
	parts := make([]string, len(types))
	for i, item := range types {
		parts[i] = string(item)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func formatEndpoint(chatID int64, hasTopic bool, topicID int, username, title string) string {
	reference := strconv.FormatInt(chatID, 10)
	if username != "" {
		reference = "@" + username
	}
	normalized := ""
	if title != "" {
		normalized = strings.Join(strings.Fields(title), " ")
	}
	chat := reference
	if normalized != "" && normalized != reference && normalized != strconv.FormatInt(chatID, 10) && normalized != "Channel "+strconv.FormatInt(chatID, 10) {
		chat = normalized + " (" + reference + ")"
	}
	if !hasTopic {
		return chat
	}
	return chat + "/" + strconv.Itoa(topicID)
}

func formatPollDuration(delay time.Duration) string {
	seconds := int(delay / time.Second)
	if seconds%86400 == 0 {
		return strconv.Itoa(seconds/86400) + "d"
	}
	if seconds%3600 == 0 {
		return strconv.Itoa(seconds/3600) + "h"
	}
	return strconv.Itoa(seconds/60) + "m"
}

func membershipReportText(report MembershipReport) string {
	lines := []string{
		"频道检查完成: " +
			"已加入 " + strconv.Itoa(report.Count(MembershipJoined)) + ", " +
			"未加入 " + strconv.Itoa(report.Count(MembershipMissing)) + ", " +
			"无法自动重建 " + strconv.Itoa(report.Count(MembershipUnavailable)) + ", " +
			"无需加入 " + strconv.Itoa(report.Count(MembershipNotRequired)) + ", " +
			"资料更新 " + strconv.Itoa(report.Updated) + "。",
	}
	for _, item := range report.Items {
		lines = append(lines, membershipLine(item, true))
	}
	return boundedOutput(lines, 3900)
}

func rebuildStartedText(report MembershipReport) string {
	var pending, unavailable []MembershipItem
	for _, item := range report.Items {
		switch item.State {
		case MembershipMissing:
			pending = append(pending, item)
		case MembershipUnavailable:
			unavailable = append(unavailable, item)
		}
	}
	lines := []string{"没有可自动重建的未加入频道。"}
	if len(pending) > 0 {
		lines[0] = "重建任务已启动, 共 " + strconv.Itoa(len(pending)) + " 个待加入频道; 每次尝试间隔不低于 5 分钟并带随机波动。"
	}
	if len(unavailable) > 0 {
		lines = append(lines, "以下频道缺少用户名或可用邀请链接, 请人工处理:")
		for _, item := range unavailable {
			lines = append(lines, membershipLine(item, false))
		}
	}
	lines = append(lines, "使用 /route rebuild status 查看进度。")
	return boundedOutput(lines, 3900)
}

func membershipLine(item MembershipItem, includeState bool) string {
	labels := map[MembershipState]string{
		MembershipJoined:      "已加入",
		MembershipMissing:     "未加入",
		MembershipUnavailable: "无法自动重建",
		MembershipNotRequired: "无需加入",
	}
	access := item.Access
	title := access.Title
	if title == "" {
		title = access.Username
	}
	if title == "" {
		title = strconv.FormatInt(access.ChatID, 10)
	}
	link := access.JoinReference()
	if link == "" {
		link = "无公开链接"
	}
	routes := make([]string, len(item.RouteIDs))
	for i, id := range item.RouteIDs {
		routes[i] = strconv.Itoa(id)
	}
	prefix := "- "
	if includeState {
		prefix = "[" + labels[item.State] + "] "
	}
	suffix := ""
	if item.MetadataError != "" {
		suffix = "; 元数据读取失败=" + item.MetadataError
	}
	return prefix + title + " | id=" + strconv.FormatInt(access.ChatID, 10) + " | link=" + link +
		" | roles=" + strings.Join(item.Roles, ",") + " | routes=" + strings.Join(routes, ",") + suffix
}

func rebuildProgressText(progress RebuildProgress) string {
	state := "未运行"
	if progress.Active {
		state = "运行中"
	} else if progress.Total > 0 && progress.Completed == progress.Total {
		state = "已完成"
	}
	lines := []string{
		"重建状态: " + state + "; 总数 " + strconv.Itoa(progress.Total) + "; 完成 " + strconv.Itoa(progress.Completed) + "; " +
			"新加入 " + strconv.Itoa(progress.Joined) + "; 已加入 " + strconv.Itoa(progress.AlreadyJoined) + "; " +
			"等待审批 " + strconv.Itoa(progress.ApprovalPending) + "; 失败 " + strconv.Itoa(progress.Failed) + "。",
	}
	if progress.HasCurrent {
		lines = append(lines, "当前频道: "+strconv.FormatInt(progress.CurrentChatID, 10))
	}
	if progress.HasNextAttempt {
		lines = append(lines, "下次尝试时间戳: "+strconv.FormatInt(progress.NextAttemptAt.Unix(), 10))
	}
	for _, failure := range progress.Failures {
		lines = append(lines, "失败 "+strconv.FormatInt(failure.ChatID, 10)+": "+failure.Error)
	}
	return boundedOutput(lines, 3900)
}

func boundedOutput(lines []string, limit int) string {
	output := make([]string, 0, len(lines))
	length := 0
	for _, line := range lines {
		added := utf8.RuneCountInString(line)
		if len(output) > 0 {
			added++
		}
		if length+added > limit {
			output = append(output, "其余结果因消息长度限制省略。")
			break
		}
		output = append(output, line)
		length += added
	}
	return strings.Join(output, "\n")
}

type shellLexer struct {
	runes []rune
	pos   int
	state rune
	token strings.Builder
}

func splitShell(input string) ([]string, error) {
	lex := shellLexer{runes: []rune(input), state: ' '}
	var out []string
	for {
		token, ok, err := lex.readToken()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, token)
	}
}

func (l *shellLexer) next() (rune, bool) {
	if l.pos >= len(l.runes) {
		return 0, false
	}
	ch := l.runes[l.pos]
	l.pos++
	return ch, true
}

func (l *shellLexer) readToken() (string, bool, error) {
	quoted := false
	escapedState := rune(' ')
	for {
		ch, ok := l.next()
		switch l.state {
		case 0:
			l.token.Reset()
			result := ""
			if quoted {
				return result, true, nil
			}
			return "", false, nil
		case ' ':
			if !ok {
				l.state = 0
				break
			}
			if isShellSpace(ch) {
				if l.token.Len() > 0 || quoted {
					result := l.token.String()
					l.token.Reset()
					return result, true, nil
				}
				continue
			}
			if ch == '\\' {
				escapedState = 'a'
				l.state = '\\'
				continue
			}
			if ch == '\'' || ch == '"' {
				l.state = ch
				continue
			}
			l.token.WriteRune(ch)
			l.state = 'a'
		case '\'', '"':
			quoted = true
			if !ok {
				return "", false, valueErr("No closing quotation")
			}
			if ch == l.state {
				l.state = 'a'
				continue
			}
			if l.state == '"' && ch == '\\' {
				escapedState = l.state
				l.state = '\\'
				continue
			}
			l.token.WriteRune(ch)
		case '\\':
			if !ok {
				return "", false, valueErr("No escaped character")
			}
			if (escapedState == '\'' || escapedState == '"') && ch != '\\' && ch != escapedState {
				l.token.WriteRune('\\')
			}
			l.token.WriteRune(ch)
			l.state = escapedState
		case 'a':
			if !ok {
				l.state = 0
				result := l.token.String()
				l.token.Reset()
				if !quoted && result == "" {
					return "", false, nil
				}
				return result, true, nil
			}
			if isShellSpace(ch) {
				l.state = ' '
				if l.token.Len() > 0 || quoted {
					result := l.token.String()
					l.token.Reset()
					return result, true, nil
				}
				continue
			}
			if ch == '\'' || ch == '"' {
				l.state = ch
				continue
			}
			if ch == '\\' {
				escapedState = 'a'
				l.state = '\\'
				continue
			}
			l.token.WriteRune(ch)
		}
	}
}

func isShellSpace(ch rune) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}
