package mk

type AstInterp struct {
	errors *DiagnosticBag
	symtab *SymbolTable

	returnVal  any
	returnType *TypeDef
}

// ---------- Expr ----------

func (interp *AstInterp) VisitIdent(n *IdentExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitAssign(n *AssignExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitInt(n *IntLitExpr) (Node, error) {
	interp.returnVal = n.Value
	interp.returnType = SysInt32
	return n, nil
}

func (interp *AstInterp) VisitBool(n *BoolLitExpr) (Node, error) {
	interp.returnVal = n.Value
	interp.returnType = SysBool
	return n, nil
}

func (interp *AstInterp) VisitString(n *StringLitExpr) (Node, error) {
	interp.returnVal = n.Value
	interp.returnType = SysString
	return n, nil
}

func (interp *AstInterp) VisitMap(n *MapLitExpr) (Node, error) {
	data := map[string]any{}
	for key, expr := range n.Values {
		expr.Accept(interp)
		data[key] = interp.returnVal
	}
	interp.returnVal = data
	interp.returnType = SysMap
	return n, nil
}

func (interp *AstInterp) VisitList(n *ListLitExpr) (Node, error) {
	data := []any{}
	for _, expr := range n.Values {
		expr.Accept(interp)
		data = append(data, interp.returnVal)
	}
	interp.returnType = SysList
	return n, nil
}

func (interp *AstInterp) VisitNull(n *NullLitExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitDecimal(n *DecimalLitExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitArithmetic(n *ArithmeticExpr) (Node, error) {
	n.Left.Accept(interp)
	n.Right.Accept(interp)
	interp.returnType = SysInt32
	return n, nil
}

func (interp *AstInterp) VisitComparison(n *ComparisonExpr) (Node, error) {
	n.Left.Accept(interp)
	n.Right.Accept(interp)
	interp.returnType = SysBool
	return n, nil
}

func (interp *AstInterp) VisitUnary(n *UnaryExpr) (Node, error) {
	n.Right.Accept(interp)
	if n.Op == MINUS && interp.returnType != SysInt32 {
		panic("")
	}
	if n.Op == TILDE && interp.returnType != SysBool {
		panic("")
	}
	return n, nil
}

func (interp *AstInterp) VisitCtx(n *CtxExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitCall(n *CallExpr) (Node, error) {
	switch n.Func.(type) {
	case *IntLitExpr, *BoolLitExpr, *StringLitExpr:
		//panic(&checkingError{})
	}
	return n, nil
}

func (interp *AstInterp) VisitIndex(n *IndexExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitDot(n *DotExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitParen(n *ParenExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitTernary(n *TernaryExpr) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitFunc(n *FuncStmt) (Node, error) {
	return n, nil
}

// ---------- Stmt ----------

func (interp *AstInterp) VisitLet(n *LetStmt) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitIf(n *IfStmt) (Node, error) {
	n.Cond.Accept(interp)
	if interp.returnType != SysBool {
		panic("")
	}
	n.Then.Accept(interp)
	if n.Else != nil {
		n.Else.Accept(interp)
	}
	return n, nil
}

func (interp *AstInterp) VisitWhile(n *WhileStmt) (Node, error) {
	n.Cond.Accept(interp)
	interp.symtab.Enter("while")
	n.Body.Accept(interp)
	interp.symtab.Leave()
	return n, nil
}

func (interp *AstInterp) VisitBlock(n *BlockStmt) (Node, error) {
	returned := false
	for _, stmt := range n.Statements {
		if returned {
			interp.errors.Report(ErrUnreachableCode, 0, 0)
			panic(ErrUnreachableCode.Template)
		}
		stmt.Accept(interp)

		if _, ok := stmt.(*ReturnStmt); ok {
			returned = true
		}
	}
	return n, nil
}

func (interp *AstInterp) VisitReturn(n *ReturnStmt) (Node, error) {
	if n.Value == nil {
		interp.returnType = SysVoid
	} else {
		n.Value.Accept(interp)
	}
	return n, nil
}

func (interp *AstInterp) VisitBreak(n *BreakStmt) (Node, error) {
	return n, nil
}

func (interp *AstInterp) VisitExpr(n *ExprStmt) (Node, error) {
	n.Expr.Accept(interp)
	return n, nil
}

func (interp *AstInterp) VisitProgram(n *Program) (Node, error) {
	returned := false
	for _, stmt := range n.Statements {
		if returned {
			interp.errors.Report(ErrUnreachableCode, 0, 0)
			panic(ErrUnreachableCode.Template)
		}
		stmt.Accept(interp)

		if _, ok := stmt.(*ReturnStmt); ok {
			returned = true
		}
	}
	return n, nil
}
