package summarizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type senderPayload struct {
	ID   *int64 `json:"id"`
	Name string `json:"name"`
}

type messagePayload struct {
	MessageIDs       []int         `json:"message_ids"`
	Time             string        `json:"time"`
	Sender           senderPayload `json:"sender"`
	ReplyToMessageID *int          `json:"reply_to_message_id"`
	ForwardedFrom    *string       `json:"forwarded_from"`
	Text             string        `json:"text"`
	Links            []string      `json:"links"`
}

type actionPayload struct {
	Task     string  `json:"task"`
	Owner    *string `json:"owner"`
	Deadline *string `json:"deadline"`
}

type topicPayload struct {
	Title              string          `json:"title"`
	Summary            string          `json:"summary"`
	Participants       []string        `json:"participants"`
	EvidenceMessageIDs []int           `json:"evidence_message_ids"`
	Decisions          []string        `json:"decisions"`
	ActionItems        []actionPayload `json:"action_items"`
	OpenQuestions      []string        `json:"open_questions"`
}

type candidatePayload struct {
	Topics []topicPayload `json:"topics"`
}

func messagePayloadFrom(message SummaryMessage) messagePayload {
	ids := message.MessageIDs()
	if ids == nil {
		ids = []int{}
	}
	var forwarded *string
	if message.ForwardedFrom != "" {
		value := message.ForwardedFrom
		forwarded = &value
	}
	links := message.Links
	if links == nil {
		links = []string{}
	}
	return messagePayload{
		MessageIDs:       ids,
		Time:             pythonISO(message.OccurredAt),
		Sender:           senderPayload{ID: message.SenderID, Name: message.SenderName},
		ReplyToMessageID: message.ReplyToMessageID,
		ForwardedFrom:    forwarded,
		Text:             message.Text,
		Links:            links,
	}
}

func topicPayloadFrom(topic SummaryTopic) topicPayload {
	actions := make([]actionPayload, 0, len(topic.ActionItems))
	for _, item := range topic.ActionItems {
		actions = append(actions, actionPayload{Task: item.Task, Owner: item.Owner, Deadline: item.Deadline})
	}
	return topicPayload{
		Title:              topic.Title,
		Summary:            topic.Summary,
		Participants:       nonNilStrings(topic.Participants),
		EvidenceMessageIDs: nonNilInts(topic.EvidenceMessageIDs),
		Decisions:          nonNilStrings(topic.Decisions),
		ActionItems:        actions,
		OpenQuestions:      nonNilStrings(topic.OpenQuestions),
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilInts(values []int) []int {
	if values == nil {
		return []int{}
	}
	return values
}

func marshalCompact(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

type documentAction struct {
	Task     string  `json:"task"`
	Owner    *string `json:"owner"`
	Deadline *string `json:"deadline"`
}

type documentTopic struct {
	Title              string           `json:"title"`
	Summary            string           `json:"summary"`
	EvidenceMessageIDs []int            `json:"evidence_message_ids"`
	Participants       []string         `json:"participants"`
	Decisions          []string         `json:"decisions"`
	ActionItems        []documentAction `json:"action_items"`
	OpenQuestions      []string         `json:"open_questions"`
}

type documentBody struct {
	Topics []documentTopic `json:"topics"`
}

// DocumentJSON matches Python _document_json: compact separators and ensure_ascii false.
func DocumentJSON(document SummaryDocument) (string, error) {
	topics := make([]documentTopic, 0, len(document.Topics))
	for _, topic := range document.Topics {
		actions := make([]documentAction, 0, len(topic.ActionItems))
		for _, item := range topic.ActionItems {
			actions = append(actions, documentAction{Task: item.Task, Owner: item.Owner, Deadline: item.Deadline})
		}
		topics = append(topics, documentTopic{
			Title:              topic.Title,
			Summary:            topic.Summary,
			EvidenceMessageIDs: nonNilInts(topic.EvidenceMessageIDs),
			Participants:       nonNilStrings(topic.Participants),
			Decisions:          nonNilStrings(topic.Decisions),
			ActionItems:        actions,
			OpenQuestions:      nonNilStrings(topic.OpenQuestions),
		})
	}
	return marshalCompact(documentBody{Topics: topics})
}

func mustMarshalPayload(message SummaryMessage) json.RawMessage {
	raw, err := marshalCompact(messagePayloadFrom(message))
	if err != nil {
		panic(err)
	}
	return json.RawMessage(raw)
}

// pythonISO matches datetime.isoformat for an aware time.
// UTC is rendered as +00:00, and microseconds are omitted when zero.
func pythonISO(t time.Time) string {
	formatted := t.Format("2006-01-02T15:04:05")
	if t.Nanosecond() != 0 {
		formatted += fmt.Sprintf(".%06d", t.Nanosecond()/1000)
	}
	_, offset := t.Zone()
	sign := byte('+')
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	return fmt.Sprintf("%s%c%02d:%02d", formatted, sign, offset/3600, (offset%3600)/60)
}
