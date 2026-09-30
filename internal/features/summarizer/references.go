package summarizer

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var numericTopic = regexp.MustCompile(`^(-?[1-9][0-9]*)/([1-9][0-9]*)$`)

var telegramHosts = map[string]struct{}{
	"t.me":            {},
	"telegram.me":     {},
	"www.t.me":        {},
	"www.telegram.me": {},
}

// EndpointReference is a chat or forum-topic reference before it is resolved.
// Numeric is set when ChatID is the identifier. Otherwise Username includes the @ prefix.
// HasTopic distinguishes an explicit topic, including zero, from no topic.
type EndpointReference struct {
	Numeric  bool
	ChatID   int64
	Username string
	TopicID  int
	HasTopic bool
}

// ParseEndpointReference parses a Telegram id, @username, public link, or topic link.
func ParseEndpointReference(value string) (EndpointReference, error) {
	reference := strings.TrimSpace(value)
	if reference == "" {
		return EndpointReference{}, &ValueError{Msg: "Telegram reference must not be empty"}
	}
	if match := numericTopic.FindStringSubmatch(reference); match != nil {
		chatID, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return EndpointReference{}, &ValueError{Msg: "聊天引用必须是 ID、@用户名、公开链接或话题链接"}
		}
		topicID, err := strconv.Atoi(match[2])
		if err != nil {
			return EndpointReference{}, &ValueError{Msg: "聊天引用必须是 ID、@用户名、公开链接或话题链接"}
		}
		return EndpointReference{Numeric: true, ChatID: chatID, TopicID: topicID, HasTopic: true}, nil
	}
	if chatID, err := strconv.ParseInt(reference, 10, 64); err == nil {
		return EndpointReference{Numeric: true, ChatID: chatID}, nil
	}
	if strings.HasPrefix(reference, "@") && len(reference) > 1 && !strings.Contains(reference, "/") {
		return EndpointReference{Username: reference}, nil
	}
	parsed, err := url.Parse(reference)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !telegramHost(parsed.Host) {
		return EndpointReference{}, &ValueError{Msg: "聊天引用必须是 ID、@用户名、公开链接或话题链接"}
	}
	parts := pathParts(parsed.Path)
	if len(parts) > 0 && parts[0] == "s" {
		parts = parts[1:]
	}
	if len(parts) == 0 || strings.HasPrefix(parts[0], "+") || parts[0] == "joinchat" {
		return EndpointReference{}, &ValueError{Msg: "摘要规则暂不支持私有邀请链接"}
	}
	if parts[0] == "c" {
		if len(parts) != 3 || !allDigits(parts[1]) || !allDigits(parts[2]) {
			return EndpointReference{}, &ValueError{Msg: "私有群话题链接格式不正确"}
		}
		chatID, err := strconv.ParseInt("-100"+parts[1], 10, 64)
		if err != nil {
			return EndpointReference{}, &ValueError{Msg: "私有群话题链接格式不正确"}
		}
		topicID, err := strconv.Atoi(parts[2])
		if err != nil {
			return EndpointReference{}, &ValueError{Msg: "私有群话题链接格式不正确"}
		}
		return EndpointReference{Numeric: true, ChatID: chatID, TopicID: topicID, HasTopic: true}, nil
	}
	if len(parts) == 1 {
		return EndpointReference{Username: "@" + parts[0]}, nil
	}
	if len(parts) == 2 && allDigits(parts[1]) {
		topicID, err := strconv.Atoi(parts[1])
		if err != nil {
			return EndpointReference{}, &ValueError{Msg: "Telegram 公开链接格式不正确"}
		}
		return EndpointReference{Username: "@" + parts[0], TopicID: topicID, HasTopic: true}, nil
	}
	return EndpointReference{}, &ValueError{Msg: "Telegram 公开链接格式不正确"}
}

func telegramHost(host string) bool {
	_, ok := telegramHosts[strings.ToLower(host)]
	return ok
}

func pathParts(path string) []string {
	raw := strings.Split(path, "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
