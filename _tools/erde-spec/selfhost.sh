#!/bin/sh
# Self-hosting check: compiles the reference Erde compiler (which is written in
# Erde) to Lua w/ go-erde, then checks that the result compiles the reference
# compiler's sources byte-for-byte the same as the reference compiler itself.
#
# usage: _tools/erde-spec/selfhost.sh <path to erde-lang/erde checkout>
#
# Requires lua (5.1+) w/ the erde checkout's dependencies installed.
set -eu

erde_repo=$(cd "$1" && pwd)
go_erde=$(cd "$(dirname "$0")/../.." && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

(cd "$go_erde" && go build -o "$work_dir/erde" ./cmd/erde)

mkdir -p "$work_dir/generated/erde" "$work_dir/reference-out" "$work_dir/generated-out"
for source in "$erde_repo"/erde/*.erde; do
  "$work_dir/erde" -c "$source" > "$work_dir/generated/erde/$(basename "$source" .erde).lua"
done

cat > "$work_dir/compile_all.lua" <<LUA
local compile = require('erde.compile')
for path in io.popen('ls $erde_repo/erde/*.erde'):lines() do
  local f = assert(io.open(path))
  local source = f:read('*a')
  f:close()
  local out = assert(io.open(OUT_DIR .. '/' .. path:match('([^/]+)%.erde$') .. '.lua', 'w'))
  out:write((compile(source, { lua_target = 'jit', alias = path })))
  out:close()
end
LUA

LUA_PATH="$erde_repo/?.lua;$erde_repo/?/init.lua;${LUA_PATH:-;}" \
  lua -e "OUT_DIR='$work_dir/reference-out'" "$work_dir/compile_all.lua"
LUA_PATH="$work_dir/generated/?.lua;$work_dir/generated/?/init.lua;${LUA_PATH:-;}" \
  lua -e "OUT_DIR='$work_dir/generated-out'" "$work_dir/compile_all.lua"

diff -r "$work_dir/reference-out" "$work_dir/generated-out"
echo "ok: go-erde compiled the Erde compiler, which reproduces the reference output"
