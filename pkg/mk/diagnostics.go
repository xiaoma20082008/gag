package mk

import (
	"fmt"
	"strings"
	"sync"
)

type DiagnosticDescriptor struct {
	Code     string         // 错误码，如 "E0001", "E0002"
	Kind     DiagnosticKind // 词法、语法、运行
	Template string         // 带有占位符的文案模板
}

type Diagnostic struct {
	Descriptor DiagnosticDescriptor
	Kind       DiagnosticKind // 错误类别：词法、语法或运行时
	Filename   string
	Line       int
	Column     int
	Message    string
	Source     string
}

type DiagnosticKind int

const (
	KindLexical  DiagnosticKind = iota // 词法错误（Lexer 阶段）
	KindSyntax                         // 语法错误（Parser 阶段）
	KindSemantic                       // 语义错误（Analyzer 阶段）
	KindRuntime                        // 运行时错误（Evaluator 阶段）
)

type DiagnosticBag struct {
	diagnostics []Diagnostic
	mu          sync.Mutex
	filename    string
	codelines   []string
}

func (k DiagnosticKind) String() string {
	switch k {
	case KindLexical:
		return "Lexical Error"
	case KindSyntax:
		return "Syntax Error"
	case KindSemantic:
		return "Semantic Error"
	case KindRuntime:
		return "Runtime Error"
	default:
		return "Error"
	}
}

func (d Diagnostic) String() string {
	var result strings.Builder
	fmt.Fprintf(&result, "[%s] %s: %s\n  --> %s:%d:%d\n",
		d.Descriptor.Kind.String(),
		d.Descriptor.Code,
		d.Message,
		d.Filename,
		d.Line,
		d.Column)
	if d.Source != "" {
		lineStr := fmt.Sprintf("%d", d.Line)
		padding := strings.Repeat(" ", len(lineStr))

		// 第一行：    |
		fmt.Fprintf(&result, " %s |\n", padding)

		// 第二行： 5  |  a = 5
		fmt.Fprintf(&result, " %s | %s\n", lineStr, d.Source)

		// 第三行：    |      ^
		fmt.Fprintf(&result, " %s | ", padding)
		for i := 1; i < d.Column; i++ {
			result.WriteString(" ")
		}
		result.WriteString("^\n")
	}
	return result.String()
}

func (b *DiagnosticBag) Report(desc DiagnosticDescriptor, line int, col int, args ...any) {
	var srcLine string
	if line > 0 && line <= len(b.codelines) {
		srcLine = b.codelines[line-1]
	}
	msg := fmt.Sprintf(desc.Template, args...)
	b.diagnostics = append(b.diagnostics, Diagnostic{
		Descriptor: desc,
		Filename:   b.filename,
		Line:       line,
		Column:     col,
		Message:    msg,
		Source:     srcLine,
	})
}

func (b *DiagnosticBag) Diagnostics() []Diagnostic {
	return b.diagnostics
}

func NewBag(ctx *CompilationContext) *DiagnosticBag {
	bag := &DiagnosticBag{
		diagnostics: []Diagnostic{},
	}
	bag.filename = ctx.Filename
	bag.codelines = strings.Split(ctx.Source, "\n")
	return bag
}

var (
	// ==========================================
	// 1. 词法错误 (Lexical Errors: E1000 - E1999)
	// ==========================================

	// E1001: 遇到了不属于语言规范的未知字符
	ErrInvalidChar = DiagnosticDescriptor{
		Code:     "E1001",
		Kind:     KindLexical,
		Template: "invalid character '%s'",
	}
	// E1002: 字符串没有闭合，比如 "hello 换行了或者到文件末尾了
	ErrUnterminatedString = DiagnosticDescriptor{
		Code:     "E1002",
		Kind:     KindLexical,
		Template: "unterminated string literal",
	}

	// E1002: 多行注释没有闭合，比如 "hello 换行了或者到文件末尾了
	ErrUnterminatedComment = DiagnosticDescriptor{
		Code:     "E1003",
		Kind:     KindLexical,
		Template: "unterminated multi line comment",
	}

	// ==========================================
	// 2. 语法错误 (Syntax Errors: E2000 - E2999)
	// ==========================================

	// E2001: 期待某种符号但落空了，比如 if 后面没有带 {，或者没有带 )
	ErrExpectedToken = DiagnosticDescriptor{
		Code:     "E2001",
		Kind:     KindSyntax,
		Template: "expected '%s', got '%s'",
	}
	// E2002: 强行换行却没有加分号 ;
	ErrMissingSemi = DiagnosticDescriptor{
		Code:     "E2002",
		Kind:     KindSyntax,
		Template: "expected ';', got '%s'",
	}
	// E2003: 表达式单独成行，但它既不是赋值也不是调用（如孤零零的 1 + 2;）
	ErrNotAStmt = DiagnosticDescriptor{
		Code:     "E2003",
		Kind:     KindSyntax,
		Template: "expression '%s' cannot be used as a standalone statement",
	}

	// E2004: 赋值号 = 左边不是一个合法的左值（如 5 = a;）
	ErrInvalidLValue = DiagnosticDescriptor{
		Code:     "E2004",
		Kind:     KindSyntax,
		Template: "cannot assign to '%s'; invalid left-hand side in assignment",
	}
	// E2005: 遇到了无法解析的前缀表达式
	ErrNoPrefixParseFunc = DiagnosticDescriptor{
		Code:     "E2005",
		Kind:     KindSyntax,
		Template: "expected an expression before '%s'",
	}
	// E2006: 遇到了未知的前缀表达式
	ErrUnknownPrefixKind = DiagnosticDescriptor{
		Code:     "E2006",
		Kind:     KindSyntax,
		Template: "unknown token '%s'",
	}
	// E2007: 遇到了无法解析的中缀表达式
	ErrNoInfixParseFunc = DiagnosticDescriptor{
		Code:     "E2007",
		Kind:     KindSyntax,
		Template: "no infix parse function for '%s' found",
	}
	// E2008: 遇到了未知的中缀表达式
	ErrUnknownInfixKind = DiagnosticDescriptor{
		Code:     "E2008",
		Kind:     KindSyntax,
		Template: "unknown infix type '%s'",
	}
	ErrBreakOutOfLoop = DiagnosticDescriptor{
		Code:     "E2009",
		Kind:     KindSyntax,
		Template: "'break' only can be used in 'while' loop",
	}

	// ==========================================
	// 3. 语义错误 (Semantic Errors: E3000 - E3999)
	// ==========================================

	// E3001: 赋值时左右两侧类型不匹配
	ErrTypeMismatchAssign = DiagnosticDescriptor{
		Code:     "E3001",
		Kind:     KindSemantic,
		Template: "type mismatch in assignment: cannot assign type '%s' to variable of type '%s'",
	}

	// E3002: 运算符两边的操作数类型不匹配（例如 string + int）
	ErrTypeMismatchOperator = DiagnosticDescriptor{
		Code:     "E3002",
		Kind:     KindSemantic,
		Template: "type mismatch for operator '%s': unsupported operand types '%s' and '%s'",
	}

	// E3003: 函数调用时，实参和形参的类型不匹配
	ErrTypeMismatchArgument = DiagnosticDescriptor{
		Code:     "E3003",
		Kind:     KindSemantic,
		Template: "type mismatch in function call: argument %d expects type '%s', got '%s'",
	}

	// E3004: 函数返回值类型与声明的返回类型不匹配
	ErrTypeMismatchReturn = DiagnosticDescriptor{
		Code:     "E3004",
		Kind:     KindSemantic,
		Template: "type mismatch in return: expected type '%s', got '%s'",
	}

	// E3005: 变量重复声明错误（如果在同一个作用域里）
	ErrRedefinedIdentifier = DiagnosticDescriptor{
		Code:     "E3005",
		Kind:     KindSemantic,
		Template: "semantic error: identifier '%s' has already been declared in this scope",
	}

	// E3006: 变量在静态分析时无法推导出唯一类型
	ErrCannotInferType = DiagnosticDescriptor{
		Code:     "E3006",
		Kind:     KindSemantic,
		Template: "type inference error: cannot determine the type of variable '%s'; please provide more context",
	}

	// E3007: 使用了未定义的变量（在静态语言中，这绝对是编译期/语义错误，而不是运行时错误）
	ErrUndefinedIdentifierStatic = DiagnosticDescriptor{
		Code:     "E3007",
		Kind:     KindSemantic,
		Template: "compiler error: name '%s' is not defined in this scope",
	}

	// E3008: 试图修改常量或不可变变量（类似无 let/const 关键字但有不可变约定的情况）
	ErrAssignToImmutable = DiagnosticDescriptor{
		Code:     "E3008",
		Kind:     KindSemantic,
		Template: "cannot assign to immutable variable/binding '%s'",
	}

	ErrUnreachableCode = DiagnosticDescriptor{
		Code:     "E3009",
		Kind:     KindSemantic,
		Template: "cannot reachable the code",
	}

	// ==========================================
	// 4. 运行时错误 (Runtime Errors: E4000 - E4999)
	// ==========================================

	// E4001: 数学除以零错误
	ErrDivByZero = DiagnosticDescriptor{
		Code:     "E4001",
		Kind:     KindRuntime,
		Template: "division by zero",
	}
	// E4002: 使用了没有定义的变量
	ErrUndefinedIdentifier = DiagnosticDescriptor{
		Code:     "E4002",
		Kind:     KindRuntime,
		Template: "undefined variable '%s'",
	}
	// E4003: 对非函数对象尝试进行调用，比如 a = 5; a();
	ErrNotAFunction = DiagnosticDescriptor{
		Code:     "E4003",
		Kind:     KindRuntime,
		Template: "'%s' is not a function",
	}
	// E4004: 函数调用时，传入的实参个数和定义的形参个数不匹配
	ErrWrongArgCount = DiagnosticDescriptor{
		Code:     "E4004",
		Kind:     KindRuntime,
		Template: "wrong number of arguments; expected %d, got %d",
	}
	// E4005: 数组越界，比如长度为 3 的数组访问了 index = 5
	ErrIndexOutOfBounds = DiagnosticDescriptor{
		Code:     "E4005",
		Kind:     KindRuntime,
		Template: "index out of bounds: %d",
	}
)
