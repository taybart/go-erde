package parse

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/taybart/go-erde/ast"
)

// Erde (https://erde-lang.github.io/) tokenizer. Mirrors erde/tokenize.erde:
// strings are split into delimiter / content / interpolation tokens, so the
// parser sees interpolated expressions as ordinary tokens.

type erdeTokenType int

const (
	erdeEOF erdeTokenType = iota
	erdeSymbol
	erdeWord
	erdeNumber
	erdeSingleQuote // `'` delimiter
	erdeDoubleQuote // `"` delimiter
	erdeBlockString // `[==[` / `]==]` delimiter
	erdeStringContent
	erdeInterpolation // `{` / `}` around an interpolated expression
)

type erdeToken struct {
	Type  erdeTokenType
	Value string // decoded value for erdeStringContent
	Line  int
}

var erdeSymbols = map[string]bool{
	"->": true, "=>": true, "...": true, "::": true,
	"||": true, "&&": true, "==": true, "!=": true, "<=": true, ">=": true,
	"..": true, "//": true, "<<": true, ">>": true,
}

type erdeLexer struct {
	source string
	text   string
	pos    int
	line   int
	tokens []erdeToken
}

func (lx *erdeLexer) throw(line int, format string, args ...interface{}) {
	panic(&Error{
		Pos:     ast.Position{Source: lx.source, Line: line},
		Message: fmt.Sprintf(format, args...),
	})
}

// throwEOF throws an error for input that ends early (see erdeParser.throwEOF).
func (lx *erdeLexer) throwEOF(format string, args ...interface{}) {
	panic(&Error{
		Pos:     ast.Position{Source: lx.source, Line: EOF},
		Message: fmt.Sprintf(format, args...),
	})
}

func (lx *erdeLexer) peek(n int) byte {
	if lx.pos+n < len(lx.text) {
		return lx.text[lx.pos+n]
	}
	return 0
}

func (lx *erdeLexer) eof() bool { return lx.pos >= len(lx.text) }

func (lx *erdeLexer) emit(typ erdeTokenType, value string, line int) {
	lx.tokens = append(lx.tokens, erdeToken{Type: typ, Value: value, Line: line})
}

func isErdeWordHead(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func isErdeWordBody(c byte) bool { return isErdeWordHead(c) || isErdeDigit(c) }

func isErdeDigit(c byte) bool { return '0' <= c && c <= '9' }

func isErdeHex(c byte) bool {
	return isErdeDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func erdeTokenize(text, source string) (tokens []erdeToken, err error) {
	lx := &erdeLexer{source: source, text: text, line: 1}

	defer func() {
		if e := recover(); e != nil {
			if perr, ok := e.(*Error); ok {
				err = perr
				return
			}
			panic(e)
		}
	}()

	if strings.HasPrefix(text, "#!") {
		for !lx.eof() && lx.text[lx.pos] != '\n' {
			lx.pos++
		}
	}

	for !lx.eof() {
		lx.token()
	}

	lx.emit(erdeEOF, "", lx.line)
	return lx.tokens, nil
}

func (lx *erdeLexer) token() {
	c := lx.text[lx.pos]

	switch {
	case c == '\n':
		lx.pos++
		lx.line++
	case c == ' ' || c == '\t' || c == '\r':
		lx.pos++
	case isErdeWordHead(c):
		start := lx.pos
		for !lx.eof() && isErdeWordBody(lx.text[lx.pos]) {
			lx.pos++
		}
		lx.emit(erdeWord, lx.text[start:lx.pos], lx.line)
	case c == '\'':
		lx.singleQuoteString()
	case c == '"':
		lx.doubleQuoteString()
	case c == '-' && lx.peek(1) == '-':
		lx.comment()
	case c == '0' && (lx.peek(1) == 'x' || lx.peek(1) == 'X'):
		lx.hex()
	case c == '0' && (lx.peek(1) == 'b' || lx.peek(1) == 'B'):
		lx.binary()
	case isErdeDigit(c) || c == '.' && isErdeDigit(lx.peek(1)):
		lx.decimal()
	case c == '[' && (lx.peek(1) == '[' || lx.peek(1) == '='):
		lx.blockString()
	default:
		for _, n := range []int{3, 2} {
			if lx.pos+n <= len(lx.text) && erdeSymbols[lx.text[lx.pos:lx.pos+n]] {
				lx.emit(erdeSymbol, lx.text[lx.pos:lx.pos+n], lx.line)
				lx.pos += n
				return
			}
		}
		lx.emit(erdeSymbol, string(c), lx.line)
		lx.pos++
	}
}

func (lx *erdeLexer) decimal() {
	start := lx.pos
	isInteger := true

	for isErdeDigit(lx.peek(0)) {
		lx.pos++
	}

	if lx.peek(0) == '.' && isErdeDigit(lx.peek(1)) {
		isInteger = false
		lx.pos++
		for isErdeDigit(lx.peek(0)) {
			lx.pos++
		}
	}

	if c := lx.peek(0); c == 'e' || c == 'E' {
		isInteger = false
		lx.pos++
		if c := lx.peek(0); c == '+' || c == '-' {
			lx.pos++
		}
		if !isErdeDigit(lx.peek(0)) {
			lx.throw(lx.line, "missing exponent value")
		}
		for isErdeDigit(lx.peek(0)) {
			lx.pos++
		}
	}

	value := lx.text[start:lx.pos]
	if isInteger {
		// gopher-lua parses integer literals w/ base prefixes, so `010` would be
		// read as octal.
		value = strings.TrimLeft(value, "0")
		if value == "" {
			value = "0"
		}
	}

	lx.emit(erdeNumber, value, lx.line)
}

func (lx *erdeLexer) hex() {
	lx.pos += 2 // 0[xX]

	if !isErdeHex(lx.peek(0)) && !(lx.peek(0) == '.' && isErdeHex(lx.peek(1))) {
		lx.throw(lx.line, "malformed hex")
	}

	value := 0.0
	for isErdeHex(lx.peek(0)) {
		d, _ := strconv.ParseUint(string(lx.peek(0)), 16, 8)
		value = 16*value + float64(d)
		lx.pos++
	}

	if lx.peek(0) == '.' && isErdeHex(lx.peek(1)) {
		lx.pos++
		for counter := 1.0; isErdeHex(lx.peek(0)); counter++ {
			d, _ := strconv.ParseUint(string(lx.peek(0)), 16, 8)
			value += float64(d) / math.Pow(16, counter)
			lx.pos++
		}
	}

	if c := lx.peek(0); c == 'p' || c == 'P' {
		lx.pos++
		sign := 1.0
		if c := lx.peek(0); c == '+' || c == '-' {
			if c == '-' {
				sign = -1
			}
			lx.pos++
		}
		if !isErdeDigit(lx.peek(0)) {
			lx.throw(lx.line, "missing exponent value")
		}
		exponent := 0.0
		for isErdeDigit(lx.peek(0)) {
			exponent = 10*exponent + float64(lx.peek(0)-'0')
			lx.pos++
		}
		value *= math.Pow(2, sign*exponent)
	}

	lx.emit(erdeNumber, strconv.FormatFloat(value, 'g', -1, 64), lx.line)
}

func (lx *erdeLexer) binary() {
	lx.pos += 2 // 0[bB]

	if c := lx.peek(0); c != '0' && c != '1' {
		lx.throw(lx.line, "malformed binary")
	}

	value := 0.0
	for c := lx.peek(0); c == '0' || c == '1'; c = lx.peek(0) {
		value = 2*value + float64(c-'0')
		lx.pos++
	}

	lx.emit(erdeNumber, strconv.FormatFloat(value, 'g', -1, 64), lx.line)
}

func (lx *erdeLexer) comment() {
	lx.pos += 2 // --

	if lx.peek(0) == '[' {
		equals := 0
		for lx.peek(1+equals) == '=' {
			equals++
		}

		if lx.peek(1+equals) == '[' {
			lx.pos += 2 + equals
			closeQuote := "]" + strings.Repeat("=", equals) + "]"

			for !strings.HasPrefix(lx.text[lx.pos:], closeQuote) {
				if lx.eof() {
					lx.throwEOF("unterminated comment")
				}
				if lx.text[lx.pos] == '\n' {
					lx.line++
				}
				lx.pos++
			}

			lx.pos += len(closeQuote)
			return
		}
	}

	for !lx.eof() && lx.text[lx.pos] != '\n' {
		lx.pos++
	}
}

// escapeSequence decodes a Lua escape sequence, starting after the backslash.
func (lx *erdeLexer) escapeSequence(buf *bytes.Buffer) {
	c := lx.peek(0)

	switch c {
	case 'a':
		buf.WriteByte('\a')
	case 'b':
		buf.WriteByte('\b')
	case 'f':
		buf.WriteByte('\f')
	case 'n':
		buf.WriteByte('\n')
	case 'r':
		buf.WriteByte('\r')
	case 't':
		buf.WriteByte('\t')
	case 'v':
		buf.WriteByte('\v')
	case '\\', '"', '\'':
		buf.WriteByte(c)
	case '\n':
		buf.WriteByte('\n')
		lx.line++
	case 'z':
		lx.pos++
		for !lx.eof() && strings.IndexByte(" \t\r\n\f\v", lx.text[lx.pos]) >= 0 {
			if lx.text[lx.pos] == '\n' {
				lx.line++
			}
			lx.pos++
		}
		return
	case 'x':
		if !isErdeHex(lx.peek(1)) || !isErdeHex(lx.peek(2)) {
			lx.throw(lx.line, "escape sequence \\xXX must use exactly 2 hex characters")
		}
		v, _ := strconv.ParseUint(lx.text[lx.pos+1:lx.pos+3], 16, 8)
		buf.WriteByte(byte(v))
		lx.pos += 3
		return
	case 'u':
		lx.pos++
		if lx.peek(0) != '{' {
			lx.throw(lx.line, "missing { in escape sequence \\u{XXX}")
		}
		lx.pos++
		start := lx.pos
		for isErdeHex(lx.peek(0)) {
			lx.pos++
		}
		if start == lx.pos {
			lx.throw(lx.line, "missing hex in escape sequence \\u{XXX}")
		}
		if lx.peek(0) != '}' {
			lx.throw(lx.line, "missing } in escape sequence \\u{XXX}")
		}
		v, err := strconv.ParseUint(lx.text[start:lx.pos], 16, 32)
		if err != nil || v > utf8.MaxRune {
			lx.throw(lx.line, "UTF-8 value too large")
		}
		var encoded [utf8.UTFMax]byte
		buf.Write(encoded[:utf8.EncodeRune(encoded[:], rune(v))])
		lx.pos++
		return
	default:
		if isErdeDigit(c) {
			start := lx.pos
			for lx.pos-start < 3 && isErdeDigit(lx.peek(0)) {
				lx.pos++
			}
			v, _ := strconv.Atoi(lx.text[start:lx.pos])
			if v > 255 {
				lx.throw(lx.line, "decimal escape too large")
			}
			buf.WriteByte(byte(v))
			return
		}
		lx.throw(lx.line, "invalid escape sequence \\%c", c)
	}

	lx.pos++
}

func (lx *erdeLexer) singleQuoteString() {
	lx.emit(erdeSingleQuote, "'", lx.line)
	lx.pos++

	var content bytes.Buffer
	for lx.peek(0) != '\'' {
		switch c := lx.peek(0); {
		case lx.eof() || c == '\n':
			lx.throw(lx.line, "unterminated string")
		case c == '\\':
			lx.pos++
			lx.escapeSequence(&content)
		default:
			content.WriteByte(c)
			lx.pos++
		}
	}

	if content.Len() > 0 {
		lx.emit(erdeStringContent, content.String(), lx.line)
	}

	lx.emit(erdeSingleQuote, "'", lx.line)
	lx.pos++
}

func (lx *erdeLexer) doubleQuoteString() {
	lx.emit(erdeDoubleQuote, "\"", lx.line)
	lx.pos++

	var content bytes.Buffer
	contentLine := lx.line

	flush := func() {
		if content.Len() > 0 {
			lx.emit(erdeStringContent, content.String(), contentLine)
			content.Reset()
		}
		contentLine = lx.line
	}

	for lx.peek(0) != '"' {
		switch c := lx.peek(0); {
		case lx.eof() || c == '\n':
			lx.throw(lx.line, "unterminated string")
		case c == '\\':
			lx.pos++
			if c := lx.peek(0); c == '{' || c == '}' {
				content.WriteByte(c)
				lx.pos++
			} else {
				lx.escapeSequence(&content)
			}
		case c == '{':
			flush()
			lx.interpolation()
			contentLine = lx.line
		default:
			content.WriteByte(c)
			lx.pos++
		}
	}

	flush()
	lx.emit(erdeDoubleQuote, "\"", lx.line)
	lx.pos++
}

func (lx *erdeLexer) blockString() {
	lx.pos++ // [

	equals := 0
	for lx.peek(0) == '=' {
		equals++
		lx.pos++
	}

	if lx.peek(0) != '[' {
		lx.throw(lx.line, "unterminated block string opening")
	}
	lx.pos++

	openQuote := "[" + strings.Repeat("=", equals) + "["
	closeQuote := "]" + strings.Repeat("=", equals) + "]"
	lx.emit(erdeBlockString, openQuote, lx.line)

	// Lua skips a newline directly following the opening quote.
	if strings.HasPrefix(lx.text[lx.pos:], "\r\n") {
		lx.pos += 2
		lx.line++
	} else if lx.peek(0) == '\n' {
		lx.pos++
		lx.line++
	}

	var content bytes.Buffer
	contentLine := lx.line

	flush := func() {
		if content.Len() > 0 {
			lx.emit(erdeStringContent, content.String(), contentLine)
			content.Reset()
		}
		contentLine = lx.line
	}

	for !strings.HasPrefix(lx.text[lx.pos:], closeQuote) {
		switch c := lx.peek(0); {
		case lx.eof():
			lx.throwEOF("unterminated block string")
		case c == '\n':
			content.WriteByte(c)
			lx.pos++
			lx.line++
		case c == '\\':
			lx.pos++
			if c := lx.peek(0); c == '{' || c == '}' {
				content.WriteByte(c)
				lx.pos++
			} else {
				content.WriteByte('\\')
			}
		case c == '{':
			flush()
			lx.interpolation()
			contentLine = lx.line
		default:
			content.WriteByte(c)
			lx.pos++
		}
	}

	flush()
	lx.emit(erdeBlockString, closeQuote, lx.line)
	lx.pos += len(closeQuote)
}

func (lx *erdeLexer) interpolation() {
	lx.emit(erdeInterpolation, "{", lx.line)
	lx.pos++

	braceDepth := 0

	for lx.peek(0) != '}' || braceDepth > 0 {
		switch {
		case lx.eof():
			lx.throwEOF("unterminated interpolation")
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

	lx.emit(erdeInterpolation, "}", lx.line)
	lx.pos++
}
