package mk

import "fmt"

type Checker struct {
	errors *DiagnosticBag
	symtab *SymbolTable

	loopDepth int

	returnVal  any
	returnType *TypeDef
}

func NewChecker(ctx *CompilationContext) *Checker {
	return &Checker{
		errors: ctx.errors,
		symtab: ctx.symtab,
	}
}

func (c *Checker) Check(n Node) {
	n.Accept(c)
}

// ---------- Expr ----------

func (c *Checker) VisitIdent(n *IdentExpr) (Node, error) {
	if sym := c.symtab.Resolve(n.Id); sym == nil {
		c.errors.Report(ErrUndefinedIdentifierStatic, 0, 0, n.Id)
		panic(&checkingError{})
	}
	return n, nil
}

func (c *Checker) VisitAssign(n *AssignExpr) (Node, error) {
	if !c.isLValue(n.Name) {
		name := Format(n.Name)
		c.errors.Report(ErrInvalidLValue, 0, 0, name)
		panic(&checkingError{fmt.Sprintf(ErrInvalidLValue.Template, name)})
	}

	if ident, ok := n.Name.(*IdentExpr); ok {
		c.symtab.Define(ident.Id, nil)
	}

	return n, nil
}

func (c *Checker) VisitInt(n *IntLitExpr) (Node, error) {
	c.returnVal = n.Value
	c.returnType = SysInt32
	return n, nil
}

func (c *Checker) VisitBool(n *BoolLitExpr) (Node, error) {
	c.returnVal = n.Value
	c.returnType = SysBool
	return n, nil
}

func (c *Checker) VisitString(n *StringLitExpr) (Node, error) {
	c.returnVal = n.Value
	c.returnType = SysString
	return n, nil
}

func (c *Checker) VisitMap(n *MapLitExpr) (Node, error) {
	data := map[string]any{}
	for key, expr := range n.Values {
		expr.Accept(c)
		data[key] = c.returnVal
	}
	c.returnVal = data
	c.returnType = SysMap
	return n, nil
}

func (c *Checker) VisitList(n *ListLitExpr) (Node, error) {
	data := []any{}
	for _, expr := range n.Values {
		expr.Accept(c)
		data = append(data, c.returnVal)
	}
	c.returnType = SysList
	return n, nil
}

func (c *Checker) VisitArithmetic(n *ArithmeticExpr) (Node, error) {
	n.Left.Accept(c)
	n.Right.Accept(c)
	c.returnType = SysInt32
	return n, nil
}

func (c *Checker) VisitComparison(n *ComparisonExpr) (Node, error) {
	n.Left.Accept(c)
	n.Right.Accept(c)
	c.returnType = SysBool
	return n, nil
}

func (c *Checker) VisitUnary(n *UnaryExpr) (Node, error) {
	n.Right.Accept(c)
	if n.Op == MINUS && c.returnType != SysInt32 {
		panic("")
	}
	if n.Op == TILDE && c.returnType != SysBool {
		panic("")
	}
	return n, nil
}

func (c *Checker) VisitCtx(n *CtxExpr) (Node, error) {
	return n, nil
}

func (c *Checker) VisitCall(n *CallExpr) (Node, error) {
	switch n.Func.(type) {
	case *IntLitExpr, *BoolLitExpr, *StringLitExpr:
		panic(&checkingError{})
	}
	return n, nil
}

func (c *Checker) VisitIndex(n *IndexExpr) (Node, error) {
	return n, nil
}

func (c *Checker) VisitDot(n *DotExpr) (Node, error) {
	return n, nil
}

func (c *Checker) VisitParen(n *ParenExpr) (Node, error) {
	return n, nil
}

func (c *Checker) VisitTernary(n *TernaryExpr) (Node, error) {
	return n, nil
}

func (c *Checker) VisitFunc(n *FuncExpr) (Node, error) {
	return n, nil
}

// ---------- Stmt ----------

func (c *Checker) VisitIf(n *IfStmt) (Node, error) {
	n.Cond.Accept(c)
	if c.returnType != SysBool {
		panic("")
	}
	n.Then.Accept(c)
	if n.Else != nil {
		n.Else.Accept(c)
	}
	return n, nil
}

func (c *Checker) VisitWhile(n *WhileStmt) (Node, error) {
	n.Cond.Accept(c)
	c.symtab.Enter()
	c.loopDepth++
	n.Body.Accept(c)
	c.loopDepth--
	c.symtab.Leave()
	return n, nil
}

func (c *Checker) VisitBlock(n *BlockStmt) (Node, error) {
	returned := false
	for _, stmt := range n.Statements {
		if returned {
			c.errors.Report(ErrUnreachableCode, 0, 0)
			panic(ErrUnreachableCode.Template)
		}
		stmt.Accept(c)

		if _, ok := stmt.(*ReturnStmt); ok {
			returned = true
		}
	}
	return n, nil
}

func (c *Checker) VisitReturn(n *ReturnStmt) (Node, error) {
	if n.Value == nil {
		c.returnType = SysVoid
	} else {
		n.Value.Accept(c)
	}
	return n, nil
}

func (c *Checker) VisitBreak(n *BreakStmt) (Node, error) {
	if c.loopDepth <= 0 {
		c.errors.Report(ErrBreakOutOfLoop, 0, 0)
		panic(ErrBreakOutOfLoop.Template)
	}
	return n, nil
}

func (c *Checker) VisitExpr(n *ExprStmt) (Node, error) {
	n.Expr.Accept(c)
	return n, nil
}

func (c *Checker) VisitProgram(n *Program) (Node, error) {
	returned := false
	for _, stmt := range n.Statements {
		if returned {
			c.errors.Report(ErrUnreachableCode, 0, 0)
			panic(ErrUnreachableCode.Template)
		}
		stmt.Accept(c)

		if _, ok := stmt.(*ReturnStmt); ok {
			returned = true
		}
	}
	return n, nil
}

func (c *Checker) resolveType(n Expression) *TypeDef {
	switch n.(type) {
	case *IntLitExpr:
		return SysInt32
	case *BoolLitExpr:
		return SysBool
	case *StringLitExpr:
		return SysString
	case *MapLitExpr:
		return SysMap
	case *ListLitExpr:
		return SysList
	case *FuncExpr:
		return SysFunction
	default:
		return SysObject
	}
}

func (c *Checker) isLValue(n Expression) bool {
	switch n.(type) {
	case *IdentExpr, *DotExpr:
		return true
	default:
		return false
	}
}

type checkingError struct {
	msg string
}
