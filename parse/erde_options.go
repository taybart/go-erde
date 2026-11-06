package parse

import (
	"fmt"
	"strings"
)

// ErdeOptions enables experimental Erde extensions. These are go-erde only
// (the reference compiler does not support them), so they are all off by
// default. With everything off, the parser accepts exactly the language of the
// reference compiler (ex. `match` is only ever an ordinary name).
type ErdeOptions struct {
	// Match enables (Rust style) `match` statements:
	//
	//	match mode {
	//	  "movie" => { ... }
	//	  "show" | "series" => { ... }
	//	  _ if count > 5 => { ... }
	//	  _ => { ... }
	//	}
	//
	// Patterns are expressions compared w/ `==` (names are values, not
	// bindings). The subject is evaluated once and there is no fallthrough.
	//
	// `match` is a contextual keyword, so it is still usable as a name (ex.
	// `s:match(p)`, `local match = 1`, `match(x)`).
	Match bool

	// JSX enables JSX style elements, which are lowered to calls of
	// JSXFactory (see erde_jsx.go):
	//
	//	local page = <Layout title="home">
	//	  <p class="greeting">hello {name}!</p>
	//	</Layout>
	//
	// The runtime library `erde.jsx` provides a factory (`h`) that renders to
	// HTML strings.
	JSX bool
	// JSXFactory is the (possibly dotted) name of the function elements are
	// lowered to. By default, it's `h` if declared, otherwise the runtime
	// library's `h` (imported automatically).
	JSXFactory string
}

// ErdeExperiments lists the experiment names accepted by Enable.
var ErdeExperiments = []string{"match", "jsx"}

// Enable turns on experiments by name, given as a comma separated list (ex.
// from a command line flag).
func (o *ErdeOptions) Enable(names string) error {
	for _, name := range strings.Split(names, ",") {
		switch strings.TrimSpace(name) {
		case "":
		case "match":
			o.Match = true
		case "jsx":
			o.JSX = true
		default:
			return fmt.Errorf("unknown erde experiment '%s' (available: %s)", name, strings.Join(ErdeExperiments, ", "))
		}
	}
	return nil
}
