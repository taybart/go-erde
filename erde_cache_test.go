package lua

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/taybart/go-erde/parse"
)

func TestCompiledFileCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mod.erde")
	write := func(src string, mod time.Time) {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	load := func(opts parse.ErdeOptions) (*LState, error) {
		L := NewState(Options{Erde: opts})
		L.SetField(L.GetField(L.Get(GlobalsIndex), "package"), "path", LString(filepath.Join(dir, "?.erde")))
		return L, L.DoString(`m = require('mod')`)
	}

	jsx := parse.ErdeOptions{JSX: true}
	write("local t = { n = 1, el = tostring(<b/>) }\nreturn t", time.Unix(1000, 0))

	a, err := load(jsx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := load(jsx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// shared code, separate modules
	key, proto, _ := a.compiledFile(path)
	if proto == nil {
		t.Fatalf("not cached: %+v", key)
	}
	if err := a.DoString(`m.n = 2`); err != nil {
		t.Fatal(err)
	}
	if err := b.DoString(`assert(m.n == 1 and m.el == '<b></b>', 'module shared between states')`); err != nil {
		t.Fatal(err)
	}

	// the erde options are part of the key (w/o jsx, it doesn't parse)
	if c, err := load(parse.ErdeOptions{}); err == nil {
		c.Close()
		t.Error("used code compiled w/ other erde options")
	}

	// a changed file is compiled again
	write("return { n = 3 }", time.Unix(2000, 0))
	d, err := load(jsx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.DoString(`assert(m.n == 3, 'stale module')`); err != nil {
		t.Fatal(err)
	}

	// and it can be turned off
	CacheCompiledFiles = false
	defer func() { CacheCompiledFiles = true }()
	if _, proto, _ := d.compiledFile(path); proto != nil {
		t.Error("cache used while off")
	}
}
