package forwarder

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRouteHelpAndEndpointReferences(t *testing.T) {
	if strings.HasPrefix(RouteHelp, "/") {
		t.Fatal("help must not look like an outgoing command")
	}
	cases := []struct {
		value string
		chat  string
		topic int
		has   bool
	}{
		{value: "-1003953295839/546", chat: "-1003953295839", topic: 546, has: true},
		{value: "@source/546", chat: "@source", topic: 546, has: true},
		{value: "https://t.me/c/3953295839/546", chat: "-1003953295839", topic: 546, has: true},
		{value: "https://t.me/public_group/546", chat: "@public_group", topic: 546, has: true},
		{value: "@source", chat: "@source"},
	}
	for _, tc := range cases {
		got, err := endpointReference(tc.value)
		if err != nil || got.chat != tc.chat || got.hasTopic != tc.has || (tc.has && got.topic != tc.topic) {
			t.Fatalf("%s got %#v err %v", tc.value, got, err)
		}
	}
	if _, err := endpointReference("https://t.me/c/not-a-number/1"); err == nil || err.Error() != "Telegram 私有群话题链接格式不正确" {
		t.Fatalf("private topic link: %v", err)
	}
}

func TestSplitShellMatchesPythonQuoting(t *testing.T) {
	got, err := splitShell(`add @source/7 @target/9 forward`)
	if err != nil || strings.Join(got, "|") != "add|@source/7|@target/9|forward" {
		t.Fatalf("%#v %v", got, err)
	}
	quoted, err := splitShell(`add "a b" c`)
	if err != nil || strings.Join(quoted, "|") != "add|a b|c" {
		t.Fatalf("%#v %v", quoted, err)
	}
	if _, err := splitShell(`add "foo`); err == nil || err.Error() != "No closing quotation" {
		t.Fatalf("quote error %v", err)
	}
}

func TestRouteCommandParsesConfigurationAndErrors(t *testing.T) {
	ctx := context.Background()
	routes := mustRoutes()
	service, err := NewForwarderManagementService(routes, ForwarderManagementConfig{})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewForwarderCommands(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := commands.Handle(ctx, ControlCommand{RawArguments: "add -1001 -2001"})
	if err != nil || result.Text != "Route 1 is configured." {
		t.Fatalf("%q %v", result.Text, err)
	}
	stored, err := routes.ListAll(ctx)
	if err != nil || len(stored) != 1 || stored[0].Mode != ForwardModeForward || !stored[0].FallbackToCopy {
		t.Fatalf("stored %#v %v", stored, err)
	}
	shown, err := commands.Handle(ctx, ControlCommand{RawArguments: "show 1"})
	if err != nil || !strings.Contains(shown.Text, "keywords: none") || !strings.Contains(shown.Text, "fallback to copy: true") {
		t.Fatalf("show %q %v", shown.Text, err)
	}
	enabled, err := commands.Handle(ctx, ControlCommand{RawArguments: "disable 1"})
	if err != nil || enabled.Text != "Route 1 is disabled." {
		t.Fatalf("%q %v", enabled.Text, err)
	}
	enabled, err = commands.Handle(ctx, ControlCommand{RawArguments: "enable 1"})
	if err != nil || enabled.Text != "Route 1 is enabled." {
		t.Fatalf("%q %v", enabled.Text, err)
	}
	removed, err := commands.Handle(ctx, ControlCommand{RawArguments: "remove 1"})
	if err != nil || removed.Text != "Route 1 is removed." {
		t.Fatalf("%q %v", removed.Text, err)
	}
	empty, err := commands.Handle(ctx, ControlCommand{RawArguments: "list"})
	if err != nil || empty.Text != "No forwarding routes." {
		t.Fatalf("%q %v", empty.Text, err)
	}

	cases := []struct {
		args string
		want string
	}{
		{args: "", want: RouteHelp},
		{args: "help", want: RouteHelp},
		{args: `add "foo`, want: "Invalid arguments: No closing quotation"},
		{args: "show nope", want: "invalid literal for int() with base 10: 'nope'"},
		{args: "add -1001 -2001 nope", want: "'nope' is not a valid ForwardMode"},
		{args: "add -1001 -2001 --nope", want: "未知选项: --nope"},
		{args: "add -1001 -2001 --poll 5x", want: "轮询间隔格式应为 5m、2h 或 1d"},
		{args: "add -1001 -2001 --poll", want: "--poll 必须且只能指定一次间隔"},
		{args: "add a b c d", want: "路由参数数量不正确"},
		{args: "add @source @target", want: "chat reference must be a numeric ID"},
		{args: "add https://t.me/+source_hash -2001 --poll 5m", want: "轮询源不能使用私有邀请链接, 请改用实时模式"},
	}
	for _, tc := range cases {
		result, err := commands.Handle(ctx, ControlCommand{RawArguments: tc.args})
		if err != nil || result.Text != tc.want {
			t.Fatalf("%q got %q err %v", tc.args, result.Text, err)
		}
	}
}

func TestUsernameAndPollCommands(t *testing.T) {
	ctx := context.Background()
	routes := mustRoutes()
	sources := newFakeSources()
	cursors := NewInMemoryPollCursorRepository(nil)
	accesses := NewInMemoryChatAccessRepository(nil)
	service, err := NewForwarderManagementService(routes, ForwarderManagementConfig{
		Sources:  sources,
		Cursors:  cursors,
		Accesses: accesses,
	})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewForwarderCommands(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := commands.Handle(ctx, ControlCommand{RawArguments: "add @source/7 @target/9 forward"})
	if err != nil || result.Text != "Route 1 is configured." {
		t.Fatalf("%q %v", result.Text, err)
	}
	stored, err := routes.ListAll(ctx)
	if err != nil || len(stored) != 1 {
		t.Fatal(err)
	}
	route := stored[0]
	if route.Source.ChatID != -1001 || !route.Source.HasTopic || route.Source.TopicID != 7 || route.Source.Username != "source" {
		t.Fatalf("source %#v", route.Source)
	}
	if route.Destination.ChatID != -2001 || !route.Destination.HasTopic || route.Destination.TopicID != 9 || route.Destination.Username != "target" {
		t.Fatalf("destination %#v", route.Destination)
	}
	if len(sources.resolutions) != 2 || sources.resolutions[0] != "@source" || sources.resolutions[1] != "@target" {
		t.Fatalf("resolutions %#v", sources.resolutions)
	}
	if len(sources.prepared) != 1 || !sources.prepared[0].join {
		t.Fatalf("prepared %#v", sources.prepared)
	}
	listed, err := commands.Handle(ctx, ControlCommand{RawArguments: "list"})
	if err != nil || listed.Text != "1: Source channel (@source)/7 -> Target group (@target)/9 (forward, enabled)" {
		t.Fatalf("%q %v", listed.Text, err)
	}

	routes = mustRoutes()
	sources = newFakeSources()
	cursors = NewInMemoryPollCursorRepository(nil)
	service, err = NewForwarderManagementService(routes, ForwarderManagementConfig{Sources: sources, Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	commands, err = NewForwarderCommands(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Handle(ctx, ControlCommand{RawArguments: "add @source @target forward --poll 5m"}); err != nil {
		t.Fatal(err)
	}
	stored, err = routes.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !stored[0].Source.IsPolled() || stored[0].Source.PollEvery != 300*time.Second {
		t.Fatalf("poll %#v", stored[0].Source)
	}
	if len(sources.prepared) != 1 || sources.prepared[0].join {
		t.Fatalf("poll prepare %#v", sources.prepared)
	}
	cursor, ok, err := cursors.Get(ctx, -1001)
	if err != nil || !ok || cursor.LastMessageID != 42 {
		t.Fatalf("cursor %#v ok %v err %v", cursor, ok, err)
	}
	listed, err = commands.Handle(ctx, ControlCommand{RawArguments: "list"})
	if err != nil || listed.Text != "1: Source channel (@source) -> Target group (@target) (forward, enabled, poll=5m)" {
		t.Fatalf("%q %v", listed.Text, err)
	}

	sources.failEnsure = NewPermanentDeliveryError("无法加入源频道")
	failed, err := commands.Handle(ctx, ControlCommand{RawArguments: "add @source @target"})
	if err != nil || failed.Text != "无法加入源频道" {
		t.Fatalf("%q %v", failed.Text, err)
	}
}

func TestInviteLinksAndGeneratedRoutes(t *testing.T) {
	ctx := context.Background()
	routes := mustRoutes()
	sources := newFakeSources()
	sourceLink := "https://t.me/+source_hash"
	targetLink := "https://t.me/joinchat/target_hash"
	sourceIdentity, err := NewChatIdentityOptional(-1001, "", sourceLink)
	if err != nil {
		t.Fatal(err)
	}
	targetIdentity, err := NewChatIdentityOptional(-2001, "", targetLink)
	if err != nil {
		t.Fatal(err)
	}
	sources.identities[sourceLink] = sourceIdentity
	sources.identities[targetLink] = targetIdentity
	accesses := NewInMemoryChatAccessRepository(nil)
	service, err := NewForwarderManagementService(routes, ForwarderManagementConfig{Sources: sources, Accesses: accesses})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewForwarderCommands(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := commands.Handle(ctx, ControlCommand{RawArguments: "add " + sourceLink + " " + targetLink})
	if err != nil || result.Text != "Route 1 is configured." {
		t.Fatalf("%q %v", result.Text, err)
	}
	stored, err := accesses.GetMany(ctx, []int64{-1001, -2001})
	if err != nil || len(stored) != 2 {
		t.Fatalf("%#v %v", stored, err)
	}
	if stored[0].ChatID != -2001 || stored[0].Title != "Target group" || stored[0].InviteLink != targetLink {
		t.Fatalf("target %#v", stored[0])
	}
	if stored[1].ChatID != -1001 || stored[1].Title != "Source channel" || stored[1].InviteLink != sourceLink {
		t.Fatalf("source %#v", stored[1])
	}

	seed := mustRoute(7, mustSource(-7001, SourceConfig{}), mustDestination(-7002, DestinationConfig{}))
	routes = mustRoutes(seed)
	service, err = NewForwarderManagementService(routes, ForwarderManagementConfig{})
	if err != nil {
		t.Fatal(err)
	}
	draft := NewRouteDraft(mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{}))
	first, err := service.AddGeneratedRoute(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.AddGeneratedRoute(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 8 || !repeated.Equal(first) {
		t.Fatalf("first %d repeated %d", first.ID, repeated.ID)
	}
	if _, err := service.AddRoute(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddRoute(ctx, seed); err != nil {
		t.Fatal(err)
	}
	different := mustRoute(7, mustSource(-1001, SourceConfig{}), mustDestination(-3001, DestinationConfig{}))
	if _, err := service.AddRoute(ctx, different); err == nil || !strings.Contains(err.Error(), "different configuration") {
		t.Fatalf("got %v", err)
	}
	disabled, err := service.SetEnabled(ctx, 8, false)
	if err != nil || disabled.Enabled {
		t.Fatal(err)
	}
	again, err := service.SetEnabled(ctx, 8, false)
	if err != nil || !again.Equal(disabled) {
		t.Fatal(err)
	}
	if err := service.RemoveRoute(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveRoute(ctx, 8); err != nil {
		t.Fatal(err)
	}
}

func TestSetKeepsIDAndUpdatesInviteLinks(t *testing.T) {
	ctx := context.Background()
	routes := mustRoutes()
	sources := newFakeSources()
	service, err := NewForwarderManagementService(routes, ForwarderManagementConfig{Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewForwarderCommands(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Handle(ctx, ControlCommand{RawArguments: "add @source @target"}); err != nil {
		t.Fatal(err)
	}
	updated, err := commands.Handle(ctx, ControlCommand{RawArguments: "set 1 @source @target copy"})
	if err != nil || updated.Text != "Route 1 is updated." {
		t.Fatalf("%q %v", updated.Text, err)
	}
	stored, err := routes.ListAll(ctx)
	if err != nil || len(stored) != 1 || stored[0].ID != 1 || stored[0].Mode != ForwardModeCopy {
		t.Fatalf("%#v %v", stored, err)
	}

	sourceLink := "https://t.me/+source_hash"
	targetLink := "https://t.me/+target_hash"
	sourceIdentity, err := NewChatIdentityOptional(-1001, "", sourceLink)
	if err != nil {
		t.Fatal(err)
	}
	targetIdentity, err := NewChatIdentityOptional(-2001, "", targetLink)
	if err != nil {
		t.Fatal(err)
	}
	sources.identities[sourceLink] = sourceIdentity
	sources.identities[targetLink] = targetIdentity
	accesses := NewInMemoryChatAccessRepository(nil)
	routes = mustRoutes()
	service, err = NewForwarderManagementService(routes, ForwarderManagementConfig{Sources: sources, Accesses: accesses})
	if err != nil {
		t.Fatal(err)
	}
	commands, err = NewForwarderCommands(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Handle(ctx, ControlCommand{RawArguments: "add @source @target"}); err != nil {
		t.Fatal(err)
	}
	updated, err = commands.Handle(ctx, ControlCommand{RawArguments: "set 1 " + sourceLink + " " + targetLink})
	if err != nil || updated.Text != "Route 1 is updated." {
		t.Fatalf("%q %v", updated.Text, err)
	}
	saved, err := accesses.GetMany(ctx, []int64{-1001, -2001})
	if err != nil || len(saved) != 2 {
		t.Fatalf("%#v %v", saved, err)
	}
	if saved[0].ChatID != -2001 || saved[0].Title != "Target group" || saved[0].InviteLink != targetLink || saved[0].Username != "" {
		t.Fatalf("target %#v", saved[0])
	}
	if saved[1].ChatID != -1001 || saved[1].Title != "Source channel" || saved[1].InviteLink != sourceLink || saved[1].Username != "" {
		t.Fatalf("source %#v", saved[1])
	}
}

func TestAddingRoutePreparesForumTopic(t *testing.T) {
	ctx := context.Background()
	routes := mustRoutes()
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	gateway.titles[-1001] = "Source channel"
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	service, err := NewForwarderManagementService(routes, ForwarderManagementConfig{Topics: topics})
	if err != nil {
		t.Fatal(err)
	}
	route := mustRoute(7, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{}))
	if _, err := service.AddRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	if len(gateway.created) != 1 || gateway.created[0].chatID != -2001 || gateway.created[0].title != "Source channel" {
		t.Fatalf("created %#v", gateway.created)
	}

	sources := newFakeSources()
	sources.topicTitles[7] = "Announcements"
	gateway = newFakeGateway()
	gateway.forums[-2001] = true
	topics = NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	routes = mustRoutes()
	service, err = NewForwarderManagementService(routes, ForwarderManagementConfig{Topics: topics, Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	topicRoute := mustRoute(7, mustSource(-1001, SourceConfig{TopicID: intPtr(7)}), mustDestination(-2001, DestinationConfig{}))
	if _, err := service.AddRoute(ctx, topicRoute); err != nil {
		t.Fatal(err)
	}
	if len(gateway.created) != 1 || gateway.created[0].title != "Source channel/Announcements" {
		t.Fatalf("created %#v", gateway.created)
	}
}
