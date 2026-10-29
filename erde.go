package lua

// Erde (https://erde-lang.github.io/) support. Erde is implemented entirely as
// a second parser front end (parse.ParseErde), which produces the same AST as
// the Lua parser. The compiler and VM are unchanged gopher-lua.
//
// To keep merging upstream gopher-lua changes easy, Erde additions live in new
// files wherever possible. Changes to upstream files are marked w/ `go-erde`.

import (
	"io"
	"strings"

	"github.com/taybart/go-erde/parse"
)

const ErdeName = "go-erde"
const ErdeVersion = "0.1.0"
const ErdeCopyRight = ErdeName + " " + ErdeVersion + " (based on " + PackageName + " " + PackageVersion + ")"

func isErdePath(path string) bool {
	return strings.HasSuffix(path, ".erde")
}

// LoadErde is like Load, but for Erde source.
func (ls *LState) LoadErde(reader io.Reader, name string) (*LFunction, error) {
	chunk, err := parse.ParseErdeWithOptions(reader, name, ls.Options.Erde)
	if err != nil {
		return nil, newApiErrorE(ApiErrorSyntax, err)
	}
	proto, err := Compile(chunk, name)
	if err != nil {
		return nil, newApiErrorE(ApiErrorSyntax, err)
	}
	return newLFunctionL(proto, ls.currentEnv(), 0), nil
}

// LoadErdeString is like LoadString, but for Erde source.
func (ls *LState) LoadErdeString(source string) (*LFunction, error) {
	return ls.LoadErde(strings.NewReader(source), "<string>")
}

// DoErdeString is like DoString, but for Erde source.
func (ls *LState) DoErdeString(source string) error {
	if fn, err := ls.LoadErdeString(source); err != nil {
		return err
	} else {
		ls.Push(fn)
		return ls.PCall(0, MultRet, nil)
	}
}
