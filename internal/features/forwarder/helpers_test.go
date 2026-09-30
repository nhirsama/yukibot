package forwarder

import (
	"context"
	"sync"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

type memSub struct {
	bus  *memBus
	kind int
	id   int
	once sync.Once
}

func (s *memSub) Unsubscribe() {
	s.once.Do(func() { s.bus.remove(s.kind, s.id) })
}

type memBus struct {
	mu        sync.Mutex
	next      int
	received  map[int]func(context.Context, contracts.TelegramMessageReceived) error
	edited    map[int]func(context.Context, contracts.TelegramMessageEdited) error
	deleted   map[int]func(context.Context, contracts.TelegramMessagesDeleted) error
	published []any
}

func newMemBus() *memBus {
	return &memBus{
		received: map[int]func(context.Context, contracts.TelegramMessageReceived) error{},
		edited:   map[int]func(context.Context, contracts.TelegramMessageEdited) error{},
		deleted:  map[int]func(context.Context, contracts.TelegramMessagesDeleted) error{},
	}
}

func (b *memBus) add(kind int) (*memSub, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	return &memSub{bus: b, kind: kind, id: b.next}, b.next
}

func (b *memBus) remove(kind, id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case 1:
		delete(b.received, id)
	case 2:
		delete(b.edited, id)
	case 3:
		delete(b.deleted, id)
	}
}

func (b *memBus) SubscribeReceived(handler func(context.Context, contracts.TelegramMessageReceived) error) (EventSubscription, error) {
	sub, id := b.add(1)
	b.mu.Lock()
	b.received[id] = handler
	b.mu.Unlock()
	return sub, nil
}

func (b *memBus) SubscribeEdited(handler func(context.Context, contracts.TelegramMessageEdited) error) (EventSubscription, error) {
	sub, id := b.add(2)
	b.mu.Lock()
	b.edited[id] = handler
	b.mu.Unlock()
	return sub, nil
}

func (b *memBus) SubscribeDeleted(handler func(context.Context, contracts.TelegramMessagesDeleted) error) (EventSubscription, error) {
	sub, id := b.add(3)
	b.mu.Lock()
	b.deleted[id] = handler
	b.mu.Unlock()
	return sub, nil
}

func (b *memBus) Publish(ctx context.Context, event any) error {
	b.mu.Lock()
	b.published = append(b.published, event)
	var received []func(context.Context, contracts.TelegramMessageReceived) error
	var edited []func(context.Context, contracts.TelegramMessageEdited) error
	var deleted []func(context.Context, contracts.TelegramMessagesDeleted) error
	switch event.(type) {
	case contracts.TelegramMessageReceived:
		for _, handler := range b.received {
			received = append(received, handler)
		}
	case contracts.TelegramMessageEdited:
		for _, handler := range b.edited {
			edited = append(edited, handler)
		}
	case contracts.TelegramMessagesDeleted:
		for _, handler := range b.deleted {
			deleted = append(deleted, handler)
		}
	}
	b.mu.Unlock()
	switch ev := event.(type) {
	case contracts.TelegramMessageReceived:
		for _, handler := range received {
			if err := handler(ctx, ev); err != nil {
				return err
			}
		}
	case contracts.TelegramMessageEdited:
		for _, handler := range edited {
			if err := handler(ctx, ev); err != nil {
				return err
			}
		}
	case contracts.TelegramMessagesDeleted:
		for _, handler := range deleted {
			if err := handler(ctx, ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *memBus) handlerCount(kind int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case 1:
		return len(b.received)
	case 2:
		return len(b.edited)
	case 3:
		return len(b.deleted)
	default:
		return 0
	}
}

type memTasks struct {
	mu      sync.Mutex
	active  int
	started []startedTask
}

type startedTask struct {
	name     string
	critical bool
}

func (s *memTasks) Go(name string, critical bool, fn func(context.Context) error) (RunningTask, error) {
	ctx, cancel := context.WithCancel(context.Background())
	task := &memTask{cancelFn: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.active++
	s.started = append(s.started, startedTask{name: name, critical: critical})
	s.mu.Unlock()
	go func() {
		err := fn(ctx)
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		task.err = err
		close(task.done)
	}()
	return task, nil
}

func (s *memTasks) activeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

type memTask struct {
	cancelFn context.CancelFunc
	done     chan struct{}
	err      error
}

func (t *memTask) Cancel() { t.cancelFn() }

func (t *memTask) Wait(ctx context.Context) error {
	select {
	case <-t.done:
		return t.err
	case <-ctx.Done():
		select {
		case <-t.done:
			return t.err
		default:
			return ctx.Err()
		}
	}
}

type deliveryCall struct {
	messages    []IncomingMessage
	destination DestinationEndpoint
	mode        ForwardMode
	reply       *int
}

type sentText struct {
	text        string
	destination DestinationEndpoint
	reply       *int
}

type fakeGateway struct {
	mu           sync.Mutex
	calls        []deliveryCall
	sent         []sentText
	edits        []MessageRef
	deletes      []MessageRef
	nextID       int
	rejectNative bool
	titles       map[int64]string
	forums       map[int64]bool
	created      []createdTopic
	edited       []editedTopic
	nextTopic    int
}

type createdTopic struct {
	chatID   int64
	title    string
	randomID int64
}

type editedTopic struct {
	chatID  int64
	topicID int
	title   string
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{
		nextID:    1000,
		nextTopic: 500,
		titles:    map[int64]string{},
		forums:    map[int64]bool{},
	}
}

func (g *fakeGateway) ChatTitle(chatID int64) (string, bool) {
	title, ok := g.titles[chatID]
	return title, ok
}

func (g *fakeGateway) IsForum(chatID int64) bool { return g.forums[chatID] }

func (g *fakeGateway) CreateForumTopic(ctx context.Context, destinationChatID int64, title string, randomID int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.created = append(g.created, createdTopic{chatID: destinationChatID, title: title, randomID: randomID})
	id := g.nextTopic
	g.nextTopic++
	return id, nil
}

func (g *fakeGateway) EditForumTopic(ctx context.Context, destinationChatID int64, topicID int, title string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.edited = append(g.edited, editedTopic{chatID: destinationChatID, topicID: topicID, title: title})
	g.mu.Unlock()
	return nil
}

func (g *fakeGateway) DeliverMessage(ctx context.Context, message IncomingMessage, destination DestinationEndpoint, mode ForwardMode, replyToMessageID *int) (MessageRef, error) {
	if err := ctx.Err(); err != nil {
		return MessageRef{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, deliveryCall{messages: []IncomingMessage{message}, destination: destination, mode: mode, reply: replyToMessageID})
	if mode == ForwardModeForward && g.rejectNative {
		return MessageRef{}, NativeForwardUnsupported{}
	}
	return g.nextRef(destination.ChatID), nil
}

func (g *fakeGateway) DeliverAlbum(ctx context.Context, messages []IncomingMessage, destination DestinationEndpoint, mode ForwardMode, replyToMessageID *int) ([]MessageRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	copied := append([]IncomingMessage(nil), messages...)
	g.calls = append(g.calls, deliveryCall{messages: copied, destination: destination, mode: mode, reply: replyToMessageID})
	if mode == ForwardModeForward && g.rejectNative {
		return nil, NativeForwardUnsupported{}
	}
	refs := make([]MessageRef, len(messages))
	for i := range messages {
		refs[i] = g.nextRef(destination.ChatID)
	}
	return refs, nil
}

func (g *fakeGateway) SendText(ctx context.Context, text string, destination DestinationEndpoint, replyToMessageID *int) (MessageRef, error) {
	if err := ctx.Err(); err != nil {
		return MessageRef{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, sentText{text: text, destination: destination, reply: replyToMessageID})
	return g.nextRef(destination.ChatID), nil
}

func (g *fakeGateway) EditFromSource(ctx context.Context, source IncomingMessage, target MessageRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.edits = append(g.edits, target)
	g.mu.Unlock()
	return nil
}

func (g *fakeGateway) DeleteMessage(ctx context.Context, target MessageRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.deletes = append(g.deletes, target)
	g.mu.Unlock()
	return nil
}

func (g *fakeGateway) nextRef(chatID int64) MessageRef {
	ref := MessageRef{ChatID: chatID, MessageID: g.nextID}
	g.nextID++
	return ref
}

type preparedSource struct {
	source SourceEndpoint
	join   bool
}

type fakeSources struct {
	identities  map[string]ChatIdentity
	prepared    []preparedSource
	resolutions []string
	latest      int
	topicTitles map[int]string
	titles      map[int64]string
	failEnsure  error
	fetches     [][2]int
	messages    []IncomingMessage
}

func newFakeSources() *fakeSources {
	source, _ := NewChatIdentity(-1001, "source", "")
	target, _ := NewChatIdentity(-2001, "target", "")
	return &fakeSources{
		identities: map[string]ChatIdentity{
			"@source": source,
			"@target": target,
		},
		latest:      42,
		topicTitles: map[int]string{},
		titles: map[int64]string{
			-1001: "Source channel",
			-2001: "Target group",
		},
	}
}

func (f *fakeSources) ChatTitle(chatID int64) (string, bool) {
	title, ok := f.titles[chatID]
	if ok {
		return title, true
	}
	return "", false
}

func (f *fakeSources) SourceTitle(ctx context.Context, source SourceEndpoint) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	title, ok := f.ChatTitle(source.ChatID)
	if !ok {
		return "", false, nil
	}
	if !source.HasTopic {
		return title, true, nil
	}
	topicTitle, ok := f.topicTitles[source.TopicID]
	if !ok {
		return title, true, nil
	}
	return title + "/" + topicTitle, true, nil
}

func (f *fakeSources) ResolveChat(ctx context.Context, reference string) (ChatIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ChatIdentity{}, err
	}
	f.resolutions = append(f.resolutions, reference)
	identity, ok := f.identities[reference]
	if !ok {
		return ChatIdentity{}, valueErr("unknown chat reference")
	}
	return identity, nil
}

func (f *fakeSources) EnsureSource(ctx context.Context, source SourceEndpoint, join bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.failEnsure != nil {
		return f.failEnsure
	}
	f.prepared = append(f.prepared, preparedSource{source: source, join: join})
	return nil
}

func (f *fakeSources) LatestMessageID(ctx context.Context, source SourceEndpoint) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.latest, nil
}

func (f *fakeSources) FetchMessagesAfter(ctx context.Context, source SourceEndpoint, afterMessageID int, limit int) ([]IncomingMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.fetches = append(f.fetches, [2]int{afterMessageID, limit})
	var out []IncomingMessage
	for _, message := range f.messages {
		if message.Ref.MessageID > afterMessageID {
			out = append(out, message)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

type recoveryGateway struct {
	inspections map[int64]ChatInspection
	inspected   [][]int64
	joined      []int64
}

func newRecoveryGateway(items []ChatInspection) *recoveryGateway {
	stored := map[int64]ChatInspection{}
	for _, item := range items {
		stored[item.Access.ChatID] = item
	}
	return &recoveryGateway{inspections: stored}
}

func (g *recoveryGateway) InspectChats(ctx context.Context, chatIDs []int64) ([]ChatInspection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copied := append([]int64(nil), chatIDs...)
	g.inspected = append(g.inspected, copied)
	out := make([]ChatInspection, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		item, ok := g.inspections[chatID]
		if !ok {
			item = ChatInspection{Access: ChatAccess{ChatID: chatID}}
		}
		out = append(out, item)
	}
	return out, nil
}

func (g *recoveryGateway) JoinChat(ctx context.Context, access ChatAccess) (RebuildJoinResult, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	g.joined = append(g.joined, access.ChatID)
	return RebuildJoined, nil
}

func mustRoute(id int, source SourceEndpoint, destination DestinationEndpoint) Route {
	route, err := NewRoute(id, source, destination)
	if err != nil {
		panic(err)
	}
	return route
}

func mustSource(chatID int64, cfg SourceConfig) SourceEndpoint {
	endpoint, err := NewSourceEndpoint(chatID, cfg)
	if err != nil {
		panic(err)
	}
	return endpoint
}

func mustDestination(chatID int64, cfg DestinationConfig) DestinationEndpoint {
	endpoint, err := NewDestinationEndpoint(chatID, cfg)
	if err != nil {
		panic(err)
	}
	return endpoint
}

func mustRoutes(routes ...Route) *InMemoryRouteRepository {
	repo, err := NewInMemoryRouteRepository(routes)
	if err != nil {
		panic(err)
	}
	return repo
}

func textMessage(id int, text string) IncomingMessage {
	return IncomingMessage{
		Ref:         MessageRef{ChatID: -1001, MessageID: id},
		ContentType: contracts.ContentText,
		OccurredAt:  time.Unix(100, 0).UTC(),
		Text:        text,
	}
}
