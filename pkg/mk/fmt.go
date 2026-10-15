package mk

import (
	"strconv"
	"strings"
)

func Format(n Node) string {
	return newFormatter().Format(n)
}

type formatter struct {
	sb strings.Builder

	level int
	ident string
}

func newFormatter() *formatter {
	return &formatter{
		level: 0,
		ident: "  ",
	}
}

func (f *formatter) Format(n Node) string {
	n.Accept(f)
	return f.sb.String()
}

// ---------- Expr ----------

func (f *formatter) VisitIdent(n *IdentExpr) (Node, error) {
	f.sb.WriteString(n.Id)
	return n, nil
}

func (f *formatter) VisitAssign(n *AssignExpr) (Node, error) {
	f.writeIndent()
	n.Name.Accept(f)
	f.sb.WriteString(" = ")
	n.Value.Accept(f)
	f.sb.WriteString("\n")
	return n, nil
}

func (f *formatter) VisitInt(n *IntLitExpr) (Node, error) {
	f.sb.WriteString(strconv.FormatInt(int64(n.Value), 10))
	return n, nil
}

func (f *formatter) VisitBool(n *BoolLitExpr) (Node, error) {
	f.sb.WriteString(strconv.FormatBool(n.Value))
	return n, nil
}

func (f *formatter) VisitString(n *StringLitExpr) (Node, error) {
	f.sb.WriteString(n.Value)
	return n, nil
}

func (f *formatter) VisitMap(n *MapLitExpr) (Node, error) {
	f.sb.WriteString("{")
	first := true
	for k, v := range n.Values {
		if !first {
			f.sb.WriteString(",")
			first = true
		}
		f.sb.WriteString(k)
		f.sb.WriteString(":")
		v.Accept(f)
	}
	f.sb.WriteString("}")
	return n, nil
}

func (f *formatter) VisitList(n *ListLitExpr) (Node, error) {
	f.sb.WriteString("[")
	for i, v := range n.Values {
		if i > 0 {
			f.sb.WriteString(",")
		}
		v.Accept(f)
	}
	f.sb.WriteString("]")
	return n, nil
}

func (f *formatter) VisitArithmetic(n *ArithmeticExpr) (Node, error) {
	n.Left.Accept(f)
	f.sb.WriteString(n.Op)
	n.Right.Accept(f)
	return n, nil
}

func (f *formatter) VisitComparison(n *ComparisonExpr) (Node, error) {
	n.Left.Accept(f)
	f.sb.WriteString(n.Op)
	n.Right.Accept(f)
	return n, nil
}

func (f *formatter) VisitUnary(n *UnaryExpr) (Node, error) {
	f.sb.WriteString(n.Op)
	n.Right.Accept(f)
	return n, nil
}

func (f *formatter) VisitCtx(n *CtxExpr) (Node, error) {
	f.sb.WriteString("$")
	return n, nil
}

func (f *formatter) VisitCall(n *CallExpr) (Node, error) {
	n.Func.Accept(f)
	f.sb.WriteString("(")
	for i := 0; i < len(n.Args); i++ {
		if i > 0 {
			f.sb.WriteString(",")
		}
		n.Args[i].Accept(f)
	}
	f.sb.WriteString(")")
	return n, nil
}

func (f *formatter) VisitIndex(n *IndexExpr) (Node, error) {
	n.Node.Accept(f)
	f.sb.WriteString("[")
	n.Index.Accept(f)
	f.sb.WriteString("]")
	return n, nil
}

func (f *formatter) VisitDot(n *DotExpr) (Node, error) {
	n.Object.Accept(f)
	f.sb.WriteString(".")
	f.sb.WriteString(n.Property)
	return n, nil
}

func (f *formatter) VisitParen(n *ParenExpr) (Node, error) {
	f.sb.WriteString("(")
	n.Sub.Accept(f)
	f.sb.WriteString(")")
	return n, nil
}

func (f *formatter) VisitTernary(n *TernaryExpr) (Node, error) {
	n.Cond.Accept(f)
	f.sb.WriteString("?")
	n.Then.Accept(f)
	f.sb.WriteString(":")
	n.Else.Accept(f)
	return n, nil
}

func (f *formatter) VisitFunc(n *FuncExpr) (Node, error) {
	f.sb.WriteString(n.Name)
	f.sb.WriteString("(")
	for i, expr := range n.ArgNames {
		if i > 0 {
			f.sb.WriteString(", ")
		}
		expr.Accept(f)
	}
	f.sb.WriteString(")")
	n.Body.Accept(f)
	return n, nil
}

// ---------- Stmt ----------

func (f *formatter) VisitIf(n *IfStmt) (Node, error) {
	f.writeIndent()
	f.sb.WriteString("if ")
	n.Cond.Accept(f)
	n.Then.Accept(f)
	if n.Else != nil {
		f.sb.WriteString(" else ")
		n.Else.Accept(f)
	}
	return n, nil
}

func (f *formatter) VisitWhile(n *WhileStmt) (Node, error) {
	f.writeIndent()
	f.sb.WriteString("while (")
	n.Cond.Accept(f)
	f.sb.WriteString(") ")
	n.Body.Accept(f)
	return n, nil
}

func (f *formatter) VisitBlock(n *BlockStmt) (Node, error) {
	f.sb.WriteString("{\n")
	f.level++
	for _, stmt := range n.Statements {
		stmt.Accept(f)
		f.sb.WriteString("\n")
	}
	f.level--
	f.writeIndent()
	f.sb.WriteString("}")
	return n, nil
}

func (f *formatter) VisitReturn(n *ReturnStmt) (Node, error) {
	f.writeIndent()
	f.sb.WriteString("return")
	if n.Value != nil {
		f.sb.WriteString(" ")
		n.Value.Accept(f)
		f.sb.WriteString("\n")
	}
	return n, nil
}

func (f *formatter) VisitBreak(n *BreakStmt) (Node, error) {
	f.writeIndent()
	f.sb.WriteString("break\n")
	return n, nil
}

func (f *formatter) VisitExpr(n *ExprStmt) (Node, error) {
	f.writeIndent()
	n.Expr.Accept(f)
	return n, nil
}

func (f *formatter) VisitProgram(n *Program) (Node, error) {
	for _, stmt := range n.Statements {
		stmt.Accept(f)
		f.sb.WriteString("\n")
	}
	return n, nil
}

func (f *formatter) writeIndent() {
	f.sb.WriteString(strings.Repeat(f.ident, f.level))
}
