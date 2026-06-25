package visualize

// Theme carries presentation choices an emitter may honor. It is a stub in this
// iteration (a default value is threaded through every emit path); later work
// fills it with concrete styling slots. Keeping it in the signature now means
// adding fields later is additive, never a breaking change.
type Theme struct {
	// Name identifies the theme. The zero value selects the emitter's built-in
	// default, so callers that do not care about theming pass DefaultTheme.
	Name string
}

// DefaultTheme is the neutral theme used when a caller does not supply one.
var DefaultTheme = Theme{Name: "default"}

// Options holds optional emit behavior. It is populated by the functional
// Option tail rather than constructed directly, so new knobs are additive. The
// zero value is valid and selects each emitter's defaults.
type Options struct {
	// Title, when set, is rendered as a diagram heading by emitters that
	// support one. An empty title emits no heading.
	Title string
}

// Option configures emit behavior. Following the repo convention, required
// inputs are positional on Emit and optional behavior arrives as a variadic
// Option tail, so capabilities can be added without changing the signature.
type Option func(*Options)

// WithTitle sets a diagram heading. Emitters that have no notion of a heading
// ignore it.
func WithTitle(title string) Option {
	return func(o *Options) { o.Title = title }
}

// applyOptions folds a variadic Option tail onto a zero Options value and
// returns the result, so each Emit call starts from a clean, default state.
func applyOptions(opts []Option) Options {
	var o Options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// Emitter turns a render-agnostic ViewModel into diagram source. It is the
// pluggable seam of the package: Mermaid is the first implementation, and a
// future renderer is added by writing another Emitter without touching the view
// model. Required inputs (the model and a theme) are positional; optional
// behavior arrives as a variadic Option tail.
type Emitter interface {
	// Emit renders vm using theme and any options, returning the diagram source.
	// It returns an error only when the model cannot be expressed as valid
	// source; a well-formed model always emits successfully.
	Emit(vm ViewModel, theme Theme, opts ...Option) (string, error)
}
