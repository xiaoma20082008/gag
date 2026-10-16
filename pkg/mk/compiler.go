package mk

type Compiler struct {
	ctx *CompilationContext
}

func NewCompiler(ctx *CompilationContext) *Compiler {
	return &Compiler{
		ctx: ctx,
	}
}

func (c *Compiler) Compile() error {
	p := c.ctx.parser.ParseCode()
	c.ctx.checker.Check(p)
	c.ctx.codegen.Gen(p)
	return nil
}
