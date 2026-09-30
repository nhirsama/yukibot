package forwarder

import (
	"strings"
	"testing"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestGeneralTopicRepresentationsMatch(t *testing.T) {
	source := mustSource(-1001, SourceConfig{TopicID: intPtr(1)})
	if !source.Matches(-1001, nil) || !source.Matches(-1001, intPtr(0)) || !source.Matches(-1001, intPtr(1)) {
		t.Fatal("general topic representations must match")
	}
}

func TestSourceWithoutTopicMatchesEveryTopic(t *testing.T) {
	source := mustSource(-1001, SourceConfig{})
	if !source.Matches(-1001, nil) || !source.Matches(-1001, intPtr(99)) || source.Matches(-1002, intPtr(99)) {
		t.Fatal("a source without a topic must match every topic in its chat")
	}
}

func TestFilterIsCaseInsensitiveAndBlacklistWins(t *testing.T) {
	rule := NewMessageFilter(
		[]string{"telegram"},
		[]ContentType{contracts.ContentText, contracts.ContentSticker},
		[]ContentType{contracts.ContentSticker},
		false,
	)
	allowed := textMessage(10, "Hello Telegram")
	if !rule.Allows(allowed) {
		t.Fatal("keyword match must be case insensitive")
	}
	blocked := textMessage(10, "")
	blocked.ContentType = contracts.ContentSticker
	blocked.Text = ""
	if rule.Allows(blocked) {
		t.Fatal("blocked content must win over the allow list")
	}
}

func TestServiceMessagesAreOptIn(t *testing.T) {
	event := textMessage(10, "")
	event.ContentType = contracts.ContentService
	event.Text = ""
	event.Service = &contracts.ServiceMessage{Kind: contracts.ServiceMembersJoined, MemberNames: []string{"Ling"}}
	if NewMessageFilter(nil, nil, nil, false).Allows(event) {
		t.Fatal("service messages are excluded by default")
	}
	if !NewMessageFilter(nil, nil, nil, true).Allows(event) {
		t.Fatal("service messages must be allowed when opted in")
	}
}

func TestServiceTypeRequiresServiceDetails(t *testing.T) {
	event := textMessage(10, "")
	event.ContentType = contracts.ContentService
	event.Text = ""
	if _, err := NewIncomingMessage(event); err == nil || !strings.Contains(err.Error(), "service details") {
		t.Fatalf("got %v", err)
	}
}

func TestRouteGraphAllowsChainsButRejectsCycles(t *testing.T) {
	first := mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-1002, DestinationConfig{}))
	second := mustRoute(2, mustSource(-1002, SourceConfig{}), mustDestination(-1003, DestinationConfig{}))
	if err := AssertAcyclicRoutes([]Route{first, second}); err != nil {
		t.Fatal(err)
	}
	cycle := mustRoute(3, mustSource(-1003, SourceConfig{}), mustDestination(-1001, DestinationConfig{}))
	err := AssertAcyclicRoutes([]Route{first, second, cycle})
	if err == nil || err.Error() != "forwarding route cycle detected: -1001 -> -1002 -> -1003 -> -1001" {
		t.Fatalf("got %v", err)
	}
}
