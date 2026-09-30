package forwarder

import (
	"slices"
	"strings"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// ContentType is the normalized Telegram message kind.
type ContentType = contracts.ContentType

// ServiceKind classifies a service message.
type ServiceKind = contracts.ServiceKind

// MessageRef identifies one message in a chat.
type MessageRef = contracts.MessageRef

// ServiceMessage is the normalized service payload.
type ServiceMessage = contracts.ServiceMessage

// IncomingMessage is the adapter-neutral message passed to the forwarder.
type IncomingMessage = contracts.TelegramMessage

// MessagesDeleted lists messages removed from a chat.
type MessagesDeleted = contracts.TelegramMessagesDeleted

// ForwardMode selects native forward or copy delivery.
type ForwardMode string

const (
	ForwardModeCopy    ForwardMode = "copy"
	ForwardModeForward ForwardMode = "forward"
)

// ChatIdentity is a Telegram chat resolved from an ID, username, or invite link.
type ChatIdentity struct {
	ChatID     int64
	Username   string
	InviteLink string
}

// NewChatIdentity validates and normalizes a chat identity.
func NewChatIdentity(chatID int64, username, inviteLink string) (ChatIdentity, error) {
	if err := validateChatID(chatID); err != nil {
		return ChatIdentity{}, err
	}
	name, err := normalizeUsername(username, username != "")
	if err != nil {
		return ChatIdentity{}, err
	}
	link := strings.TrimSpace(inviteLink)
	return ChatIdentity{ChatID: chatID, Username: name, InviteLink: link}, nil
}

// NewChatIdentityOptional treats an empty username as unset.
func NewChatIdentityOptional(chatID int64, username, inviteLink string) (ChatIdentity, error) {
	if username == "" {
		if err := validateChatID(chatID); err != nil {
			return ChatIdentity{}, err
		}
		return ChatIdentity{ChatID: chatID, InviteLink: strings.TrimSpace(inviteLink)}, nil
	}
	return NewChatIdentity(chatID, username, inviteLink)
}

// SourceEndpoint is a source chat and an optional topic filter.
// HasTopic false means every topic.
type SourceEndpoint struct {
	ChatID    int64
	TopicID   int
	HasTopic  bool
	Username  string
	PollEvery time.Duration
	Polled    bool
}

// SourceConfig supplies optional source fields.
type SourceConfig struct {
	TopicID   *int
	Username  string
	PollEvery time.Duration
	Polled    bool
}

// NewSourceEndpoint validates a source.
func NewSourceEndpoint(chatID int64, cfg SourceConfig) (SourceEndpoint, error) {
	if err := validateChatID(chatID); err != nil {
		return SourceEndpoint{}, err
	}
	if cfg.TopicID != nil {
		if err := validateTopicID(*cfg.TopicID); err != nil {
			return SourceEndpoint{}, err
		}
	}
	username, err := normalizeUsername(cfg.Username, cfg.Username != "")
	if err != nil {
		return SourceEndpoint{}, err
	}
	if cfg.Polled && cfg.PollEvery < 60*time.Second {
		return SourceEndpoint{}, valueErr("poll interval must be at least 60 seconds")
	}
	endpoint := SourceEndpoint{
		ChatID:    chatID,
		Username:  username,
		PollEvery: cfg.PollEvery,
		Polled:    cfg.Polled,
	}
	if cfg.TopicID != nil {
		endpoint.TopicID = *cfg.TopicID
		endpoint.HasTopic = true
	}
	return endpoint, nil
}

// IsPolled reports whether the source is ingested by polling.
func (s SourceEndpoint) IsPolled() bool { return s.Polled }

// Matches reports whether a chat and topic belong to the source.
func (s SourceEndpoint) Matches(chatID int64, topicID *int) bool {
	if s.ChatID != chatID {
		return false
	}
	if !s.HasTopic {
		return true
	}
	return NormalizeGeneralTopic(intPtr(s.TopicID)) == NormalizeGeneralTopic(topicID)
}

// DestinationEndpoint is a destination chat. HasTopic false means no explicit forum topic.
type DestinationEndpoint struct {
	ChatID   int64
	TopicID  int
	HasTopic bool
	Username string
}

// DestinationConfig supplies optional destination fields.
type DestinationConfig struct {
	TopicID  *int
	Username string
}

// NewDestinationEndpoint validates a destination.
func NewDestinationEndpoint(chatID int64, cfg DestinationConfig) (DestinationEndpoint, error) {
	if err := validateChatID(chatID); err != nil {
		return DestinationEndpoint{}, err
	}
	if cfg.TopicID != nil {
		if err := validateTopicID(*cfg.TopicID); err != nil {
			return DestinationEndpoint{}, err
		}
	}
	username, err := normalizeUsername(cfg.Username, cfg.Username != "")
	if err != nil {
		return DestinationEndpoint{}, err
	}
	endpoint := DestinationEndpoint{ChatID: chatID, Username: username}
	if cfg.TopicID != nil {
		endpoint.TopicID = *cfg.TopicID
		endpoint.HasTopic = true
	}
	return endpoint, nil
}

// PollCursor is the highest source message ID durably handed to the queue.
type PollCursor struct {
	SourceChatID  int64
	LastMessageID int
}

// NewPollCursor validates a cursor.
func NewPollCursor(sourceChatID int64, lastMessageID int) (PollCursor, error) {
	if err := validateChatID(sourceChatID); err != nil {
		return PollCursor{}, err
	}
	if lastMessageID < 0 {
		return PollCursor{}, valueErr("last_message_id must not be negative")
	}
	return PollCursor{SourceChatID: sourceChatID, LastMessageID: lastMessageID}, nil
}

// ManagedTopic is a topic created from one source chat/topic in one destination forum.
type ManagedTopic struct {
	SourceChatID      int64
	DestinationChatID int64
	TopicID           int
	Title             string
	SourceTopicID     int
	HasSourceTopic    bool
}

// NewManagedTopic validates and normalizes a managed topic.
func NewManagedTopic(sourceChatID, destinationChatID int64, topicID int, title string, sourceTopicID *int) (ManagedTopic, error) {
	if err := validateChatID(sourceChatID); err != nil {
		return ManagedTopic{}, err
	}
	if err := validateChatID(destinationChatID); err != nil {
		return ManagedTopic{}, err
	}
	if sourceTopicID != nil {
		if err := validateTopicID(*sourceTopicID); err != nil {
			return ManagedTopic{}, err
		}
	}
	if topicID <= 0 {
		return ManagedTopic{}, valueErr("topic_id must be positive")
	}
	if title == "" {
		return ManagedTopic{}, valueErr("topic title must not be empty")
	}
	topic := ManagedTopic{
		SourceChatID:      sourceChatID,
		DestinationChatID: destinationChatID,
		TopicID:           topicID,
		Title:             title,
	}
	if sourceTopicID != nil {
		normalized := NormalizeGeneralTopic(sourceTopicID)
		topic.SourceTopicID = normalized
		topic.HasSourceTopic = true
	}
	return topic, nil
}

// MessageFilter selects messages by keyword and content type.
type MessageFilter struct {
	Keywords       []string
	Allowed        []ContentType
	Blocked        []ContentType
	IncludeService bool
}

// NewMessageFilter drops blank keywords. Empty allowed means every type.
func NewMessageFilter(keywords []string, allowed, blocked []ContentType, includeService bool) MessageFilter {
	normalized := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" {
			normalized = append(normalized, keyword)
		}
	}
	return MessageFilter{
		Keywords:       normalized,
		Allowed:        append([]ContentType(nil), allowed...),
		Blocked:        append([]ContentType(nil), blocked...),
		IncludeService: includeService,
	}
}

// Equal compares filters. Content-type lists are sets.
func (f MessageFilter) Equal(other MessageFilter) bool {
	return f.IncludeService == other.IncludeService &&
		slices.Equal(f.Keywords, other.Keywords) &&
		sameContentSet(f.Allowed, other.Allowed) &&
		sameContentSet(f.Blocked, other.Blocked)
}

// Allows reports whether one message passes the filter.
func (f MessageFilter) Allows(message IncomingMessage) bool {
	return f.allows(message, true)
}

func (f MessageFilter) allows(message IncomingMessage, checkKeywords bool) bool {
	if message.ContentType == contracts.ContentService && !f.IncludeService {
		return false
	}
	if len(f.Allowed) > 0 && !slices.Contains(f.Allowed, message.ContentType) {
		return false
	}
	if slices.Contains(f.Blocked, message.ContentType) {
		return false
	}
	if checkKeywords && len(f.Keywords) > 0 {
		text := strings.ToLower(message.SearchableText())
		for _, keyword := range f.Keywords {
			if strings.Contains(text, strings.ToLower(keyword)) {
				return true
			}
		}
		return false
	}
	return true
}

// AllowsAlbum reports whether every item passes and the joined text matches keywords.
func (f MessageFilter) AllowsAlbum(messages []IncomingMessage) bool {
	if len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		if !f.allows(message, false) {
			return false
		}
	}
	if len(f.Keywords) == 0 {
		return true
	}
	parts := make([]string, len(messages))
	for i, message := range messages {
		parts[i] = message.SearchableText()
	}
	text := strings.ToLower(strings.Join(parts, "\n"))
	for _, keyword := range f.Keywords {
		if strings.Contains(text, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// Route is one enabled or disabled forwarding rule.
type Route struct {
	ID             int
	Source         SourceEndpoint
	Destination    DestinationEndpoint
	Mode           ForwardMode
	Filter         MessageFilter
	Enabled        bool
	FallbackToCopy bool
}

// NewRoute builds an enabled forward route that falls back to copy.
func NewRoute(id int, source SourceEndpoint, destination DestinationEndpoint) (Route, error) {
	return NewRouteWith(id, source, destination, MessageFilter{}, ForwardModeForward, true, true)
}

// NewRouteWith validates a fully specified route.
func NewRouteWith(id int, source SourceEndpoint, destination DestinationEndpoint, filter MessageFilter, mode ForwardMode, enabled, fallback bool) (Route, error) {
	route := Route{
		ID:             id,
		Source:         source,
		Destination:    destination,
		Mode:           mode,
		Filter:         filter,
		Enabled:        enabled,
		FallbackToCopy: fallback,
	}
	if err := route.Validate(); err != nil {
		return Route{}, err
	}
	return route, nil
}

// Validate checks route invariants.
func (r Route) Validate() error {
	if r.ID <= 0 {
		return valueErr("route id must be positive")
	}
	if err := r.Source.Validate(); err != nil {
		return err
	}
	if err := r.Destination.Validate(); err != nil {
		return err
	}
	if r.Mode != ForwardModeCopy && r.Mode != ForwardModeForward {
		return valueErr("'" + string(r.Mode) + "' is not a valid ForwardMode")
	}
	return nil
}

// Equal compares every route field.
func (r Route) Equal(other Route) bool {
	return r.ID == other.ID &&
		r.Source == other.Source &&
		r.Destination == other.Destination &&
		r.Mode == other.Mode &&
		r.Filter.Equal(other.Filter) &&
		r.Enabled == other.Enabled &&
		r.FallbackToCopy == other.FallbackToCopy
}

// Matches reports whether an enabled route accepts the message.
func (r Route) Matches(message IncomingMessage) bool {
	return r.Enabled &&
		r.Source.Matches(message.Ref.ChatID, message.TopicID) &&
		r.Filter.Allows(message)
}

// MatchesAlbum reports whether an enabled route accepts the album.
func (r Route) MatchesAlbum(messages []IncomingMessage) bool {
	if len(messages) == 0 {
		return false
	}
	return r.Enabled &&
		r.Source.Matches(messages[0].Ref.ChatID, messages[0].TopicID) &&
		r.Filter.AllowsAlbum(messages)
}

// RouteDraft is a route whose persistent ID has not been allocated yet.
type RouteDraft struct {
	Source         SourceEndpoint
	Destination    DestinationEndpoint
	Mode           ForwardMode
	Filter         MessageFilter
	Enabled        bool
	FallbackToCopy bool
}

// NewRouteDraft builds an enabled forward draft that falls back to copy.
func NewRouteDraft(source SourceEndpoint, destination DestinationEndpoint) RouteDraft {
	return RouteDraft{
		Source:         source,
		Destination:    destination,
		Mode:           ForwardModeForward,
		Enabled:        true,
		FallbackToCopy: true,
	}
}

// Bind allocates the draft's persistent ID.
func (d RouteDraft) Bind(routeID int) (Route, error) {
	return NewRouteWith(routeID, d.Source, d.Destination, d.Filter, d.Mode, d.Enabled, d.FallbackToCopy)
}

// Matches ignores the route ID and enabled flag.
func (d RouteDraft) Matches(route Route) bool {
	return d.Source == route.Source &&
		d.Destination == route.Destination &&
		d.Mode == route.Mode &&
		d.Filter.Equal(route.Filter) &&
		d.FallbackToCopy == route.FallbackToCopy
}

// MessageLink maps one source message to its destination delivery.
type MessageLink struct {
	RouteID      int
	Source       MessageRef
	Destination  MessageRef
	DeliveryMode ForwardMode
}

// NewMessageLink defaults the delivery mode to copy.
func NewMessageLink(routeID int, source, destination MessageRef, mode ForwardMode) (MessageLink, error) {
	if mode == "" {
		mode = ForwardModeCopy
	}
	link := MessageLink{RouteID: routeID, Source: source, Destination: destination, DeliveryMode: mode}
	if err := link.Validate(); err != nil {
		return MessageLink{}, err
	}
	return link, nil
}

// Validate checks the link.
func (l MessageLink) Validate() error {
	if l.RouteID <= 0 {
		return valueErr("route_id must be positive")
	}
	return nil
}

// NewIncomingMessage validates a normalized message.
func NewIncomingMessage(message IncomingMessage) (IncomingMessage, error) {
	if err := message.Validate(); err != nil {
		return IncomingMessage{}, err
	}
	return message, nil
}

// NormalizeGeneralTopic maps Telegram's None/0/1 representations to the General topic.
func NormalizeGeneralTopic(topicID *int) int {
	if topicID == nil || *topicID == 0 || *topicID == 1 {
		return 1
	}
	return *topicID
}

func (s SourceEndpoint) Validate() error {
	if err := validateChatID(s.ChatID); err != nil {
		return err
	}
	if s.HasTopic {
		if err := validateTopicID(s.TopicID); err != nil {
			return err
		}
	}
	if _, err := normalizeUsername(s.Username, s.Username != ""); err != nil {
		return err
	}
	if s.Polled && s.PollEvery < 60*time.Second {
		return valueErr("poll interval must be at least 60 seconds")
	}
	return nil
}

func (d DestinationEndpoint) Validate() error {
	if err := validateChatID(d.ChatID); err != nil {
		return err
	}
	if d.HasTopic {
		if err := validateTopicID(d.TopicID); err != nil {
			return err
		}
	}
	_, err := normalizeUsername(d.Username, d.Username != "")
	return err
}

func validateChatID(chatID int64) error {
	if chatID == 0 {
		return valueErr("chat_id must not be zero")
	}
	return nil
}

func validateTopicID(topicID int) error {
	if topicID < 0 {
		return valueErr("topic_id must not be negative")
	}
	return nil
}

func normalizeUsername(username string, present bool) (string, error) {
	if !present {
		return "", nil
	}
	normalized := strings.TrimPrefix(strings.TrimSpace(username), "@")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" || strings.ContainsFunc(normalized, unicodeSpace) {
		return "", valueErr("Telegram username must not be empty or contain whitespace")
	}
	return normalized, nil
}

func unicodeSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

func sameContentSet(left, right []ContentType) bool {
	if len(left) != len(right) {
		return false
	}
	seen := map[ContentType]int{}
	for _, item := range left {
		seen[item]++
	}
	for _, item := range right {
		seen[item]--
		if seen[item] < 0 {
			return false
		}
	}
	return true
}

func intPtr(value int) *int { return &value }

func topicPtr(endpoint SourceEndpoint) *int {
	if !endpoint.HasTopic {
		return nil
	}
	return intPtr(endpoint.TopicID)
}
