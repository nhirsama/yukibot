package forwarder

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// SyncOperation is an edit or delete synchronization.
type SyncOperation string

const (
	SyncEdit   SyncOperation = "edit"
	SyncDelete SyncOperation = "delete"
)

// DeliveryOutcome is one successful or deduplicated route delivery.
type DeliveryOutcome struct {
	RouteID      int
	Sources      []MessageRef
	Destinations []MessageRef
	ModeUsed     ForwardMode
	Deduplicated bool
}

// DeliveryFailure is one route that did not deliver.
type DeliveryFailure struct {
	RouteID int
	Sources []MessageRef
	Error   error
}

// ForwardingReport is the result of delivering one message or album.
type ForwardingReport struct {
	Outcomes      []DeliveryOutcome
	Failures      []DeliveryFailure
	MatchedRoutes int
	IgnoredReason string
	Buffered      bool
}

// DeliveredMessages counts destinations that were sent during this call.
func (r ForwardingReport) DeliveredMessages() int {
	total := 0
	for _, outcome := range r.Outcomes {
		if !outcome.Deduplicated {
			total += len(outcome.Destinations)
		}
	}
	return total
}

// DeduplicatedMessages counts destinations reused from stored links.
func (r ForwardingReport) DeduplicatedMessages() int {
	total := 0
	for _, outcome := range r.Outcomes {
		if outcome.Deduplicated {
			total += len(outcome.Destinations)
		}
	}
	return total
}

// Errors returns the route failures.
func (r ForwardingReport) Errors() []error {
	out := make([]error, 0, len(r.Failures))
	for _, failure := range r.Failures {
		if failure.Error != nil {
			out = append(out, failure.Error)
		}
	}
	return out
}

// SyncFailure is one link that could not be edited or deleted.
type SyncFailure struct {
	Operation SyncOperation
	Link      MessageLink
	Error     error
}

// SyncReport is the result of an edit or delete synchronization.
type SyncReport struct {
	Operation     SyncOperation
	Synchronized  int
	Failures      []SyncFailure
	IgnoredReason string
}

// Errors returns the synchronization failures.
func (r SyncReport) Errors() []error {
	out := make([]error, 0, len(r.Failures))
	for _, failure := range r.Failures {
		if failure.Error != nil {
			out = append(out, failure.Error)
		}
	}
	return out
}

// ForwarderOptions controls edit and delete synchronization.
// Nil sync pointers keep the Python defaults (both enabled).
type ForwarderOptions struct {
	SyncEdits             *bool
	SyncDeletes           *bool
	AllowAmbiguousDeletes bool
}

func (o ForwarderOptions) syncEdits() bool {
	if o.SyncEdits == nil {
		return true
	}
	return *o.SyncEdits
}

func (o ForwarderOptions) syncDeletes() bool {
	if o.SyncDeletes == nil {
		return true
	}
	return *o.SyncDeletes
}

// ForwarderService coordinates routes, message mappings, and delivery.
type ForwarderService struct {
	routes   RouteRepository
	links    MessageLinkRepository
	telegram TelegramGateway
	options  ForwarderOptions
	topics   *ManagedTopicService

	mu    sync.Mutex
	locks map[int]*sync.Mutex
}

// NewForwarderService returns a forwarding use-case service.
// The zero ForwarderOptions value synchronizes edits and deletes.
func NewForwarderService(routes RouteRepository, links MessageLinkRepository, telegram TelegramGateway, options ForwarderOptions, topics *ManagedTopicService) *ForwarderService {
	return &ForwarderService{
		routes:   routes,
		links:    links,
		telegram: telegram,
		options:  options,
		topics:   topics,
		locks:    map[int]*sync.Mutex{},
	}
}

// ForwardMessage delivers one message to every matching route.
func (s *ForwarderService) ForwardMessage(ctx context.Context, message IncomingMessage) (ForwardingReport, error) {
	if err := ctx.Err(); err != nil {
		return ForwardingReport{}, err
	}
	title, hasTitle := changedTitle(message)
	if message.Outgoing && !hasTitle {
		return ForwardingReport{IgnoredReason: "outgoing_message"}, nil
	}
	candidates, err := s.routes.ListForSourceChat(ctx, message.Ref.ChatID)
	if err != nil {
		return ForwardingReport{}, err
	}
	matchedIDs := map[int]struct{}{}
	matched := 0
	if !message.Outgoing {
		for _, route := range candidates {
			if route.Matches(message) {
				matchedIDs[route.ID] = struct{}{}
				matched++
			}
		}
	}
	var outcomes []DeliveryOutcome
	var failures []DeliveryFailure
	for _, route := range candidates {
		_, matchedRoute := matchedIDs[route.ID]
		if !matchedRoute && (!hasTitle || !route.Enabled) {
			continue
		}
		outcome, failure, err := s.forwardOne(ctx, route, message, matchedRoute, title, hasTitle)
		if err != nil {
			return ForwardingReport{}, err
		}
		if outcome != nil {
			outcomes = append(outcomes, *outcome)
		}
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	return ForwardingReport{Outcomes: outcomes, Failures: failures, MatchedRoutes: matched}, nil
}

// ForwardAlbum delivers one media group to every matching route.
func (s *ForwarderService) ForwardAlbum(ctx context.Context, messages []IncomingMessage) (ForwardingReport, error) {
	if err := ctx.Err(); err != nil {
		return ForwardingReport{}, err
	}
	ordered, err := orderAlbum(messages)
	if err != nil {
		return ForwardingReport{}, err
	}
	for _, message := range ordered {
		if message.Outgoing {
			return ForwardingReport{IgnoredReason: "outgoing_message"}, nil
		}
	}
	first := ordered[0]
	candidates, err := s.routes.ListForSourceChat(ctx, first.Ref.ChatID)
	if err != nil {
		return ForwardingReport{}, err
	}
	var matched []Route
	for _, route := range candidates {
		if route.MatchesAlbum(ordered) {
			matched = append(matched, route)
		}
	}
	sources := make([]MessageRef, len(ordered))
	for i, message := range ordered {
		sources[i] = message.Ref
	}
	var outcomes []DeliveryOutcome
	var failures []DeliveryFailure
	for _, route := range matched {
		outcome, failure, err := s.forwardAlbum(ctx, route, ordered, sources)
		if err != nil {
			return ForwardingReport{}, err
		}
		if outcome != nil {
			outcomes = append(outcomes, *outcome)
		}
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	return ForwardingReport{Outcomes: outcomes, Failures: failures, MatchedRoutes: len(matched)}, nil
}

// SynchronizeEdit applies a source edit to every copied destination.
func (s *ForwarderService) SynchronizeEdit(ctx context.Context, message IncomingMessage) (SyncReport, error) {
	if err := ctx.Err(); err != nil {
		return SyncReport{}, err
	}
	if !s.options.syncEdits() {
		return SyncReport{Operation: SyncEdit, IgnoredReason: "edit_sync_disabled"}, nil
	}
	if message.Outgoing {
		return SyncReport{Operation: SyncEdit, IgnoredReason: "outgoing_message"}, nil
	}
	links, err := s.links.FindAll(ctx, message.Ref)
	if err != nil {
		return SyncReport{}, err
	}
	synchronized := 0
	var failures []SyncFailure
	for _, link := range links {
		if link.DeliveryMode == ForwardModeForward {
			synchronized++
			continue
		}
		err := s.telegram.EditFromSource(ctx, message, link.Destination)
		if err == nil {
			synchronized++
			continue
		}
		if isCancel(err) {
			return SyncReport{}, err
		}
		var unchanged MessageNotModified
		if errors.As(err, &unchanged) {
			synchronized++
			continue
		}
		var missing MessageNotFound
		if errors.As(err, &missing) {
			if removeErr := s.links.Remove(ctx, link); isCancel(removeErr) {
				return SyncReport{}, removeErr
			}
			failures = append(failures, SyncFailure{Operation: SyncEdit, Link: link, Error: err})
			continue
		}
		failures = append(failures, SyncFailure{Operation: SyncEdit, Link: link, Error: err})
	}
	return SyncReport{Operation: SyncEdit, Synchronized: synchronized, Failures: failures}, nil
}

// SynchronizeDelete removes destination copies of deleted source messages.
func (s *ForwarderService) SynchronizeDelete(ctx context.Context, event MessagesDeleted) (SyncReport, error) {
	if err := ctx.Err(); err != nil {
		return SyncReport{}, err
	}
	if !s.options.syncDeletes() {
		return SyncReport{Operation: SyncDelete, IgnoredReason: "delete_sync_disabled"}, nil
	}
	if event.ChatID == nil && !s.options.AllowAmbiguousDeletes {
		return SyncReport{Operation: SyncDelete, IgnoredReason: "source_chat_unknown"}, nil
	}
	links, err := s.linksForDelete(ctx, event)
	if err != nil {
		return SyncReport{}, err
	}
	synchronized := 0
	var failures []SyncFailure
	for _, link := range links {
		err := s.telegram.DeleteMessage(ctx, link.Destination)
		if err != nil {
			if isCancel(err) {
				return SyncReport{}, err
			}
			var missing MessageNotFound
			if !errors.As(err, &missing) {
				failures = append(failures, SyncFailure{Operation: SyncDelete, Link: link, Error: err})
				continue
			}
		}
		if removeErr := s.links.Remove(ctx, link); isCancel(removeErr) {
			return SyncReport{}, removeErr
		}
		synchronized++
	}
	return SyncReport{Operation: SyncDelete, Synchronized: synchronized, Failures: failures}, nil
}

func (s *ForwarderService) forwardOne(ctx context.Context, route Route, message IncomingMessage, matched bool, title string, hasTitle bool) (*DeliveryOutcome, *DeliveryFailure, error) {
	var outcome *DeliveryOutcome
	var failure *DeliveryFailure
	err := s.withRoute(route.ID, func() error {
		var sourceTitle *string
		if hasTitle && !route.Source.HasTopic {
			sourceTitle = &title
		}
		effective, err := s.resolveDestination(ctx, route, sourceTitle)
		if err != nil {
			if isCancel(err) {
				return err
			}
			failure = &DeliveryFailure{RouteID: route.ID, Sources: []MessageRef{message.Ref}, Error: err}
			return nil
		}
		if !matched {
			return nil
		}
		existing, ok, err := s.links.Get(ctx, route.ID, message.Ref)
		if err != nil {
			if isCancel(err) {
				return err
			}
			failure = &DeliveryFailure{RouteID: route.ID, Sources: []MessageRef{message.Ref}, Error: err}
			return nil
		}
		if ok {
			outcome = &DeliveryOutcome{
				RouteID:      route.ID,
				Sources:      []MessageRef{message.Ref},
				Destinations: []MessageRef{existing.Destination},
				ModeUsed:     existing.DeliveryMode,
				Deduplicated: true,
			}
			return nil
		}
		replyTo, err := s.resolveReply(ctx, message, route)
		if err != nil {
			if isCancel(err) {
				return err
			}
			failure = &DeliveryFailure{RouteID: route.ID, Sources: []MessageRef{message.Ref}, Error: err}
			return nil
		}
		destination, mode, err := s.deliverOne(ctx, message, effective, replyTo)
		if err != nil {
			if isCancel(err) {
				return err
			}
			failure = &DeliveryFailure{RouteID: route.ID, Sources: []MessageRef{message.Ref}, Error: err}
			return nil
		}
		link, err := NewMessageLink(route.ID, message.Ref, destination, mode)
		if err != nil {
			failure = &DeliveryFailure{RouteID: route.ID, Sources: []MessageRef{message.Ref}, Error: err}
			return nil
		}
		if err := s.links.SaveMany(ctx, []MessageLink{link}); err != nil {
			if isCancel(err) {
				return err
			}
			failure = &DeliveryFailure{RouteID: route.ID, Sources: []MessageRef{message.Ref}, Error: err}
			return nil
		}
		outcome = &DeliveryOutcome{
			RouteID:      route.ID,
			Sources:      []MessageRef{message.Ref},
			Destinations: []MessageRef{destination},
			ModeUsed:     mode,
		}
		return nil
	})
	return outcome, failure, err
}

func (s *ForwarderService) forwardAlbum(ctx context.Context, route Route, ordered []IncomingMessage, sources []MessageRef) (*DeliveryOutcome, *DeliveryFailure, error) {
	var outcome *DeliveryOutcome
	var failure *DeliveryFailure
	err := s.withRoute(route.ID, func() error {
		fail := func(cause error) error {
			if isCancel(cause) {
				return cause
			}
			failure = &DeliveryFailure{RouteID: route.ID, Sources: append([]MessageRef(nil), sources...), Error: cause}
			return nil
		}
		existing := make([]MessageLink, len(sources))
		present := make([]bool, len(sources))
		all := true
		any := false
		for i, source := range sources {
			link, ok, err := s.links.Get(ctx, route.ID, source)
			if err != nil {
				return fail(err)
			}
			if ok {
				existing[i] = link
				present[i] = true
				any = true
			} else {
				all = false
			}
		}
		if all {
			destinations := make([]MessageRef, len(existing))
			for i, link := range existing {
				destinations[i] = link.Destination
			}
			outcome = &DeliveryOutcome{
				RouteID:      route.ID,
				Sources:      append([]MessageRef(nil), sources...),
				Destinations: destinations,
				ModeUsed:     existing[0].DeliveryMode,
				Deduplicated: true,
			}
			return nil
		}
		if any {
			return fail(PartialDeliveryState{msg: fmt.Sprintf("route %d has an incomplete persisted album mapping", route.ID)})
		}
		_ = present
		replyTo, err := s.resolveReply(ctx, ordered[0], route)
		if err != nil {
			return fail(err)
		}
		effective, err := s.resolveDestination(ctx, route, nil)
		if err != nil {
			return fail(err)
		}
		destinations, mode, err := s.deliverAlbum(ctx, ordered, effective, replyTo)
		if err != nil {
			return fail(err)
		}
		if len(destinations) != len(ordered) {
			return fail(DeliveryResultMismatch{msg: fmt.Sprintf(
				"sent %d album items but received %d destination references",
				len(ordered),
				len(destinations),
			)})
		}
		links := make([]MessageLink, len(ordered))
		for i := range ordered {
			link, err := NewMessageLink(route.ID, sources[i], destinations[i], mode)
			if err != nil {
				return fail(err)
			}
			links[i] = link
		}
		if err := s.links.SaveMany(ctx, links); err != nil {
			return fail(err)
		}
		outcome = &DeliveryOutcome{
			RouteID:      route.ID,
			Sources:      append([]MessageRef(nil), sources...),
			Destinations: destinations,
			ModeUsed:     mode,
		}
		return nil
	})
	return outcome, failure, err
}

func (s *ForwarderService) deliverOne(ctx context.Context, message IncomingMessage, route Route, replyTo *int) (MessageRef, ForwardMode, error) {
	if message.Service != nil {
		text, err := FormatServiceMessage(message)
		if err != nil {
			return MessageRef{}, "", err
		}
		destination, err := s.telegram.SendText(ctx, text, route.Destination, replyTo)
		if err != nil {
			return MessageRef{}, "", err
		}
		return destination, ForwardModeCopy, nil
	}
	destination, err := s.telegram.DeliverMessage(ctx, message, route.Destination, route.Mode, replyTo)
	if err == nil {
		return destination, route.Mode, nil
	}
	var unsupported NativeForwardUnsupported
	if !errors.As(err, &unsupported) || route.Mode != ForwardModeForward || !route.FallbackToCopy {
		return MessageRef{}, "", err
	}
	destination, err = s.telegram.DeliverMessage(ctx, message, route.Destination, ForwardModeCopy, replyTo)
	if err != nil {
		return MessageRef{}, "", err
	}
	return destination, ForwardModeCopy, nil
}

func (s *ForwarderService) deliverAlbum(ctx context.Context, messages []IncomingMessage, route Route, replyTo *int) ([]MessageRef, ForwardMode, error) {
	destinations, err := s.telegram.DeliverAlbum(ctx, messages, route.Destination, route.Mode, replyTo)
	if err == nil {
		return destinations, route.Mode, nil
	}
	var unsupported NativeForwardUnsupported
	if !errors.As(err, &unsupported) || route.Mode != ForwardModeForward || !route.FallbackToCopy {
		return nil, "", err
	}
	destinations, err = s.telegram.DeliverAlbum(ctx, messages, route.Destination, ForwardModeCopy, replyTo)
	if err != nil {
		return nil, "", err
	}
	return destinations, ForwardModeCopy, nil
}

func (s *ForwarderService) resolveReply(ctx context.Context, message IncomingMessage, route Route) (*int, error) {
	if message.ReplyToMessageID == nil {
		return nil, nil
	}
	parent := MessageRef{ChatID: message.Ref.ChatID, MessageID: *message.ReplyToMessageID}
	mapping, ok, err := s.links.Get(ctx, route.ID, parent)
	if err != nil || !ok {
		return nil, err
	}
	id := mapping.Destination.MessageID
	return &id, nil
}

func (s *ForwarderService) resolveDestination(ctx context.Context, route Route, sourceTitle *string) (Route, error) {
	if s.topics == nil {
		return route, nil
	}
	destination, err := s.topics.Resolve(ctx, route, sourceTitle)
	if err != nil {
		return Route{}, err
	}
	if destination != route.Destination {
		route.Destination = destination
	}
	return route, nil
}

func (s *ForwarderService) withRoute(routeID int, fn func() error) error {
	s.mu.Lock()
	lock := s.locks[routeID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[routeID] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

type linkIdentity struct {
	route  int
	source MessageRef
}

func (s *ForwarderService) linksForDelete(ctx context.Context, event MessagesDeleted) ([]MessageLink, error) {
	seen := map[linkIdentity]struct{}{}
	var found []MessageLink
	for _, messageID := range event.MessageIDs {
		var matches []MessageLink
		var err error
		if event.ChatID == nil {
			matches, err = s.links.FindBySourceMessageID(ctx, messageID)
		} else {
			matches, err = s.links.FindAll(ctx, MessageRef{ChatID: *event.ChatID, MessageID: messageID})
		}
		if err != nil {
			return nil, err
		}
		for _, link := range matches {
			key := linkIdentity{route: link.RouteID, source: link.Source}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			found = append(found, link)
		}
	}
	return found, nil
}

func orderAlbum(messages []IncomingMessage) ([]IncomingMessage, error) {
	if len(messages) == 0 {
		return nil, valueErr("an album must contain at least one message")
	}
	ordered := append([]IncomingMessage(nil), messages...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Ref.MessageID < ordered[j].Ref.MessageID
	})
	first := ordered[0]
	if isNilValue(first.GroupedID) {
		return nil, valueErr("album messages must have a grouped_id")
	}
	for _, message := range ordered[1:] {
		if message.Ref.ChatID != first.Ref.ChatID || !sameTopicPtr(message.TopicID, first.TopicID) || !sameGrouped(message.GroupedID, first.GroupedID) {
			return nil, valueErr("album messages must belong to the same chat, topic and media group")
		}
	}
	return ordered, nil
}

func sameTopicPtr(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameGrouped(left, right any) bool {
	if isNilValue(left) || isNilValue(right) {
		return isNilValue(left) && isNilValue(right)
	}
	return fmt.Sprint(left) == fmt.Sprint(right) && reflectGroupKind(left) == reflectGroupKind(right)
}

func reflectGroupKind(value any) string {
	return fmt.Sprintf("%T", value)
}

// FormatServiceMessage renders a normalized service message as plain text.
func FormatServiceMessage(message IncomingMessage) (string, error) {
	service := message.Service
	if service == nil {
		return "", valueErr("message is not a service message")
	}
	suffix := ""
	if message.TopicID != nil && *message.TopicID != 0 {
		suffix = fmt.Sprintf(" in topic %d", *message.TopicID)
	}
	switch service.Kind {
	case contracts.ServiceMembersJoined:
		names := strings.Join(service.MemberNames, ", ")
		if names == "" {
			names = "A member"
		}
		return names + " joined the group" + suffix + ".", nil
	case contracts.ServiceMemberLeft:
		actor := service.ActorName
		if actor == "" {
			actor = "A member"
		}
		return actor + " left the group" + suffix + ".", nil
	case contracts.ServiceMessagePinned:
		actor := service.ActorName
		if actor == "" {
			actor = "an administrator"
		}
		return "A message was pinned by " + actor + suffix + ".", nil
	case contracts.ServiceTitleChanged:
		title := service.NewTitle
		if title == "" {
			title = "an unnamed title"
		}
		return "The group title was changed to " + title + ".", nil
	case contracts.ServiceTopicCreated:
		return "A topic was created" + suffix + ".", nil
	case contracts.ServiceTopicClosed:
		return "The topic was closed" + suffix + ".", nil
	case contracts.ServiceTopicReopened:
		return "The topic was reopened" + suffix + ".", nil
	default:
		return "A system event occurred" + suffix + ".", nil
	}
}

func changedTitle(message IncomingMessage) (string, bool) {
	if message.Service == nil || message.Service.Kind != contracts.ServiceTitleChanged || message.Service.NewTitle == "" {
		return "", false
	}
	return message.Service.NewTitle, true
}

func isCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
