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

func (ctx *CompilationContext) Compile() []byte {
	p := ctx.parser.ParseCode()
	ctx.checker.Check(p)
	return ctx.codegen.Gen(p)
}

// Diagnostics 返回本次编译产生的词法/语法/语义诊断。
//
// 上层（如网关的配置加载流程）依赖它把脚本错误挡在下发之前。
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
