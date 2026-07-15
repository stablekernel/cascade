package generate

import (
	"fmt"
	"strings"
)

// writeOutputHeredocLines emits shell lines that append a multiline value to
// $GITHUB_OUTPUT using the documented heredoc form with a delimiter minted at
// runtime from random bytes.
//
// The values these blocks carry are free-form text (changelogs embed arbitrary
// commit-message lines, plan summaries embed manifest-derived names). With a
// fixed delimiter, a value containing that delimiter as a bare line terminates
// the heredoc early and the remaining lines parse as forged step outputs, so
// the delimiter must be unguessable per write.
//
// valueCmds are shell commands whose combined stdout forms the value.
func writeOutputHeredocLines(sb *strings.Builder, indent, key string, valueCmds ...string) {
	fmt.Fprintf(sb, "%sCASCADE_DELIM=\"$(dd if=/dev/urandom bs=15 count=1 status=none | base64)\"\n", indent)
	fmt.Fprintf(sb, "%s{\n", indent)
	fmt.Fprintf(sb, "%s  echo \"%s<<${CASCADE_DELIM}\"\n", indent, key)
	for _, cmd := range valueCmds {
		fmt.Fprintf(sb, "%s  %s\n", indent, cmd)
	}
	fmt.Fprintf(sb, "%s  echo \"${CASCADE_DELIM}\"\n", indent)
	fmt.Fprintf(sb, "%s} >> \"$GITHUB_OUTPUT\"\n", indent)
}
