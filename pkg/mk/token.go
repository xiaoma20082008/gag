package mk

type TokenType int

type Token struct {
	Type   TokenType
	Lit    string
	Line   int
	Column int
	Offset int
}

const (
	_ TokenType = iota
	ERR
	EOF

	IDENT
	INT
	STRING

	// 运算符
	ASSIGN
	PLUS
	MINUS
	STAR
	SLASH
	BANG
	LT
	LE
	GT
	GE
	NE
	EQ
	TILDE
	LTLT
	GTGT

	BIT_XOR
	BIT_AND
	BIT_OR

	BIT_AND_AND
	BIT_OR_OR

	PERCENT
	QUESTION

	// 分隔符
	COMMA
	SEMI
	COLON

	DOT

	// 请求上下文
	DOLLAR

	LPAREN
	RPAREN
	LBRACE
	RBRACE
	LBRACKET
	RBRACKET

	// 关键字
	AND
	OR
	IF
	WHILE
	BREAK
	RETURN
	TRUE
	FALSE
	ELSE
	NULL
	TYPEOF
	SIZEOF
	FUNC

	COMMENT
)

func (t TokenType) String() string {
	switch t {
	case ERR:
		return "ERR"
	case EOF:
		return "EOF"
	case IDENT:
		return "IDENT"
	case INT:
		return "INT"
	case STRING:
		return "STRING"

	case ASSIGN:
		return "="
	case PLUS:
		return "+"
	case MINUS:
		return "-"
	case STAR:
		return "*"
	case SLASH:
		return "/"
	case BANG:
		return "!"
	case LT:
		return "<"
	case LE:
		return "<="
	case GT:
		return ">"
	case GE:
		return ">="
	case NE:
		return "!="
	case EQ:
		return "=="
	case TILDE:
		return "~"
	case LTLT:
		return "<<"
	case GTGT:
		return ">>"

	case BIT_XOR:
		return "^"
	case BIT_AND:
		return "&"
	case BIT_OR:
		return "|"

	case BIT_AND_AND:
		return "&&"
	case BIT_OR_OR:
		return "||"

	case PERCENT:
		return "%"
	case QUESTION:
		return "?"

	case COMMA:
		return ","
	case SEMI:
		return ";"
	case COLON:
		return ":"

	case DOT:
		return "."

	case DOLLAR:
		return "$"

	case LPAREN:
		return "("
	case RPAREN:
		return ")"
	case LBRACE:
		return "{"
	case RBRACE:
		return "}"
	case LBRACKET:
		return "["
	case RBRACKET:
		return "]"

	case AND:
		return "AND"
	case OR:
		return "OR"
	case IF:
		return "IF"
	case WHILE:
		return "WHILE"
	case BREAK:
		return "BREAK"
	case RETURN:
		return "RETURN"
	case TRUE:
		return "TRUE"
	case FALSE:
		return "FALSE"
	case ELSE:
		return "ELSE"
	case NULL:
		return "NULL"
	case TYPEOF:
		return "TYPEOF"
	case SIZEOF:
		return "SIZEOF"
	case FUNC:
		return "FN"
	default:
		return ""
	}
}

const (
	_ int = iota
	precLOWEST
	precASSIGN       // =
	precQUESTION     // ?
	precLOG_OR       // ||
	precLOG_AND      // &&
	precEQUALS       // ==, !=
	precLESS_GREATER // >, <, >=, <=
	precBIT_OR       // |
	precBIT_XOR      // ^
	precBIT_AND      // &
	precSHIFT        // <<, >>
	precSUM          // +, -
	precPRODUCT      // *, /, %
	precPREFIX       // 一元前缀: +, -, !, ~
	precCALL         // 方法/函数调用: .(, (
	precHIGHEST
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
	ASSIGN: precASSIGN, // =

	// 三元条件运算符
	QUESTION: precQUESTION, // ?

	// 逻辑
	BIT_OR_OR:   precLOG_OR,
	BIT_AND_AND: precLOG_AND,
	OR:          precLOG_OR,
	AND:         precLOG_AND,

	// 关系比较
	EQ: precEQUALS,       // ==
	NE: precEQUALS,       // !=
	LT: precLESS_GREATER, // <
	GT: precLESS_GREATER, // >
	LE: precLESS_GREATER, // <=
	GE: precLESS_GREATER, // >=

	// 算术运算
	BIT_OR:  precBIT_OR,
	BIT_XOR: precBIT_XOR,
	BIT_AND: precBIT_AND,
	LTLT:    precSHIFT,   // <<
	GTGT:    precSHIFT,   // >>
	PLUS:    precSUM,     // +
	MINUS:   precSUM,     // -
	SLASH:   precPRODUCT, // /
	STAR:    precPRODUCT, // *
	PERCENT: precPRODUCT, // % (取模)

	// 函数调用
	LPAREN: precCALL, // (
	// 数组/索引访问
	LBRACKET: precCALL, // [
	DOT:      precCALL, // .
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
