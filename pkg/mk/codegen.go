package mk

type Codegen struct {
	ctx *CompilationContext
}

func NewCodegen(ctx *CompilationContext) *Codegen {
	return &Codegen{
		ctx: ctx,
	}
}

func (c *Codegen) Gen(n Node) []byte {
	n.Accept(c)
	return nil
}

// ---------- Expr ----------

func (c *Codegen) VisitIdent(n *IdentExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitAssign(n *AssignExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitInt(n *IntLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitBool(n *BoolLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitString(n *StringLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitMap(n *MapLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitList(n *ListLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitNull(n *NullLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitDecimal(n *DecimalLitExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitArithmetic(n *ArithmeticExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitComparison(n *ComparisonExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitUnary(n *UnaryExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitCtx(n *CtxExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitCall(n *CallExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitIndex(n *IndexExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitDot(n *DotExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitParen(n *ParenExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitTernary(n *TernaryExpr) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitFunc(n *FuncStmt) (Node, error) {
	return n, nil
}

// ---------- Stmt ----------

func (c *Codegen) VisitLet(n *LetStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitIf(n *IfStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitWhile(n *WhileStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitBlock(n *BlockStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitReturn(n *ReturnStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitBreak(n *BreakStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitExpr(n *ExprStmt) (Node, error) {
	return n, nil
}

func (c *Codegen) VisitProgram(n *Program) (Node, error) {
	return n, nil
}
