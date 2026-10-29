package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"strings"

	"github.com/chzyer/readline"
	"github.com/taybart/go-erde"
	"github.com/taybart/go-erde/ast"
	"github.com/taybart/go-erde/luagen"
	"github.com/taybart/go-erde/parse"
)

func main() {
	os.Exit(mainAux())
}

func mainAux() int {
	var opt_e, opt_l, opt_p, opt_lang, opt_x string
	var opt_i, opt_v, opt_dt, opt_dc, opt_c bool
	var opt_m int
	flag.StringVar(&opt_e, "e", "", "")
	flag.StringVar(&opt_l, "l", "", "")
	flag.StringVar(&opt_p, "p", "", "")
	flag.StringVar(&opt_lang, "lang", "", "")
	flag.StringVar(&opt_x, "X", "", "")
	flag.IntVar(&opt_m, "mx", 0, "")
	flag.BoolVar(&opt_i, "i", false, "")
	flag.BoolVar(&opt_v, "v", false, "")
	flag.BoolVar(&opt_dt, "dt", false, "")
	flag.BoolVar(&opt_dc, "dc", false, "")
	flag.BoolVar(&opt_c, "c", false, "")
	flag.Usage = func() {
		fmt.Println(`Usage: erde [options] [script [args]].
Available options are:
  -e stat  execute string 'stat'
  -lang l  source language, 'erde' or 'lua' (default: 'lua' for *.lua
           scripts, otherwise 'erde')
  -l name  require library 'name'
  -X list  enable experimental erde extensions (comma separated: ` + strings.Join(parse.ErdeExperiments, ", ") + `)
  -mx MB   memory limit(default: unlimited)
  -dt      dump AST trees
  -dc      dump VM codes
  -c       print 'script' compiled to Lua (without running it)
  -i       enter interactive mode after executing 'script'
  -p file  write cpu profiles to the file
  -v       show version information
A script of '-' is read from stdin.`)
	}
	flag.Parse()
	if opt_lang != "" && opt_lang != "erde" && opt_lang != "lua" {
		fmt.Printf("invalid -lang '%s' (expected 'erde' or 'lua')\n", opt_lang)
		return 1
	}
	var erdeOpts parse.ErdeOptions
	if err := erdeOpts.Enable(opt_x); err != nil {
		fmt.Println(err.Error())
		return 1
	}
	if len(opt_p) != 0 {
		f, err := os.Create(opt_p)
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	if len(opt_e) == 0 && !opt_i && !opt_v && flag.NArg() == 0 {
		opt_i = true
	}

	status := 0

	L := lua.NewState(lua.Options{Erde: erdeOpts})
	defer L.Close()
	if opt_m > 0 {
		L.SetMx(opt_m)
	}

	if opt_v || opt_i {
		fmt.Println(lua.ErdeCopyRight)
	}

	if len(opt_l) > 0 {
		if err := L.DoFile(opt_l); err != nil {
			fmt.Println(err.Error())
		}
	}

	if nargs := flag.NArg(); nargs > 0 {
		script := flag.Arg(0)
		argtb := L.NewTable()
		for i := 1; i < nargs; i++ {
			L.RawSet(argtb, lua.LNumber(i), lua.LString(flag.Arg(i)))
		}
		L.SetGlobal("arg", argtb)
		source, err := readScript(script)
		if err != nil {
			fmt.Println(err.Error())
			return 1
		}
		isErde := opt_lang == "erde" || opt_lang == "" && !strings.HasSuffix(script, ".lua")
		if opt_c {
			chunk, err := parseSource(source, script, isErde, erdeOpts)
			if err != nil {
				fmt.Println(err.Error())
				return 1
			}
			fmt.Print(luagen.Generate(chunk))
			return 0
		}
		if opt_dt || opt_dc {
			chunk, err2 := parseSource(source, script, isErde, erdeOpts)
			if err2 != nil {
				fmt.Println(err2.Error())
				return 1
			}
			if opt_dt {
				fmt.Println(parse.Dump(chunk))
			}
			if opt_dc {
				proto, err3 := lua.Compile(chunk, script)
				if err3 != nil {
					fmt.Println(err3.Error())
					return 1
				}
				fmt.Println(proto.String())
			}
		}
		if err := doSource(L, source, script, isErde); err != nil {
			fmt.Println(err.Error())
			status = 1
		}
	}

	if len(opt_e) > 0 {
		if err := doSource(L, []byte(opt_e), "<string>", opt_lang != "lua"); err != nil {
			fmt.Println(err.Error())
			status = 1
		}
	}

	if opt_i {
		doREPL(L, opt_lang != "lua")
	}
	return status
}

// readScript reads a script ('-' for stdin). Works for pipes / process
// substitution (ex. `erde <(cat script.erde)`), which have no file extension.
func readScript(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func parseSource(source []byte, name string, isErde bool, erdeOpts parse.ErdeOptions) ([]ast.Stmt, error) {
	if isErde {
		return parse.ParseErdeWithOptions(bytes.NewReader(source), name, erdeOpts)
	}
	if bytes.HasPrefix(source, []byte("#")) {
		// Skip the shebang line, but keep the newline so line numbers match.
		if i := bytes.IndexByte(source, '\n'); i >= 0 {
			source = source[i:]
		} else {
			source = nil
		}
	}
	return parse.Parse(bytes.NewReader(source), name)
}

func doSource(L *lua.LState, source []byte, name string, isErde bool) error {
	chunk, err := parseSource(source, name, isErde, L.Options.Erde)
	if err != nil {
		return err
	}
	proto, err := lua.Compile(chunk, name)
	if err != nil {
		return err
	}
	L.Push(L.NewFunctionFromProto(proto))
	return L.PCall(0, lua.MultRet, nil)
}

// do read/eval/print/loop
func doREPL(L *lua.LState, isErde bool) {
	rl, err := readline.New("> ")
	if err != nil {
		panic(err)
	}
	defer rl.Close()
	for {
		fn, err := loadline(rl, L, isErde)
		if err == io.EOF || err == readline.ErrInterrupt {
			return
		} else if err != nil {
			fmt.Println(err)
			continue
		}

		top := L.GetTop()
		L.Push(fn)
		if err := L.PCall(0, lua.MultRet, nil); err != nil {
			fmt.Println(err)
			continue
		}

		// print returned values (ex. from `1 + 1`)
		if L.GetTop() > top {
			results := make([]string, 0, L.GetTop()-top)
			for i := top + 1; i <= L.GetTop(); i++ {
				results = append(results, L.ToStringMeta(L.Get(i)).String())
			}
			fmt.Println(strings.Join(results, "\t"))
			L.SetTop(top)
		}
	}
}

func loadString(L *lua.LState, source string, isErde bool) (*lua.LFunction, error) {
	if isErde {
		return L.LoadErdeString(source)
	}
	return L.LoadString(source)
}

func incomplete(err error) bool {
	if lerr, ok := err.(*lua.ApiError); ok {
		if perr, ok := lerr.Cause.(*parse.Error); ok {
			return perr.Pos.Line == parse.EOF
		}
	}
	return false
}

// loadline reads a chunk, prompting for more lines while it is incomplete.
// Expressions are evaluated as `return <expression>` so their values print.
func loadline(rl *readline.Instance, L *lua.LState, isErde bool) (*lua.LFunction, error) {
	rl.SetPrompt("> ")
	chunk, err := rl.Readline()
	if err != nil {
		return nil, err
	}

	for {
		if fn, err := loadString(L, "return "+chunk, isErde); err == nil {
			return fn, nil
		}

		fn, err := loadString(L, chunk, isErde)
		if !incomplete(err) {
			return fn, err
		}

		rl.SetPrompt(">> ")
		line, err := rl.Readline()
		if err != nil {
			return nil, err
		}
		chunk += "\n" + line
	}
}
