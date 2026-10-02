// Package textutil contains shared text semantics used by migrated features.
package textutil

import "strings"

// Casefold matches Python 3.12's Unicode 15.0 full, locale-independent folding.
// It deliberately does not normalize accents (NFC/NFKC). The generated table
// avoids behavior changing silently when Go updates its Unicode version.
func Casefold(s string) string {
	var b strings.Builder
	for _, r := range s {
		if folded, ok := casefoldTable[r]; ok {
			b.WriteString(folded)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
