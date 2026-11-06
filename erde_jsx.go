package lua

// go-erde: runtime library for the `jsx` experiment (see parse/erde_jsx.go),
// preloaded as `erde.jsx`.

import (
	_ "embed"
	"strings"
	"sync"

	"github.com/taybart/go-erde/parse"
)

// JSXLibName is the module name of the jsx runtime (`require('erde.jsx')`).
const JSXLibName = "erde.jsx"

//go:embed lib/jsx.lua
var JSXLibSource string

// The runtime is compiled once, and its (immutable) bytecode is shared by
// every state that requires it.
var jsxLib struct {
	once  sync.Once
	proto *FunctionProto
	err   error
}

func jsxLibProto() (*FunctionProto, error) {
	jsxLib.once.Do(func() {
		chunk, err := parse.Parse(strings.NewReader(JSXLibSource), JSXLibName)
		if err != nil {
			jsxLib.err = err
			return
		}
		jsxLib.proto, jsxLib.err = Compile(chunk, JSXLibName)
	})
	return jsxLib.proto, jsxLib.err
}

// OpenJSX registers the jsx runtime in package.preload, so it's loaded (once
// per state) by `require('erde.jsx')`.
func OpenJSX(L *LState) int {
	L.PreloadModule(JSXLibName, func(L *LState) int {
		proto, err := jsxLibProto()
		if err != nil {
			L.RaiseError("%s", err.Error())
		}
		L.Push(L.NewFunctionFromProto(proto))
		L.Call(0, 1)
		return 1
	})
	return 0
}
