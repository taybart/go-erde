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
}

// ErdeExperiments lists the experiment names accepted by Enable.
var ErdeExperiments = []string{"match"}

// Enable turns on experiments by name, given as a comma separated list (ex.
// from a command line flag).
func (o *ErdeOptions) Enable(names string) error {
	for _, name := range strings.Split(names, ",") {
		switch strings.TrimSpace(name) {
		case "":
		case "match":
			o.Match = true
		default:
			return fmt.Errorf("unknown erde experiment '%s' (available: %s)", name, strings.Join(ErdeExperiments, ", "))
		}
	}
	return nil
}
