package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// ForwardJobKind is the durable operation stored for one event.
type ForwardJobKind string

const (
	ForwardJobReceive ForwardJobKind = "receive"
	ForwardJobEdit    ForwardJobKind = "edit"
	ForwardJobDelete  ForwardJobKind = "delete"
)

// PendingForwardJob is a job waiting to be inserted.
type PendingForwardJob struct {
	Kind             ForwardJobKind
	Event            any
	DeduplicationKey string
	AvailableAt      time.Time
	GroupKey         *string
}

// ForwardJob is a claimed job. Attempts is already incremented.
type ForwardJob struct {
	ID       int
	Kind     ForwardJobKind
	Event    any
	Attempts int
	GroupKey *string
}

// NewForwardJob validates a claimed job.
func NewForwardJob(id int, kind ForwardJobKind, event any, attempts int, groupKey *string) (ForwardJob, error) {
	if id <= 0 {
		return ForwardJob{}, valueErr("job id must be positive")
	}
	if attempts <= 0 {
		return ForwardJob{}, valueErr("job attempts must be positive after claiming")
	}
	return ForwardJob{ID: id, Kind: kind, Event: event, Attempts: attempts, GroupKey: groupKey}, nil
}

// PendingJobsForEvent builds the durable jobs for one contract event.
func PendingJobsForEvent(event any, now time.Time, albumDelay time.Duration) ([]PendingForwardJob, error) {
	if albumDelay < 0 {
		return nil, valueErr("album_delay must not be negative")
	}
	switch ev := event.(type) {
	case contracts.TelegramMessageReceived:
		return []PendingForwardJob{pendingReceive(ev, now, albumDelay)}, nil
	case *contracts.TelegramMessageReceived:
		if ev == nil {
			return nil, valueErr("forwarding event is required")
		}
		return []PendingForwardJob{pendingReceive(*ev, now, albumDelay)}, nil
	case contracts.TelegramMessageEdited:
		return []PendingForwardJob{pendingEdit(ev, now)}, nil
	case *contracts.TelegramMessageEdited:
		if ev == nil {
			return nil, valueErr("forwarding event is required")
		}
		return []PendingForwardJob{pendingEdit(*ev, now)}, nil
	case contracts.TelegramMessagesDeleted:
		return pendingDeletes(ev, now), nil
	case *contracts.TelegramMessagesDeleted:
		if ev == nil {
			return nil, valueErr("forwarding event is required")
		}
		return pendingDeletes(*ev, now), nil
	default:
		return nil, valueErr("unsupported forwarding event")
	}
}

func pendingReceive(event contracts.TelegramMessageReceived, now time.Time, albumDelay time.Duration) PendingForwardJob {
	message := event.Message
	available := now
	var group *string
	if !isNilValue(message.GroupedID) {
		key := fmt.Sprintf("album:%d:%s", message.Ref.ChatID, formatGroupedID(message.GroupedID))
		group = &key
		available = now.Add(albumDelay)
	}
	return PendingForwardJob{
		Kind:             ForwardJobReceive,
		Event:            event,
		DeduplicationKey: fmt.Sprintf("receive:%d:%d", message.Ref.ChatID, message.Ref.MessageID),
		AvailableAt:      available,
		GroupKey:         group,
	}
}

func pendingEdit(event contracts.TelegramMessageEdited, now time.Time) PendingForwardJob {
	message := event.Message
	version := message.OccurredAt
	if message.EditedAt != nil {
		version = *message.EditedAt
	}
	key := fmt.Sprintf("edit:%d:%d:%s:%s", message.Ref.ChatID, message.Ref.MessageID, pythonISOFormat(version), editableFingerprint(message))
	return PendingForwardJob{
		Kind:             ForwardJobEdit,
		Event:            event,
		DeduplicationKey: key,
		AvailableAt:      now,
	}
}

func pendingDeletes(event contracts.TelegramMessagesDeleted, now time.Time) []PendingForwardJob {
	chat := "unknown"
	if event.ChatID != nil {
		chat = strconv.FormatInt(*event.ChatID, 10)
	}
	jobs := make([]PendingForwardJob, 0, len(event.MessageIDs))
	for _, messageID := range event.MessageIDs {
		deleted := contracts.TelegramMessagesDeleted{
			MessageIDs: []int{messageID},
			OccurredAt: event.OccurredAt,
			ChatID:     event.ChatID,
		}
		jobs = append(jobs, PendingForwardJob{
			Kind:             ForwardJobDelete,
			Event:            deleted,
			DeduplicationKey: fmt.Sprintf("delete:%s:%d", chat, messageID),
			AvailableAt:      now,
		})
	}
	return jobs
}

func editableFingerprint(message contracts.TelegramMessage) string {
	kind, actor, members, title := "", "", "", ""
	if message.Service != nil {
		kind = string(message.Service.Kind)
		actor = message.Service.ActorName
		members = strings.Join(message.Service.MemberNames, "\x1e")
		title = message.Service.NewTitle
	}
	fields := []string{
		string(message.ContentType),
		message.Text,
		message.Caption,
		kind,
		actor,
		members,
		title,
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x1f")))
	return hex.EncodeToString(sum[:])[:16]
}

func pythonISOFormat(value time.Time) string {
	value = value.UTC()
	base := value.Format("2006-01-02T15:04:05")
	micro := value.Nanosecond() / 1000
	if micro == 0 {
		return base + "+00:00"
	}
	return fmt.Sprintf("%s.%06d+00:00", base, micro)
}

func formatGroupedID(value any) string {
	switch n := value.(type) {
	case int:
		return strconv.Itoa(n)
	case int8:
		return strconv.FormatInt(int64(n), 10)
	case int16:
		return strconv.FormatInt(int64(n), 10)
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case uint:
		return strconv.FormatUint(uint64(n), 10)
	case uint32:
		return strconv.FormatUint(uint64(n), 10)
	case uint64:
		return strconv.FormatUint(n, 10)
	case string:
		return n
	default:
		return fmt.Sprint(value)
	}
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
