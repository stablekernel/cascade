package generate

import (
	"fmt"
	"os"

	"github.com/stablekernel/cascade/internal/config"
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

// ApplyDiskPinOverrides overlays the pins from an on-disk action_pins.yaml onto
// cfg.ActionPins so a version-pinned binary regenerates against the repo's
// current pins instead of its stale compiled-in copy. It fills only keys the
// config does not already override (an explicit user pin still wins), and it
// reaches every emitted ref because actionRef resolves cfg.ActionPins first.
// The overlaid value carries the trailing "# <version>" in sha mode so an
// adopted sha stays auditable.
func ApplyDiskPinOverrides(cfg *config.TrunkConfig, path string) error {
	table, err := LoadActionPinTable(path)
	if err != nil {
		return err
	}
	if cfg.ActionPins == nil {
		cfg.ActionPins = map[string]string{}
	}
	sha := cfg.GetPinMode() == config.PinModeSHA
	for action, pin := range table {
		if _, set := cfg.ActionPins[action]; set {
			continue
		}
		if sha {
			cfg.ActionPins[action] = pin.sha + " # " + pin.shaVersion
		} else {
			cfg.ActionPins[action] = pin.tag
		}
	}
	return nil
}
