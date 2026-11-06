package lua

// go-erde: runtime library for the `markup` experiment (see parse/erde_markup.go),
// preloaded as `erde.markup` (and its vendored morph library as
// `erde.markup.idiomorph`).

import (
	_ "embed"
	"strings"
	"sync"

	"github.com/taybart/go-erde/parse"
)

// MarkupLibName is the module name of the markup runtime (`require('erde.markup')`).
const MarkupLibName = "erde.markup"

// MarkupLibOldName is the old (deprecated) name of the markup runtime, the
// same module as MarkupLibName.
const MarkupLibOldName = "erde.jsx"

// MarkupMorphLibName is the module name of the vendored idiomorph (the script, as
// a string), which erde.markup inlines in interactive pages.
const MarkupMorphLibName = "erde.markup.idiomorph"

//go:embed lib/markup.lua
var MarkupLibSource string

//go:embed lib/idiomorph.lua
var MarkupMorphLibSource string

// An embedded lua module. It's compiled once, and its (immutable) bytecode is
// shared by every state that requires it.
type embeddedLib struct {
	name, source string
	once         sync.Once
	proto        *FunctionProto
	err          error
}

func (lib *embeddedLib) compiled() (*FunctionProto, error) {
	lib.once.Do(func() {
		chunk, err := parse.Parse(strings.NewReader(lib.source), lib.name)
		if err != nil {
			lib.err = err
			return
		}
		lib.proto, lib.err = Compile(chunk, lib.name)
	})
	return lib.proto, lib.err
}

func (lib *embeddedLib) preload(L *LState) {
	L.PreloadModule(lib.name, func(L *LState) int {
		proto, err := lib.compiled()
		if err != nil {
			L.RaiseError("%s", err.Error())
		}
		L.Push(L.NewFunctionFromProto(proto))
		L.Call(0, 1)
		return 1
	})
}

var markupLibs = []*embeddedLib{
	{name: MarkupLibName, source: MarkupLibSource},
	{name: MarkupMorphLibName, source: MarkupMorphLibSource},
}

// OpenMarkup registers the markup runtime in package.preload, so it's loaded (once
// per state) by `require('erde.markup')`.
func OpenMarkup(L *LState) int {
	for _, lib := range markupLibs {
		lib.preload(L)
	}
	L.PreloadModule(MarkupLibOldName, func(L *LState) int {
		L.Push(L.GetGlobal("require"))
		L.Push(LString(MarkupLibName))
		L.Call(1, 1)
		return 1
	})
	return 0
}
