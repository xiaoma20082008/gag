package mk

import (
	"sort"
)

const EOI = 0

type Lexer struct {
	filename string
	input    string

	pos     int
	readPos int
	ch      byte

	token Token
	saved []Token

	lineStarts []int
	errors     *DiagnosticBag
}

func (l *Lexer) NextToken() Token {
	if len(l.saved) > 0 {
		l.token = l.saved[0]
		l.saved = l.saved[1:]
	} else {
		l.token = l.readToken()
	}
	return l.token
}

func (l *Lexer) Token() Token {
	return l.Lookahead(0)
}

func (l *Lexer) Lookahead(lookahead int) Token {
	if lookahead == 0 {
		return l.token
	} else {
		l.ensure(lookahead)
		return l.saved[lookahead-1]
	}
}

func (l *Lexer) LineMap(pos int) (int, int) {
	if pos <= 0 {
		return 1, 1
	}
	if pos > len(l.input) {
		pos = len(l.input)
	}
	line := sort.Search(len(l.lineStarts), func(i int) bool {
		return l.lineStarts[i] > pos
	})
	startOfLine := l.lineStarts[line-1]
	column := pos - startOfLine + 1
	return line, column
}

func (l *Lexer) File() string {
	return l.filename
}

func (l *Lexer) Reset(filename, source string) {
	l.filename = filename
	l.input = source

	l.saved = []Token{}
	l.token = DUMMY

	l.pos = 0
	l.readPos = 0
	l.ch = 0

	l.lineStarts = []int{0}

	l.readChar()
}

func (l *Lexer) readToken() Token {
	l.skipWhitespace()
	var kind TokenType
	startPos := l.pos
	lit := ""
	line, column := 0, 0
	shouldAdvance := true
	switch l.ch {
	case '=':
		if l.peekChar() == '=' {
			l.readChar()
			kind = EQ
			lit = "=="
		} else {
			kind = ASSIGN
			lit = "="
		}
	case '+':
		kind = PLUS
		lit = "+"
	case '-':
		kind = MINUS
		lit = "-"
	case '*':
		kind = STAR
		lit = "*"
	case '&':
		if l.peekChar() == '&' {
			l.readChar()
			kind = LOG_AND
			lit = "&&"
		} else {
			kind = BIT_AND
			lit = "&"
		}
	case '|':
		if l.peekChar() == '|' {
			l.readChar()
			kind = LOG_OR
			lit = "||"
		} else {
			kind = BIT_OR
			lit = "|"
		}
	case '^':
		kind = BIT_XOR
		lit = "^"
	case '/':
		pc := l.peekChar()
		switch pc {
		case '/':
			comment, ok := l.readComment(false)
			if !ok {
				lit = ""
				kind = ERR
			} else {
				lit = comment
				kind = COMMENT
			}
		case '*':
			comment, ok := l.readComment(true)
			if !ok {
				lit = ""
				kind = ERR
			} else {
				lit = comment
				kind = COMMENT
			}
		default:
			kind = SLASH
			lit = "/"
		}
	case '!':
		if l.peekChar() == '=' {
			l.readChar()
			kind = NE
			lit = "!="
		} else {
			kind = BANG
			lit = "!"
		}
	case '<':
		if l.peekChar() == '=' {
			l.readChar()
			kind = LE
			lit = "<="
		} else if l.peekChar() == '<' {
			l.readChar()
			kind = LTLT
			lit = "<<"
		} else {
			kind = LT
			lit = "<"
		}
	case '>':
		if l.peekChar() == '=' {
			l.readChar()
			kind = GE
			lit = ">="
		} else if l.peekChar() == '>' {
			l.readChar()
			kind = GTGT
			lit = ">>"
		} else {
			kind = GT
			lit = ">"
		}
	case '%':
		kind = PERCENT
		lit = "%"
	case '?':
		kind = QUESTION
		lit = "?"
	case '~':
		kind = TILDE
		lit = "~"
	case ',':
		kind = COMMA
		lit = ","
	case ';':
		kind = SEMI
		lit = ";"
	case '{':
		kind = LBRACE
		lit = "{"
	case '}':
		kind = RBRACE
		lit = "}"
	case '(':
		kind = LPAREN
		lit = "("
	case ')':
		kind = RPAREN
		lit = ")"
	case '[':
		kind = LBRACKET
		lit = "["
	case ']':
		kind = RBRACKET
		lit = "]"
	case ':':
		kind = COLON
		lit = ":"
	case '$':
		kind = DOLLAR
		lit = "$"
	case '.':
		kind = DOT
		lit = "."
	case 0:
		kind = EOF
		lit = ""
	case '"':
		s, ok := l.readString()
		if ok {
			lit = s
			kind = STRING
		} else {
			lit = ""
			kind = ERR
			// 此时 l.ch 已经是 0 (EOI) 了
		}
		shouldAdvance = false
	default:
		if isLetter(l.ch) {
			lit = l.readIdent()
			kind = LookupIdentKind(lit)
			shouldAdvance = false
		} else if isDigit(l.ch) {
			kind = INT
			lit = l.readInt()
			shouldAdvance = false
		} else {
			line, column = l.LineMap(startPos)
			l.errors.Report(ErrInvalidChar, line, column, string(l.ch))
			kind = ERR
			lit = string(l.ch)
		}
	}
	if shouldAdvance {
		l.readChar()
	}
	if line == 0 && column == 0 {
		line, column = l.LineMap(startPos)
	}
	return NewToken(kind, lit, line, column, startPos)
}

func (l *Lexer) ensure(lookahead int) {
	for i := len(l.saved); i < lookahead; i++ {
		l.saved = append(l.saved, l.readToken())
	}
}

func (l *Lexer) readString0() (string, bool) {
	// 1.
	p := l.pos

	// 2.跳过开头的"
	l.readChar()

	for l.ch != '"' && l.ch != 0 {
		if l.ch == '\\' {
			l.readChar()
			if l.ch == EOI {
				break
			}
		}
		l.readChar()
	}

	// 3.
	if l.ch == EOI {
		line, col := l.LineMap(p)
		l.errors.Report(ErrUnterminatedString, line, col)
		return "", false
	}

	// 4.
	ret := l.input[p+1 : l.pos]

	// 5. 跳过末尾的"
	l.readChar()

	return ret, true
}

func (l *Lexer) readString() (string, bool) {
	p := l.pos

	// 跳过开头的"
	l.readChar()

	buf := make([]byte, 0, 16)

	for l.ch != '"' && l.ch != 0 {
		if l.ch != '\\' {
			buf = append(buf, l.ch)
			l.readChar()
			continue
		}
		bslashPos := l.pos
		l.readChar()
		if l.ch == EOI {
			break
		}
		switch l.ch {
		case 'r':
			buf = append(buf, '\r')
		case 'n':
			buf = append(buf, '\n')
		case 't':
			buf = append(buf, '\t')
		case 'f':
			buf = append(buf, '\f')
		case '"':
			buf = append(buf, '"')
		case '0':
			buf = append(buf, 0)
		case '\\':
			buf = append(buf, '\\')
		case '\'':
			buf = append(buf, '\'')
		default:
			line, col := l.LineMap(bslashPos)
			l.errors.Report(ErrInvalidEscape, line, col, string(l.ch))
			buf = append(buf, '\\', l.ch)
		}
		l.readChar()
	}

	if l.ch == EOI {
		line, col := l.LineMap(p)
		l.errors.Report(ErrUnterminatedString, line, col)
		return "", false
	}

	ret := string(buf)

	// 跳过末尾的"
	l.readChar()

	return ret, true
}

func (l *Lexer) readComment(multi bool) (string, bool) {
	l.readChar() // 跳过 / 或 *
	p := l.pos

	for l.ch != EOI {
		if multi && l.ch == '*' && l.peekChar() == '/' {
			l.readChar()
			break
		} else if !multi && l.ch == '\n' {
			break
		}
		l.readChar()
	}

	if multi && l.ch == EOI {
		line, col := l.LineMap(p)
		l.errors.Report(ErrUnterminatedComment, line, col)
		return "", false
	}
	ret := l.input[p:l.pos]

	if multi { // 读取下一个
		l.readChar()
	}
	return ret, true
}

func (l *Lexer) readInt() string {
	p := l.pos
	for isDigit(l.ch) {
		l.readChar()
	}
	return l.input[p:l.pos]
}

func (l *Lexer) readIdent() string {
	p := l.pos
	for isLetter(l.ch) || isDigit(l.ch) {
		l.readChar()
	}
	return l.input[p:l.pos]
}

func (l *Lexer) peekChar() byte {
	if l.readPos >= len(l.input) {
		return EOI
	} else {
		return l.input[l.readPos]
	}
}

func (l *Lexer) readChar() {
	if l.readPos >= len(l.input) {
		l.ch = EOI
	} else {
		l.ch = l.input[l.readPos]
	}
	l.pos = l.readPos
	l.readPos++

	if l.ch == '\n' {
		l.lineStarts = append(l.lineStarts, l.pos+1)
	}
}

func (l *Lexer) skipWhitespace() {
	for l.ch == ' ' || l.ch == '\n' || l.ch == '\t' || l.ch == '\r' {
		l.readChar()
	}
}

func NewLexer(ctx *CompilationContext) *Lexer {
	l := &Lexer{
		errors: ctx.errors,
		saved:  []Token{},
	}
	l.Reset(ctx.Filename, ctx.Source)
	return l
}

func isDigit(ch byte) bool {
	return ('0' <= ch && ch <= '9')
}

func isLetter(ch byte) bool {
	return ch == '_' || ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z')
}
