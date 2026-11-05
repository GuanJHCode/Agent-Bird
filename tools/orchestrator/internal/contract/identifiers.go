package contract

import (
	"strings"
	"unicode/utf8"
)

// New control identifiers reserve envelope space for generated launch bindings.
// This validates admission only; retained rows/receipts are not rewritten.
const MaxControlIDBytes = 128

func ValidControlID(value string) bool {
	return len(value) > 0 && len(value) <= MaxControlIDBytes && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
