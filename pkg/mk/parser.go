package mk

import (
	"fmt"
	"strconv"
)

type prefixParseFn func() Expression
type infixParseFn func(Expression) Expression

type Parser struct {
	lexer  *Lexer
	errors *DiagnosticBag

	token     Token
	prevToken Token

	prefixFns map[TokenType]prefixParseFn
	infixFns  map[TokenType]infixParseFn

	depth int
}

func NewParser(ctx *CompilationContext) *Parser {
	p := &Parser{
		lexer:     ctx.lexer,
		errors:    ctx.errors,
		prefixFns: map[TokenType]prefixParseFn{},
		infixFns:  map[TokenType]infixParseFn{},
	}

	// parsePrefix主要包括：
	// 1. 字面量，如：10，20，"Tom"
	// 2. 标识符，如：id，name
	// 3. 前缀运算符，如：-，!，~
	// 4. 分组符号，如：(
	// 5. 其它符号，如：[ （数组），{ （对象/Map）

	// parseInfix主要包括：
	// 1. 二元中缀运算符，如：+，-，*，/ 等
	// 2. 后缀运算符，如：!（阶乘），++，--
	// 3. 特殊运算符，如：函数调用add()、索引访问arr[10]、字段访问obj.field

	// 1.
	p.prefixFns[IDENT] = p.parsePrefix
	// 2.
	p.prefixFns[INT] = p.parsePrefix
	p.prefixFns[STRING] = p.parsePrefix
	// 3.
	p.prefixFns[TRUE] = p.parsePrefix
	p.prefixFns[FALSE] = p.parsePrefix
	p.prefixFns[NULL] = p.parsePrefix
	// 4.
	p.prefixFns[PLUS] = p.parsePrefix
	p.prefixFns[MINUS] = p.parsePrefix
	p.prefixFns[BANG] = p.parsePrefix
	p.prefixFns[TILDE] = p.parsePrefix
	// 5.
	p.prefixFns[LPAREN] = p.parsePrefix
	p.prefixFns[LBRACE] = p.parsePrefix
	p.prefixFns[LBRACKET] = p.parsePrefix

	p.prefixFns[DOLLAR] = p.parsePrefix

	p.prefixFns[FUNC] = p.parseFunc

	// 1. =
	p.infixFns[ASSIGN] = p.parseAssign
	// 2. + - * / % << >> & | ^
	p.infixFns[PLUS] = p.parseArithmetic
	p.infixFns[MINUS] = p.parseArithmetic
	p.infixFns[STAR] = p.parseArithmetic
	p.infixFns[SLASH] = p.parseArithmetic
	p.infixFns[PERCENT] = p.parseArithmetic
	p.infixFns[LTLT] = p.parseArithmetic
	p.infixFns[GTGT] = p.parseArithmetic
	p.infixFns[BIT_AND] = p.parseArithmetic
	p.infixFns[BIT_OR] = p.parseArithmetic
	p.infixFns[BIT_XOR] = p.parseArithmetic
	// > >= == < <= !=
	p.infixFns[LT] = p.parseComparison
	p.infixFns[LE] = p.parseComparison
	p.infixFns[EQ] = p.parseComparison
	p.infixFns[GT] = p.parseComparison
	p.infixFns[GE] = p.parseComparison
	p.infixFns[NE] = p.parseComparison
	// and or && ||
	p.infixFns[AND] = p.parseComparison
	p.infixFns[OR] = p.parseComparison
	p.infixFns[BIT_AND_AND] = p.parseComparison
	p.infixFns[BIT_OR_OR] = p.parseComparison

	// a? b: c
	p.infixFns[QUESTION] = p.parseTernary
	// (
	p.infixFns[LPAREN] = p.parseCall
	// .
	p.infixFns[DOT] = p.parseDot
	// [
	p.infixFns[LBRACKET] = p.parseIndex
	p.nextToken()
	return p
}

func (p *Parser) ParseExpr() Expression {
	return p.safeParseExpr()
}

func (p *Parser) ParseStmt() Statement {
	if p.token.Type == EOF {
		return nil
	}
	if p.token.Type == SEMI {
		p.nextToken()
	}
	if p.token.Type == EOF {
		return nil
	}
	switch p.token.Type {
	case IF:
		return p.parseIf()
	case RETURN:
		return p.parseReturn()
	case WHILE:
		return p.parseWhile()
	case BREAK:
		return p.parseBreak()
	case LBRACE:
		return p.parseBlock()
	default:
		expr := p.ParseExpr()
		p.expect(SEMI)
		return &ExprStmt{Expr: expr}
	}
}

func (p *Parser) ParseCode() *Program {
	program := &Program{
		Statements: []Statement{},
	}
	for p.token.Type != EOF {
		stmt := p.safeParseStmt()
		if stmt != nil {
			program.Statements = append(program.Statements, stmt)
		}
	}
	return program
}

func (p *Parser) safeParseExpr() (expr Expression) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(*parsingError); ok {
				p.crashRecovery()
				expr = nil
				return
			} else {
				panic(r)
			}
		}
	}()
	return p.parseExpr(0)
}

func (p *Parser) safeParseStmt() (stmt Statement) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(*parsingError); ok {
				p.crashRecovery()
				stmt = nil
				return
			} else {
				panic(r)
			}
		}
	}()
	return p.ParseStmt()
}

func (p *Parser) crashRecovery() {
	p.nextToken()
	for p.token.Type != EOF {
		if p.token.Type == SEMI {
			p.nextToken()
			return
		}
		switch p.token.Type {
		case IF, WHILE, RETURN:
			return
		}
		p.nextToken()
	}
}

func (p *Parser) parseExpr(prec int) Expression {
	var expr Expression
	prefixFn, ok := p.prefixFns[p.token.Type]
	if !ok {
		line, col := p.lexer.LineMap(p.token.Offset)
		p.errors.Report(ErrNoPrefixParseFunc, line, col, p.token.Lit)
		panic(&parsingError{"no prefix parse func found for token: " + p.token.Lit})
	}
	expr = prefixFn()
	for prec < GetPrecedence(p.token.Type) {
		infixFn, ok := p.infixFns[p.token.Type]
		if !ok {
			return expr
		}
		expr = infixFn(expr)
	}
	return expr
}

func (p *Parser) parsePrefix() Expression {
	// 1. 字面量: ident
	// 2. 数据类型: string,int,true,false,null
	// 3. 一元运算符: +5,-10,!false,~x,typeof(),sizeof()
	// 4. 复合表达式: (), fn, new
	var expr Expression
	switch p.token.Type {
	case IDENT:
		expr = p.parseIdent()
	// bool,int,string,list,map
	case TRUE, FALSE:
		v, _ := strconv.ParseBool(p.token.Lit)
		expr = &BoolLitExpr{Value: v}
		p.nextToken()
	case INT:
		v, _ := strconv.ParseInt(p.token.Lit, 10, 32)
		expr = &IntLitExpr{Value: int32(v)}
		p.nextToken()
	case STRING:
		expr = &StringLitExpr{Value: p.token.Lit}
		p.nextToken()
	case LBRACKET:
		expr = p.parseList()
	case LBRACE:
		expr = p.parseMap()
	case PLUS, MINUS, BANG, TILDE:
		expr = p.parseUnary()
	case TYPEOF, SIZEOF:
		tok := p.token
		p.nextToken()
		ce := &CallExpr{Func: &IdentExpr{Id: tok.Lit}}
		p.expect(LPAREN)
		ce.Args = []Expression{p.ParseExpr()}
		p.expect(RPAREN)
		expr = ce
	case LPAREN:
		expr = p.parseGroup()
	case DOLLAR:
		expr = p.parseCtx()
	case FUNC:
		expr = p.parseFunc()
	case NULL:
		p.nextToken()
		expr = &NullLitExpr{}
	default:
		line, col := p.lexer.LineMap(p.token.Offset)
		p.errors.Report(ErrUnknownPrefixKind, line, col, p.token.Lit)
		panic(&parsingError{"unknown token: " + p.token.Lit})
	}
	return expr
}

func (p *Parser) parseCtx() Expression {
	p.expect(DOLLAR)
	return &CtxExpr{}
}

func (p *Parser) parseFunc() Expression {
	p.expect(FUNC)
	tok := p.token
	p.expect(IDENT)
	name := tok.Lit
	p.expect(LPAREN)
	argNames := []*IdentExpr{}
	argTypes := []*IdentExpr{}
	if p.token.Type != RPAREN {
		var argName *IdentExpr
		var argType *IdentExpr
		argName, argType = p.parseVarDef()
		argNames = append(argNames, argName)
		argTypes = append(argTypes, argType)
		for p.token.Type == COMMA {
			p.nextToken()
			argName, argType = p.parseVarDef()
			argNames = append(argNames, argName)
			argTypes = append(argTypes, argType)
		}
	}
	p.expect(RPAREN)
	body := p.parseBlock()
	return &FuncExpr{Name: name, ArgNames: argNames, ArgTypes: argTypes, Body: body}
}

func (p *Parser) parseVarDef() (name, typ *IdentExpr) {
	nameTok := p.token
	p.expect(IDENT)
	name = &IdentExpr{Id: nameTok.Lit}
	if p.token.Type == IDENT {
		typ = &IdentExpr{Id: p.token.Lit}
		p.nextToken()
	}
	return
}

func (p *Parser) parseUnary() Expression {
	tok := p.token
	p.nextToken()
	expr := p.parseExpr(precPREFIX)
	return &UnaryExpr{Op: tok.Type, Right: expr}
}

func (p *Parser) parseList() Expression {
	// [v1,v2,v3]
	p.expect(LBRACKET)
	v := []Expression{}
	if p.token.Type != RBRACKET {
		v = append(v, p.ParseExpr())
		for p.token.Type == COMMA {
			p.nextToken()
			v = append(v, p.ParseExpr())
		}
	}
	p.expect(RBRACKET)
	return &ListLitExpr{Values: v}
}

func (p *Parser) parseMap() Expression {
	// {k:v,k:v,}
	// tok := p.token
	p.expect(LBRACE)
	v := map[string]Expression{}
	keys := []string{}
	if p.token.Type != RBRACE {
		key := p.token.Lit
		p.nextToken()
		p.expect(COLON)
		val := p.ParseExpr()
		v[key] = val
		keys = append(keys, key)

		for p.token.Type == COMMA {
			p.nextToken()
			if p.token.Type == RBRACE { // 支持尾随逗号 {a: 1, b: 2, }
				break
			}
			key := p.token.Lit
			p.nextToken()
			p.expect(COLON)
			val := p.ParseExpr()
			keys = append(keys, key)

			v[key] = val
		}
	}
	p.expect(RBRACE)
	return &MapLitExpr{Keys: keys, Values: v}
}

func (p *Parser) parseGroup() Expression {
	// Paren: ( x )
	// tok := p.token
	p.expect(LPAREN)
	expr := p.ParseExpr()
	p.expect(RPAREN)
	return &ParenExpr{Sub: expr}
}

func (p *Parser) parseIdent() *IdentExpr {
	if p.token.Type != IDENT {
		line, col := p.lexer.LineMap(p.token.Offset)
		p.errors.Report(ErrExpectedToken, line, col, p.token.Lit)
		panic(&parsingError{"expect an ident, but got: " + p.token.Lit})
	}
	e := &IdentExpr{Id: p.token.Lit}
	p.nextToken()
	return e
}

func (p *Parser) parseCall(left Expression) Expression {
	expr := &CallExpr{Func: left}
	p.expect(LPAREN)

	args := []Expression{}
	if p.token.Type != RPAREN {
		args = append(args, p.ParseExpr())
		for p.token.Type == COMMA {
			p.nextToken()
			args = append(args, p.ParseExpr())
		}
	}
	p.expect(RPAREN)
	expr.Args = args
	return expr
}

func (p *Parser) parseTernary(left Expression) Expression {
	tok := p.token
	prec := GetPrecedence(tok.Type)
	p.nextToken()

	thn := p.ParseExpr() // 三元中部表达式通常允许从最低优先级重新解析
	p.expect(COLON)
	els := p.parseExpr(prec - 1)

	return &TernaryExpr{Cond: left, Then: thn, Else: els}
}

func (p *Parser) parseDot(left Expression) Expression {
	// tok := p.token
	p.expect(DOT)
	tok := p.token
	if intLit, ok := left.(*IntLitExpr); ok && tok.Type == INT {
		p.nextToken()
		return &DecimalLitExpr{Value: fmt.Sprintf("%d.%s", intLit.Value, tok.Lit)}
	}
	p.expect(IDENT)
	return &DotExpr{Object: left, Property: tok.Lit}
}

func (p *Parser) parseIndex(left Expression) Expression {
	// tok := p.token
	p.expect(LBRACKET)
	idx := p.ParseExpr()
	p.expect(RBRACKET)
	return &IndexExpr{Node: left, Index: idx}
}

func (p *Parser) parseAssign(left Expression) Expression {
	p.expect(ASSIGN)
	val := p.ParseExpr()
	return &AssignExpr{Name: left, Value: val}
}

func (p *Parser) parseArithmetic(left Expression) Expression {
	tok := p.token
	prec := GetPrecedence(p.token.Type)
	p.nextToken()
	right := p.parseExpr(prec)
	return &ArithmeticExpr{Op: tok.Type, Left: left, Right: right}
}

func (p *Parser) parseComparison(left Expression) Expression {
	tok := p.token
	prec := GetPrecedence(p.token.Type)
	p.nextToken()
	right := p.parseExpr(prec)
	return &ComparisonExpr{Op: tok.Type, Left: left, Right: right}
}

func (p *Parser) nextToken() {
	p.prevToken = p.token
	for {
		p.token = p.lexer.NextToken()
		// 跳过注释
		if p.token.Type != COMMENT {
			break
		}
	}
}

func (p *Parser) peekToken() Token {
	return p.lexer.Lookahead(1)
}

func (p *Parser) parseIf() *IfStmt {
	// tok := p.token
	p.expect(IF)
	p.expect(LPAREN)
	cond := p.ParseExpr()
	p.expect(RPAREN)
	then := p.parseBlock()

	s := &IfStmt{Cond: cond, Then: then}
	if p.token.Type == ELSE {
		p.nextToken()
		s.Else = p.parseBlock()
	}
	return s
}

func (p *Parser) parseWhile() *WhileStmt {
	// tok := p.token
	p.expect(WHILE)
	p.expect(LPAREN)
	cond := p.ParseExpr()
	p.expect(RPAREN)
	body := p.parseBlock()
	return &WhileStmt{Cond: cond, Body: body}
}

func (p *Parser) parseBreak() *BreakStmt {
	p.nextToken()
	var label *IdentExpr
	if p.token.Type == IDENT {
		label = &IdentExpr{Id: p.token.Lit}
		p.nextToken()
	}
	p.expect(SEMI)
	return &BreakStmt{Label: label}
}

func (p *Parser) parseBlock() *BlockStmt {
	// tok := p.token
	p.expect(LBRACE)
	stmts := []Statement{}
	for p.token.Type != RBRACE && p.token.Type != EOF {
		stmt := p.safeParseStmt()
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
	}
	p.expect(RBRACE)
	return &BlockStmt{Statements: stmts}
}

func (p *Parser) parseReturn() *ReturnStmt {
	// return;
	// return xxx;
	// tok := p.token
	p.expect(RETURN)
	var expr Expression
	if p.token.Type != SEMI {
		expr = p.ParseExpr()
	}
	p.expect(SEMI)
	return &ReturnStmt{Value: expr}
}

func (p *Parser) expect(kind TokenType) {
	if p.token.Type == kind {
		p.nextToken()
		return
	}

	pos := p.token.Offset
	if p.token.Type == EOF {
		pos = p.prevToken.Offset + len(p.prevToken.Lit)
	}
	line, col := p.lexer.LineMap(pos)

	p.errors.Report(ErrExpectedToken, line, col, kind.String(), p.token.Lit)
	panic(&parsingError{"unknown token: " + p.token.Lit})
}

type parsingError struct {
	msg string
}
