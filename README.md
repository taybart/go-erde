# go-erde

[Erde](https://erde-lang.github.io/) for Go: run Erde (and Lua) scripts in Go
programs, or compile Erde to Lua.

go-erde is a fork of [gopher-lua](https://github.com/yuin/gopher-lua) (a Lua 5.1
VM written in Go) that adds an Erde parser. Erde is parsed directly into
gopher-lua's AST, which then goes through gopher-lua's unchanged compiler and
VM. There is no intermediate Lua source and no dependency on the reference
(Lua) Erde compiler.

```
             Lua source ──> parse.Parse ─────┐
                                             ├──> AST ──> Compile ──> VM
            Erde source ──> parse.ParseErde ─┘     │
                                                   └──> luagen ──> Lua source
```

## Installation

```sh
go get github.com/taybart/go-erde
go install github.com/taybart/go-erde/cmd/erde@latest # CLI
```

## CLI

```sh
erde script.erde         # run a script (*.lua scripts run as Lua)
erde -c script.erde      # print the script compiled to Lua
erde -e 'print("{1 + 1}")'
erde                     # REPL
cat script.erde | erde - # stdin (also works w/ `erde <(...)`)
```

Scripts that don't end in `.lua` are treated as Erde (use `-lang lua` or
`-lang erde` to override). See `erde -h` for all options.

## Go API

The package name is `lua`, since it is still a Lua VM. Everything from
gopher-lua works the same way (see [docs/gopher-lua.md](docs/gopher-lua.md)),
plus:

```go
import lua "github.com/taybart/go-erde"

L := lua.NewState()
defer L.Close()

// Erde equivalents of Load / LoadString / DoString
L.DoErdeString(`print("hello {_VERSION}")`)
fn, err := L.LoadErdeString(source)
fn, err := L.LoadErde(reader, "name.erde")

// *.erde files load as Erde, and `require` finds `?.erde` modules
L.DoFile("script.erde")
```

Erde and Lua modules can `require` each other freely, since both compile to the
same bytecode.

Lower level:

```go
import (
	"github.com/taybart/go-erde/luagen"
	"github.com/taybart/go-erde/parse"
)

chunk, err := parse.ParseErde(reader, "name.erde") // []ast.Stmt, same as parse.Parse
proto, err := lua.Compile(chunk, "name.erde")
source := luagen.Generate(chunk) // Lua source (works for any chunk, including from Lua)
```

## Differences from the reference compiler

go-erde aims to match the [reference compiler](https://github.com/erde-lang/erde)
exactly, and is tested against its spec suite (see [Testing](#testing)). The
differences:

- **No Lua targets.** gopher-lua is Lua 5.1 + `goto`, so there are no
  target-specific restrictions (ex. `goto` is always allowed, `break` doesn't
  have to be the last statement in a block).
- **Bit operators** (`|`, `~`, `&`, `<<`, `>>`, unary `~`) compile to calls
  into a `bit32` library (Lua 5.1 has no bit operators), which go-erde adds to
  the standard libraries.
- **Error line numbers** refer to the Erde source directly (no source maps or
  error rewriting needed).
- **Compound assignment evaluates index chains once.** `t[f()] += 1` calls `f`
  once (the reference compiler calls it twice).
- **Lua keywords as names.** Erde allows names like `end` that are keywords in
  Lua. go-erde uses them as is; the reference compiler renames them (`end_`),
  which is only visible for globals.

### luagen output

`erde -c` / `luagen.Generate` output is valid Lua 5.1+ (and LuaJIT), except:

- `continue` and `goto` compile to `goto`, which needs Lua 5.2+ or LuaJIT.
- Bit operators compile to `bit32` calls, which need a `bit32` library (builtin
  in Lua 5.2, otherwise use a polyfill).

Statements are kept on their original lines, so errors in the generated Lua
report the Erde line numbers. Comments are not preserved.

## Testing

```sh
go test ./...
```

Besides gopher-lua's own tests, this runs:

- **Erde spec tests** ([erde_spec_test.go](erde_spec_test.go)): every snippet
  from the reference compiler's spec suite, run three ways (reference compiler
  output, parsed natively, compiled w/ luagen), which must all give the same
  results. Snippets the reference compiler rejects must be rejected. Regenerate
  [testdata/erde-spec](testdata/erde-spec) w/
  `_tools/erde-spec/update.sh <erde checkout>`.
- **luagen round trips** ([luagen_test.go](luagen_test.go)): gopher-lua's Lua
  test scripts, run after Lua -> AST -> Lua.

`_tools/erde-spec/selfhost.sh <erde checkout>` compiles the reference compiler
(written in Erde) to Lua w/ go-erde, and checks that the result compiles its own
sources byte-for-byte the same as the reference compiler.

## Porting Erde to other Lua runtimes

See [docs/PORTING.md](docs/PORTING.md), which describes how go-erde was built
(only a new parser front end, no VM changes) as a guide for doing the same in
other Lua implementations.

## License

MIT, see [LICENSE](LICENSE). Based on
[gopher-lua](https://github.com/yuin/gopher-lua) by Yusuke Inuzuka.
