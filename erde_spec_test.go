package lua

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/taybart/go-erde/luagen"
	"github.com/taybart/go-erde/parse"
)

// Differential tests against the reference Erde compiler, using the snippets
// from its spec suite (see _tools/erde-spec/update.sh).
//
// Each valid snippet runs three ways, which must all give the same results:
//   - the reference compiler's Lua output (NNNN.ref.lua)
//   - the snippet, parsed natively (parse.ParseErde)
//   - the snippet, compiled to Lua (luagen)

const erdeSpecDir = "testdata/erde-spec"

func TestErdeSpecValid(t *testing.T) {
	sources, _ := filepath.Glob(filepath.Join(erdeSpecDir, "valid", "*.erde"))
	if len(sources) == 0 {
		t.Fatal("no spec snippets found")
	}

	for _, path := range sources {
		source, _ := os.ReadFile(path)
		ref, _ := os.ReadFile(strings.TrimSuffix(path, ".erde") + ".ref.lua")

		want := erdeSpecRun(func(L *LState) error { return L.DoString(string(ref)) })
		native := erdeSpecRun(func(L *LState) error { return L.DoErdeString(string(source)) })

		var generated string
		compiled := erdeSpecRun(func(L *LState) error {
			chunk, err := parse.ParseErde(strings.NewReader(string(source)), "<string>")
			if err != nil {
				return err
			}
			generated = luagen.Generate(chunk)
			return L.DoString(generated)
		})

		if native != want {
			t.Errorf("%s (native)\n%s\n  got:  %s\n  want: %s", path, source, native, want)
		}
		if compiled != want {
			t.Errorf("%s (luagen)\n%s\n%s\n  got:  %s\n  want: %s", path, source, generated, compiled, want)
		}
	}
}

func TestErdeSpecInvalid(t *testing.T) {
	sources, _ := filepath.Glob(filepath.Join(erdeSpecDir, "invalid", "*.erde"))
	if len(sources) == 0 {
		t.Fatal("no spec snippets found")
	}

	for _, path := range sources {
		source, _ := os.ReadFile(path)
		reason, _ := os.ReadFile(strings.TrimSuffix(path, ".erde") + ".txt")

		L := NewState()
		if _, err := L.LoadErdeString(string(source)); err == nil {
			t.Errorf("%s: accepted invalid snippet (reference: %s)\n%s", path, reason, source)
		}
		L.Close()
	}
}

var erdeSpecPositions = regexp.MustCompile(`<string>:\d+:|line:\d+\(column:\d+\)|at EOF:`)

// erdeSpecRun runs a snippet and returns its results (or error) as a string.
func erdeSpecRun(run func(*LState) error) string {
	L := NewState()
	defer L.Close()
	L.SetGlobal("print", L.NewFunction(func(*LState) int { return 0 }))

	if err := run(L); err != nil {
		msg := strings.SplitN(err.Error(), "\n", 2)[0]
		return "error: " + erdeSpecPositions.ReplaceAllString(msg, "<pos>")
	}

	results := []string{}
	for i := 1; i <= L.GetTop(); i++ {
		results = append(results, erdeSpecSerialize(L.Get(i), map[*LTable]bool{}))
	}
	return strings.Join(results, ", ")
}

func erdeSpecSerialize(value LValue, seen map[*LTable]bool) string {
	switch v := value.(type) {
	case *LTable:
		if seen[v] {
			return "<cycle>"
		}
		seen[v] = true
		fields := []string{}
		v.ForEach(func(key, value LValue) {
			fields = append(fields, erdeSpecSerialize(key, seen)+"="+erdeSpecSerialize(value, seen))
		})
		sort.Strings(fields)
		return "{" + strings.Join(fields, ",") + "}"
	case *LFunction:
		return "<function>"
	case LString:
		return fmt.Sprintf("%q", string(v))
	}
	return value.String()
}
