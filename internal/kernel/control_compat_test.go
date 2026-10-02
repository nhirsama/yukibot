package kernel

import "testing"

func TestCommandWhitespaceMatchesPython(t *testing.T) {
	for _, space := range []rune{'\x1c', '\x1d', '\x1e', '\x1f', '\u00a0', '\u3000'} {
		text := "/route" + string(space) + " add source target"
		name, args, ok := SplitCommand(text)
		if !ok || name != "/route" || args != " add source target" {
			t.Fatalf("delimiter %U: %q %q %t", space, name, args, ok)
		}
		if commandToken(text) {
			t.Fatalf("registration accepted a whitespace-containing name: %q", text)
		}
		if got := trimSpace(string(space) + "/route" + string(space)); got != "/route" {
			t.Fatalf("help lookup %U: %q", space, got)
		}
	}
}
