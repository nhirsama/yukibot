package summarizer

import (
	"strings"
	"testing"
)

func TestDocumentJSONMatchesPythonShape(t *testing.T) {
	owner := "Ada"
	raw, err := DocumentJSON(SummaryDocument{Topics: []SummaryTopic{{
		Title:              "A & B",
		Summary:            "ok",
		EvidenceMessageIDs: nil,
		Participants:       nil,
		Decisions:          []string{},
		ActionItems: []SummaryActionItem{
			{Task: "ship"},
			{Task: "review", Owner: &owner},
		},
		OpenQuestions: []string{},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, ": ") || strings.Contains(raw, ", ") || strings.Contains(raw, "\n") || strings.Contains(raw, `\u0026`) {
		t.Fatalf("expected compact unescaped JSON, got %s", raw)
	}
	for _, want := range []string{
		`"title":"A & B"`,
		`"evidence_message_ids":[]`,
		`"participants":[]`,
		`"owner":null`,
		`"deadline":null`,
		`"owner":"Ada"`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
}
