package forwarder

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ManagedTopicService creates and renames route-owned forum topics.
type ManagedTopicService struct {
	repository ManagedTopicRepository
	telegram   TelegramGateway

	mu    sync.Mutex
	locks map[topicLockKey]*sync.Mutex
}

type topicLockKey struct {
	source int64
	dest   int64
	topic  int
	has    bool
}

// NewManagedTopicService returns a topic resolver.
func NewManagedTopicService(repository ManagedTopicRepository, telegram TelegramGateway) *ManagedTopicService {
	return &ManagedTopicService{
		repository: repository,
		telegram:   telegram,
		locks:      map[topicLockKey]*sync.Mutex{},
	}
}

// Resolve returns an explicit destination, creating or renaming an automatic topic.
// sourceTitle nil reuses a stored title. A non-nil title is an explicit rename.
func (s *ManagedTopicService) Resolve(ctx context.Context, route Route, sourceTitle *string) (DestinationEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return DestinationEndpoint{}, err
	}
	if route.Destination.HasTopic || !s.telegram.IsForum(route.Destination.ChatID) {
		return route.Destination, nil
	}
	var sourceTopic *int
	if route.Source.HasTopic {
		normalized := NormalizeGeneralTopic(intPtr(route.Source.TopicID))
		sourceTopic = &normalized
	}
	key := topicLockKey{source: route.Source.ChatID, dest: route.Destination.ChatID}
	if sourceTopic != nil {
		key.has = true
		key.topic = *sourceTopic
	}
	unlock := s.lock(key)
	defer unlock()

	existing, ok, err := s.repository.Get(ctx, route.Source.ChatID, sourceTopic, route.Destination.ChatID)
	if err != nil {
		return DestinationEndpoint{}, err
	}
	if ok {
		title := existing.Title
		if sourceTitle != nil {
			title, err = topicTitle(*sourceTitle)
			if err != nil {
				return DestinationEndpoint{}, err
			}
		}
		if title != existing.Title {
			if err := s.telegram.EditForumTopic(ctx, existing.DestinationChatID, existing.TopicID, title); err != nil {
				return DestinationEndpoint{}, err
			}
			existing.Title = title
			if err := s.repository.Save(ctx, existing); err != nil {
				return DestinationEndpoint{}, err
			}
		}
		return DestinationEndpoint{ChatID: existing.DestinationChatID, TopicID: existing.TopicID, HasTopic: true}, nil
	}

	raw := ""
	if sourceTitle != nil {
		raw = *sourceTitle
	}
	if strings.TrimSpace(raw) == "" {
		if title, ok := s.telegram.ChatTitle(route.Source.ChatID); ok {
			raw = title
		}
	}
	if strings.TrimSpace(raw) == "" {
		return DestinationEndpoint{}, permanentDeliveryf(
			"cannot create an automatic topic because chat %d has no resolved title",
			route.Source.ChatID,
		)
	}
	title, err := topicTitle(raw)
	if err != nil {
		return DestinationEndpoint{}, err
	}
	topicID, err := s.telegram.CreateForumTopic(ctx, route.Destination.ChatID, title, creationRandomID(route.Source.ChatID, sourceTopic, route.Destination.ChatID))
	if err != nil {
		return DestinationEndpoint{}, err
	}
	var storedTopic *int
	if sourceTopic != nil {
		value := *sourceTopic
		storedTopic = &value
	}
	topic, err := NewManagedTopic(route.Source.ChatID, route.Destination.ChatID, topicID, title, storedTopic)
	if err != nil {
		return DestinationEndpoint{}, err
	}
	if err := s.repository.Save(ctx, topic); err != nil {
		return DestinationEndpoint{}, err
	}
	return DestinationEndpoint{ChatID: topic.DestinationChatID, TopicID: topic.TopicID, HasTopic: true}, nil
}

func (s *ManagedTopicService) lock(key topicLockKey) func() {
	s.mu.Lock()
	lock := s.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[key] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func topicTitle(value string) (string, error) {
	title := strings.TrimSpace(value)
	if title == "" {
		return "", valueErr("topic title must not be empty")
	}
	encoded := []byte(title)
	if len(encoded) <= 128 {
		return title, nil
	}
	cut := encoded[:128]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRightFunc(string(cut), unicode.IsSpace), nil
}

func creationRandomID(sourceChatID int64, sourceTopicID *int, destinationChatID int64) int64 {
	identity := fmt.Sprintf("%d:%d", sourceChatID, destinationChatID)
	if sourceTopicID != nil {
		identity = fmt.Sprintf("%d:%d:%d", sourceChatID, *sourceTopicID, destinationChatID)
	}
	sum := sha256.Sum256([]byte("yukibot-topic:" + identity))
	value := int64(binary.BigEndian.Uint64(sum[:8]))
	if value == 0 {
		return 1
	}
	return value
}
