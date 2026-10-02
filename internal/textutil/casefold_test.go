package textutil

import "testing"

func TestCasefold(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"ABC中文", "abc中文"}, {"Straße", "strasse"},
		{"İ", "i\u0307"}, {"ΟΣς", "οσσ"}, {"ﬃ", "ffi"},
		{"ᏸꭰ", "ᏰᎠ"}, {"e\u0301", "e\u0301"},
		// These gained case mappings after Unicode 15.0. Preserve the Python
		// baseline instead of inheriting changes from Go's Unicode tables.
		{"\u1c89\ua7cb\U00010d50\U00016ea0", "\u1c89\ua7cb\U00010d50\U00016ea0"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			if got := Casefold(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
