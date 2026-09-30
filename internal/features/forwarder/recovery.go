package forwarder

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
)

// MembershipState is the account's relationship to one routed chat.
type MembershipState string

const (
	MembershipJoined      MembershipState = "joined"
	MembershipMissing     MembershipState = "missing"
	MembershipUnavailable MembershipState = "unavailable"
	MembershipNotRequired MembershipState = "not_required"
)

// RebuildJoinResult is the outcome of one join attempt.
type RebuildJoinResult string

const (
	RebuildJoined          RebuildJoinResult = "joined"
	RebuildAlreadyJoined   RebuildJoinResult = "already_joined"
	RebuildApprovalPending RebuildJoinResult = "approval_pending"
)

// ChatAccess is the last known join metadata for a chat.
// Empty strings mean the field is unset.
type ChatAccess struct {
	ChatID     int64
	Title      string
	Username   string
	InviteLink string
}

// PublicLink is the t.me link for a username, or empty.
func (a ChatAccess) PublicLink() string {
	if a.Username == "" {
		return ""
	}
	return "https://t.me/" + a.Username
}

// JoinReference prefers a public username link over an invite link.
func (a ChatAccess) JoinReference() string {
	if link := a.PublicLink(); link != "" {
		return link
	}
	return a.InviteLink
}

func normalizeChatAccess(access ChatAccess) (ChatAccess, error) {
	if access.ChatID == 0 {
		return ChatAccess{}, valueErr("chat_id must not be zero")
	}
	access.Title = strings.TrimSpace(access.Title)
	access.InviteLink = strings.TrimSpace(access.InviteLink)
	username := strings.TrimSpace(access.Username)
	username = strings.TrimPrefix(username, "@")
	access.Username = strings.TrimSpace(username)
	return access, nil
}

// ChatInspection is one gateway observation of a chat.
type ChatInspection struct {
	Access        ChatAccess
	Joined        bool
	MetadataError string
}

// MembershipItem is one chat in a membership report.
type MembershipItem struct {
	Access        ChatAccess
	State         MembershipState
	RouteIDs      []int
	Roles         []string
	MetadataError string
}

// MembershipReport classifies every chat used by the selected routes.
type MembershipReport struct {
	Items   []MembershipItem
	Updated int
}

// Count returns how many items have state.
func (r MembershipReport) Count(state MembershipState) int {
	total := 0
	for _, item := range r.Items {
		if item.State == state {
			total++
		}
	}
	return total
}

// RebuildFailure records one chat that exhausted its join attempts.
type RebuildFailure struct {
	ChatID int64
	Error  string
}

// RebuildProgress is the in-memory rebuild queue snapshot.
type RebuildProgress struct {
	Active          bool
	Total           int
	Completed       int
	Joined          int
	AlreadyJoined   int
	ApprovalPending int
	Failed          int
	CurrentChatID   int64
	HasCurrent      bool
	NextAttemptAt   time.Time
	HasNextAttempt  bool
	Failures        []RebuildFailure
}

type aggregatedChat struct {
	chatID   int64
	routeIDs map[int]struct{}
	roles    map[string]struct{}
	required bool
	username string
}

// MembershipRecoveryService inspects routed chats and queues missing joins.
type MembershipRecoveryService struct {
	routes    RouteRepository
	accesses  ChatAccessRepository
	gateway   TelegramRecoveryGateway
	rebuilder *MembershipRebuilder
}

// NewMembershipRecoveryService returns the check-and-rebuild use case.
func NewMembershipRecoveryService(routes RouteRepository, accesses ChatAccessRepository, gateway TelegramRecoveryGateway, rebuilder *MembershipRebuilder) *MembershipRecoveryService {
	return &MembershipRecoveryService{routes: routes, accesses: accesses, gateway: gateway, rebuilder: rebuilder}
}

// Check refreshes joined metadata and classifies every routed chat.
// includeDisabled false keeps only enabled routes.
func (s *MembershipRecoveryService) Check(ctx context.Context, includeDisabled bool) (MembershipReport, error) {
	routes, err := s.routes.ListAll(ctx)
	if err != nil {
		return MembershipReport{}, err
	}
	if !includeDisabled {
		enabled := routes[:0]
		for _, route := range routes {
			if route.Enabled {
				enabled = append(enabled, route)
			}
		}
		routes = enabled
	}
	chats := aggregateChats(routes)
	if len(chats) == 0 {
		return MembershipReport{}, nil
	}
	ids := make([]int64, 0, len(chats))
	for id := range chats {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	storedRows, err := s.accesses.GetMany(ctx, ids)
	if err != nil {
		return MembershipReport{}, err
	}
	stored := map[int64]ChatAccess{}
	for _, item := range storedRows {
		stored[item.ChatID] = item
	}
	inspected, err := s.gateway.InspectChats(ctx, ids)
	if err != nil {
		return MembershipReport{}, err
	}
	inspections := map[int64]ChatInspection{}
	for _, item := range inspected {
		inspections[item.Access.ChatID] = item
	}
	updated := 0
	items := make([]MembershipItem, 0, len(ids))
	for _, chatID := range ids {
		aggregate := chats[chatID]
		previous, hasPrevious := stored[chatID]
		fallback := fallbackAccess(chatID, previous, hasPrevious, aggregate)
		inspection, ok := inspections[chatID]
		if !ok {
			inspection = ChatInspection{Access: fallback, Joined: false}
		}
		access := fallback
		if inspection.Joined {
			access = mergeAccess(fallback, inspection.Access)
			if !hasPrevious || access != previous {
				updated++
			}
			if err := s.accesses.Save(ctx, access); err != nil {
				return MembershipReport{}, err
			}
		}
		items = append(items, MembershipItem{
			Access:        access,
			State:         membershipState(aggregate.required, inspection.Joined, access),
			RouteIDs:      sortedRouteIDs(aggregate.routeIDs),
			Roles:         sortedRoles(aggregate.roles),
			MetadataError: inspection.MetadataError,
		})
	}
	return MembershipReport{Items: items, Updated: updated}, nil
}

// Rebuild checks membership and starts a serial join of missing chats.
func (s *MembershipRecoveryService) Rebuild(ctx context.Context, includeDisabled bool) (MembershipReport, error) {
	report, err := s.Check(ctx, includeDisabled)
	if err != nil {
		return MembershipReport{}, err
	}
	var targets []ChatAccess
	for _, item := range report.Items {
		if item.State == MembershipMissing {
			targets = append(targets, item.Access)
		}
	}
	if err := s.rebuilder.Start(targets); err != nil {
		return MembershipReport{}, err
	}
	return report, nil
}

// Progress returns the current rebuild snapshot.
func (s *MembershipRecoveryService) Progress() RebuildProgress {
	return s.rebuilder.Progress()
}

// Cancel stops the active rebuild. It reports whether one was active.
func (s *MembershipRecoveryService) Cancel() bool {
	return s.rebuilder.Cancel()
}

// MembershipRebuilderConfig tunes serial joins. Zero intervals use 300s and 600s.
type MembershipRebuilderConfig struct {
	MinInterval    time.Duration
	MaxInterval    time.Duration
	MaxAttempts    int
	Clock          func() time.Time
	RandomInterval func(min, max time.Duration) time.Duration
	Logger         *slog.Logger
}

type rebuildItem struct {
	access   ChatAccess
	attempts int
}

// MembershipRebuilder joins missing chats one at a time.
type MembershipRebuilder struct {
	gateway     TelegramRecoveryGateway
	minInterval time.Duration
	maxInterval time.Duration
	maxAttempts int
	clock       func() time.Time
	random      func(min, max time.Duration) time.Duration
	log         *slog.Logger

	mu         sync.Mutex
	queue      []rebuildItem
	progress   RebuildProgress
	stopping   bool
	generation int
	wake       chan struct{}
}

// NewMembershipRebuilder rejects intervals below five minutes.
func NewMembershipRebuilder(gateway TelegramRecoveryGateway, cfg MembershipRebuilderConfig) (*MembershipRebuilder, error) {
	if gateway == nil {
		return nil, valueErr("recovery gateway is required")
	}
	if cfg.MinInterval < 0 || cfg.MaxInterval < 0 || cfg.MaxAttempts < 0 {
		return nil, valueErr("rebuild interval must be at least 300 seconds")
	}
	if cfg.MinInterval == 0 {
		cfg.MinInterval = 300 * time.Second
	}
	if cfg.MaxInterval == 0 {
		cfg.MaxInterval = 600 * time.Second
	}
	if cfg.MinInterval < 300*time.Second {
		return nil, valueErr("rebuild interval must be at least 300 seconds")
	}
	if cfg.MaxInterval < cfg.MinInterval {
		return nil, valueErr("rebuild intervals must be ordered")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.RandomInterval == nil {
		cfg.RandomInterval = defaultRandomInterval
	}
	return &MembershipRebuilder{
		gateway:     gateway,
		minInterval: cfg.MinInterval,
		maxInterval: cfg.MaxInterval,
		maxAttempts: cfg.MaxAttempts,
		clock:       cfg.Clock,
		random:      cfg.RandomInterval,
		log:         loggerOrDiscard(cfg.Logger),
		wake:        make(chan struct{}, 1),
	}, nil
}

// Progress returns a copy of the in-memory snapshot.
func (r *MembershipRebuilder) Progress() RebuildProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyProgress(r.progress)
}

// Prepare clears an in-memory queue before the feature starts the loop.
func (r *MembershipRebuilder) Prepare() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopping = false
	r.queue = nil
	r.progress = RebuildProgress{}
	r.generation++
	select {
	case <-r.wake:
	default:
	}
}

// Start replaces the queue with the unique targets sorted by chat ID.
func (r *MembershipRebuilder) Start(targets []ChatAccess) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.progress.Active {
		return valueErr("已有频道重建任务正在运行")
	}
	unique := map[int64]ChatAccess{}
	ids := make([]int64, 0, len(targets))
	for _, target := range targets {
		if _, ok := unique[target.ChatID]; ok {
			continue
		}
		unique[target.ChatID] = target
		ids = append(ids, target.ChatID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	r.queue = make([]rebuildItem, 0, len(ids))
	for _, id := range ids {
		r.queue = append(r.queue, rebuildItem{access: unique[id]})
	}
	r.generation++
	r.progress = RebuildProgress{Active: len(r.queue) > 0, Total: len(r.queue)}
	r.signalLocked()
	return nil
}

// Cancel drops the active queue. It reports whether a rebuild was active.
func (r *MembershipRebuilder) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.progress.Active {
		return false
	}
	r.queue = nil
	r.generation++
	r.progress.Active = false
	r.progress.HasCurrent = false
	r.progress.CurrentChatID = 0
	r.signalLocked()
	return true
}

// RequestStop cancels the queue and unblocks Run.
func (r *MembershipRebuilder) RequestStop() {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.Cancel()
	r.mu.Lock()
	r.signalLocked()
	r.mu.Unlock()
}

// Run joins queued chats until RequestStop.
func (r *MembershipRebuilder) Run(ctx context.Context) error {
	for {
		if r.isStopping() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := r.ProcessOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		if r.isStopping() {
			return nil
		}
		r.mu.Lock()
		select {
		case <-r.wake:
		default:
		}
		wait, hasWait := dueIn(r.progress.NextAttemptAt, r.progress.HasNextAttempt, r.clock())
		wake := r.wake
		r.mu.Unlock()
		if r.isStopping() {
			return nil
		}
		if !hasWait {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
			}
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// ProcessOnce joins the next chat when it is due.
func (r *MembershipRebuilder) ProcessOnce(ctx context.Context) (bool, error) {
	return r.process(ctx, r.clock(), false)
}

// ProcessOnceAt joins the next chat using now instead of the clock.
func (r *MembershipRebuilder) ProcessOnceAt(ctx context.Context, now time.Time) (bool, error) {
	return r.process(ctx, now, true)
}

func (r *MembershipRebuilder) process(ctx context.Context, now time.Time, explicitNow bool) (bool, error) {
	r.mu.Lock()
	if !r.progress.Active || len(r.queue) == 0 {
		r.mu.Unlock()
		return false, nil
	}
	if r.progress.HasNextAttempt && r.progress.NextAttemptAt.After(now) {
		r.mu.Unlock()
		return false, nil
	}
	item := r.queue[0]
	r.queue = r.queue[1:]
	generation := r.generation
	r.progress.HasCurrent = true
	r.progress.CurrentChatID = item.access.ChatID
	r.mu.Unlock()

	result, retryAfter, errorText, err := r.join(ctx, item.access)
	if err != nil {
		return false, err
	}
	item.attempts++

	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != r.generation {
		return true, nil
	}
	completed := r.progress.Completed
	joined := r.progress.Joined
	already := r.progress.AlreadyJoined
	approval := r.progress.ApprovalPending
	failed := r.progress.Failed
	failures := append([]RebuildFailure(nil), r.progress.Failures...)
	switch {
	case result == RebuildJoined:
		completed++
		joined++
	case result == RebuildAlreadyJoined:
		completed++
		already++
	case result == RebuildApprovalPending:
		completed++
		approval++
	case retryAfter != nil && item.attempts < r.maxAttempts:
		r.queue = append([]rebuildItem{item}, r.queue...)
	default:
		completed++
		failed++
		if errorText == "" {
			errorText = "unknown error"
		}
		failures = append(failures, RebuildFailure{ChatID: item.access.ChatID, Error: errorText})
	}
	delay := r.random(r.minInterval, r.maxInterval)
	if delay < r.minInterval || delay > r.maxInterval {
		return false, valueErr("random rebuild interval is outside configured bounds")
	}
	if retryAfter != nil && *retryAfter > delay {
		delay = *retryAfter
	}
	finished := now
	if !explicitNow {
		finished = r.clock()
	}
	active := len(r.queue) > 0
	progress := RebuildProgress{
		Active:          active,
		Total:           r.progress.Total,
		Completed:       completed,
		Joined:          joined,
		AlreadyJoined:   already,
		ApprovalPending: approval,
		Failed:          failed,
		Failures:        failures,
	}
	if active {
		progress.HasNextAttempt = true
		progress.NextAttemptAt = finished.Add(delay)
	}
	r.progress = progress
	resultLabel := "retry_or_failed"
	if result != "" {
		resultLabel = string(result)
	}
	var next any
	if active {
		next = delay.Seconds()
	}
	r.log.Info("chat rebuild attempt completed",
		"feature", "forwarder",
		"chat_id", item.access.ChatID,
		"attempt", item.attempts,
		"result", resultLabel,
		"next_attempt_in", next,
	)
	return true, nil
}

func (r *MembershipRebuilder) join(ctx context.Context, access ChatAccess) (RebuildJoinResult, *time.Duration, string, error) {
	result, err := r.gateway.JoinChat(ctx, access)
	if err == nil {
		return result, nil, "", nil
	}
	if isCancel(err) {
		return "", nil, "", err
	}
	var retry RetryAfter
	if errors.As(err, &retry) {
		delay := retry.Delay
		return "", &delay, retry.Error(), nil
	}
	text := typeName(err) + ": " + err.Error()
	runes := []rune(text)
	if len(runes) > 500 {
		text = string(runes[:500])
	}
	return "", nil, text, nil
}

func (r *MembershipRebuilder) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

func (r *MembershipRebuilder) signalLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func copyProgress(progress RebuildProgress) RebuildProgress {
	progress.Failures = append([]RebuildFailure(nil), progress.Failures...)
	return progress
}

func aggregateChats(routes []Route) map[int64]*aggregatedChat {
	chats := map[int64]*aggregatedChat{}
	ensure := func(chatID int64) *aggregatedChat {
		chat := chats[chatID]
		if chat == nil {
			chat = &aggregatedChat{
				chatID:   chatID,
				routeIDs: map[int]struct{}{},
				roles:    map[string]struct{}{},
			}
			chats[chatID] = chat
		}
		return chat
	}
	for _, route := range routes {
		source := ensure(route.Source.ChatID)
		source.routeIDs[route.ID] = struct{}{}
		role := "source"
		if route.Source.IsPolled() {
			role = "poll_source"
		}
		source.roles[role] = struct{}{}
		source.required = source.required || !route.Source.IsPolled()
		if source.username == "" {
			source.username = route.Source.Username
		}
		destination := ensure(route.Destination.ChatID)
		destination.routeIDs[route.ID] = struct{}{}
		destination.roles["destination"] = struct{}{}
		destination.required = true
		if destination.username == "" {
			destination.username = route.Destination.Username
		}
	}
	return chats
}

func fallbackAccess(chatID int64, previous ChatAccess, hasPrevious bool, aggregate *aggregatedChat) ChatAccess {
	username := aggregate.username
	title := ""
	invite := ""
	if hasPrevious {
		title = previous.Title
		if previous.Username != "" {
			username = previous.Username
		}
		invite = previous.InviteLink
	}
	access, err := normalizeChatAccess(ChatAccess{ChatID: chatID, Title: title, Username: username, InviteLink: invite})
	if err != nil {
		return ChatAccess{ChatID: chatID, Title: title, Username: username, InviteLink: invite}
	}
	return access
}

func mergeAccess(stored, observed ChatAccess) ChatAccess {
	title := observed.Title
	if title == "" {
		title = stored.Title
	}
	invite := observed.InviteLink
	if invite == "" && isPrivateInvite(stored.InviteLink) {
		invite = stored.InviteLink
	}
	access, err := normalizeChatAccess(ChatAccess{
		ChatID:     stored.ChatID,
		Title:      title,
		Username:   observed.Username,
		InviteLink: invite,
	})
	if err != nil {
		return ChatAccess{ChatID: stored.ChatID, Title: title, Username: observed.Username, InviteLink: invite}
	}
	return access
}

func membershipState(required, joined bool, access ChatAccess) MembershipState {
	if !required || access.ChatID > 0 {
		return MembershipNotRequired
	}
	if joined {
		return MembershipJoined
	}
	if access.JoinReference() != "" {
		return MembershipMissing
	}
	return MembershipUnavailable
}

func sortedRouteIDs(ids map[int]struct{}) []int {
	out := make([]int, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func sortedRoles(roles map[string]struct{}) []string {
	out := make([]string, 0, len(roles))
	for role := range roles {
		out = append(out, role)
	}
	sort.Strings(out)
	return out
}

func dueIn(due time.Time, has bool, now time.Time) (time.Duration, bool) {
	if !has {
		return 0, false
	}
	delay := due.Sub(now)
	if delay < 0 {
		return 0, true
	}
	return delay, true
}

func defaultRandomInterval(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	span := int64(max - min)
	n, err := rand.Int(rand.Reader, big.NewInt(span+1))
	if err != nil {
		return min
	}
	return min + time.Duration(n.Int64())
}
