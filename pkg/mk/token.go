package mk

type TokenType string
type Token struct {
	Type   TokenType
	Lit    string
	Line   int
	Column int
	Offset int
}

const (
	ERR = "ERR"
	EOF = "EOF"

	IDENT  = "IDENT"
	INT    = "INT"
	STRING = "STRING"

	// 运算符
	ASSIGN = "="
	PLUS   = "+"
	MINUS  = "-"
	STAR   = "*"
	SLASH  = "/"
	BANG   = "!"
	LT     = "<"
	LE     = "<="
	GT     = ">"
	GE     = ">="
	NE     = "!="
	EQ     = "=="
	TILDE  = "~"
	LTLT   = "<<"
	GTGT   = ">>"

	BIT_XOR = "^"
	BIT_AND = "&"
	BIT_OR  = "|"

	BIT_AND_AND = "&&"
	BIT_OR_OR   = "||"

	PERCENT  = "%"
	QUESTION = "?"

	// 分隔符
	COMMA = ","
	SEMI  = ";"
	COLON = ":"

	DOT = "."

	// 请求上下文
	DOLLAR = "$"

	LPAREN   = "("
	RPAREN   = ")"
	LBRACE   = "{"
	RBRACE   = "}"
	LBRACKET = "["
	RBRACKET = "]"

	// 关键字
	AND    = "AND"
	OR     = "OR"
	IF     = "IF"
	WHILE  = "WHILE"
	BREAK  = "BREAK"
	RETURN = "RETURN"
	TRUE   = "TRUE"
	FALSE  = "FALSE"
	ELSE   = "ELSE"
	NULL   = "NULL"
	TYPEOF = "TYPEOF"
	SIZEOF = "SIZEOF"
	FUNC   = "FN"
)

var DUMMY = Token{ERR, "", 0, 0, 0}

var keywords = map[string]TokenType{
	"and":    AND,
	"or":     OR,
	"return": RETURN,
	"true":   TRUE,
	"false":  FALSE,
	"null":   NULL,
	"if":     IF,
	"else":   ELSE,
	"while":  WHILE,
	"break":  BREAK,
	"sizeof": SIZEOF,
	"typeof": TYPEOF,
	"fn":     FUNC,
}

var precedences = map[TokenType]int{
	// 赋值运算符 (最低)
	ASSIGN: 1, // =

	// 三元条件运算符
	QUESTION: 2, // ?

	// 逻辑
	BIT_AND_AND: 21,
	BIT_OR_OR:   22,

	// 关系比较
	EQ: 23, // ==
	NE: 24, // !=
	LT: 25, // <
	GT: 26, // >
	LE: 27, // <=
	GE: 28, // >=

	// 算术运算
	BIT_OR:  31,
	BIT_XOR: 32,
	BIT_AND: 33,
	LTLT:    34, // <<
	GTGT:    35, // >>
	PLUS:    36, // +
	MINUS:   37, // -
	SLASH:   38, // /
	STAR:    39, // *
	PERCENT: 40, // % (取模)

	// 函数调用
	LPAREN: 41, // (

	// 数组/索引访问
	LBRACKET: 42, // [
	DOT:      43, // .
}

func GetPrecedence(k TokenType) int {
	if p, ok := precedences[k]; ok {
		return p
	}
	return 0
}

func LookupIdentKind(ident string) TokenType {
	if typ, ok := keywords[ident]; ok {
		return typ
	}
	return IDENT
}

func NewToken(typ TokenType, lit string, line, col, pos int) Token {
	return Token{Type: typ, Lit: lit, Line: line, Column: col, Offset: pos}
}
