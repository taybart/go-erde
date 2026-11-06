package lua

// go-erde: runtime library for the `jsx` experiment (see parse/erde_jsx.go),
// preloaded as `erde.jsx`.

import (
	_ "embed"
)

// JSXLibName is the module name of the jsx runtime (`require('erde.jsx')`).
const JSXLibName = "erde.jsx"

//go:embed lib/jsx.lua
var JSXLibSource string

// OpenJSX registers the jsx runtime in package.preload, so it's loaded (once)
// by `require('erde.jsx')`.
func OpenJSX(L *LState) int {
	L.PreloadModule(JSXLibName, func(L *LState) int {
		fn, err := L.LoadString(JSXLibSource)
		if err != nil {
			L.RaiseError("%s", err.Error())
		}
		L.Push(fn)
		L.Call(0, 1)
		return 1
	})
	return 0
}
