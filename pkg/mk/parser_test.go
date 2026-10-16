package mk

import (
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestParser_ParseExpr(t *testing.T) {
	tests := []struct {
		name string
		ctx  *CompilationContext
		want Expression
	}{
		{
			name: "ident",
			ctx:  NewCompilationContext("test.mk", `id`),
			want: &IdentExpr{pos: Pos{1, 1}, Id: "id"},
		},
		{
			name: "int",
			ctx:  NewCompilationContext("test.mk", `10`),
			want: &IntLitExpr{pos: Pos{1, 1}, Value: 10},
		},
		{
			name: "int32-max",
			ctx:  NewCompilationContext("test.mk", strconv.FormatInt(math.MaxInt32, 10)),
			want: &IntLitExpr{pos: Pos{1, 1}, Value: math.MaxInt32},
		},
		{
			name: "int32-max+1",
			ctx:  NewCompilationContext("test.mk", strconv.FormatInt(math.MaxInt32+1, 10)),
			want: &IntLitExpr{pos: Pos{1, 1}, Value: math.MaxInt32},
		},
		{
			name: "-int32",
			ctx:  NewCompilationContext("test.mk", "-"+strconv.FormatInt(math.MaxInt32+1, 10)),
			want: &IntLitExpr{pos: Pos{1, 1}, Value: math.MinInt32},
		},
		{
			name: "-int32",
			ctx:  NewCompilationContext("test.mk", "-"+strconv.FormatInt(math.MaxInt32+2, 10)),
			want: &IntLitExpr{pos: Pos{1, 1}, Value: math.MinInt32},
		},
		{
			name: "decimal",
			ctx:  NewCompilationContext("test.mk", `10.25`),
			want: &DecimalLitExpr{pos: Pos{1, 1}, Value: "10.25"},
		},
		{
			name: "bool",
			ctx:  NewCompilationContext("test.mk", `true`),
			want: &BoolLitExpr{pos: Pos{1, 1}, Value: true},
		},
		{
			name: "string",
			ctx:  NewCompilationContext("test.mk", `"tom hanks"`),
			want: &StringLitExpr{pos: Pos{1, 1}, Value: "tom hanks"},
		},
		{
			name: "map",
			ctx:  NewCompilationContext("test.mk", `{"a":"10", "b": 10}`),
			want: &MapLitExpr{pos: Pos{1, 1}, Keys: []string{"a", "b"}, Values: map[string]Expression{"a": &StringLitExpr{pos: Pos{1, 6}, Value: "10"}, "b": &IntLitExpr{pos: Pos{1, 17}, Value: 10}}},
		},
		{
			name: "list",
			ctx:  NewCompilationContext("test.mk", `["10", 10]`),
			want: &ListLitExpr{pos: Pos{1, 1}, Values: []Expression{&StringLitExpr{pos: Pos{1, 2}, Value: "10"}, &IntLitExpr{pos: Pos{1, 8}, Value: 10}}},
		},
		{
			name: "arithmetic",
			ctx:  NewCompilationContext("test.mk", `10+20*(3-5)`),
			want: &ArithmeticExpr{Left: &IntLitExpr{pos: Pos{1, 1}, Value: 10}, Op: PLUS, Right: &ArithmeticExpr{Left: &IntLitExpr{pos: Pos{1, 4}, Value: 20}, Op: STAR, Right: &ParenExpr{pos: Pos{1, 7}, Sub: &ArithmeticExpr{Left: &IntLitExpr{pos: Pos{1, 8}, Value: 3}, Op: MINUS, Right: &IntLitExpr{pos: Pos{1, 10}, Value: 5}}}}},
		},
		{
			name: "comparison",
			ctx:  NewCompilationContext("test.mk", `a>b && c==d`),
			want: &ComparisonExpr{Left: &ComparisonExpr{Left: &IdentExpr{pos: Pos{1, 1}, Id: "a"}, Op: GT, Right: &IdentExpr{pos: Pos{1, 3}, Id: "b"}}, Op: LOG_AND, Right: &ComparisonExpr{Left: &IdentExpr{pos: Pos{1, 8}, Id: "c"}, Op: EQ, Right: &IdentExpr{pos: Pos{1, 11}, Id: "d"}}},
		},
		{
			name: "dot-string",
			ctx:  NewCompilationContext("test.mk", `$.header`),
			want: &DotExpr{pos: Pos{1, 2}, Object: &CtxExpr{pos: Pos{1, 1}}, Property: &IdentExpr{pos: Pos{1, 3}, Id: "header"}},
		},
		{
			name: "dot-call",
			ctx:  NewCompilationContext("test.mk", `$.header()`),
			want: &CallExpr{pos: Pos{1, 9}, Func: &DotExpr{pos: Pos{1, 2}, Object: &CtxExpr{pos: Pos{1, 1}}, Property: &IdentExpr{pos: Pos{1, 3}, Id: "header"}}, Args: []Expression{}},
		},
		{
			name: "question",
			ctx:  NewCompilationContext("test.mk", `a?b:c`),
			want: &TernaryExpr{pos: Pos{1, 2}, Cond: &IdentExpr{pos: Pos{1, 1}, Id: "a"}, Then: &IdentExpr{pos: Pos{1, 3}, Id: "b"}, Else: &IdentExpr{pos: Pos{1, 5}, Id: "c"}},
		},
		{
			name: "unary",
			ctx:  NewCompilationContext("test.mk", `!a`),
			want: &UnaryExpr{pos: Pos{1, 1}, Op: BANG, Right: &IdentExpr{pos: Pos{1, 2}, Id: "a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.ctx.parser
			got := p.ParseExpr()
			if !reflect.DeepEqual(got, tt.want) {
				for _, diagnostic := range tt.ctx.errors.diagnostics {
					t.Error(diagnostic.String())
				}
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParser_ParseStmt(t *testing.T) {
	tests := []struct {
		name string
		ctx  *CompilationContext
		want Statement
	}{
		{
			name: "if",
			ctx:  NewCompilationContext("test.mk", `if ( a ) { c = 10;}`),
			want: &IfStmt{pos: Pos{1, 1}, Cond: &IdentExpr{pos: Pos{1, 6}, Id: "a"}, Then: &BlockStmt{pos: Pos{1, 10}, Statements: []Statement{&ExprStmt{pos: Pos{1, 14}, Expr: &AssignExpr{pos: Pos{1, 14}, Name: &IdentExpr{pos: Pos{1, 12}, Id: "c"}, Value: &IntLitExpr{pos: Pos{1, 16}, Value: 10}}}}}},
		},
		{
			name: "while",
			ctx:  NewCompilationContext("test.mk", `while ( a ) { c = 10;}`),
			want: &WhileStmt{pos: Pos{1, 1}, Cond: &IdentExpr{pos: Pos{1, 9}, Id: "a"}, Body: &BlockStmt{pos: Pos{1, 13}, Statements: []Statement{&ExprStmt{pos: Pos{1, 17}, Expr: &AssignExpr{pos: Pos{1, 17}, Name: &IdentExpr{pos: Pos{1, 15}, Id: "c"}, Value: &IntLitExpr{pos: Pos{1, 19}, Value: 10}}}}}},
		},
		{
			name: "return-void",
			ctx:  NewCompilationContext("test.mk", `return;`),
			want: &ReturnStmt{pos: Pos{1, 1}},
		},
		{
			name: "return-expr",
			ctx:  NewCompilationContext("test.mk", `return a;`),
			want: &ReturnStmt{pos: Pos{1, 1}, Value: &IdentExpr{pos: Pos{1, 8}, Id: "a"}},
		},
		{
			name: "break-void",
			ctx:  NewCompilationContext("test.mk", `break;`),
			want: &BreakStmt{pos: Pos{1, 1}},
		},
		{
			name: "break-expr",
			ctx:  NewCompilationContext("test.mk", `break a;`),
			want: &BreakStmt{pos: Pos{1, 1}, Label: &IdentExpr{pos: Pos{1, 7}, Id: "a"}},
		},
		{
			name: "fn",
			ctx:  NewCompilationContext("test.mk", `fn a10(x,y) {}`),
			want: &FuncStmt{pos: Pos{1, 1}, Name: &IdentExpr{pos: Pos{1, 4}, Id: "a10"}, ArgNames: []*IdentExpr{{pos: Pos{1, 8}, Id: "x"}, {pos: Pos{1, 10}, Id: "y"}}, ArgTypes: []*IdentExpr{nil, nil}, Body: &BlockStmt{pos: Pos{1, 13}, Statements: []Statement{}}},
		},
		{
			name: "fn2",
			ctx:  NewCompilationContext("test.mk", `fn a10(x int,y string,z bool) {}`),
			want: &FuncStmt{pos: Pos{1, 1}, Name: &IdentExpr{pos: Pos{1, 4}, Id: "a10"}, ArgNames: []*IdentExpr{{pos: Pos{1, 8}, Id: "x"}, {pos: Pos{1, 14}, Id: "y"}, {pos: Pos{1, 23}, Id: "z"}}, ArgTypes: []*IdentExpr{{pos: Pos{1, 10}, Id: "int"}, {pos: Pos{1, 16}, Id: "string"}, {pos: Pos{1, 25}, Id: "bool"}}, Body: &BlockStmt{pos: Pos{1, 31}, Statements: []Statement{}}},
		},
		{
			name: "let",
			ctx:  NewCompilationContext("test.mk", `let x = -2147483648;`),
			want: &LetStmt{pos: Pos{1, 1}, Name: &IdentExpr{pos: Pos{1, 5}, Id: "x"}, Value: &IntLitExpr{pos: Pos{1, 9}, Value: math.MinInt32}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.ctx.parser
			got := p.ParseStmt()
			if !reflect.DeepEqual(got, tt.want) {
				for _, diagnostic := range tt.ctx.errors.diagnostics {
					t.Error(diagnostic.String())
				}
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParser_Positions(t *testing.T) {
	// A. 真实位置：10+20*(3-5) > a（1 起算）
	//    1=1 0=2 +=3 2=4 0=5 *=6 (=7 3=8 -=9 5=10 )=11 sp=12 >=13 sp=14 a=15
	ctx := NewCompilationContext("test.mk", `10+20*(3-5) > a`)
	root := ctx.parser.ParseExpr()
	check := func(n Node, want Pos, what string) {
		if n.Pos() != want {
			t.Errorf("%s: %T.Pos() = %v, want %v", what, n, n.Pos(), want)
		}
	}
	cmp, ok := root.(*ComparisonExpr)
	if !ok {
		t.Fatalf("root = %T, want *ComparisonExpr", root)
	}
	ar, ok := cmp.Left.(*ArithmeticExpr)
	if !ok {
		t.Fatalf("Left = %T, want *ArithmeticExpr", cmp.Left)
	}
	mul, ok := ar.Right.(*ArithmeticExpr) // 20*(3-5)：* 比 + 后结合
	if !ok {
		t.Fatalf("Right = %T, want *ArithmeticExpr", ar.Right)
	}
	inner, ok := mul.Right.(*ParenExpr)
	if !ok {
		t.Fatalf("mul.Right = %T, want *ParenExpr", mul.Right)
	}
	check(root, Pos{1, 1}, "根 >（无 pos 字段，委托最左 10）")
	check(cmp.Left, Pos{1, 1}, "加号 +（委托最左 10）")
	check(ar.Left, Pos{1, 1}, "10")
	check(mul.Left, Pos{1, 4}, "20")
	check(inner, Pos{1, 7}, "括号（锚点是 (）")
	check(inner.Sub, Pos{1, 8}, "减号 -（委托最左 3）")
	check(cmp.Right, Pos{1, 15}, "a")

	left := &IntLitExpr{pos: Pos{Line: 3, Column: 7}}
	if (&ArithmeticExpr{Left: left, Op: PLUS, Right: &IntLitExpr{pos: Pos{Line: 3, Column: 11}}}).Pos() != (Pos{Line: 3, Column: 7}) {
		t.Error("ArithmeticExpr.Pos() 应等于 Left.Pos()")
	}
	if (&ComparisonExpr{Left: left, Op: GT, Right: &IdentExpr{pos: Pos{Line: 3, Column: 11}}}).Pos() != (Pos{Line: 3, Column: 7}) {
		t.Error("ComparisonExpr.Pos() 应等于 Left.Pos()")
	}
	nested := &ArithmeticExpr{Left: &ArithmeticExpr{Left: left, Op: PLUS, Right: &IntLitExpr{pos: Pos{Line: 3, Column: 11}}}, Op: STAR, Right: &IntLitExpr{pos: Pos{Line: 4, Column: 1}}}
	if nested.Pos() != (Pos{Line: 3, Column: 7}) {
		t.Errorf("嵌套 (a+b)*c 的 Pos() 应一路委托到最左: got %v", nested.Pos())
	}

	br := NewCompilationContext("test.mk", `break a;`).parser.ParseStmt().(*BreakStmt)
	if br.Label != nil && br.Label.Pos() != (Pos{1, 7}) {
		t.Errorf("BreakStmt.Label.Pos() = %v；parseBreak 会给 Label 接线后应改为真实位置", br.Label.Pos())
	}
	ifStmt := NewCompilationContext("test.mk", `if ( a ) { c = 10;}`).parser.ParseStmt().(*IfStmt)
	es := ifStmt.Then.Statements[0].(*ExprStmt)
	if es.Pos() != (Pos{1, 14}) {
		t.Errorf("ExprStmt.Pos() = %v；ParseStmt 的 default 分支会给 ExprStmt 接线后应改为真实位置", es.Pos())
	}
}
