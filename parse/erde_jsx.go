package parse

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/taybart/go-erde/ast"
)

// JSX experiment (see ErdeOptions.JSX). Elements are lowered to calls of the
// factory function (`h` by default), same as JSX's classic runtime:
//
//	<div class="card" id={id}>hi {name}</div>
//	h('div', { class = 'card', id = id }, 'hi ', name)
//
//	<Card {...props} title="x"/>   h(Card, <props merged left to right>)
//	<ui.Button/>                   h(ui.Button, nil)
//	<>a {b}</>                     h(nil, nil, 'a ', b)
//
// `h` is whatever `h` is in scope. When there isn't one, it's imported from
// the runtime library (`require('erde.jsx').h`), so components don't need any
// imports (like JSX's automatic runtime).
//
// Lowercase tags (and tags w/ `-` or `:`) are strings, other tags are
// expressions. Attribute strings are erde strings (so `class="btn {kind}"`
// interpolates), an attribute w/o a value is `true`, and text is cleaned up
// like JSX (lines are trimmed, whitespace only lines are dropped, and lines
// are joined w/ a single space).
//
// JSX is lexed as part of the token stream: a `<` followed by a name or `>`
// starts an element wherever an expression may start (ie. not after a token
// that ends a value, so `a < b` is still a comparison).

/* Lexer {{{ */

func isJsxNameHead(c byte) bool { return isErdeWordHead(c) || c == '@' || c == ':' }

func isJsxNameBody(c byte) bool {
	return isErdeWordBody(c) || c == '-' || c == '.' || c == ':'
}

// jsxStart returns whether the `<` at the current position starts an element.
func (lx *erdeLexer) jsxStart() bool {
	if c := lx.peek(1); !(isErdeWordHead(c) || c == '>') {
		return false
	}
	if len(lx.tokens) == 0 {
		return true
	}

	// Elements can't follow a token that ends a value.
	prev := lx.tokens[len(lx.tokens)-1]
	switch prev.Type {
	case erdeWord:
		return erdeKeywords[prev.Value]
	case erdeNumber, erdeSingleQuote, erdeDoubleQuote, erdeBlockString:
		return false
	case erdeSymbol:
		switch prev.Value {
		case ")", "]", "}", "...":
			return false
		}
	case erdeJsx:
		return prev.Value != ">" && prev.Value != "/>"
	}
	return true
}

// jsxSpace skips whitespace and comments inside of a tag.
func (lx *erdeLexer) jsxSpace() {
	for !lx.eof() {
		switch c := lx.peek(0); {
		case c == '\n':
			lx.line++
			lx.pos++
		case c == ' ' || c == '\t' || c == '\r':
			lx.pos++
		case c == '-' && lx.peek(1) == '-':
			lx.comment()
		default:
			return
		}
	}
}

func (lx *erdeLexer) jsxName() {
	if !isJsxNameHead(lx.peek(0)) {
		lx.jsxUnexpected("name")
	}
	start := lx.pos
	lx.pos++
	for !lx.eof() && isJsxNameBody(lx.peek(0)) {
		lx.pos++
	}
	lx.emit(erdeJsxName, lx.text[start:lx.pos], lx.line)
}

func (lx *erdeLexer) jsxUnexpected(expected string) {
	if lx.eof() {
		lx.throwEOF("unterminated jsx element")
	}
	r, _ := utf8.DecodeRuneInString(lx.text[lx.pos:])
	lx.throw(lx.line, "unexpected '%c' in jsx element (expected %s)", r, expected)
}

// jsxElement lexes an element, starting at its `<`.
func (lx *erdeLexer) jsxElement() {
	lx.emit(erdeJsx, "<", lx.line)
	lx.pos++

	if lx.peek(0) == '>' { // fragment
		lx.emit(erdeJsx, ">", lx.line)
		lx.pos++
		lx.jsxChildren()
		return
	}

	lx.jsxName()

	for {
		lx.jsxSpace()

		switch c := lx.peek(0); {
		case c == '/' && lx.peek(1) == '>':
			lx.emit(erdeJsx, "/>", lx.line)
			lx.pos += 2
			return
		case c == '>':
			lx.emit(erdeJsx, ">", lx.line)
			lx.pos++
			lx.jsxChildren()
			return
		case c == '{': // spread
			lx.jsxExpression()
		case isJsxNameHead(c):
			lx.jsxName()
			lx.jsxSpace()
			if lx.peek(0) != '=' {
				continue
			}
			lx.emit(erdeJsx, "=", lx.line)
			lx.pos++
			lx.jsxSpace()
			switch lx.peek(0) {
			case '"':
				lx.doubleQuoteString()
			case '\'':
				lx.singleQuoteString()
			case '{':
				lx.jsxExpression()
			default:
				lx.jsxUnexpected("attribute value")
			}
		default:
			lx.jsxUnexpected("attribute, '>', or '/>'")
		}
	}
}

// jsxChildren lexes an element's children, up to and including its closing
// tag.
func (lx *erdeLexer) jsxChildren() {
	start, startLine := lx.pos, lx.line

	flush := func() {
		if text := jsxText(lx.text[start:lx.pos]); text != "" {
			lx.emit(erdeJsxText, text, startLine)
		}
	}

	for {
		switch c := lx.peek(0); {
		case lx.eof():
			lx.throwEOF("unterminated jsx element")
		case c == '<' && lx.peek(1) == '/':
			flush()
			lx.emit(erdeJsx, "</", lx.line)
			lx.pos += 2
			lx.jsxSpace()
			if lx.peek(0) != '>' {
				lx.jsxName()
				lx.jsxSpace()
			}
			if lx.peek(0) != '>' {
				lx.jsxUnexpected("'>'")
			}
			lx.emit(erdeJsx, ">", lx.line)
			lx.pos++
			return
		case c == '<':
			flush()
			lx.jsxElement()
			start, startLine = lx.pos, lx.line
		case c == '{':
			flush()
			lx.jsxExpression()
			start, startLine = lx.pos, lx.line
		default:
			if c == '\n' {
				lx.line++
			}
			lx.pos++
		}
	}
}

// jsxExpression lexes a `{...}` expression (or spread) inside of an element.
func (lx *erdeLexer) jsxExpression() {
	lx.emit(erdeJsx, "{", lx.line)
	lx.pos++

	braceDepth := 0
	for lx.peek(0) != '}' || braceDepth > 0 {
		switch {
		case lx.eof():
			lx.throwEOF("unterminated jsx expression")
		case lx.peek(0) == '{':
			braceDepth++
			lx.emit(erdeSymbol, "{", lx.line)
			lx.pos++
		case lx.peek(0) == '}':
			braceDepth--
			lx.emit(erdeSymbol, "}", lx.line)
			lx.pos++
		default:
			lx.token()
		}
	}

	lx.emit(erdeJsx, "}", lx.line)
	lx.pos++
}

// jsxText cleans up the whitespace in text children the same way as JSX
// (Babel's cleanJSXElementLiteralChild) and decodes HTML entities.
func jsxText(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")

	lastNonEmpty := 0
	for i, line := range lines {
		if strings.Trim(line, " \t") != "" {
			lastNonEmpty = i
		}
	}

	var b strings.Builder
	for i, line := range lines {
		line = strings.ReplaceAll(line, "\t", " ")
		if i > 0 {
			line = strings.TrimLeft(line, " ")
		}
		if i < len(lines)-1 {
			line = strings.TrimRight(line, " ")
		}
		if line != "" {
			b.WriteString(line)
			if i != lastNonEmpty {
				b.WriteByte(' ')
			}
		}
	}

	return jsxDecodeEntities(b.String())
}

var jsxEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": " ",
	"copy": "©", "reg": "®", "trade": "™", "hellip": "…", "mdash": "—",
	"ndash": "–", "lsquo": "‘", "rsquo": "’", "ldquo": "“", "rdquo": "”",
	"bull": "•", "middot": "·", "times": "×", "larr": "←", "rarr": "→",
	"uarr": "↑", "darr": "↓", "lbrace": "{", "rbrace": "}",
}

// jsxDecodeEntities decodes named (see jsxEntities) and numeric HTML entities.
// Anything else is left as is.
func jsxDecodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}

	var b strings.Builder
	for {
		amp := strings.IndexByte(s, '&')
		if amp < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:amp])
		s = s[amp:]

		semi := strings.IndexByte(s, ';')
		if semi > 1 && semi < 12 {
			if decoded, ok := jsxDecodeEntity(s[1:semi]); ok {
				b.WriteString(decoded)
				s = s[semi+1:]
				continue
			}
		}
		b.WriteByte('&')
		s = s[1:]
	}
}

func jsxDecodeEntity(name string) (string, bool) {
	if !strings.HasPrefix(name, "#") {
		decoded, ok := jsxEntities[name]
		return decoded, ok
	}

	var code uint64
	var err error
	if strings.HasPrefix(name, "#x") || strings.HasPrefix(name, "#X") {
		code, err = strconv.ParseUint(name[2:], 16, 32)
	} else {
		code, err = strconv.ParseUint(name[1:], 10, 32)
	}
	if err != nil || !utf8.ValidRune(rune(code)) {
		return "", false
	}
	return string(rune(code)), true
}

/* }}} */

/* Parser {{{ */

func (p *erdeParser) isJsx(value string) bool {
	return p.cur.Type == erdeJsx && p.cur.Value == value
}

func (p *erdeParser) expectJsx(value string) {
	if p.cur.Type == erdeEOF {
		p.throwEOF("unexpected eof (expected %s)", value)
	}
	if !p.isJsx(value) {
		p.throw(p.cur.Line, "expected '%s' got '%s'", value, p.cur.Value)
	}
	p.consume()
}

// jsxFactory returns the function elements are lowered to: JSXFactory if set,
// otherwise `h` if it's declared, otherwise `require('erde.jsx').h` (the
// "automatic runtime", imported once at the top of the chunk, see
// jsxRuntimeImport).
func (p *erdeParser) jsxFactory(line int) ast.Expr {
	name := p.opts.JSXFactory
	if name == "" {
		if p.lookup("h") != "" {
			return p.resolve("h", line)
		}
		if p.jsxRuntime == "" {
			p.jsxRuntime = p.newTmpName()
		}
		return erdeIdent(p.jsxRuntime, line)
	}
	parts := strings.Split(name, ".")
	expr := p.resolve(parts[0], line)
	for _, key := range parts[1:] {
		expr = erdeAttr(expr, erdeString(key, line), line)
	}
	return expr
}

// JSXRuntimeModule is the module the automatic jsx factory is imported from.
const JSXRuntimeModule = "erde.jsx"

// jsxRuntimeImport is `local <jsxRuntime> = require('erde.jsx').h`.
func (p *erdeParser) jsxRuntimeImport() ast.Stmt {
	runtime := erdeCall(erdeIdent("require", 1), []ast.Expr{erdeString(JSXRuntimeModule, 1)}, 1)
	return erdeLocal([]string{p.jsxRuntime}, []ast.Expr{erdeAttr(runtime, erdeString("h", 1), 1)}, 1)
}

// jsxTag returns the expression for a tag name: a string for HTML elements
// (lowercase, or containing `-` / `:`), otherwise a (dotted) name.
func (p *erdeParser) jsxTag(name string, line int) ast.Expr {
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		expr := p.resolve(parts[0], line)
		for _, key := range parts[1:] {
			expr = erdeAttr(expr, erdeString(key, line), line)
		}
		return expr
	}
	if c := name[0]; 'a' <= c && c <= 'z' || strings.ContainsAny(name, "-:") {
		return erdeString(name, line)
	}
	return p.resolve(name, line)
}

func (p *erdeParser) jsxElement() ast.Expr {
	line := p.cur.Line
	p.expectJsx("<")

	if p.isJsx(">") { // fragment
		p.consume()
		children := p.jsxChildren("", line)
		args := append([]ast.Expr{erdeLine(&ast.NilExpr{}, line), erdeLine(&ast.NilExpr{}, line)}, children...)
		return erdeCall(p.jsxFactory(line), args, line)
	}

	name := p.consume()
	args := []ast.Expr{p.jsxTag(name, line), p.jsxAttributes(line)}

	if p.isJsx("/>") {
		p.consume()
	} else {
		p.expectJsx(">")
		args = append(args, p.jsxChildren(name, line)...)
	}

	return erdeCall(p.jsxFactory(line), args, line)
}

// jsxAttributes parses an opening tag's attributes into the props
// expression: nil when there are none, a table constructor, or (when there
// are spreads) a function call that merges them in order.
func (p *erdeParser) jsxAttributes(line int) ast.Expr {
	type attribute struct {
		field  *ast.Field
		spread ast.Expr
	}
	attrs := []attribute{}
	hasSpread := false

	for !p.isJsx(">") && !p.isJsx("/>") {
		attrLine := p.cur.Line

		if p.isJsx("{") {
			p.consume()
			p.expect("...")
			spread := p.expression(1)
			p.expectJsx("}")
			attrs = append(attrs, attribute{spread: spread})
			hasSpread = true
			continue
		}

		if p.cur.Type != erdeJsxName {
			p.throw(attrLine, "unexpected token '%s' in jsx element", p.cur.Value)
		}
		key := erdeString(p.consume(), attrLine)

		var value ast.Expr = erdeLine(&ast.TrueExpr{}, attrLine)
		if p.isJsx("=") {
			p.consume()
			switch {
			case p.cur.Type == erdeSingleQuote || p.cur.Type == erdeDoubleQuote:
				value = p.stringExpr()
			case p.isJsx("{"):
				p.consume()
				if p.isJsx("}") {
					p.throw(attrLine, "jsx attribute value can't be empty")
				}
				value = p.jsxValue(p.expression(1))
				p.expectJsx("}")
			}
		}

		attrs = append(attrs, attribute{field: &ast.Field{Key: key, Value: value}})
	}

	if len(attrs) == 0 {
		return erdeLine(&ast.NilExpr{}, line)
	}

	if !hasSpread {
		fields := make([]*ast.Field, len(attrs))
		for i, attr := range attrs {
			fields[i] = attr.field
		}
		return erdeLine(&ast.TableExpr{Fields: fields}, line)
	}

	// (function() local t = {} t.a = 1 for k, v in pairs(spread or {}) { t[k] = v } return t end)()
	//
	// Spreading nil is a no-op (same as JSX).
	t, k, v := p.newTmpName(), p.newTmpName(), p.newTmpName()
	stmts := []ast.Stmt{erdeLocal([]string{t}, []ast.Expr{erdeLine(&ast.TableExpr{}, line)}, line)}
	for _, attr := range attrs {
		if attr.spread == nil {
			stmts = append(stmts, erdeAssign(erdeAttr(erdeIdent(t, line), attr.field.Key, line), attr.field.Value, line))
			continue
		}
		spread := erdeLine(&ast.LogicalOpExpr{Operator: "or", Lhs: attr.spread, Rhs: erdeLine(&ast.TableExpr{}, line)}, line)
		stmts = append(stmts, erdeLine(&ast.GenericForStmt{
			Names: []string{k, v},
			Exprs: []ast.Expr{erdeCall(erdeIdent("pairs", line), []ast.Expr{spread}, line)},
			Stmts: []ast.Stmt{erdeAssign(erdeAttr(erdeIdent(t, line), erdeIdent(k, line), line), erdeIdent(v, line), line)},
		}, line))
	}
	stmts = append(stmts, erdeLine(&ast.ReturnStmt{Exprs: []ast.Expr{erdeIdent(t, line)}}, line))

	fn := erdeLine(&ast.FunctionExpr{ParList: &ast.ParList{}, Stmts: stmts}, line)
	return erdeCall(fn, []ast.Expr{}, line)
}

// jsxChildren parses an element's children, up to and including the closing
// tag (which must match `name`).
func (p *erdeParser) jsxChildren(name string, line int) []ast.Expr {
	children := []ast.Expr{}

	for {
		switch {
		case p.cur.Type == erdeJsxText:
			children = append(children, erdeString(p.cur.Value, p.cur.Line))
			p.consume()
		case p.isJsx("<"):
			children = append(children, p.jsxElement())
		case p.isJsx("{"):
			p.consume()
			if !p.isJsx("}") { // `{}` / `{--[[ comment ]]}` are ignored
				children = append(children, p.jsxValue(p.expression(1)))
			}
			p.expectJsx("}")
		case p.isJsx("</"):
			closeLine := p.cur.Line
			p.consume()
			closing := ""
			if p.cur.Type == erdeJsxName {
				closing = p.consume()
			}
			if closing != name {
				p.throw(closeLine, "expected closing tag '</%s>' (for line %d) got '</%s>'", name, line, closing)
			}
			p.expectJsx(">")
			return children
		case p.cur.Type == erdeEOF:
			p.throwEOF("unterminated jsx element '<%s>'", name)
		default:
			p.throw(p.cur.Line, "unexpected token '%s' in jsx element", p.cur.Value)
		}
	}
}

// jsxValue truncates multiple values to one (same as parentheses).
func (p *erdeParser) jsxValue(expr ast.Expr) ast.Expr {
	switch ex := expr.(type) {
	case *ast.FuncCallExpr:
		ex.AdjustRet = true
	case *ast.Comma3Expr:
		ex.AdjustRet = true
	}
	return expr
}

/* }}} */
