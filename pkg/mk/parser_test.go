package mk

import (
	"reflect"
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
			want: &IdentExpr{Id: "id"},
		},
		{
			name: "int",
			ctx:  NewCompilationContext("test.mk", `10`),
			want: &IntLitExpr{Value: 10},
		},
		{
			name: "bool",
			ctx:  NewCompilationContext("test.mk", `true`),
			want: &BoolLitExpr{Value: true},
		},
		{
			name: "string",
			ctx:  NewCompilationContext("test.mk", `"tom hanks"`),
			want: &StringLitExpr{Value: "tom hanks"},
		},
		{
			name: "map",
			ctx:  NewCompilationContext("test.mk", `{"a":"10", "b": 10}`),
			want: &MapLitExpr{Values: map[string]Expression{"a": &StringLitExpr{Value: "10"}, "b": &IntLitExpr{Value: 10}}},
		},
		{
			name: "list",
			ctx:  NewCompilationContext("test.mk", `["10", 10]`),
			want: &ListLitExpr{Values: []Expression{&StringLitExpr{Value: "10"}, &IntLitExpr{Value: 10}}},
		},
		{
			name: "arithmetic",
			ctx:  NewCompilationContext("test.mk", `10+20*(3-5)`),
			want: &ArithmeticExpr{Left: &IntLitExpr{Value: 10}, Op: "+", Right: &ArithmeticExpr{Left: &IntLitExpr{Value: 20}, Op: "*", Right: &ParenExpr{Sub: &ArithmeticExpr{Left: &IntLitExpr{Value: 3}, Op: "-", Right: &IntLitExpr{Value: 5}}}}},
		},
		{
			name: "comparison",
			ctx:  NewCompilationContext("test.mk", `a>b && c==d`),
			want: &ComparisonExpr{Left: &ComparisonExpr{Left: &IdentExpr{Id: "a"}, Op: ">", Right: &IdentExpr{Id: "b"}}, Op: "&&", Right: &ComparisonExpr{Left: &IdentExpr{Id: "c"}, Op: "==", Right: &IdentExpr{Id: "d"}}},
		},
		{
			name: "dot-string",
			ctx:  NewCompilationContext("test.mk", `$.header`),
			want: &DotExpr{Object: &CtxExpr{}, Property: "header"},
		},
		{
			name: "dot-call",
			ctx:  NewCompilationContext("test.mk", `$.header()`),
			want: &CallExpr{Func: &DotExpr{Object: &CtxExpr{}, Property: "header"}, Args: []Expression{}},
		},
		{
			name: "question",
			ctx:  NewCompilationContext("test.mk", `a?b:c`),
			want: &TernaryExpr{Cond: &IdentExpr{Id: "a"}, Then: &IdentExpr{Id: "b"}, Else: &IdentExpr{Id: "c"}},
		},
		{
			name: "unary",
			ctx:  NewCompilationContext("test.mk", `-10`),
			want: &UnaryExpr{Op: "-", Right: &IntLitExpr{Value: 10}},
		},
		{
			name: "fn",
			ctx:  NewCompilationContext("test.mk", `fn a10(x,y) {}`),
			want: &FuncExpr{Name: "a", ArgNames: []*IdentExpr{{Id: "x"}, {Id: "y"}}, ArgTypes: []*IdentExpr{nil, nil}, Body: &BlockStmt{Statements: []Statement{}}},
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
			want: &IfStmt{Cond: &IdentExpr{Id: "a"}, Then: &BlockStmt{Statements: []Statement{&ExprStmt{&AssignExpr{Name: &IdentExpr{Id: "c"}, Value: &IntLitExpr{Value: 10}}}}}},
		},
		{
			name: "while",
			ctx:  NewCompilationContext("test.mk", `while ( a ) { c = 10;}`),
			want: &WhileStmt{Cond: &IdentExpr{Id: "a"}, Body: &BlockStmt{Statements: []Statement{&ExprStmt{&AssignExpr{Name: &IdentExpr{Id: "c"}, Value: &IntLitExpr{Value: 10}}}}}},
		},
		{
			name: "return-void",
			ctx:  NewCompilationContext("test.mk", `return;`),
			want: &ReturnStmt{},
		},
		{
			name: "return-expr",
			ctx:  NewCompilationContext("test.mk", `return a;`),
			want: &ReturnStmt{Value: &IdentExpr{Id: "a"}},
		},
		{
			name: "break-void",
			ctx:  NewCompilationContext("test.mk", `break;`),
			want: &BreakStmt{},
		},
		{
			name: "break-expr",
			ctx:  NewCompilationContext("test.mk", `break a;`),
			want: &BreakStmt{Label: &IdentExpr{Id: "a"}},
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
			}
		})
	}
}
