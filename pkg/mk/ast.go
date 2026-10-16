package mk

type Visitor interface {

	// ---------- Expr ----------

	VisitIdent(n *IdentExpr) (Node, error)
	VisitInt(n *IntLitExpr) (Node, error)
	VisitBool(n *BoolLitExpr) (Node, error)
	VisitString(n *StringLitExpr) (Node, error)
	VisitMap(n *MapLitExpr) (Node, error)
	VisitList(n *ListLitExpr) (Node, error)
	VisitNull(n *NullLitExpr) (Node, error)
	VisitDecimal(n *DecimalLitExpr) (Node, error)
	VisitArithmetic(n *ArithmeticExpr) (Node, error)
	VisitComparison(n *ComparisonExpr) (Node, error)
	VisitUnary(n *UnaryExpr) (Node, error)
	VisitCtx(n *CtxExpr) (Node, error)
	VisitCall(n *CallExpr) (Node, error)
	VisitIndex(n *IndexExpr) (Node, error)
	VisitDot(n *DotExpr) (Node, error)
	VisitParen(n *ParenExpr) (Node, error)
	VisitTernary(n *TernaryExpr) (Node, error)

	// ---------- Stmt ----------

	VisitFunc(n *FuncStmt) (Node, error)
	VisitLet(n *LetStmt) (Node, error)
	VisitIf(n *IfStmt) (Node, error)
	VisitWhile(n *WhileStmt) (Node, error)
	VisitBlock(n *BlockStmt) (Node, error)
	VisitAssign(n *AssignExpr) (Node, error)
	VisitReturn(n *ReturnStmt) (Node, error)
	VisitBreak(n *BreakStmt) (Node, error)
	VisitExpr(n *ExprStmt) (Node, error)

	VisitProgram(n *Program) (Node, error)
}

type Pos struct {
	Line   int
	Column int
}

type Node interface {
	Pos() Pos
	sealed()
	Accept(Visitor) (Node, error)
}

type Expression interface {
	Node
	expr()
}

type Statement interface {
	Node
	stmt()
}

type IdentExpr struct {
	pos Pos

	Id string
}

type litExpr[T int32 | bool | string] struct {
	pos Pos

	Value T
}

type IntLitExpr litExpr[int32]

type BoolLitExpr litExpr[bool]

type StringLitExpr litExpr[string]

type DecimalLitExpr litExpr[string]

type NullLitExpr struct {
	pos Pos
}

type MapLitExpr struct {
	pos Pos

	Keys   []string // 保证顺序
	Values map[string]Expression
}

type ListLitExpr struct {
	pos Pos

	Values []Expression
}

type ArithmeticExpr struct {
	Left  Expression
	Op    TokenType
	Right Expression
}

type ComparisonExpr struct {
	Left  Expression
	Op    TokenType
	Right Expression
}

type UnaryExpr struct {
	pos Pos

	Op    TokenType
	Right Expression
}

// CtxExpr 表示请求上下文 $
type CtxExpr struct {
	pos Pos
}

type CallExpr struct {
	pos Pos

	Func Expression
	Args []Expression
}

type IndexExpr struct {
	pos Pos

	Node  Expression
	Index Expression
}

type DotExpr struct {
	pos Pos

	Object   Expression
	Property *IdentExpr
}

type ParenExpr struct {
	pos Pos

	Sub Expression
}

// TernaryExpr 三元运算符
type TernaryExpr struct {
	pos Pos

	Cond Expression
	Then Expression
	Else Expression
}

type AssignExpr struct {
	pos Pos

	Name  Expression
	Value Expression
}

type LetStmt struct {
	pos Pos

	Name  *IdentExpr
	Type  *IdentExpr
	Value Expression
}

type IfStmt struct {
	pos Pos

	Cond Expression
	Then *BlockStmt
	Else *BlockStmt
}

type WhileStmt struct {
	pos Pos

	Cond Expression
	Body *BlockStmt
}

type BlockStmt struct {
	pos Pos

	Statements []Statement
}

type ReturnStmt struct {
	pos Pos

	Value Expression
}

type BreakStmt struct {
	pos Pos

	Label *IdentExpr
}

type ExprStmt struct {
	pos Pos

	Expr Expression
}

type FuncStmt struct {
	pos Pos

	Name     *IdentExpr
	ArgNames []*IdentExpr
	ArgTypes []*IdentExpr
	Body     *BlockStmt
	RetType  *IdentExpr
}

type Program struct {
	pos Pos

	Statements []Statement
}

func (n *IdentExpr) sealed()      {}
func (n *IntLitExpr) sealed()     {}
func (n *BoolLitExpr) sealed()    {}
func (n *StringLitExpr) sealed()  {}
func (n *MapLitExpr) sealed()     {}
func (n *ListLitExpr) sealed()    {}
func (n *NullLitExpr) sealed()    {}
func (n *DecimalLitExpr) sealed() {}
func (n *ArithmeticExpr) sealed() {}
func (n *ComparisonExpr) sealed() {}
func (n *AssignExpr) sealed()     {}
func (n *UnaryExpr) sealed()      {}
func (n *CtxExpr) sealed()        {}
func (n *CallExpr) sealed()       {}
func (n *IndexExpr) sealed()      {}
func (n *DotExpr) sealed()        {}
func (n *ParenExpr) sealed()      {}
func (n *TernaryExpr) sealed()    {}
func (n *FuncStmt) sealed()       {}

func (n *LetStmt) sealed()    {}
func (n *IfStmt) sealed()     {}
func (n *WhileStmt) sealed()  {}
func (n *BlockStmt) sealed()  {}
func (n *ReturnStmt) sealed() {}
func (n *BreakStmt) sealed()  {}
func (n *ExprStmt) sealed()   {}
func (n *Program) sealed()    {}

func (n *IdentExpr) expr()      {}
func (n *IntLitExpr) expr()     {}
func (n *BoolLitExpr) expr()    {}
func (n *StringLitExpr) expr()  {}
func (n *MapLitExpr) expr()     {}
func (n *ListLitExpr) expr()    {}
func (n *NullLitExpr) expr()    {}
func (n *DecimalLitExpr) expr() {}
func (n *ArithmeticExpr) expr() {}
func (n *ComparisonExpr) expr() {}
func (n *UnaryExpr) expr()      {}
func (n *CtxExpr) expr()        {}
func (n *CallExpr) expr()       {}
func (n *IndexExpr) expr()      {}
func (n *DotExpr) expr()        {}
func (n *ParenExpr) expr()      {}
func (n *TernaryExpr) expr()    {}
func (n *AssignExpr) expr()     {}

func (n *IdentExpr) Pos() Pos      { return n.pos }
func (n *IntLitExpr) Pos() Pos     { return n.pos }
func (n *BoolLitExpr) Pos() Pos    { return n.pos }
func (n *StringLitExpr) Pos() Pos  { return n.pos }
func (n *MapLitExpr) Pos() Pos     { return n.pos }
func (n *ListLitExpr) Pos() Pos    { return n.pos }
func (n *NullLitExpr) Pos() Pos    { return n.pos }
func (n *DecimalLitExpr) Pos() Pos { return n.pos }
func (n *ArithmeticExpr) Pos() Pos { return n.Left.Pos() }
func (n *ComparisonExpr) Pos() Pos { return n.Left.Pos() }
func (n *AssignExpr) Pos() Pos     { return n.pos }
func (n *UnaryExpr) Pos() Pos      { return n.pos }
func (n *CtxExpr) Pos() Pos        { return n.pos }
func (n *CallExpr) Pos() Pos       { return n.pos }
func (n *IndexExpr) Pos() Pos      { return n.pos }
func (n *DotExpr) Pos() Pos        { return n.pos }
func (n *ParenExpr) Pos() Pos      { return n.pos }
func (n *TernaryExpr) Pos() Pos    { return n.pos }
func (n *FuncStmt) Pos() Pos       { return n.pos }
func (n *LetStmt) Pos() Pos        { return n.pos }
func (n *IfStmt) Pos() Pos         { return n.pos }
func (n *WhileStmt) Pos() Pos      { return n.pos }
func (n *BlockStmt) Pos() Pos      { return n.pos }
func (n *ReturnStmt) Pos() Pos     { return n.pos }
func (n *BreakStmt) Pos() Pos      { return n.pos }
func (n *ExprStmt) Pos() Pos       { return n.pos }
func (n *Program) Pos() Pos        { return n.pos }

func (n *FuncStmt) stmt()   {}
func (n *LetStmt) stmt()    {}
func (n *IfStmt) stmt()     {}
func (n *WhileStmt) stmt()  {}
func (n *BlockStmt) stmt()  {}
func (n *ReturnStmt) stmt() {}
func (n *BreakStmt) stmt()  {}
func (n *ExprStmt) stmt()   {}
func (n *Program) stmt()    {}

func (n *IdentExpr) Accept(v Visitor) (Node, error)      { return v.VisitIdent(n) }
func (n *IntLitExpr) Accept(v Visitor) (Node, error)     { return v.VisitInt(n) }
func (n *BoolLitExpr) Accept(v Visitor) (Node, error)    { return v.VisitBool(n) }
func (n *StringLitExpr) Accept(v Visitor) (Node, error)  { return v.VisitString(n) }
func (n *MapLitExpr) Accept(v Visitor) (Node, error)     { return v.VisitMap(n) }
func (n *ListLitExpr) Accept(v Visitor) (Node, error)    { return v.VisitList(n) }
func (n *NullLitExpr) Accept(v Visitor) (Node, error)    { return v.VisitNull(n) }
func (n *DecimalLitExpr) Accept(v Visitor) (Node, error) { return v.VisitDecimal(n) }
func (n *ArithmeticExpr) Accept(v Visitor) (Node, error) { return v.VisitArithmetic(n) }
func (n *ComparisonExpr) Accept(v Visitor) (Node, error) { return v.VisitComparison(n) }
func (n *UnaryExpr) Accept(v Visitor) (Node, error)      { return v.VisitUnary(n) }
func (n *CtxExpr) Accept(v Visitor) (Node, error)        { return v.VisitCtx(n) }
func (n *CallExpr) Accept(v Visitor) (Node, error)       { return v.VisitCall(n) }
func (n *IndexExpr) Accept(v Visitor) (Node, error)      { return v.VisitIndex(n) }
func (n *DotExpr) Accept(v Visitor) (Node, error)        { return v.VisitDot(n) }
func (n *ParenExpr) Accept(v Visitor) (Node, error)      { return v.VisitParen(n) }
func (n *TernaryExpr) Accept(v Visitor) (Node, error)    { return v.VisitTernary(n) }
func (n *FuncStmt) Accept(v Visitor) (Node, error)       { return v.VisitFunc(n) }
func (n *LetStmt) Accept(v Visitor) (Node, error)        { return v.VisitLet(n) }
func (n *IfStmt) Accept(v Visitor) (Node, error)         { return v.VisitIf(n) }
func (n *WhileStmt) Accept(v Visitor) (Node, error)      { return v.VisitWhile(n) }
func (n *BlockStmt) Accept(v Visitor) (Node, error)      { return v.VisitBlock(n) }
func (n *AssignExpr) Accept(v Visitor) (Node, error)     { return v.VisitAssign(n) }
func (n *ReturnStmt) Accept(v Visitor) (Node, error)     { return v.VisitReturn(n) }
func (n *BreakStmt) Accept(v Visitor) (Node, error)      { return v.VisitBreak(n) }
func (n *ExprStmt) Accept(v Visitor) (Node, error)       { return v.VisitExpr(n) }
func (n *Program) Accept(v Visitor) (Node, error)        { return v.VisitProgram(n) }

func newPos(line, column int) Pos {
	return Pos{Line: line, Column: column}
}
