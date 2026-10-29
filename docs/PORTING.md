# Porting Erde to a Lua runtime

This describes how go-erde added [Erde](https://erde-lang.github.io/) to
[gopher-lua](https://github.com/yuin/gopher-lua), as a guide for doing the same
in other Lua runtimes (C Lua via bindings such as mlua, LuaJIT, Luau, other
reimplementations).

**The main point: go-erde only added a parser.** Erde is a syntax layer over
Lua. Every Erde feature can be expressed with ordinary Lua 5.1 constructs (plus
`goto`), so a port never needs to touch the runtime's compiler, VM, garbage
collector, or standard library. go-erde lexes and parses Erde, lowers it to the
same AST the Lua parser produces, and hands that AST to gopher-lua's unchanged
compiler.

## What go-erde changed in gopher-lua

Compared to upstream gopher-lua:

| | Files | Change |
| --- | --- | --- |
| **Unchanged** | `vm.go`, `_vm.go`, `opcode.go`, `value.go`, `table.go`, `function.go`, `alloc.go`, all standard libraries | none |
| | `compile.go`, `ast/`, `parse/parser.go(.y)`, `parse/lexer.go` | import path only |
| **Hooks** (marked `go-erde`) | `auxlib.go` | `LoadFile` loads `*.erde` w/ the Erde parser (3 lines) |
| | `config.go` | `./?.erde` added to the default `package.path` |
| | `linit.go` | open the `bit32` library |
| | `state.go`, `_state.go` | `Options.Erde` (experimental extensions for Erde source loaded by the state) |
| **New** | `parse/erde_lexer.go`, `parse/erde_parser.go` | the Erde front end (Erde -> AST) |
| | `erde.go` | `LoadErde`, `LoadErdeString`, `DoErdeString` |
| | `parse/erde_options.go` | experimental extensions (ex. `match`), off by default |
| | `bit32lib.go` | Lua 5.2's `bit32` (gopher-lua is Lua 5.1, which has no bit operators) |
| | `luagen/` | AST -> Lua source (optional, see [strategy B](#b-source-to-source)) |
| | `cmd/erde/` | CLI (from gopher-lua's `glua`), plus `compile` / `clean` / `lint` / `fmt` (see [TOOLS.md](TOOLS.md)) |
| | `lint/`, `format/` | linter (on the AST) and formatter (on raw tokens, from `parse.ErdeRawTokens`) |
| | `erde_spec_test.go`, `testdata/erde-spec/`, `_tools/erde-spec/` | differential tests against the reference compiler |

To check this still holds:

```sh
git diff --stat <upstream gopher-lua commit> -- vm.go _vm.go opcode.go compile.go
```

## Choosing an integration strategy

How Erde plugs in depends on what the runtime exposes.

### A. AST front end

*What go-erde does.* If the runtime's compiler takes an AST that you can build
(typical for Lua implementations written in Go, Rust, Java, JS, ...), write an
Erde parser that produces that AST.

- Best result: no intermediate Lua text, errors and debug info point at the Erde
  source directly, Erde and Lua share one compile pipeline.
- Needs an AST you can construct, and a compiler that accepts it.

### B. Source to source

If the runtime only accepts Lua source (or bytecode), write an Erde -> Lua
compiler in the host language and load its output as a normal chunk.

- Works w/ any runtime, including C Lua / LuaJIT / Luau through bindings. For
  example, mlua (Rust) wraps the Lua C API, so there is no AST to target, but
  `Lua::load` accepts source text.
- Build it as parser -> AST -> printer, like go-erde's `parse.ParseErde` +
  `luagen`. The AST step is what makes the lowering below simple.
- Keep line numbers aligned: print each statement on its original source line
  (`luagen` does this), and runtime errors / tracebacks report Erde line
  numbers w/o needing source maps or error rewriting.
- The printer has extra work an AST front end doesn't (see
  [pitfalls](#pitfalls)), and must account for the target Lua version.

### C. Embed the reference compiler

The reference compiler ([erde-lang/erde](https://github.com/erde-lang/erde)) is
pure Lua. Load it into the runtime and add a `package.searchers` entry (5.2+) or
`package.loaders` entry (5.1) for `?.erde`.

- Least code, and exact reference behavior.
- Compilation runs inside the VM on every load (slower startup), and errors
  refer to the compiled Lua unless you use its error rewriting (see the Erde
  docs, "Error Rewriting").

For a runtime like mlua, C is the quickest way to get Erde running, and B is the
equivalent of what go-erde does.

## Front end design

go-erde's parser mirrors the reference compiler
([`erde/compile.erde`](https://github.com/erde-lang/erde/blob/master/erde/compile.erde)
and [`erde/tokenize.erde`](https://github.com/erde-lang/erde/blob/master/erde/tokenize.erde))
function by function: same recursive descent, same lookahead, same error
messages. Read them side by side; it's the most reliable way to match the
reference behavior.

### Lexer

- **Strings are split into tokens**: opening delimiter, content, interpolation
  open `{`, the tokens of the interpolated expression, interpolation close `}`,
  closing delimiter. The parser then treats interpolated expressions like any
  other expression. Track brace depth inside interpolations (`"{ { a = 1 }.a }"`).
- **Escapes**: Lua escapes in quoted strings, plus `\{` / `\}` in double quoted
  and block strings. Single quoted strings don't interpolate (and `\{` is
  invalid there). In block strings, other backslashes are literal.
- **Block strings**: skip a newline directly after the opening `[[` (as Lua
  does), but only there, not after an interpolation.
- **Numbers**: hex (incl. hex floats) and binary (`0b101`) literals. go-erde
  converts them to decimal, and strips leading zeros from integers (gopher-lua
  would read `010` as octal).
- **Shebang** (`#!`) on the first line is skipped.

### Parser

- **Calls only on the same line**: `(` starts a call only if it is on the same
  line as the previous token; otherwise it starts a new statement.
- **Arrow functions** need lookahead: a name followed by `->` / `=>`, or a
  `(...)`, `[...]`, `{...}` (params / destructure) followed by `->` / `=>`
  (look past the matching close).
- **`return (a, b)`** returns multiple values if there is a top level comma
  inside the parentheses (otherwise it is a parenthesized expression), unless it
  is followed by `->` (an arrow function).
- **Scopes**: track `local` / `global` / `module` declarations per block, since
  references to `global` / `module` names compile differently.
- **Keywords**: `not`, `and`, `or`, `end`, `then` are Lua keywords but *not*
  Erde keywords, so they are valid names.

### Lowering

Every Erde construct, as the Lua it lowers to. Names like `tmp` are fresh
temporaries (go-erde uses names that aren't valid identifiers, ex. `(erde 1)`,
so they can never clash w/ user names; a source printer must rename them).

| Erde | Lua |
| --- | --- |
| `a != b`, `!a`, `a && b`, `a \|\| b` | `a ~= b`, `not a`, `a and b`, `a or b` |
| `a // b` | `math.floor(a / b)` (native `//` on 5.3+) |
| `a \| b`, `a ~ b`, `a & b`, `a << b`, `a >> b`, `~a` | `bit32.bor(a, b)`, `bxor`, `band`, `lshift`, `rshift`, `bnot` (native on 5.3+) |
| `"a {x} b"` (also block strings) | `"a " .. tostring(x) .. " b"` |
| `'abc':upper()` | `("abc"):upper()` |
| `x += e` (any binary operator) | `x = x + (e)` |
| `a.b[k] += e` | `local B, K = a.b, k; B[K] = B[K] + (e)` (index chain evaluated once) |
| `a, b += 1, 2` | `local t1, t2 = 1, 2; a = a + t1; b = b + t2` |
| `local { a, b: c = 1 } = t` | `local a, c; local tmp = t; a = tmp.a; c = tmp.b; if c == nil then c = 1 end` |
| `local [a, b] = t` | `local a, b; local tmp = t; a = tmp[1]; b = tmp[2]` |
| `(a, b = 1) -> a + b` | `function(a, b) if b == nil then b = 1 end return a + b end` |
| `x => self.y + x` | `function(self, x) return self.y + x end` |
| `() -> (1, 2)` | `function() return 1, 2 end` |
| `({ a }) -> a` | `function(tmp) local a; a = tmp.a; return a end` |
| `(...rest) -> rest` | `function(...) local rest = {...} return rest end` |
| `return (a, b)` | `return a, b` |
| `while c { if x { continue } ... }` | `while c do if x then goto L end ... ::L:: end` |
| `global x = 1`, later `x` | `_G.x = 1`, later `_G.x` |
| `global function f() {}` | `_G.f = function() end` |
| `module x = 1`, later `x` | `_MODULE.x = 1`, later `_MODULE.x`, w/ `local _MODULE = {}` at the start of the chunk and `return _MODULE` at the end |
| `local function f() {}` | `local function f() end` (`f` can refer to itself) |
| `if`, `for`, `while`, `repeat`, `do`, `goto`, `::label::` | same as Lua, w/ braces |

Experimental (go-erde only, off by default, see the README):

| Erde | Lua |
| --- | --- |
| `match s { a \| b => { X } _ if g => { Y } _ => { Z } }` | `do local tmp = s if tmp == a or tmp == b then X elseif g then Y else Z end end` |

Keep experiments behind an option, so the default language stays identical to
the reference compiler. Prefer contextual keywords over reserved ones: `match`
is only a keyword at the start of a statement when the next token can't
continue a name (`=`, `,`, `.`, `[`, `:`, `op=`, or a call `match(x)` that isn't
followed by `{`), so code using `match` as a name (ex. `s:match(p)`) still
works with the experiment on.

Errors the parser must raise (the reference compiler does):

- `break` / `continue` outside a loop, `...` outside a vararg function.
- `module` declarations outside the top level, or combined w/ `return`.
- `local function a.b()` (scopes on table fields).
- `~=` in an expression (Lua's "not equal"; suggest `!=`). As a statement,
  `x ~= 1` is xor assignment.

## Pitfalls

Things that came up in go-erde, most of which apply to any port:

- **Parentheses don't exist in the AST.** `(f())` truncates `f`'s results to one
  value; go-erde marks this on the node (`AdjustRet`). A printer must also add
  parentheses back based on operator precedence, and for non-name prefixes
  (`("abc"):upper()`, `(function() end)()`).
- **`local f = function` vs `local function f`.** Only the latter can refer to
  itself. Lower `local function` to whatever the runtime uses for it.
- **`continue` labels go at the end of the loop body.** Lua allows jumping to a
  label at the end of a block even past local declarations. In `repeat ...
  until`, the condition follows the label, so locals declared in the body make
  `continue` an error (the reference compiler has the same limitation).
- **`break` / `return` must be last in a block** in Lua 5.1 source. Erde allows
  `break` anywhere; a printer should wrap it (`do break end`).
- **Lua 5.5 loop variables are read-only.** Erde allows reassigning them; when
  generating 5.5 source, bind the loop variable to a temporary and redeclare it
  at the start of the body when the body assigns it.
- **Keyword names** (`end`, `not`, ...): fine in an AST, but a printer must
  rename locals (`end_`), use `t["end"]` for fields, `_G["end"]` for globals,
  and pass `self` explicitly for methods (`obj["end"](obj)`).
- **Bit operators depend on the runtime.** 5.1 runtimes need a bit library
  (go-erde adds `bit32`); 5.3+ have native integer operators (note `bit32`
  works on unsigned 32-bit values, 5.3+ operators on 64-bit integers).
- **Line numbers**: set a line on every AST node, so runtime errors point at the
  right line. Report "unexpected end of input" errors distinctly (go-erde uses
  the Lua parser's `EOF` line) so a REPL can tell incomplete input from invalid
  input.
- **Host quirks**: check the host's number parsing (gopher-lua parses `010` as
  octal) and runtime limits (gopher-lua's default registry size is small for
  large string concatenations; unrelated to Erde, but the Erde compiler's own
  sources hit it).

## Integration checklist

- [ ] Load API for Erde source (strings, readers / files), alongside the Lua one.
- [ ] Load `*.erde` files w/ the Erde parser.
- [ ] `require` finds `?.erde` modules (default `package.path`, or a searcher).
      Erde and Lua modules can then require each other.
- [ ] CLI: run Erde by default for anything not ending in `.lua` (so pipes and
      `<(...)` work), w/ an option to force the language; read `-` from stdin.
- [ ] REPL: continue reading lines while the input is incomplete; try
      `return <line>` first so expressions print their values.

## Verifying a port

go-erde's tests are mostly runtime-agnostic, so reuse them:

- **Spec snippets** (`testdata/erde-spec`): every source the reference
  compiler's spec suite compiles. `valid/NNNN.erde` comes w/ `NNNN.ref.lua`, the
  reference compiler's output; run both in your runtime and compare results.
  `invalid/NNNN.erde` must be rejected (`NNNN.txt` has the reference error).
  `_tools/erde-spec/update.sh` regenerates them from an erde checkout. The
  reference output targets LuaJIT w/ `bit32` (closest to gopher-lua); change
  `lua_target` / `bitexpr` in `_tools/erde-spec/compile.lua` for other runtimes.
- **Self hosting**: the reference compiler is ~3,900 lines of Erde. Load it
  through your front end and have it compile its own sources; the output must be
  byte-identical to the reference compiler's (`_tools/erde-spec/selfhost.sh`
  does this w/ go-erde's Lua output).
- **Round trips** (source to source only): parse the runtime's own Lua test
  suite, print it, and run the printed code.
