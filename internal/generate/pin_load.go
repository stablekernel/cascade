package generate

import (
	"fmt"
	"os"
)

// LoadActionPinTable parses an action_pins.yaml from an arbitrary disk path into
// the generator pin table (emit:true entries only). A version-pinned binary uses
// it to regenerate against the repo's current on-disk pins instead of its stale
// compiled-in copy, which is the mechanism the #438 self-heal regenerate needs.
func LoadActionPinTable(path string) (map[string]actionPin, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies a trusted repo path.
	if err != nil {
		return nil, fmt.Errorf("read action pins %q: %w", path, err)
	}
	return parseActionPins(data)
}
