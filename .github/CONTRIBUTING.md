# Contributing

## Layout

| Path | What |
| --- | --- |
| `parse/erde_lexer.go`, `parse/erde_parser.go` | Erde parser (Erde -> AST) |
| `luagen/` | Lua code generator (AST -> Lua) |
| `erde.go`, `bit32lib.go` | Erde Go API, `bit32` library |
| `cmd/erde/` | CLI |
| everything else | gopher-lua (Lua parser, compiler, VM, standard libraries) |

## Keeping in sync w/ gopher-lua

go-erde should stay mergeable w/ upstream gopher-lua, so:

- Don't change the compiler or VM. Erde features are lowered to the existing AST
  in the parser (see [docs/PORTING.md](../docs/PORTING.md)).
- Put Erde additions in new files. When an upstream file has to change, keep the
  change small and mark it w/ a `go-erde` comment.

To merge upstream changes:

```sh
git remote add upstream https://github.com/yuin/gopher-lua.git
git fetch upstream
git merge upstream/master
# conflicts should only be import paths (github.com/yuin/gopher-lua -> github.com/taybart/go-erde)
```

## Building

Files starting w/ `_` generate the file of the same name without it (ex.
`_state.go` -> `state.go`) via a small inlining tool (requires python). Don't
edit the generated files directly.

```sh
make build # regenerate + build
make erde  # build the CLI
make test
```

## Testing

See [Testing](../README.md#testing) in the README. When changing the Erde
parser, also run `_tools/erde-spec/selfhost.sh <erde checkout>`.
