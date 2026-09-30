package forwarder

import (
	"context"
	"time"
)

// RouteRepository stores forwarding routes.
type RouteRepository interface {
	ListForSourceChat(ctx context.Context, chatID int64) ([]Route, error)
	ListAll(ctx context.Context) ([]Route, error)
	Add(ctx context.Context, route Route) error
	AddAuto(ctx context.Context, draft RouteDraft) (Route, error)
	Replace(ctx context.Context, route Route) error
	Remove(ctx context.Context, routeID int) (bool, error)
}

// MessageLinkRepository stores source-to-destination message mappings.
type MessageLinkRepository interface {
	SaveMany(ctx context.Context, links []MessageLink) error
	Get(ctx context.Context, routeID int, source MessageRef) (MessageLink, bool, error)
	FindAll(ctx context.Context, source MessageRef) ([]MessageLink, error)
	FindBySourceMessageID(ctx context.Context, messageID int) ([]MessageLink, error)
	Remove(ctx context.Context, link MessageLink) error
}

// ManagedTopicRepository stores automatically created forum topics.
type ManagedTopicRepository interface {
	Get(ctx context.Context, sourceChatID int64, sourceTopicID *int, destinationChatID int64) (ManagedTopic, bool, error)
	Save(ctx context.Context, topic ManagedTopic) error
}

// PollCursorRepository stores polling cursors.
type PollCursorRepository interface {
	Get(ctx context.Context, sourceChatID int64) (PollCursor, bool, error)
	Save(ctx context.Context, cursor PollCursor) error
}

// ChatAccessRepository stores the last known join metadata for a chat.
type ChatAccessRepository interface {
	GetMany(ctx context.Context, chatIDs []int64) ([]ChatAccess, error)
	Save(ctx context.Context, access ChatAccess) error
}

// ForwardJobRepository is the durable job queue.
// ClaimDue is head-of-line: the lowest pending id blocks later due jobs.
type ForwardJobRepository interface {
	Enqueue(ctx context.Context, jobs []PendingForwardJob) (int, error)
	RecoverIncomplete(ctx context.Context) (int, error)
	ClaimDue(ctx context.Context, now time.Time) ([]ForwardJob, error)
	MarkSucceeded(ctx context.Context, jobIDs []int) error
	MarkFailed(ctx context.Context, jobIDs []int, failure string) error
	Reschedule(ctx context.Context, jobIDs []int, availableAt time.Time, failure string) error
}

// TelegramGateway delivers and edits destination messages.
type TelegramGateway interface {
	ChatTitle(chatID int64) (string, bool)
	IsForum(chatID int64) bool
	CreateForumTopic(ctx context.Context, destinationChatID int64, title string, randomID int64) (int, error)
	EditForumTopic(ctx context.Context, destinationChatID int64, topicID int, title string) error
	DeliverMessage(ctx context.Context, message IncomingMessage, destination DestinationEndpoint, mode ForwardMode, replyToMessageID *int) (MessageRef, error)
	DeliverAlbum(ctx context.Context, messages []IncomingMessage, destination DestinationEndpoint, mode ForwardMode, replyToMessageID *int) ([]MessageRef, error)
	SendText(ctx context.Context, text string, destination DestinationEndpoint, replyToMessageID *int) (MessageRef, error)
	EditFromSource(ctx context.Context, source IncomingMessage, target MessageRef) error
	DeleteMessage(ctx context.Context, target MessageRef) error
}

// TelegramSourceGateway reads public sources and resolves chat references.
type TelegramSourceGateway interface {
	ChatTitle(chatID int64) (string, bool)
	SourceTitle(ctx context.Context, source SourceEndpoint) (string, bool, error)
	ResolveChat(ctx context.Context, reference string) (ChatIdentity, error)
	EnsureSource(ctx context.Context, source SourceEndpoint, join bool) error
	LatestMessageID(ctx context.Context, source SourceEndpoint) (int, error)
	FetchMessagesAfter(ctx context.Context, source SourceEndpoint, afterMessageID int, limit int) ([]IncomingMessage, error)
}

// TelegramRecoveryGateway inspects membership and joins missing chats.
type TelegramRecoveryGateway interface {
	InspectChats(ctx context.Context, chatIDs []int64) ([]ChatInspection, error)
	JoinChat(ctx context.Context, access ChatAccess) (RebuildJoinResult, error)
}
