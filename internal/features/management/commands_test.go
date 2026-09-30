package management

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nhirsama/yukibot/internal/kernel"
)

func TestAdminHelpIsNotAnOutgoingCommand(t *testing.T) {
	if strings.HasPrefix(AdminHelp, "/") {
		t.Fatalf("help starts with slash: %q", AdminHelp)
	}
	if !strings.Contains(AdminHelp, "/admin module disable <name> - 停用模块") {
		t.Fatalf("help %q", AdminHelp)
	}
	if strings.HasSuffix(AdminHelp, "\n") {
		t.Fatal("help has a trailing newline")
	}
}

func TestAdminCommands(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository(Owner{ID: 999})
	modules := &fakeModules{items: []ModuleStatus{{Name: "forwarder", Enabled: true, Running: true}}}
	commands := NewCommands(NewService(repository, modules, Owner{ID: 999}))
	outgoing := kernel.ControlCommand{Name: "/admin", Outgoing: true, ChatID: -1001, MessageID: 10}

	help, err := commands.Handle(ctx, command(outgoing, ""))
	if err != nil || text(t, help) != AdminHelp {
		t.Fatalf("empty help %q %v", text(t, help), err)
	}
	help, err = commands.Handle(ctx, command(outgoing, "help"))
	if err != nil || text(t, help) != AdminHelp {
		t.Fatalf("explicit help %q %v", text(t, help), err)
	}
	help, err = commands.Handle(ctx, command(outgoing, "nope"))
	if err != nil || text(t, help) != AdminHelp {
		t.Fatalf("unknown %q %v", text(t, help), err)
	}

	added, err := commands.Handle(ctx, command(outgoing, "admin add 123"))
	if err != nil || text(t, added) != "Administrator 123 is enabled." {
		t.Fatalf("add %q %v", text(t, added), err)
	}
	ownerAdd, err := commands.Handle(ctx, command(outgoing, "admin add 999"))
	if err != nil || text(t, ownerAdd) != "Administrator 999 is enabled." {
		t.Fatalf("add owner %q %v", text(t, ownerAdd), err)
	}
	stored, err := repository.IsAdmin(ctx, 999)
	if err != nil || stored {
		t.Fatalf("owner stored %v %v", stored, err)
	}
	listed, err := commands.Handle(ctx, command(outgoing, "admin list"))
	if err != nil || text(t, listed) != "owner: 999\nadmin: 123" {
		t.Fatalf("list %q %v", text(t, listed), err)
	}

	actor := int64(123)
	incoming := kernel.ControlCommand{Name: "/admin", ActorID: &actor, ChatID: -1001, MessageID: 11}
	removed, err := commands.Handle(ctx, command(incoming, "admin remove 123"))
	if err != nil || text(t, removed) != "Administrator 123 is removed." {
		t.Fatalf("remove %q %v", text(t, removed), err)
	}
	denied, err := commands.Handle(ctx, command(incoming, "admin add 5"))
	if err != nil || text(t, denied) != "administrator permission is required" {
		t.Fatalf("denied %q %v", text(t, denied), err)
	}
	ownerRemove, err := commands.Handle(ctx, command(outgoing, "admin remove 999"))
	if err != nil || text(t, ownerRemove) != "the current account owner cannot be removed" {
		t.Fatalf("owner remove %q %v", text(t, ownerRemove), err)
	}
	negative, err := commands.Handle(ctx, command(outgoing, "admin remove -1"))
	if err != nil || text(t, negative) != "administrator user ID must be positive" {
		t.Fatalf("negative %q %v", text(t, negative), err)
	}
	parsed, err := commands.Handle(ctx, command(outgoing, "admin add 1_000"))
	if err != nil || text(t, parsed) != "Administrator 1000 is enabled." {
		t.Fatalf("underscore %q %v", text(t, parsed), err)
	}
	invalid, err := commands.Handle(ctx, command(outgoing, "admin add abc"))
	if err != nil || text(t, invalid) != "invalid literal for int() with base 10: 'abc'" {
		t.Fatalf("invalid %q %v", text(t, invalid), err)
	}
	broken, err := commands.Handle(ctx, command(outgoing, `admin add "123`))
	if err != nil || text(t, broken) != "Invalid arguments: No closing quotation" {
		t.Fatalf("quote %q %v", text(t, broken), err)
	}

	empty, err := commands.Handle(ctx, command(outgoing, "module list"))
	if err != nil || text(t, empty) != "forwarder: enabled=true, running=true" {
		t.Fatalf("modules %q %v", text(t, empty), err)
	}
	disabled, err := commands.Handle(ctx, command(outgoing, "module disable forwarder"))
	if err != nil || text(t, disabled) != "Module forwarder is disabled." {
		t.Fatalf("disable %q %v", text(t, disabled), err)
	}
	enabled, err := commands.Handle(ctx, command(outgoing, `module enable "forwarder"`))
	if err != nil || text(t, enabled) != "Module forwarder is enabled and running." {
		t.Fatalf("enable %q %v", text(t, enabled), err)
	}
	missing, err := commands.Handle(ctx, command(outgoing, "module enable missing"))
	if err != nil || text(t, missing) != `module "missing" does not exist` {
		t.Fatalf("missing %q %v", text(t, missing), err)
	}
}

func TestModuleListEmptyAndUnexpectedError(t *testing.T) {
	ctx := context.Background()
	commands := NewCommands(NewService(NewMemoryRepository(Owner{ID: 1}), &fakeModules{}, Owner{ID: 1}))
	outgoing := kernel.ControlCommand{Outgoing: true}
	listed, err := commands.Handle(ctx, command(outgoing, "module list"))
	if err != nil || text(t, listed) != "No manageable modules." {
		t.Fatalf("empty %q %v", text(t, listed), err)
	}
	commands = NewCommands(NewService(NewMemoryRepository(Owner{ID: 1}), &fakeModules{err: errors.New("start failed")}, Owner{ID: 1}))
	_, err = commands.Handle(ctx, command(outgoing, "module enable forwarder"))
	if err == nil || err.Error() != "start failed" {
		t.Fatalf("unexpected %v", err)
	}
}

func TestSplitMatchesPythonShlex(t *testing.T) {
	cases := []struct {
		input string
		want  []string
		err   string
	}{
		{input: "", want: nil},
		{input: "   ", want: nil},
		{input: "admin add 123", want: []string{"admin", "add", "123"}},
		{input: " admin list", want: []string{"admin", "list"}},
		{input: "help", want: []string{"help"}},
		{input: `admin add "123"`, want: []string{"admin", "add", "123"}},
		{input: "admin add '123'", want: []string{"admin", "add", "123"}},
		{input: `module enable "foo bar"`, want: []string{"module", "enable", "foo bar"}},
		{input: `module enable foo\ bar`, want: []string{"module", "enable", "foo bar"}},
		{input: `a "" b`, want: []string{"a", "", "b"}},
		{input: "a '' b", want: []string{"a", "", "b"}},
		{input: `"ab"c`, want: []string{"abc"}},
		{input: `"a\"b"`, want: []string{`a"b`}},
		{input: `"a\b"`, want: []string{`a\b`}},
		{input: `foo\bar`, want: []string{"foobar"}},
		{input: "#notcomment", want: []string{"#notcomment"}},
		{input: "admin\nlist", want: []string{"admin", "list"}},
		{input: "  \t admin   add   42  ", want: []string{"admin", "add", "42"}},
		{input: `'a\\b'`, want: []string{`a\\b`}},
		{input: `""`, want: []string{""}},
		{input: "''", want: []string{""}},
		{input: `"a" b`, want: []string{"a", "b"}},
		{input: "module enable ''", want: []string{"module", "enable", ""}},
		{input: `module enable "foo`, err: "No closing quotation"},
		{input: `trailing\`, err: "No escaped character"},
		{input: `'a\'b'`, err: "No closing quotation"},
	}
	for _, test := range cases {
		got, err := split(test.input)
		if test.err != "" {
			if err == nil || err.Error() != test.err {
				t.Fatalf("split %q error %v", test.input, err)
			}
			continue
		}
		if err != nil || !sameStrings(got, test.want) {
			t.Fatalf("split %q got %#v %v want %#v", test.input, got, err, test.want)
		}
	}
}

func command(base kernel.ControlCommand, raw string) kernel.ControlCommand {
	base.RawArguments = raw
	return base
}

func text(t *testing.T, result kernel.CommandResult) string {
	t.Helper()
	if result.Text == nil {
		t.Fatal("missing reply text")
	}
	return *result.Text
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
