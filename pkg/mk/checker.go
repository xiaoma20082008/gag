package mk

import "fmt"

// ===========================================================================
// 语义分析器（Checker）
// ===========================================================================

type Checker struct {
	errors *DiagnosticBag
	symtab *SymbolTable

	loopDepth int
	funcDepth int

	returnStack []*TypeDef
	returnType  *TypeDef

	ImplicitDeclare bool
}

func NewChecker(ctx *CompilationContext) *Checker {
	c := &Checker{
		errors: ctx.errors,
		symtab: ctx.symtab,
	}
	c.registerBuiltins()
	return c
}

func (c *Checker) registerBuiltins() {
	for name, t := range Builtins {
		_ = c.symtab.DefineSymbol(&Symbol{
			Name: name,
			Kind: KindBuiltin,
			Type: t,
		})
	}
}

func (c *Checker) Check(n Node) {
	if n == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			c.report(ErrCheckerInternal, fmt.Sprint(r))
		}
	}()
	n.Accept(c)
}

// ---------- Expr ----------

func (c *Checker) VisitIdent(n *IdentExpr) (Node, error) {
	sym, ok := c.symtab.Resolve(n.Id)
	if !ok {
		c.report(ErrUndefinedIdentifierStatic, n.Id)
		c.setType(SysObject)
		return n, nil
	}
	c.setType(sym.Type)
	return n, nil
}

func (c *Checker) VisitAssign(n *AssignExpr) (Node, error) {
	if !c.isLValue(n.Name) {
		name := Format(n.Name)
		c.report(ErrInvalidLValue, name)
		c.checkExpr(n.Value)
		c.setType(SysObject)
		return n, nil
	}

	switch target := n.Name.(type) {
	case *IdentExpr:
		valueType := c.checkExpr(n.Value)
		sym, ok := c.symtab.Resolve(target.Id)
		if !ok {
			if c.ImplicitDeclare {
				_, _ = c.symtab.Define(target.Id, KindVariable, valueType)
			} else {
				c.report(ErrUndefinedIdentifierStatic, target.Id)
			}
			c.setType(valueType)
			return n, nil
		}
		if sym.Constant {
			c.report(ErrAssignToImmutable, target.Id)
		} else if !AssignableTo(valueType, sym.Type) {
			c.report(ErrTypeMismatchAssign, valueType.String(), sym.Type.String())
		}
		c.setType(valueType)

	case *DotExpr, *IndexExpr:
		// $.header["x"] = ... ：接收者动态，元素类型未知
		c.checkExpr(n.Name)
		c.setType(c.checkExpr(n.Value))

	default:
		c.checkExpr(n.Value)
		c.setType(SysObject)
	}
	return n, nil
}

func (c *Checker) VisitInt(n *IntLitExpr) (Node, error) {
	c.setType(SysInt32)
	return n, nil
}

func (c *Checker) VisitBool(n *BoolLitExpr) (Node, error) {
	c.setType(SysBool)
	return n, nil
}

func (c *Checker) VisitString(n *StringLitExpr) (Node, error) {
	c.setType(SysString)
	return n, nil
}

func (c *Checker) VisitMap(n *MapLitExpr) (Node, error) {
	seen := make(map[string]bool, len(n.Keys))
	for _, k := range n.Keys {
		if seen[k] {
			c.report(ErrDuplicateKey, k)
		}
		seen[k] = true
	}
	for _, expr := range n.Values {
		c.checkExpr(expr)
	}
	c.setType(SysMap)
	return n, nil
}

func (c *Checker) VisitList(n *ListLitExpr) (Node, error) {
	for _, expr := range n.Values {
		c.checkExpr(expr)
	}
	c.setType(SysList)
	return n, nil
}

func (c *Checker) VisitNull(n *NullLitExpr) (Node, error) {
	c.setType(SysObject)
	return n, nil
}

func (c *Checker) VisitDecimal(n *DecimalLitExpr) (Node, error) {
	c.setType(SysDecimal)
	return n, nil
}

func (c *Checker) VisitArithmetic(n *ArithmeticExpr) (Node, error) {
	lhs := c.checkExpr(n.Left)
	rhs := c.checkExpr(n.Right)

	switch n.Op {
	case PLUS:
		// 字符串拼接：任一侧是字符串（或未知）就按字符串处理
		if lhs == SysString || rhs == SysString {
			c.setType(SysString)
			return n, nil
		}
		if !isNumericish(lhs) || !isNumericish(rhs) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		}
		c.setType(numericResult(lhs, rhs))
	case MINUS, STAR, SLASH:
		if !isNumericish(lhs) || !isNumericish(rhs) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		}
		c.setType(numericResult(lhs, rhs))
	case PERCENT, LTLT, GTGT, BIT_AND, BIT_OR, BIT_XOR:
		if !isIntish(lhs) || !isIntish(rhs) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		}
		c.setType(numericResult(lhs, rhs))
	default:
		c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		c.setType(SysObject)
	}
	return n, nil
}

func (c *Checker) VisitComparison(n *ComparisonExpr) (Node, error) {
	lhs := c.checkExpr(n.Left)
	rhs := c.checkExpr(n.Right)
	switch n.Op {
	case AND, OR, LOG_AND, LOG_OR:
		if !isBoolish(lhs) || !isBoolish(rhs) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		}
	case EQ, NE:
		if !comparable(lhs, rhs) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		}
	case LT, LE, GT, GE:
		if !ordered(lhs, rhs) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
		}
	default:
		c.report(ErrTypeMismatchOperator, n.Op.String(), lhs.String(), rhs.String())
	}
	c.setType(SysBool)
	return n, nil
}

func (c *Checker) VisitUnary(n *UnaryExpr) (Node, error) {
	t := c.checkExpr(n.Right)
	switch n.Op {
	case BANG:
		if !isBoolish(t) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), t.String(), t.String())
		}
		c.setType(SysBool)
	case PLUS, MINUS:
		if !isNumericish(t) {
			c.report(ErrTypeMismatchOperator, n.Op.String(), t.String(), t.String())
		}
		c.setType(numericResult(t, t))
	case TILDE:
		switch {
		case isIntish(t):
			c.setType(SysInt32)
		case isBoolish(t):
			c.setType(SysBool)
		default:
			c.report(ErrTypeMismatchOperator, n.Op.String(), t.String(), t.String())
			c.setType(SysObject)
		}
	default:
		c.report(ErrTypeMismatchOperator, n.Op.String(), t.String(), t.String())
		c.setType(SysObject)
	}
	return n, nil
}

func (c *Checker) VisitCtx(n *CtxExpr) (Node, error) {
	c.setType(SysObject)
	return n, nil
}

func (c *Checker) VisitCall(n *CallExpr) (Node, error) {
	callee := unwrapParen(n.Func)

	switch f := callee.(type) {
	case *IdentExpr:
		sym, ok := c.symtab.Resolve(f.Id)
		if !ok {
			c.report(ErrUndefinedFunction, f.Id) // C13
			c.checkArgs(n.Args, nil)
			c.setType(SysObject)
			return n, nil
		}
		switch sig := sym.Type.Signature(); {
		case sig != nil:
			// 有签名：校验个数与类型（C10 / C12），返回类型取签名
			c.checkArgs(n.Args, sig)
			c.setType(sig.ReturnType)
		case sym.Type == SysFunction || sym.Kind == KindFunction || sym.Kind == KindBuiltin:
			// 是函数值但拿不到签名（如 let f = fn(){}; f();）：
			// 只能查实参本身，返回 Any —— 绝不误报 C11
			c.checkArgs(n.Args, nil)
			c.setType(SysObject)
		default:
			// 明确不是函数（如 let x = 1; x();）
			c.report(ErrNotCallable, sym.Type.String()) // C11
			c.checkArgs(n.Args, nil)
			c.setType(SysObject)
		}

	case *DotExpr, *IndexExpr:
		// 方法调用：a.b(...) —— 接收者是动态的，参数照查
		c.checkExpr(callee)
		c.checkArgs(n.Args, nil)
		c.setType(SysObject) // D4

	default:
		t := c.checkExpr(n.Func)
		c.report(ErrNotCallable, t.String()) // C11
		c.checkArgs(n.Args, nil)
		c.setType(SysObject)
	}
	return n, nil
}

func (c *Checker) VisitIndex(n *IndexExpr) (Node, error) {
	obj := c.checkExpr(n.Node)
	idx := c.checkExpr(n.Index)

	switch {
	case obj == nil || obj.IsDynamic():
	case obj == SysMap:
		if !isStringish(idx) {
			c.report(ErrTypeMismatchOperator, "[]", obj.String(), idx.String())
		}
	case obj == SysList:
		if !isIntish(idx) {
			c.report(ErrTypeMismatchOperator, "[]", obj.String(), idx.String())
		}
	case obj == SysString:
		if !isIntish(idx) {
			c.report(ErrTypeMismatchOperator, "[]", obj.String(), idx.String())
		}
		c.setType(SysString)
		return n, nil
	default:
		c.report(ErrNotIndexable, obj.String())
	}
	c.setType(SysObject)
	return n, nil
}

func (c *Checker) VisitDot(n *DotExpr) (Node, error) {
	obj := c.checkExpr(n.Object)
	if !fieldAccessible(obj) {
		c.report(ErrNoFieldAccess, obj.String())
	}
	c.setType(SysObject)
	return n, nil
}

func (c *Checker) VisitParen(n *ParenExpr) (Node, error) {
	c.setType(c.checkExpr(n.Sub))
	return n, nil
}

func (c *Checker) VisitTernary(n *TernaryExpr) (Node, error) {
	cond := c.checkExpr(n.Cond)
	if !isBoolish(cond) {
		c.report(ErrConditionNotBool, cond.String())
	}
	thenT := c.checkExpr(n.Then)
	elseT := c.checkExpr(n.Else)
	c.setType(Unify(thenT, elseT))
	return n, nil
}

// ---------- Stmt ----------

func (c *Checker) VisitFunc(n *FuncStmt) (Node, error) {
	paramTypes := make([]*TypeDef, len(n.ArgNames))
	for i := range n.ArgNames {
		pt := SysObject
		if i < len(n.ArgTypes) && n.ArgTypes[i] != nil {
			if t, ok := LookupType(n.ArgTypes[i].Id); ok { // C1
				pt = t
			} else {
				c.report(ErrUnknownType, n.ArgTypes[i].Id)
			}
		}
		paramTypes[i] = pt
	}

	var returnType *TypeDef
	if n.RetType == nil {
		returnType = SysObject
	} else if t := c.resolveType(n.RetType); t != nil {
		returnType = t
	} else {
		returnType = SysObject
	}

	// 函数类型
	ft := &TypeDef{
		Name: "fn " + n.Name,
		Kind: TypeFunction,
		FunctionMetadata: &FunctionMetadata{
			Name:           n.Name,
			ParameterTypes: paramTypes,
			ReturnType:     returnType,
			MinArgs:        len(paramTypes),
			MaxArgs:        len(paramTypes),
		},
	}

	if n.Name != "" {
		if _, err := c.symtab.Define(n.Name, KindFunction, ft); err != nil {
			c.report(ErrRedefinedIdentifier, n.Name)
		}
	}

	c.symtab.Enter("fn " + n.Name)
	defer c.symtab.Leave()

	for i, argName := range n.ArgNames {
		if _, err := c.symtab.Define(argName.Id, KindParam, paramTypes[i]); err != nil {
			c.report(ErrRedefinedIdentifier, argName.Id)
		}
	}

	c.funcDepth++
	c.returnStack = append(c.returnStack, returnType)
	if n.Body != nil {
		n.Body.Accept(c)
	}
	c.returnStack = c.returnStack[:len(c.returnStack)-1]
	c.funcDepth--

	c.setType(ft)
	return n, nil
}

func (c *Checker) VisitLet(n *LetStmt) (Node, error) {
	var declared *TypeDef
	if n.Type != nil {
		if t := c.resolveType(n.Type); t != nil {
			declared = t
		} else {
			declared = SysObject
		}
	}

	valueType := SysObject
	if n.Value != nil {
		valueType = c.checkExpr(n.Value)
		if declared != nil && !AssignableTo(valueType, declared) {
			c.report(ErrTypeMismatchAssign, valueType.String(), declared.String())
		}
	}

	finalType := declared
	if finalType == nil {
		finalType = valueType // 类型推导
	}
	if _, err := c.symtab.Define(n.Name.Id, KindVariable, finalType); err != nil {
		c.report(ErrRedefinedIdentifier, n.Name.Id)
	}
	return n, nil
}

func (c *Checker) VisitIf(n *IfStmt) (Node, error) {
	cond := c.checkExpr(n.Cond)
	if !isBoolish(cond) {
		c.report(ErrConditionNotBool, cond.String())
	}
	if n.Then != nil {
		n.Then.Accept(c)
	}
	if n.Else != nil {
		n.Else.Accept(c)
	}
	return n, nil
}

func (c *Checker) VisitWhile(n *WhileStmt) (Node, error) {
	cond := c.checkExpr(n.Cond)
	if !isBoolish(cond) {
		c.report(ErrConditionNotBool, cond.String())
	}
	c.loopDepth++
	if n.Body != nil {
		n.Body.Accept(c)
	}
	c.loopDepth--
	return n, nil
}

func (c *Checker) VisitBlock(n *BlockStmt) (Node, error) {
	c.symtab.Enter("block")
	defer c.symtab.Leave()

	returned := false
	for _, stmt := range n.Statements {
		if returned {
			c.report(ErrUnreachableCode)
		}
		stmt.Accept(c)

		if _, ok := stmt.(*ReturnStmt); ok {
			returned = true
		}
	}
	return n, nil
}

func (c *Checker) VisitReturn(n *ReturnStmt) (Node, error) {
	var got *TypeDef
	if n.Value == nil {
		got = SysVoid
	} else {
		got = c.checkExpr(n.Value)
	}
	if len(c.returnStack) > 0 {
		want := c.returnStack[len(c.returnStack)-1]
		if want != nil && !AssignableTo(got, want) {
			c.report(ErrTypeMismatchReturn, want.String(), got.String())
		}
	}
	c.setType(got)
	return n, nil
}

func (c *Checker) VisitBreak(n *BreakStmt) (Node, error) {
	if c.loopDepth <= 0 {
		c.report(ErrBreakOutOfLoop)
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
			c.report(ErrUnreachableCode)
		}
		stmt.Accept(c)

		if _, ok := stmt.(*ReturnStmt); ok {
			returned = true
		}
	}
	return n, nil
}

func (c *Checker) setType(typ *TypeDef) *TypeDef {
	if typ == nil {
		typ = SysObject
	}
	c.returnType = typ
	return typ
}

func (c *Checker) checkExpr(n Expression) *TypeDef {
	if n == nil {
		return SysObject
	}
	c.returnType = nil
	n.Accept(c)
	if c.returnType == nil {
		return SysObject
	}
	return c.returnType
}

func (c *Checker) checkArgs(args []Expression, sig *FunctionMetadata) {
	if sig != nil {
		if len(args) < sig.MinArgs || (sig.MaxArgs >= 0 && len(args) > sig.MaxArgs) {
			c.report(ErrWrongArgCountStatic, sig.Name, sig.MinArgs, len(args))
		}
	}
	for i, a := range args {
		at := c.checkExpr(a)
		if sig == nil || i >= len(sig.ParameterTypes) {
			continue
		}
		want := sig.ParameterTypes[i]
		if want == nil || want.IsDynamic() {
			continue
		}
		if !AssignableTo(at, want) {
			c.report(ErrTypeMismatchArgument, i+1, want.String(), at.String())
		}
	}
}

func (c *Checker) isLValue(n Expression) bool {
	switch n.(type) {
	case *IdentExpr, *DotExpr, *IndexExpr:
		return true
	default:
		return false
	}
}

func (c *Checker) report(desc DiagnosticDescriptor, args ...any) {
	c.errors.Report(desc, 0, 0, args...)
}

func (c *Checker) resolveType(id *IdentExpr) *TypeDef {
	if id == nil {
		return nil
	}
	t, ok := LookupType(id.Id)
	if !ok {
		c.report(ErrUnknownType, id.Id)
		return nil
	}
	return t
}

func unwrapParen(e Expression) Expression {
	for {
		p, ok := e.(*ParenExpr)
		if !ok {
			return e
		}
		e = p.Sub
	}
}

// ---- 类型谓词：nil 与 Any 一律放行（D2），避免"检查不出来就报错" ----

func isBoolish(t *TypeDef) bool    { return t == nil || t.IsDynamic() || t == SysBool }
func isNumericish(t *TypeDef) bool { return t == nil || t.IsDynamic() || t.IsNumeric() }
func isIntish(t *TypeDef) bool     { return t == nil || t.IsDynamic() || t.IsInteger() }
func isStringish(t *TypeDef) bool  { return t == nil || t.IsDynamic() || t == SysString }

func fieldAccessible(t *TypeDef) bool {
	if t == nil || t.IsDynamic() {
		return true
	}
	switch t.Kind {
	case TypeClass, TypeStruct:
		return true
	}
	return false
}

// numericResult 二元数值运算的结果类型。
func numericResult(a, b *TypeDef) *TypeDef {
	if a == nil || b == nil || a.IsDynamic() || b.IsDynamic() {
		return SysObject
	}
	if a == SysDecimal || b == SysDecimal {
		return SysDecimal
	}
	return SysInt32
}

// comparable 判断两个类型能否用 == / != 比较。
func comparable(a, b *TypeDef) bool {
	if a == nil || b == nil || a.IsDynamic() || b.IsDynamic() {
		return true
	}
	if Equal(a, b) {
		return true
	}
	return false
}

// ordered 判断两个类型能否用 < <= > >= 比较。
func ordered(a, b *TypeDef) bool {
	if a == nil || b == nil || a.IsDynamic() || b.IsDynamic() {
		return true
	}
	if a.IsNumeric() && b.IsNumeric() {
		return true
	}
	return a == SysString && b == SysString
}
