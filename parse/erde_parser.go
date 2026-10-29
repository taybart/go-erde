package parse

import (
	"fmt"
	"io"
	"strconv"

	"github.com/taybart/go-erde/ast"
)

// Erde (https://erde-lang.github.io/) parser. Erde is a Lua dialect, so instead
// of compiling to Lua source (like the reference compiler, erde/compile.erde)
// we lower it directly into gopher-lua's (Lua 5.1) AST. Parsing mirrors the
// reference compiler, so the two should be read side by side.

var erdeKeywords = map[string]bool{
	"break": true, "continue": true, "do": true, "else": true, "elseif": true,
	"for": true, "function": true, "global": true, "if": true, "in": true,
	"local": true, "module": true, "repeat": true, "return": true,
	"until": true, "while": true,
}

var erdeTerminals = map[string]bool{
	"true": true, "false": true, "nil": true, "...": true,
}

type erdeBinop struct {
	prec       int
	rightAssoc bool
}

var erdeBinops = map[string]erdeBinop{
	"||": {3, false},
	"&&": {4, false},
	"==": {5, false}, "!=": {5, false}, "<=": {5, false}, ">=": {5, false}, "<": {5, false}, ">": {5, false},
	"|":  {6, false},
	"~":  {7, false},
	"&":  {8, false},
	"<<": {9, false}, ">>": {9, false},
	"..": {10, false},
	"+":  {11, false}, "-": {11, false},
	"*": {12, false}, "/": {12, false}, "//": {12, false}, "%": {12, false},
	"^": {14, true},
}

const erdeUnopPrec = 13

var erdeBitlibMethods = map[string]string{
	"|": "bor", "~": "bxor", "&": "band", "<<": "lshift", ">>": "rshift",
}

// Names for the library tables used by lowered operators. These are resolved
// like any other name, same as the reference compiler.
const (
	erdeBitlib     = "bit32"
	erdeModuleName = "_MODULE"
)

const (
	erdeScopeLocal  = "local"
	erdeScopeGlobal = "global"
	erdeScopeModule = "module"
)

type erdeLoop struct {
	continueLabel string
	hasContinue   bool
}

type erdeParser struct {
	source string
	tokens []erdeToken
	index  int
	cur    erdeToken

	blockDepth int
	scopes     []map[string]string
	tmpCounter int

	// Innermost loop, or nil outside of loops (reset at function boundaries).
	loop *erdeLoop

	isVarargs           bool
	isModuleReturnBlock bool
	moduleReturnLine    int
	hasModuleDecls      bool
}

// Result of parsing an index chain (ex. `a.b[c]:d()`).
type erdeChain struct {
	expr       ast.Expr
	chainLen   int
	isCall     bool
	finalBase  ast.Expr // `a.b` in `a.b[c]`
	finalIndex ast.Expr // `c` in `a.b[c]`
}

// Result of parsing a destructure (ex. `{ a, b: c = 1 }`).
type erdeDestructure struct {
	tmpName string
	names   []string
	stmts   []ast.Stmt
}

// ParseErde parses Erde source into a Lua chunk, which can be passed to
// `lua.Compile` like any chunk returned by `Parse`.
func ParseErde(reader io.Reader, name string) (chunk []ast.Stmt, err error) {
	text, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	tokens, err := erdeTokenize(string(text), name)
	if err != nil {
		return nil, err
	}

	p := &erdeParser{
		source:              name,
		tokens:              tokens,
		cur:                 tokens[0],
		blockDepth:          1,
		scopes:              []map[string]string{{}},
		isVarargs:           true,
		isModuleReturnBlock: true,
	}

	defer func() {
		if e := recover(); e != nil {
			if perr, ok := e.(*Error); ok {
				chunk, err = nil, perr
				return
			}
			panic(e)
		}
	}()

	return p.module(), nil
}

/* Parse helpers {{{ */

func (p *erdeParser) throw(line int, format string, args ...interface{}) {
	panic(&Error{
		Pos:     ast.Position{Source: p.source, Line: line},
		Message: fmt.Sprintf(format, args...),
		Token:   p.cur.Value,
	})
}

// throwEOF throws an error for input that ends early. These are reported at
// line `EOF` (same as the Lua parser), so callers can tell incomplete input
// apart from invalid input (ex. to continue reading lines in a REPL).
func (p *erdeParser) throwEOF(format string, args ...interface{}) {
	panic(&Error{
		Pos:     ast.Position{Source: p.source, Line: EOF},
		Message: fmt.Sprintf(format, args...),
	})
}

func (p *erdeParser) consume() string {
	value := p.cur.Value
	p.index++
	p.cur = p.tokens[p.index]
	return value
}

func (p *erdeParser) prevLine() int { return p.tokens[p.index-1].Line }

func isErdeSyntax(token erdeToken, value string) bool {
	switch token.Type {
	case erdeSymbol, erdeWord, erdeInterpolation:
		return token.Value == value
	}
	return false
}

func (p *erdeParser) is(value string) bool { return isErdeSyntax(p.cur, value) }

func (p *erdeParser) branch(value string) bool {
	if p.is(value) {
		p.consume()
		return true
	}
	return false
}

func (p *erdeParser) expect(value string) {
	if p.cur.Type == erdeEOF {
		p.throwEOF("unexpected eof (expected %s)", value)
	}
	if !p.is(value) {
		p.throw(p.cur.Line, "expected '%s' got '%s'", value, p.cur.Value)
	}
	p.consume()
}

func (p *erdeParser) list(callback func()) {
	for {
		callback()
		if !p.branch(",") {
			return
		}
	}
}

// surroundList parses `open {item ,} close`, allowing a trailing comma.
func (p *erdeParser) surroundList(open, close string, allowEmpty bool, callback func()) {
	p.expect(open)
	if !p.is(close) || !allowEmpty {
		for {
			callback()
			if !p.branch(",") || p.is(close) {
				break
			}
		}
	}
	p.expect(close)
}

var erdeSurroundEnds = map[string]string{"(": ")", "[": "]", "{": "}"}

// lookPastSurround returns the token (and its index) following the surround
// (parens, brackets, or braces) starting at `start`.
func (p *erdeParser) lookPastSurround(start int) (erdeToken, int) {
	open := p.tokens[start]
	close := erdeSurroundEnds[open.Value]
	depth := 1

	i := start + 1
	for depth > 0 {
		token := p.tokens[i]
		if token.Type == erdeEOF {
			p.throwEOF("unexpected eof, missing '%s'", close)
		}
		if isErdeSyntax(token, open.Value) {
			depth++
		} else if isErdeSyntax(token, close) {
			depth--
		}
		i++
	}

	return p.tokens[i], i
}

func (p *erdeParser) newTmpName() string {
	p.tmpCounter++
	// Not a valid identifier, so it can never clash w/ user names.
	return "(erde " + strconv.Itoa(p.tmpCounter) + ")"
}

/* }}} */

/* Scopes {{{ */

func (p *erdeParser) pushScope() { p.scopes = append(p.scopes, map[string]string{}) }

func (p *erdeParser) popScope() { p.scopes = p.scopes[:len(p.scopes)-1] }

func (p *erdeParser) declare(name, scope string) { p.scopes[len(p.scopes)-1][name] = scope }

func (p *erdeParser) lookup(name string) string {
	for i := len(p.scopes) - 1; i >= 0; i-- {
		if scope, ok := p.scopes[i][name]; ok {
			return scope
		}
	}
	return ""
}

// target returns the expression referring to `name` when declared w/ `scope`.
func (p *erdeParser) target(name, scope string, line int) ast.Expr {
	switch scope {
	case erdeScopeModule:
		return erdeAttr(erdeIdent(erdeModuleName, line), erdeString(name, line), line)
	case erdeScopeGlobal:
		return erdeAttr(erdeIdent("_G", line), erdeString(name, line), line)
	}
	return erdeIdent(name, line)
}

func (p *erdeParser) resolve(name string, line int) ast.Expr {
	return p.target(name, p.lookup(name), line)
}

/* }}} */

/* AST helpers {{{ */

func erdeLine[T ast.PositionHolder](node T, line int) T {
	node.SetLine(line)
	node.SetLastLine(line)
	return node
}

// closed sets the last line of a block statement to the closing brace.
func (p *erdeParser) closed(stmt ast.Stmt) ast.Stmt {
	stmt.SetLastLine(p.prevLine())
	return stmt
}

func erdeIdent(name string, line int) *ast.IdentExpr {
	return erdeLine(&ast.IdentExpr{Value: name}, line)
}

func erdeString(value string, line int) *ast.StringExpr {
	return erdeLine(&ast.StringExpr{Value: value}, line)
}

func erdeAttr(object, key ast.Expr, line int) *ast.AttrGetExpr {
	return erdeLine(&ast.AttrGetExpr{Object: object, Key: key}, line)
}

func erdeCall(fn ast.Expr, args []ast.Expr, line int) *ast.FuncCallExpr {
	return erdeLine(&ast.FuncCallExpr{Func: fn, Args: args}, line)
}

// erdeLibCall calls `lib.method(args...)`, truncated to a single value.
func erdeLibCall(lib, method string, args []ast.Expr, line int) *ast.FuncCallExpr {
	call := erdeCall(erdeAttr(erdeIdent(lib, line), erdeString(method, line), line), args, line)
	call.AdjustRet = true
	return call
}

func erdeAssign(lhs, rhs ast.Expr, line int) *ast.AssignStmt {
	return erdeLine(&ast.AssignStmt{Lhs: []ast.Expr{lhs}, Rhs: []ast.Expr{rhs}}, line)
}

func erdeLocal(names []string, exprs []ast.Expr, line int) *ast.LocalAssignStmt {
	if exprs == nil {
		exprs = []ast.Expr{}
	}
	return erdeLine(&ast.LocalAssignStmt{Names: names, Exprs: exprs}, line)
}

// erdeDefault compiles `if target == nil { target = value }`.
func erdeDefault(target, value ast.Expr, line int) *ast.IfStmt {
	condition := erdeLine(&ast.RelationalOpExpr{Operator: "==", Lhs: target, Rhs: erdeLine(&ast.NilExpr{}, line)}, line)
	return erdeLine(&ast.IfStmt{
		Condition: condition,
		Then:      []ast.Stmt{erdeAssign(target, value, line)},
		Else:      []ast.Stmt{},
	}, line)
}

func (p *erdeParser) binop(op string, line int, lhs, rhs ast.Expr) ast.Expr {
	switch op {
	case "||":
		return erdeLine(&ast.LogicalOpExpr{Operator: "or", Lhs: lhs, Rhs: rhs}, line)
	case "&&":
		return erdeLine(&ast.LogicalOpExpr{Operator: "and", Lhs: lhs, Rhs: rhs}, line)
	case "!=":
		return erdeLine(&ast.RelationalOpExpr{Operator: "~=", Lhs: lhs, Rhs: rhs}, line)
	case "==", "<", "<=", ">", ">=":
		return erdeLine(&ast.RelationalOpExpr{Operator: op, Lhs: lhs, Rhs: rhs}, line)
	case "..":
		return erdeLine(&ast.StringConcatOpExpr{Lhs: lhs, Rhs: rhs}, line)
	case "//":
		quotient := erdeLine(&ast.ArithmeticOpExpr{Operator: "/", Lhs: lhs, Rhs: rhs}, line)
		return erdeLibCall("math", "floor", []ast.Expr{quotient}, line)
	}

	if method, ok := erdeBitlibMethods[op]; ok {
		return erdeLibCall(erdeBitlib, method, []ast.Expr{lhs, rhs}, line)
	}

	return erdeLine(&ast.ArithmeticOpExpr{Operator: op, Lhs: lhs, Rhs: rhs}, line)
}

/* }}} */

/* Variables {{{ */

func (p *erdeParser) name() string {
	switch {
	case p.cur.Type == erdeEOF:
		p.throwEOF("unexpected eof")
	case p.cur.Type != erdeWord:
		p.throw(p.cur.Line, "unexpected token '%s'", p.cur.Value)
	case erdeKeywords[p.cur.Value]:
		p.throw(p.cur.Line, "unexpected keyword '%s'", p.cur.Value)
	case erdeTerminals[p.cur.Value]:
		p.throw(p.cur.Line, "unexpected builtin '%s'", p.cur.Value)
	}
	return p.consume()
}

func (p *erdeParser) arrayDestructure(scope string) *erdeDestructure {
	d := &erdeDestructure{tmpName: p.newTmpName()}
	index := 0

	p.surroundList("[", "]", false, func() {
		index++
		line := p.cur.Line

		name := p.name()
		d.names = append(d.names, name)

		target := p.target(name, scope, line)
		key := erdeLine(&ast.NumberExpr{Value: strconv.Itoa(index)}, line)
		d.stmts = append(d.stmts, erdeAssign(target, erdeAttr(erdeIdent(d.tmpName, line), key, line), line))

		if p.branch("=") {
			d.stmts = append(d.stmts, erdeDefault(target, p.expression(1), line))
		}
	})

	return d
}

func (p *erdeParser) mapDestructure(scope string) *erdeDestructure {
	d := &erdeDestructure{tmpName: p.newTmpName()}

	p.surroundList("{", "}", false, func() {
		line := p.cur.Line

		key := p.name()
		name := key
		if p.branch(":") {
			name = p.name()
		}
		d.names = append(d.names, name)

		target := p.target(name, scope, line)
		d.stmts = append(d.stmts, erdeAssign(target, erdeAttr(erdeIdent(d.tmpName, line), erdeString(key, line), line), line))

		if p.branch("=") {
			d.stmts = append(d.stmts, erdeDefault(target, p.expression(1), line))
		}
	})

	return d
}

// variable parses a name or destructure. Exactly one of the results is set.
func (p *erdeParser) variable(scope string) (string, *erdeDestructure) {
	if p.is("[") {
		return "", p.arrayDestructure(scope)
	} else if p.is("{") {
		return "", p.mapDestructure(scope)
	}
	return p.name(), nil
}

/* }}} */

/* Index chains {{{ */

func (p *erdeParser) indexChain(base ast.Expr) *erdeChain {
	chain := &erdeChain{expr: base}

	for {
		line := p.cur.Line

		switch {
		// Use newlines to infer whether the parentheses belong to a function call
		// or the next statement.
		case p.is("(") && line == p.prevLine():
			chain.isCall = true
			chain.expr = erdeCall(chain.expr, p.arguments(), line)
		case p.is("["):
			p.consume()
			chain.isCall = false
			chain.finalBase, chain.finalIndex = chain.expr, p.expression(1)
			chain.expr = erdeAttr(chain.expr, chain.finalIndex, line)
			p.expect("]")
		case p.is("."):
			p.consume()
			chain.isCall = false
			chain.finalBase, chain.finalIndex = chain.expr, erdeString(p.name(), line)
			chain.expr = erdeAttr(chain.expr, chain.finalIndex, line)
		case p.is(":"):
			p.consume()
			chain.isCall = true
			method := p.name()
			chain.expr = erdeLine(&ast.FuncCallExpr{Receiver: chain.expr, Method: method, Args: p.arguments()}, line)
		default:
			return chain
		}

		chain.chainLen++
	}
}

func (p *erdeParser) arguments() []ast.Expr {
	args := []ast.Expr{}
	p.surroundList("(", ")", true, func() {
		args = append(args, p.expression(1))
	})
	return args
}

/* }}} */

/* Strings {{{ */

func (p *erdeParser) stringExpr() ast.Expr {
	line := p.cur.Line
	delimiter := p.cur.Type
	parts := []ast.Expr{}

	p.consume() // opening quote

	for p.cur.Type != delimiter {
		switch {
		case p.cur.Type == erdeStringContent:
			parts = append(parts, erdeString(p.cur.Value, p.cur.Line))
			p.consume()
		case p.cur.Type == erdeInterpolation:
			interpolationLine := p.cur.Line
			p.consume() // {
			value := p.expression(1)
			p.expect("}")
			parts = append(parts, erdeCall(erdeIdent("tostring", interpolationLine), []ast.Expr{value}, interpolationLine))
		default:
			p.throw(p.cur.Line, "unexpected token '%s'", p.cur.Value)
		}
	}

	p.consume() // closing quote

	if len(parts) == 0 {
		return erdeString("", line)
	}

	// `..` is right associative in Lua
	expr := parts[len(parts)-1]
	for i := len(parts) - 2; i >= 0; i-- {
		expr = erdeLine(&ast.StringConcatOpExpr{Lhs: parts[i], Rhs: expr}, line)
	}
	return expr
}

/* }}} */

/* Tables {{{ */

func (p *erdeParser) tableConstructor() ast.Expr {
	line := p.cur.Line
	fields := []*ast.Field{}

	p.surroundList("{", "}", true, func() {
		next := p.tokens[p.index+1]

		if p.is("[") {
			p.consume()
			key := p.expression(1)
			p.expect("]")
			p.expect("=")
			fields = append(fields, &ast.Field{Key: key, Value: p.expression(1)})
		} else if next.Type == erdeSymbol && next.Value == "=" {
			keyLine := p.cur.Line
			key := p.name()
			p.consume() // =
			fields = append(fields, &ast.Field{Key: erdeString(key, keyLine), Value: p.expression(1)})
		} else {
			fields = append(fields, &ast.Field{Value: p.expression(1)})
		}
	})

	return erdeLine(&ast.TableExpr{Fields: fields}, line)
}

/* }}} */

/* Returns {{{ */

// returnList parses the values of `return` (and arrow function expression
// bodies), where `(a, b)` returns multiple values.
func (p *erdeParser) returnList() []ast.Expr {
	if !p.is("(") {
		return p.expressionList()
	}

	limit, limitIndex := p.lookPastSurround(p.index)
	if isErdeSyntax(limit, "->") || isErdeSyntax(limit, "=>") {
		return []ast.Expr{p.arrowFunction()}
	}

	for i := p.index + 1; i < limitIndex; {
		token := p.tokens[i]
		if _, ok := erdeSurroundEnds[token.Value]; ok && token.Type == erdeSymbol {
			_, i = p.lookPastSurround(i)
		} else if token.Type == erdeSymbol && token.Value == "," {
			exprs := []ast.Expr{}
			p.surroundList("(", ")", false, func() {
				exprs = append(exprs, p.expression(1))
			})
			return exprs
		} else {
			i++
		}
	}

	return []ast.Expr{p.expression(1)}
}

func (p *erdeParser) returnStmt() ast.Stmt {
	line := p.cur.Line
	if p.isModuleReturnBlock {
		p.moduleReturnLine = line
	}

	p.consume() // return
	exprs := []ast.Expr{}

	if p.blockDepth == 1 {
		if p.cur.Type != erdeEOF {
			exprs = p.returnList()
		}
		if p.cur.Type != erdeEOF {
			p.throw(p.cur.Line, "expected '<eof>', got '%s'", p.cur.Value)
		}
	} else {
		if !p.is("}") {
			exprs = p.returnList()
		}
		if !p.is("}") {
			p.throw(p.cur.Line, "expected '}', got '%s'", p.cur.Value)
		}
	}

	return erdeLine(&ast.ReturnStmt{Exprs: exprs}, line)
}

/* }}} */

/* Functions {{{ */

// parameters parses a parameter list into the function scope (which must
// already be pushed). Returns statements to prepend to the function body for
// defaults / destructures.
func (p *erdeParser) parameters() (*ast.ParList, []ast.Stmt) {
	parList := &ast.ParList{Names: []string{}}
	prelude := []ast.Stmt{}

	p.surroundList("(", ")", true, func() {
		line := p.cur.Line

		if p.branch("...") {
			parList.HasVargs = true

			if p.cur.Type == erdeWord {
				name := p.name()
				p.declare(name, erdeScopeLocal)
				varargs := erdeLine(&ast.TableExpr{Fields: []*ast.Field{{Value: erdeLine(&ast.Comma3Expr{}, line)}}}, line)
				prelude = append(prelude, erdeLocal([]string{name}, []ast.Expr{varargs}, line))
			}

			p.branch(",")
			if !p.is(")") {
				p.throw(p.cur.Line, "expected ')' got '%s'", p.cur.Value)
			}
			return
		}

		name, destructure := p.variable(erdeScopeLocal)
		if destructure != nil {
			name = destructure.tmpName
			for _, n := range destructure.names {
				p.declare(n, erdeScopeLocal)
			}
		} else {
			p.declare(name, erdeScopeLocal)
		}

		parList.Names = append(parList.Names, name)

		if p.branch("=") {
			prelude = append(prelude, erdeDefault(erdeIdent(name, line), p.expression(1), line))
		}

		if destructure != nil {
			prelude = append(prelude, erdeLocal(destructure.names, nil, line))
			prelude = append(prelude, destructure.stmts...)
		}
	})

	return parList, prelude
}

// functionBlock parses `{ ... }` as a function body.
func (p *erdeParser) functionBlock() ([]ast.Stmt, int) {
	oldIsModuleReturnBlock, oldLoop := p.isModuleReturnBlock, p.loop
	p.isModuleReturnBlock, p.loop = false, nil

	p.expect("{")
	stmts := p.block()
	lastLine := p.cur.Line
	p.expect("}")

	p.isModuleReturnBlock, p.loop = oldIsModuleReturnBlock, oldLoop
	return stmts, lastLine
}

func (p *erdeParser) arrowFunction() ast.Expr {
	line := p.cur.Line
	oldIsVarargs := p.isVarargs

	p.pushScope()
	defer p.popScope()

	var parList *ast.ParList
	var prelude []ast.Stmt

	if p.is("(") {
		parList, prelude = p.parameters()
		p.isVarargs = parList.HasVargs
	} else {
		p.isVarargs = false
		parList = &ast.ParList{Names: []string{}}

		name, destructure := p.variable(erdeScopeLocal)
		if destructure != nil {
			parList.Names = append(parList.Names, destructure.tmpName)
			for _, n := range destructure.names {
				p.declare(n, erdeScopeLocal)
			}
			prelude = append(prelude, erdeLocal(destructure.names, nil, line))
			prelude = append(prelude, destructure.stmts...)
		} else {
			p.declare(name, erdeScopeLocal)
			parList.Names = append(parList.Names, name)
		}
	}

	switch {
	case p.branch("->"):
	case p.branch("=>"):
		parList.Names = append([]string{"self"}, parList.Names...)
	case p.cur.Type == erdeEOF:
		p.throwEOF("unexpected eof (expected '->' or '=>')")
	default:
		p.throw(p.cur.Line, "unexpected token '%s' (expected '->' or '=>')", p.cur.Value)
	}

	var body []ast.Stmt
	lastLine := line

	if p.is("{") {
		body, lastLine = p.functionBlock()
	} else {
		returnLine := p.cur.Line
		exprs := []ast.Expr{}
		if p.is("(") {
			exprs = p.returnList()
		} else {
			exprs = append(exprs, p.expression(1))
		}
		body = []ast.Stmt{erdeLine(&ast.ReturnStmt{Exprs: exprs}, returnLine)}
		lastLine = p.prevLine()
	}

	p.isVarargs = oldIsVarargs

	fn := &ast.FunctionExpr{ParList: parList, Stmts: append(prelude, body...)}
	fn.SetLine(line)
	fn.SetLastLine(lastLine)
	return fn
}

// functionBody parses the parameters and body of a function declaration.
func (p *erdeParser) functionBody(line int) *ast.FunctionExpr {
	oldIsVarargs := p.isVarargs

	p.pushScope()
	defer p.popScope()

	parList, prelude := p.parameters()
	p.isVarargs = parList.HasVargs
	body, lastLine := p.functionBlock()
	p.isVarargs = oldIsVarargs

	fn := &ast.FunctionExpr{ParList: parList, Stmts: append(prelude, body...)}
	fn.SetLine(line)
	fn.SetLastLine(lastLine)
	return fn
}

func (p *erdeParser) functionDeclaration(scope string) ast.Stmt {
	line := p.cur.Line
	p.consume() // function

	baseName := p.name()

	if scope != "" && (p.is(".") || p.is(":")) {
		// Lua does not allow scope for table values (ex. `local function a.b()`)
		p.throw(p.cur.Line, "cannot use scopes for table values")
	}

	if scope == erdeScopeLocal {
		p.declare(baseName, scope)
		return erdeLocal([]string{baseName}, []ast.Expr{p.functionBody(line)}, line)
	}

	var signature ast.Expr
	if scope != "" {
		p.declare(baseName, scope)
		signature = p.target(baseName, scope, line)
	} else {
		signature = p.resolve(baseName, line)
	}

	for p.branch(".") {
		signature = erdeAttr(signature, erdeString(p.name(), line), line)
	}

	name := &ast.FuncName{Func: signature}
	if p.branch(":") {
		name = &ast.FuncName{Receiver: signature, Method: p.name()}
	}

	return erdeLine(&ast.FuncDefStmt{Name: name, Func: p.functionBody(line)}, line)
}

/* }}} */

/* Expressions {{{ */

func (p *erdeParser) expressionList() []ast.Expr {
	exprs := []ast.Expr{}
	p.list(func() { exprs = append(exprs, p.expression(1)) })
	return exprs
}

func (p *erdeParser) terminalExpression() ast.Expr {
	line := p.cur.Line

	switch p.cur.Type {
	case erdeEOF:
		p.throwEOF("unexpected eof (expected expression)")
	case erdeNumber:
		return erdeLine(&ast.NumberExpr{Value: p.consume()}, line)
	case erdeSingleQuote, erdeDoubleQuote, erdeBlockString:
		return p.indexChain(p.stringExpr()).expr
	}

	if p.cur.Type == erdeWord || p.cur.Type == erdeSymbol {
		switch p.cur.Value {
		case "true":
			p.consume()
			return erdeLine(&ast.TrueExpr{}, line)
		case "false":
			p.consume()
			return erdeLine(&ast.FalseExpr{}, line)
		case "nil":
			p.consume()
			return erdeLine(&ast.NilExpr{}, line)
		case "...":
			if !p.isVarargs {
				p.throw(line, "cannot use '...' outside a vararg function")
			}
			p.consume()
			return erdeLine(&ast.Comma3Expr{}, line)
		}
	}

	next := p.tokens[p.index+1]
	isArrowFunction := isErdeSyntax(next, "->") || isErdeSyntax(next, "=>")

	if _, ok := erdeSurroundEnds[p.cur.Value]; ok && !isArrowFunction && p.cur.Type == erdeSymbol {
		past, _ := p.lookPastSurround(p.index)
		isArrowFunction = isErdeSyntax(past, "->") || isErdeSyntax(past, "=>")
	}

	switch {
	case isArrowFunction:
		return p.arrowFunction()
	case p.is("{"):
		return p.tableConstructor()
	case p.is("("):
		return p.indexChain(p.parenthesizedExpression()).expr
	}

	return p.indexChain(p.resolve(p.name(), line)).expr
}

func (p *erdeParser) parenthesizedExpression() ast.Expr {
	p.expect("(")
	expr := p.expression(1)
	p.expect(")")

	// Parentheses truncate multiple values to one.
	switch ex := expr.(type) {
	case *ast.FuncCallExpr:
		ex.AdjustRet = true
	case *ast.Comma3Expr:
		ex.AdjustRet = true
	}

	return expr
}

func (p *erdeParser) unopExpression() ast.Expr {
	line := p.cur.Line
	op := p.consume()
	operand := p.expression(erdeUnopPrec + 1)

	switch op {
	case "!":
		return erdeLine(&ast.UnaryNotOpExpr{Expr: operand}, line)
	case "-":
		return erdeLine(&ast.UnaryMinusOpExpr{Expr: operand}, line)
	case "#":
		return erdeLine(&ast.UnaryLenOpExpr{Expr: operand}, line)
	}

	return erdeLibCall(erdeBitlib, "bnot", []ast.Expr{operand}, line)
}

func (p *erdeParser) expression(minPrec int) ast.Expr {
	if p.cur.Type == erdeEOF {
		p.throwEOF("unexpected eof (expected expression)")
	}

	var expr ast.Expr
	switch {
	case p.is("-"), p.is("#"), p.is("!"), p.is("~"):
		expr = p.unopExpression()
	default:
		expr = p.terminalExpression()
	}

	for p.cur.Type == erdeSymbol {
		op, ok := erdeBinops[p.cur.Value]
		if !ok || op.prec < minPrec {
			break
		}

		line := p.cur.Line
		token := p.consume()

		if token == "~" && p.is("=") {
			p.throw(line, "unexpected token '~=', did you mean '!='?")
		}

		if op.rightAssoc {
			expr = p.binop(token, line, expr, p.expression(op.prec))
		} else {
			expr = p.binop(token, line, expr, p.expression(op.prec+1))
		}
	}

	return expr
}

/* }}} */

/* Blocks {{{ */

// block parses statements until a closing brace (not consumed).
func (p *erdeParser) block() []ast.Stmt {
	p.blockDepth++
	p.pushScope()

	stmts := []ast.Stmt{}
	for !p.is("}") {
		stmts = append(stmts, p.statement()...)
	}

	p.popScope()
	p.blockDepth--
	return stmts
}

func (p *erdeParser) braceBlock() []ast.Stmt {
	p.expect("{")
	stmts := p.block()
	p.expect("}")
	return stmts
}

func (p *erdeParser) loopBlock() []ast.Stmt {
	oldLoop := p.loop
	p.loop = &erdeLoop{continueLabel: p.newTmpName()}

	stmts := p.braceBlock()
	if p.loop.hasContinue {
		stmts = append(stmts, erdeLine(&ast.LabelStmt{Name: p.loop.continueLabel}, p.prevLine()))
	}

	p.loop = oldLoop
	return stmts
}

func (p *erdeParser) forLoop() ast.Stmt {
	line := p.cur.Line
	p.consume() // for

	next := p.tokens[p.index+1]
	if isErdeSyntax(next, "=") {
		name := p.name()
		p.consume() // =

		exprsLine := p.cur.Line
		exprs := p.expressionList()
		if len(exprs) != 2 && len(exprs) != 3 {
			p.throw(exprsLine, "invalid numeric for, expected 2-3 expressions, got %d", len(exprs))
		}

		p.pushScope()
		p.declare(name, erdeScopeLocal)
		stmts := p.loopBlock()
		p.popScope()

		stmt := &ast.NumberForStmt{Name: name, Init: exprs[0], Limit: exprs[1], Stmts: stmts}
		if len(exprs) == 3 {
			stmt.Step = exprs[2]
		}
		return p.closed(erdeLine(stmt, line))
	}

	names := []string{}
	declared := []string{}
	prelude := []ast.Stmt{}

	p.list(func() {
		varLine := p.cur.Line
		name, destructure := p.variable(erdeScopeLocal)
		if destructure != nil {
			names = append(names, destructure.tmpName)
			declared = append(declared, destructure.names...)
			prelude = append(prelude, erdeLocal(destructure.names, nil, varLine))
			prelude = append(prelude, destructure.stmts...)
		} else {
			names = append(names, name)
			declared = append(declared, name)
		}
	})

	p.expect("in")
	exprs := p.expressionList()

	p.pushScope()
	for _, name := range declared {
		p.declare(name, erdeScopeLocal)
	}
	stmts := append(prelude, p.loopBlock()...)
	p.popScope()

	return p.closed(erdeLine(&ast.GenericForStmt{Names: names, Exprs: exprs, Stmts: stmts}, line))
}

func (p *erdeParser) ifElse() ast.Stmt {
	line := p.cur.Line
	p.consume() // if

	root := erdeLine(&ast.IfStmt{Condition: p.expression(1), Then: p.braceBlock(), Else: []ast.Stmt{}}, line)
	last := root

	for p.is("elseif") {
		elseifLine := p.cur.Line
		p.consume()
		elseif := erdeLine(&ast.IfStmt{Condition: p.expression(1), Then: p.braceBlock(), Else: []ast.Stmt{}}, elseifLine)
		last.Else = []ast.Stmt{elseif}
		last = elseif
	}

	if p.branch("else") {
		if p.is("if") {
			p.throw(p.cur.Line, "unexpected tokens 'else if', did you mean 'elseif'?")
		}
		last.Else = p.braceBlock()
	}

	return p.closed(root)
}

/* }}} */

/* Assignments {{{ */

func (p *erdeParser) assignmentChain() *erdeChain {
	if p.is("(") {
		chain := p.indexChain(p.parenthesizedExpression())
		if chain.chainLen == 0 {
			if p.cur.Type == erdeEOF {
				p.throwEOF("unexpected eof")
			}
			p.throw(p.cur.Line, "unexpected token '%s'", p.cur.Value)
		}
		return chain
	}

	line := p.cur.Line
	return p.indexChain(p.resolve(p.name(), line))
}

var erdeAssignmentOperators = map[string]bool{
	"||": true, "&&": true, "..": true, "+": true, "-": true, "*": true,
	"/": true, "//": true, "%": true, "^": true, "|": true, "~": true,
	"&": true, "<<": true, ">>": true,
}

// operatorAssignment compiles `id op= value`. Index chains are only evaluated
// once (ex. `a[f()] += 1` only calls `f` once).
func (p *erdeParser) operatorAssignment(id *erdeChain, value ast.Expr, op string, line int) []ast.Stmt {
	if id.chainLen == 0 {
		return []ast.Stmt{erdeAssign(id.expr, p.binop(op, line, id.expr, value), line)}
	}

	stmts := []ast.Stmt{}
	base, index := id.finalBase, id.finalIndex

	if _, ok := base.(*ast.IdentExpr); !ok {
		tmpName := p.newTmpName()
		stmts = append(stmts, erdeLocal([]string{tmpName}, []ast.Expr{base}, line))
		base = erdeIdent(tmpName, line)
	}

	if _, ok := index.(ast.ConstExpr); !ok {
		tmpName := p.newTmpName()
		stmts = append(stmts, erdeLocal([]string{tmpName}, []ast.Expr{index}, line))
		index = erdeIdent(tmpName, line)
	}

	current := erdeAttr(base, index, line)
	return append(stmts, erdeAssign(erdeAttr(base, index, line), p.binop(op, line, current, value), line))
}

func (p *erdeParser) variableAssignment(first *erdeChain, line int) []ast.Stmt {
	ids := []*erdeChain{first}

	for p.branch(",") {
		idLine := p.cur.Line
		id := p.assignmentChain()
		if id.isCall {
			p.throw(idLine, "cannot assign value to function call")
		}
		ids = append(ids, id)
	}

	opLine := p.cur.Line
	op := ""
	if p.cur.Type == erdeSymbol && erdeAssignmentOperators[p.cur.Value] {
		op = p.consume()
	}

	p.expect("=")
	exprs := p.expressionList()

	if op == "" {
		lhs := make([]ast.Expr, len(ids))
		for i, id := range ids {
			lhs[i] = id.expr
		}
		return []ast.Stmt{erdeLine(&ast.AssignStmt{Lhs: lhs, Rhs: exprs}, line)}
	}

	var stmts []ast.Stmt

	if len(ids) == 1 {
		stmts = p.operatorAssignment(ids[0], exprs[0], op, opLine)
		if len(stmts) == 1 {
			return stmts
		}
	} else {
		tmpNames := make([]string, len(ids))
		for i := range ids {
			tmpNames[i] = p.newTmpName()
		}

		stmts = []ast.Stmt{erdeLocal(tmpNames, exprs, line)}
		for i, id := range ids {
			stmts = append(stmts, p.operatorAssignment(id, erdeIdent(tmpNames[i], opLine), op, opLine)...)
		}
	}

	// Scope temporary locals
	return []ast.Stmt{erdeLine(&ast.DoBlockStmt{Stmts: stmts}, line)}
}

/* }}} */

/* Declarations {{{ */

func (p *erdeParser) variableDeclaration(scope string, line int) []ast.Stmt {
	names := []string{}
	destructures := []*erdeDestructure{}

	p.list(func() {
		name, destructure := p.variable(scope)
		if destructure != nil {
			destructures = append(destructures, destructure)
			names = append(names, destructure.tmpName)
			for _, n := range destructure.names {
				p.declare(n, scope)
			}
		} else {
			names = append(names, name)
			p.declare(name, scope)
		}
	})

	if len(destructures) > 0 {
		p.expect("=")
	} else if scope == erdeScopeLocal && !p.is("=") {
		return []ast.Stmt{erdeLocal(names, nil, line)}
	} else if !p.branch("=") {
		return nil // nothing to do
	}

	exprs := p.expressionList()
	stmts := []ast.Stmt{}

	if len(destructures) > 0 {
		declared := []string{}
		for _, d := range destructures {
			if scope == erdeScopeLocal {
				declared = append(declared, d.names...)
			} else {
				declared = append(declared, d.tmpName)
			}
		}
		stmts = append(stmts, erdeLocal(declared, nil, line))
	}

	if scope == erdeScopeLocal {
		stmts = append(stmts, erdeLocal(names, exprs, line))
	} else {
		lhs := make([]ast.Expr, len(names))
		for i, name := range names {
			lhs[i] = p.target(name, scope, line)
		}
		for _, d := range destructures {
			for i, name := range names {
				if name == d.tmpName {
					lhs[i] = erdeIdent(name, line)
				}
			}
		}
		stmts = append(stmts, erdeLine(&ast.AssignStmt{Lhs: lhs, Rhs: exprs}, line))
	}

	for _, d := range destructures {
		stmts = append(stmts, d.stmts...)
	}

	return stmts
}

/* }}} */

/* Statements {{{ */

func (p *erdeParser) statement() []ast.Stmt {
	line := p.cur.Line
	var stmts []ast.Stmt

	switch {
	case p.cur.Type == erdeEOF:
		p.throwEOF("unexpected eof")
	case p.is("break"):
		p.consume()
		if p.loop == nil {
			p.throw(line, "cannot use 'break' outside of loop")
		}
		stmts = []ast.Stmt{erdeLine(&ast.BreakStmt{}, line)}
	case p.is("continue"):
		p.consume()
		if p.loop == nil {
			p.throw(line, "cannot use 'continue' outside of loop")
		}
		p.loop.hasContinue = true
		stmts = []ast.Stmt{erdeLine(&ast.GotoStmt{Label: p.loop.continueLabel}, line)}
	case p.is("goto"):
		p.consume()
		stmts = []ast.Stmt{erdeLine(&ast.GotoStmt{Label: p.name()}, line)}
	case p.is("::"):
		p.consume()
		label := p.name()
		p.expect("::")
		stmts = []ast.Stmt{erdeLine(&ast.LabelStmt{Name: label}, line)}
	case p.is("do"):
		p.consume()
		stmts = []ast.Stmt{p.closed(erdeLine(&ast.DoBlockStmt{Stmts: p.braceBlock()}, line))}
	case p.is("if"):
		stmts = []ast.Stmt{p.ifElse()}
	case p.is("for"):
		stmts = []ast.Stmt{p.forLoop()}
	case p.is("while"):
		p.consume()
		condition := p.expression(1)
		stmts = []ast.Stmt{p.closed(erdeLine(&ast.WhileStmt{Condition: condition, Stmts: p.loopBlock()}, line))}
	case p.is("repeat"):
		p.consume()
		body := p.loopBlock()
		p.expect("until")
		stmts = []ast.Stmt{erdeLine(&ast.RepeatStmt{Stmts: body, Condition: p.expression(1)}, line)}
	case p.is("return"):
		stmts = []ast.Stmt{p.returnStmt()}
	case p.is("function"):
		stmts = []ast.Stmt{p.functionDeclaration("")}
	case p.is("local"), p.is("global"), p.is("module"):
		scope := p.consume()

		if scope == erdeScopeModule {
			if p.blockDepth > 1 {
				p.throw(line, "module declarations must appear at the top level")
			}
			p.hasModuleDecls = true
		}

		if p.is("function") {
			stmts = []ast.Stmt{p.functionDeclaration(scope)}
		} else {
			stmts = p.variableDeclaration(scope, line)
		}
	default:
		chain := p.assignmentChain()
		if chain.isCall {
			stmts = []ast.Stmt{erdeLine(&ast.FuncCallStmt{Expr: chain.expr}, line)}
		} else {
			stmts = p.variableAssignment(chain, line)
		}
	}

	p.branch(";")
	return stmts
}

func (p *erdeParser) module() []ast.Stmt {
	stmts := []ast.Stmt{}
	for p.cur.Type != erdeEOF {
		stmts = append(stmts, p.statement()...)
	}

	if p.hasModuleDecls {
		if p.moduleReturnLine != 0 {
			p.throw(p.moduleReturnLine, "cannot use 'return' w/ 'module' declarations")
		}

		line := p.cur.Line
		moduleTable := erdeLine(&ast.TableExpr{Fields: []*ast.Field{}}, 1)
		stmts = append([]ast.Stmt{erdeLocal([]string{erdeModuleName}, []ast.Expr{moduleTable}, 1)}, stmts...)
		stmts = append(stmts, erdeLine(&ast.ReturnStmt{Exprs: []ast.Expr{erdeIdent(erdeModuleName, line)}}, line))
	}

	return stmts
}

/* }}} */
