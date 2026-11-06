package lua

import (
	"strings"
	"testing"

	"github.com/taybart/go-erde/format"
	"github.com/taybart/go-erde/luagen"
	"github.com/taybart/go-erde/parse"
)

var erdeJSXOptions = parse.ErdeOptions{JSX: true}

const erdeJSXPrelude = "local { h, render, map, raw, document } = require('erde.jsx')\n"

var erdeJSXCases = []struct {
	source string
	want   string
}{
	// elements, attributes, text
	{`return render(<div/>)`, `<div></div>`},
	{`return render(<br/>)`, `<br>`},
	{`return render(<p>hi</p>)`, `<p>hi</p>`},
	{`return render(<a href="/x" id='y'>link</a>)`, `<a href="/x" id="y">link</a>`},
	{`local n = 3; return render(<a data-n={n} hx-post="/p" x-on:click="go()" @click="go()">x</a>)`, `<a @click="go()" data-n="3" hx-post="/p" x-on:click="go()">x</a>`},
	{`local kind = 'big'; return render(<b class="btn btn-{kind}"/>)`, `<b class="btn btn-big"></b>`},
	{`return render(<input disabled checked={false} value={nil}/>)`, `<input disabled>`},
	{`return render(<b style={{ color = 'red', ['font-size'] = '2em' }}/>)`, `<b style="color: red; font-size: 2em"></b>`},
	{`local on = true; return render(<b class={{ 'a', 'b', on = on, off = false }}/>)`, `<b class="a b on"></b>`},
	{`return render(<b onclick={() -> 1}/>)`, `<b></b>`},
	// text whitespace (same as JSX)
	{"return render(<p>\n  hello\n  world\n</p>)", `<p>hello world</p>`},
	{"return render(<p>\n  <b>a</b>\n  <b>b</b>\n</p>)", `<p><b>a</b><b>b</b></p>`},
	{"return render(<p>  a  {1}  b  </p>)", `<p>  a  1  b  </p>`},
	{"return render(<p>a&nbsp;&amp;&lt;&#65;&#x42;&bogus; & b</p>)", "<p>a &amp;&lt;AB&amp;bogus; &amp; b</p>"},
	// expressions
	{`local name = 'x'; return render(<p>hi {name}!</p>)`, `<p>hi x!</p>`},
	{`return render(<p>{1 + 2}{'a' .. 'b'}{2 > 1 && 'y'}</p>)`, `<p>3aby</p>`},
	{`return render(<p>{nil}{false}{true}{}{--[[ comment ]]}</p>)`, `<p></p>`},
	{`local f = () -> (1, 2); return render(<p>{f()}</p>)`, `<p>1</p>`},
	{`local t = { x = 'a' }; return render(<p>{t.x}{t['x']}{({ 'b' })[1]}</p>)`, `<p>aab</p>`},
	{`return render(<ul>{map({ 'a', 'b' }, (x, i) -> <li>{i}{x}</li>)}</ul>)`, `<ul><li>1a</li><li>2b</li></ul>`},
	{`return render(<p>{<b>{<i>deep</i>}</b>}</p>)`, `<p><b><i>deep</i></b></p>`},
	{`local ok = false; return render(<p>{ok && <b/> || <i/>}</p>)`, `<p><i></i></p>`},
	{`return render(<p>{"str {1 + 1}"}</p>)`, `<p>str 2</p>`},
	// escaping
	{`local s = '<script>"&\'</script>'; return render(<p title={s}>{s}</p>)`, `<p title="&lt;script&gt;&quot;&amp;&#39;&lt;/script&gt;">&lt;script&gt;&quot;&amp;&#39;&lt;/script&gt;</p>`},
	{`return render(<p>{raw('<b>trusted</b>')}</p>)`, `<p><b>trusted</b></p>`},
	{`return render(<script>{'if (a < b) {}'}</script>)`, `<script>if (a < b) {}</script>`},
	// fragments
	{`return render(<>a<b/>c</>)`, `a<b></b>c`},
	{`return render(<p><>{1}{2}</></p>)`, `<p>12</p>`},
	// components
	{`local Hi = props -> <p>hi {props.name}</p>; return render(<Hi name="x"/>)`, `<p>hi x</p>`},
	{`local Box = props -> <div class="box">{props.children}</div>; return render(<Box><b/>text</Box>)`, `<div class="box"><b></b>text</div>`},
	{`local ui = { Btn = p -> <button>{p.label}</button> }; return render(<ui.Btn label="ok"/>)`, `<button>ok</button>`},
	{`local Nil = () -> nil; return render(<p><Nil/></p>)`, `<p></p>`},
	// spreads (merged left to right)
	{`local p = { a = 1, b = 2 }; return render(<i b="x" {...p} c={3}/>)`, `<i a="1" b="2" c="3"></i>`},
	{`local p = { a = 1 }; return render(<i {...p} a="z"/>)`, `<i a="z"></i>`},
	{`local p; return render(<i {...p} {...nil} a="z"/>)`, `<i a="z"></i>`},
	{`local p = { name = 'x' }; local Hi = props -> <p>{props.name}</p>; return render(<Hi {...p}/>)`, `<p>x</p>`},
	// positions where `<` is still an operator
	{`local a, b = 1, 2; return a < b`, `true`},
	{`local a, b = 1, 2; return a<b`, `true`},
	{`local t = { 1 }; return t[1]<2`, `true`},
	{`local f = () -> 1; return f()<2`, `true`},
	{`return 1<2`, `true`},
	{`return 'a'<'b'`, `true`},
	{`local x = 1; return (x)<2 && x <= 1 && 1 << 1 == 2`, `true`},
	// where elements may start
	{`local f = () -> <p/>; return render(f())`, `<p></p>`},
	{`local function f() { return <p/> } return render(f())`, `<p></p>`},
	{`local t = { <a/>, k = <b/> }; return render(t[1]) .. render(t.k)`, `<a></a><b></b>`},
	{`local s = render(<a/>) .. render(<b/>); return s`, `<a></a><b></b>`},
	{`return render((<p/>))`, `<p></p>`},
	{`local x = 1; local e = x > 0 && <p/> || <i/>; return render(e)`, `<p></p>`},
	// document, tostring
	{`return document(<html/>)`, `<!DOCTYPE html><html></html>`},
	{`return tostring(<p>x</p>)`, `<p>x</p>`},
	// multiline, nested
	{`local page = <html>
  <head>
    <title>t</title>
  </head>
  <body
    class="x" -- a comment in a tag
    id="y"
  >
    text that ends w/ a quote's apostrophe
  </body>
</html>
return render(page)`, `<html><head><title>t</title></head><body class="x" id="y">text that ends w/ a quote&#39;s apostrophe</body></html>`},
}

func TestErdeJSX(t *testing.T) {
	for _, c := range erdeJSXCases {
		source := erdeJSXPrelude + c.source
		L := NewState(Options{Erde: erdeJSXOptions})
		if err := L.DoErdeString(source); err != nil {
			t.Errorf("%s\n%v", c.source, err)
		} else if got := L.Get(-1).String(); got != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", c.source, got, c.want)
		}
		L.Close()
	}
}

func TestLuagenErdeJSX(t *testing.T) {
	for _, c := range erdeJSXCases {
		source := erdeJSXPrelude + c.source
		chunk, err := parse.ParseErdeWithOptions(strings.NewReader(source), "<string>", erdeJSXOptions)
		if err != nil {
			t.Fatalf("%s\n%v", c.source, err)
		}
		generated := luagen.Generate(chunk)

		L := NewState()
		if err := L.DoString(generated); err != nil {
			t.Errorf("%s\n%s\n%v", c.source, generated, err)
		} else if got := L.Get(-1).String(); got != c.want {
			t.Errorf("%s\n%s\n  got  %s\n  want %s", c.source, generated, got, c.want)
		}
		L.Close()
	}
}

// Without an `h` in scope, the runtime's `h` is imported automatically.
var erdeJSXAutoCases = []struct {
	source string
	want   string
}{
	{`return tostring(<p class="x">hi</p>)`, `<p class="x">hi</p>`},
	{`local Card = props -> <div>{props.children}</div>
return require('erde.jsx').render(<Card><b/></Card>)`, `<div><b></b></div>`},
	// a declared `h` (local, or a parameter) wins
	{`local h = (tag) -> 'mine ' .. tag; return <p/>`, `mine p`},
	{`local f = h -> <p/>; return f(t -> 'param ' .. t)`, `param p`},
	// the import doesn't clash w/ user names
	{`local require = 1; local erde = 2; return tostring(<p/>)`, `<p></p>`},
	// works w/ `module` declarations
	{`module x = tostring(<p/>)`, ``},
}

func TestErdeJSXAutomaticRuntime(t *testing.T) {
	for _, c := range erdeJSXAutoCases {
		chunk, err := parse.ParseErdeWithOptions(strings.NewReader(c.source), "<string>", erdeJSXOptions)
		if err != nil {
			t.Fatalf("%s\n%v", c.source, err)
		}
		generated := luagen.Generate(chunk)

		for _, run := range []struct {
			name string
			fn   func(L *LState) error
		}{
			{"native", func(L *LState) error { return L.DoErdeString(c.source) }},
			{"luagen", func(L *LState) error { return L.DoString(generated) }},
		} {
			L := NewState(Options{Erde: erdeJSXOptions})
			if err := run.fn(L); err != nil {
				t.Errorf("%s (%s)\n%s\n%v", c.source, run.name, generated, err)
			} else if got := L.Get(-1); c.want != "" && got.String() != c.want {
				t.Errorf("%s (%s)\n%s\n  got  %s\n  want %s", c.source, run.name, generated, got, c.want)
			}
			L.Close()
		}
	}
}

// Elements are lowered to calls of the factory, which can be renamed.
func TestErdeJSXFactory(t *testing.T) {
	source := `local react = { createElement = (tag, props, ...) -> ({ tag = tag, n = select('#', ...) }) }
local e = <div a="1">x{1}<b/></div>
return e.tag .. e.n`
	L := NewState(Options{Erde: parse.ErdeOptions{JSX: true, JSXFactory: "react.createElement"}})
	defer L.Close()
	if err := L.DoErdeString(source); err != nil {
		t.Fatal(err)
	}
	if got := L.Get(-1).String(); got != "div3" {
		t.Errorf("got %s, want div3", got)
	}
}

func TestErdeJSXSyntaxErrors(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{`local x = <p>`, "unterminated jsx element"},
		{`local x = <p>hi</b>`, "expected closing tag '</p>' (for line 2) got '</b>'"},
		{`local x = <p></>`, "expected closing tag '</p>'"},
		{`local x = <>a</p>`, "expected closing tag '</>'"},
		{`local x = <p a=1/>`, "expected attribute value"},
		{`local x = <p a={}/>`, "can't be empty"},
		{`local x = <p {a}/>`, "expected '...'"},
		{`local x = <p !/>`, "expected attribute, '>', or '/>'"},
		{`local x = <p>{1</p>`, "unterminated jsx expression"},
		{`local x = <p>{-- line comments eat the brace}</p>`, "unterminated jsx expression"},
		{`local x = <br>child</br>; require('erde.jsx').render(x)`, "can't have children"},
	}

	for _, c := range cases {
		L := NewState(Options{Erde: erdeJSXOptions})
		err := L.DoErdeString(erdeJSXPrelude + c.source)
		L.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s\n  got %v, want error containing %q", c.source, err, c.want)
		}
	}
}

// Without the experiment, `<` is always an operator.
func TestErdeJSXDisabled(t *testing.T) {
	L := NewState()
	defer L.Close()
	if _, err := L.LoadErdeString(`local x = <p/>`); err == nil {
		t.Error("jsx parsed w/o the jsx experiment")
	}
}

func TestFormatErdeJSX(t *testing.T) {
	in := "local x=<div   class='a'>\n  hi {name}\n    <b/>\n</div>\nlocal y=a<b"
	want := "local x = <div   class='a'>\n  hi {name}\n    <b/>\n</div>\nlocal y = a < b\n"
	got, err := format.Format([]byte(in), format.Options{Erde: erdeJSXOptions})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
