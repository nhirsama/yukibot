package forwarder

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"
)

// InMemoryRouteRepository stores routes and rejects forwarding cycles.
type InMemoryRouteRepository struct {
	mu     sync.Mutex
	routes map[int]Route
	nextID int
}

// NewInMemoryRouteRepository copies the initial routes and checks the graph.
func NewInMemoryRouteRepository(routes []Route) (*InMemoryRouteRepository, error) {
	stored := make(map[int]Route, len(routes))
	for _, route := range routes {
		stored[route.ID] = route
	}
	if err := AssertAcyclicRoutes(routeValues(stored)); err != nil {
		return nil, err
	}
	next := 1
	for id := range stored {
		if id+1 > next {
			next = id + 1
		}
	}
	return &InMemoryRouteRepository{routes: stored, nextID: next}, nil
}

func (r *InMemoryRouteRepository) ListForSourceChat(ctx context.Context, chatID int64) ([]Route, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	matched := make([]Route, 0)
	for _, route := range r.sorted() {
		if route.Source.ChatID == chatID {
			matched = append(matched, route)
		}
	}
	return matched, nil
}

func (r *InMemoryRouteRepository) ListAll(ctx context.Context) ([]Route, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sorted(), nil
}

func (r *InMemoryRouteRepository) Add(ctx context.Context, route Route) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.routes[route.ID]; ok {
		return valueErr("route " + strconv.Itoa(route.ID) + " already exists")
	}
	if err := AssertAcyclicRoutes(append(r.sorted(), route)); err != nil {
		return err
	}
	r.routes[route.ID] = route
	if route.ID+1 > r.nextID {
		r.nextID = route.ID + 1
	}
	return nil
}

func (r *InMemoryRouteRepository) AddAuto(ctx context.Context, draft RouteDraft) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	route, err := draft.Bind(r.nextID)
	if err != nil {
		return Route{}, err
	}
	if err := AssertAcyclicRoutes(append(r.sorted(), route)); err != nil {
		return Route{}, err
	}
	r.routes[route.ID] = route
	r.nextID++
	return route, nil
}

func (r *InMemoryRouteRepository) Replace(ctx context.Context, route Route) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.routes[route.ID]; !ok {
		return KeyError{ID: route.ID}
	}
	proposed := make([]Route, 0, len(r.routes))
	for _, existing := range r.sorted() {
		if existing.ID == route.ID {
			proposed = append(proposed, route)
			continue
		}
		proposed = append(proposed, existing)
	}
	if err := AssertAcyclicRoutes(proposed); err != nil {
		return err
	}
	r.routes[route.ID] = route
	return nil
}

func (r *InMemoryRouteRepository) Remove(ctx context.Context, routeID int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.routes[routeID]; !ok {
		return false, nil
	}
	delete(r.routes, routeID)
	return true, nil
}

func (r *InMemoryRouteRepository) sorted() []Route {
	ids := make([]int, 0, len(r.routes))
	for id := range r.routes {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]Route, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.routes[id])
	}
	return out
}

// InMemoryMessageLinkRepository stores message mappings in insertion order.
type InMemoryMessageLinkRepository struct {
	mu    sync.Mutex
	links []MessageLink
}

// NewInMemoryMessageLinkRepository copies the initial links. Later duplicates replace in place.
func NewInMemoryMessageLinkRepository(links []MessageLink) *InMemoryMessageLinkRepository {
	stored := make([]MessageLink, len(links))
	copy(stored, links)
	return &InMemoryMessageLinkRepository{links: stored}
}

func (r *InMemoryMessageLinkRepository) SaveMany(ctx context.Context, links []MessageLink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, link := range links {
		replaced := false
		for i := range r.links {
			if r.links[i].RouteID == link.RouteID && r.links[i].Source == link.Source {
				r.links[i] = link
				replaced = true
				break
			}
		}
		if !replaced {
			r.links = append(r.links, link)
		}
	}
	return nil
}

func (r *InMemoryMessageLinkRepository) Get(ctx context.Context, routeID int, source MessageRef) (MessageLink, bool, error) {
	if err := ctx.Err(); err != nil {
		return MessageLink{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, link := range r.links {
		if link.RouteID == routeID && link.Source == source {
			return link, true, nil
		}
	}
	return MessageLink{}, false, nil
}

func (r *InMemoryMessageLinkRepository) FindAll(ctx context.Context, source MessageRef) ([]MessageLink, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []MessageLink
	for _, link := range r.links {
		if link.Source == source {
			found = append(found, link)
		}
	}
	return found, nil
}

func (r *InMemoryMessageLinkRepository) FindBySourceMessageID(ctx context.Context, messageID int) ([]MessageLink, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []MessageLink
	for _, link := range r.links {
		if link.Source.MessageID == messageID {
			found = append(found, link)
		}
	}
	return found, nil
}

func (r *InMemoryMessageLinkRepository) Remove(ctx context.Context, link MessageLink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make([]MessageLink, 0, len(r.links))
	for _, existing := range r.links {
		if existing.RouteID == link.RouteID && existing.Source == link.Source {
			continue
		}
		next = append(next, existing)
	}
	r.links = next
	return nil
}

// Count is the number of stored links.
func (r *InMemoryMessageLinkRepository) Count(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.links), nil
}

// InMemoryManagedTopicRepository stores automatic forum topics.
type InMemoryManagedTopicRepository struct {
	mu     sync.Mutex
	topics map[topicKey]ManagedTopic
}

type topicKey struct {
	sourceChat int64
	destChat   int64
	topic      int
	hasTopic   bool
}

// NewInMemoryManagedTopicRepository copies the initial topics.
func NewInMemoryManagedTopicRepository(topics []ManagedTopic) *InMemoryManagedTopicRepository {
	stored := make(map[topicKey]ManagedTopic, len(topics))
	for _, topic := range topics {
		stored[topicKeyOf(topic)] = topic
	}
	return &InMemoryManagedTopicRepository{topics: stored}
}

func (r *InMemoryManagedTopicRepository) Get(ctx context.Context, sourceChatID int64, sourceTopicID *int, destinationChatID int64) (ManagedTopic, bool, error) {
	if err := ctx.Err(); err != nil {
		return ManagedTopic{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := topicKey{sourceChat: sourceChatID, destChat: destinationChatID}
	if sourceTopicID != nil {
		key.hasTopic = true
		key.topic = NormalizeGeneralTopic(sourceTopicID)
	}
	topic, ok := r.topics[key]
	return topic, ok, nil
}

func (r *InMemoryManagedTopicRepository) Save(ctx context.Context, topic ManagedTopic) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topics[topicKeyOf(topic)] = topic
	return nil
}

func topicKeyOf(topic ManagedTopic) topicKey {
	key := topicKey{sourceChat: topic.SourceChatID, destChat: topic.DestinationChatID, hasTopic: topic.HasSourceTopic}
	if topic.HasSourceTopic {
		key.topic = topic.SourceTopicID
	}
	return key
}

// InMemoryPollCursorRepository stores cursors and only moves them forward.
type InMemoryPollCursorRepository struct {
	mu      sync.Mutex
	cursors map[int64]PollCursor
}

// NewInMemoryPollCursorRepository copies the initial cursors.
func NewInMemoryPollCursorRepository(cursors []PollCursor) *InMemoryPollCursorRepository {
	stored := make(map[int64]PollCursor, len(cursors))
	for _, cursor := range cursors {
		stored[cursor.SourceChatID] = cursor
	}
	return &InMemoryPollCursorRepository{cursors: stored}
}

func (r *InMemoryPollCursorRepository) Get(ctx context.Context, sourceChatID int64) (PollCursor, bool, error) {
	if err := ctx.Err(); err != nil {
		return PollCursor{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cursor, ok := r.cursors[sourceChatID]
	return cursor, ok, nil
}

func (r *InMemoryPollCursorRepository) Save(ctx context.Context, cursor PollCursor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.cursors[cursor.SourceChatID]
	if !ok || cursor.LastMessageID > existing.LastMessageID {
		r.cursors[cursor.SourceChatID] = cursor
	}
	return nil
}

// InMemoryChatAccessRepository stores chat join metadata.
type InMemoryChatAccessRepository struct {
	mu    sync.Mutex
	items map[int64]ChatAccess
}

// NewInMemoryChatAccessRepository copies the initial records.
func NewInMemoryChatAccessRepository(items []ChatAccess) *InMemoryChatAccessRepository {
	stored := make(map[int64]ChatAccess, len(items))
	for _, item := range items {
		stored[item.ChatID] = item
	}
	return &InMemoryChatAccessRepository{items: stored}
}

func (r *InMemoryChatAccessRepository) GetMany(ctx context.Context, chatIDs []int64) ([]ChatAccess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := uniqueSorted(chatIDs)
	out := make([]ChatAccess, 0, len(ids))
	for _, id := range ids {
		if item, ok := r.items[id]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r *InMemoryChatAccessRepository) Save(ctx context.Context, access ChatAccess) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeChatAccess(access)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.items[normalized.ChatID]; ok {
		title := normalized.Title
		if title == "" {
			title = existing.Title
		}
		normalized = ChatAccess{
			ChatID:     normalized.ChatID,
			Title:      title,
			Username:   normalized.Username,
			InviteLink: normalized.InviteLink,
		}
	}
	r.items[normalized.ChatID] = normalized
	return nil
}

type jobState string

const (
	jobPending    jobState = "pending"
	jobProcessing jobState = "processing"
	jobFailed     jobState = "failed"
)

type jobRecord struct {
	id        int
	kind      ForwardJobKind
	dedup     string
	group     *string
	event     any
	attempts  int
	available time.Time
	state     jobState
	lastError string
}

// InMemoryForwardJobRepository matches the SQLite head-of-line queue.
type InMemoryForwardJobRepository struct {
	mu     sync.Mutex
	jobs   []*jobRecord
	byKey  map[string]*jobRecord
	nextID int
}

// NewInMemoryForwardJobRepository returns an empty queue.
func NewInMemoryForwardJobRepository() *InMemoryForwardJobRepository {
	return &InMemoryForwardJobRepository{byKey: map[string]*jobRecord{}}
}

// Enqueue inserts new keys. A duplicate key still slides its pending group's available_at.
func (r *InMemoryForwardJobRepository) Enqueue(ctx context.Context, jobs []PendingForwardJob) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	inserted := 0
	for _, job := range jobs {
		if _, exists := r.byKey[job.DeduplicationKey]; !exists {
			r.nextID++
			group := cloneGroup(job.GroupKey)
			record := &jobRecord{
				id:        r.nextID,
				kind:      job.Kind,
				dedup:     job.DeduplicationKey,
				group:     group,
				event:     job.Event,
				available: job.AvailableAt,
				state:     jobPending,
			}
			r.byKey[job.DeduplicationKey] = record
			r.jobs = append(r.jobs, record)
			inserted++
		}
		if job.GroupKey != nil {
			for _, record := range r.jobs {
				if record.state == jobPending && record.group != nil && *record.group == *job.GroupKey {
					record.available = job.AvailableAt
				}
			}
		}
	}
	return inserted, nil
}

// RecoverIncomplete returns processing jobs to pending without resetting attempts.
func (r *InMemoryForwardJobRepository) RecoverIncomplete(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	recovered := 0
	for _, record := range r.jobs {
		if record.state == jobProcessing {
			record.state = jobPending
			recovered++
		}
	}
	return recovered, nil
}

// ClaimDue claims the lowest pending id when it is due.
// A grouped head returns every due sibling. A future head blocks the queue.
func (r *InMemoryForwardJobRepository) ClaimDue(ctx context.Context, now time.Time) ([]ForwardJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var head *jobRecord
	for _, record := range r.jobs {
		if record.state != jobPending {
			continue
		}
		if head == nil || record.id < head.id {
			head = record
		}
	}
	if head == nil || head.available.After(now) {
		return nil, nil
	}
	selected := make([]*jobRecord, 0, 1)
	if head.group == nil {
		selected = append(selected, head)
	} else {
		for _, record := range r.jobs {
			if record.state != jobPending || record.group == nil || *record.group != *head.group {
				continue
			}
			if record.available.After(now) {
				continue
			}
			selected = append(selected, record)
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].id < selected[j].id })
	}
	claimed := make([]ForwardJob, 0, len(selected))
	for _, record := range selected {
		record.state = jobProcessing
		record.attempts++
		job, err := NewForwardJob(record.id, record.kind, record.event, record.attempts, cloneGroup(record.group))
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, job)
	}
	return claimed, nil
}

// MarkSucceeded deletes processing jobs.
func (r *InMemoryForwardJobRepository) MarkSucceeded(ctx context.Context, jobIDs []int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(jobIDs) == 0 {
		return nil
	}
	return r.update(jobIDs, func(record *jobRecord) {
		record.state = "deleted"
	}, true)
}

// MarkFailed marks processing jobs failed.
func (r *InMemoryForwardJobRepository) MarkFailed(ctx context.Context, jobIDs []int, failure string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(jobIDs) == 0 {
		return nil
	}
	return r.update(jobIDs, func(record *jobRecord) {
		record.state = jobFailed
		record.lastError = failure
	}, false)
}

// Reschedule returns processing jobs to pending at availableAt.
func (r *InMemoryForwardJobRepository) Reschedule(ctx context.Context, jobIDs []int, availableAt time.Time, failure string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(jobIDs) == 0 {
		return nil
	}
	return r.update(jobIDs, func(record *jobRecord) {
		record.state = jobPending
		record.available = availableAt
		record.lastError = failure
	}, false)
}

func (r *InMemoryForwardJobRepository) update(jobIDs []int, apply func(*jobRecord), deleteRow bool) error {
	wanted := map[int]struct{}{}
	for _, id := range jobIDs {
		wanted[id] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make([]*jobRecord, 0, len(r.jobs))
	for _, record := range r.jobs {
		_, match := wanted[record.id]
		if match && record.state == jobProcessing {
			if deleteRow {
				delete(r.byKey, record.dedup)
				continue
			}
			apply(record)
		}
		next = append(next, record)
	}
	r.jobs = next
	return nil
}

func cloneGroup(group *string) *string {
	if group == nil {
		return nil
	}
	value := *group
	return &value
}

func routeValues(routes map[int]Route) []Route {
	out := make([]Route, 0, len(routes))
	for _, route := range routes {
		out = append(out, route)
	}
	return out
}

func uniqueSorted(ids []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
