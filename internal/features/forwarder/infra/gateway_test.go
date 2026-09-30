package infra

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

func TestInviteAndUsernameReferences(t *testing.T) {
	if got := inviteHash("https://t.me/+AbC-def"); got != "AbC-def" {
		t.Fatalf("plus invite = %q", got)
	}
	if got := inviteHash("https://telegram.me/joinchat/AbC"); got != "AbC" {
		t.Fatalf("joinchat = %q", got)
	}
	if got := inviteHash("tg://join?invite=AbC"); got != "AbC" {
		t.Fatalf("tg invite = %q", got)
	}
	if inviteHash("https://t.me/public") != "" || inviteHash("@name") != "" {
		t.Fatal("public references are not invites")
	}
	if got := publicUsername("@news"); got != "news" {
		t.Fatalf("username = %q", got)
	}
	if got := publicUsername("https://t.me/news"); got != "news" {
		t.Fatalf("public link = %q", got)
	}
	if publicUsername("https://t.me/+secret") != "" || publicUsername("123") != "" {
		t.Fatal("non-public references leaked a username")
	}
}

func TestTranslateDeliveryErrors(t *testing.T) {
	flood := translate(tgerr.New(420, "FLOOD_WAIT_3"), false)
	retry, ok := flood.(forwarder.RetryAfter)
	if !ok || retry.Delay != 3*time.Second {
		t.Fatalf("flood = %#v", flood)
	}
	if got := translate(tgerr.New(420, "FLOOD_WAIT_0"), false); got.(forwarder.RetryAfter).Delay != time.Second {
		t.Fatalf("zero flood = %#v", got)
	}
	if got := translate(tgerr.New(420, "SLOWMODE_WAIT_2"), false); got.(forwarder.RetryAfter).Delay != 2*time.Second {
		t.Fatalf("slowmode = %#v", got)
	}
	if _, ok := translate(tgerr.New(400, "MESSAGE_NOT_MODIFIED"), false).(forwarder.MessageNotModified); !ok {
		t.Fatal("expected message not modified")
	}
	if _, ok := translate(tgerr.New(400, "MSG_ID_INVALID"), false).(forwarder.MessageNotFound); !ok {
		t.Fatal("expected message not found")
	}
	native := translate(tgerr.New(400, "CHAT_FORWARDS_RESTRICTED"), true)
	if _, ok := native.(forwarder.NativeForwardUnsupported); !ok {
		t.Fatalf("native restriction = %#v", native)
	}
	permanent := translate(tgerr.New(400, "CHAT_FORWARDS_RESTRICTED"), false)
	if _, ok := permanent.(forwarder.PermanentDeliveryError); !ok {
		t.Fatalf("copy restriction = %#v", permanent)
	}
	if !errors.Is(translate(context.Canceled, false), context.Canceled) {
		t.Fatal("cancel should pass through")
	}
	deadline := translate(context.DeadlineExceeded, false)
	if got, ok := deadline.(forwarder.RetryAfter); !ok || got.Delay != time.Second {
		t.Fatalf("deadline = %#v", deadline)
	}
	network := translate(timeoutError{}, false)
	if got, ok := network.(forwarder.RetryAfter); !ok || got.Delay != time.Second {
		t.Fatalf("network = %#v", network)
	}
	value := forwarder.NewValueError("keep")
	if translate(value, false) != value {
		t.Fatal("value error should pass through")
	}
	if reply := inputReply(forwarder.DestinationEndpoint{HasTopic: true, TopicID: 1}, nil); reply == nil {
		t.Fatal("general topic should still reply")
	} else if message := reply.(*tg.InputReplyToMessage); message.ReplyToMsgID != 1 || message.TopMsgID != 0 {
		t.Fatalf("general reply = %+v", message)
	}
	ordinary := 9
	reply := inputReply(forwarder.DestinationEndpoint{HasTopic: true, TopicID: 4}, &ordinary).(*tg.InputReplyToMessage)
	if reply.ReplyToMsgID != 9 || reply.TopMsgID != 4 {
		t.Fatalf("topic reply = %+v", reply)
	}
	same := inputReply(forwarder.DestinationEndpoint{HasTopic: true, TopicID: 4}, ptr(4)).(*tg.InputReplyToMessage)
	if same.TopMsgID != 0 {
		t.Fatalf("matching topic should omit top id: %+v", same)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func ptr(value int) *int { return &value }
