package mk

type CompilationContext struct {
	Filename string
	Source   string

	errors  *DiagnosticBag
	lexer   *Lexer
	parser  *Parser
	symtab  *SymbolTable
	checker *Checker
	codegen *Codegen
	ast     *AstInterp
	code    *CodeInterp
	vm      *VM

	program  *Program
	bytecode []byte
}

func NewCompilationContext(file, code string) *CompilationContext {
	ctx := &CompilationContext{
		Filename: file,
		Source:   code,
	}
	ctx.errors = NewBag(ctx)
	ctx.lexer = NewLexer(ctx)
	ctx.parser = NewParser(ctx)
	ctx.symtab = NewRootTable()
	ctx.checker = NewChecker(ctx)
	ctx.codegen = NewCodegen(ctx)
	return ctx
}

func (ctx *CompilationContext) Compile() {
	if ctx.program == nil {
		p := ctx.parser.ParseCode()
		ctx.checker.Check(p)
		ctx.program = p
	}
	if len(ctx.bytecode) == 0 {
		ctx.bytecode = ctx.codegen.Gen(ctx.program)
	}
}

func (ctx *CompilationContext) Program() *Program {
	ctx.Compile()
	return ctx.program
}

func (ctx *CompilationContext) Bytecode() []byte {
	ctx.Compile()
	return ctx.bytecode
}

// Diagnostics 返回本次编译产生的词法/语法/语义诊断。
func (ctx *CompilationContext) Diagnostics() []Diagnostic {
	return ctx.errors.Diagnostics()
}

type RuntimeContext struct {
	store map[string]any
	outer *RuntimeContext
}

func (ctx *RuntimeContext) Set(name string, val any) {
}

func (ctx *RuntimeContext) Get(name string) any {
	v, _ := ctx.store[name]
	return v
}

func NewRuntimeContext() *RuntimeContext {
	return &RuntimeContext{store: map[string]any{}}
}
