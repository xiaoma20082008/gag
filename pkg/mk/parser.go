package mk

import (
	"fmt"
	"math"
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

	p.prefixFns[SIZEOF] = p.parsePrefix
	p.prefixFns[TYPEOF] = p.parsePrefix

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
	p.infixFns[LOG_AND] = p.parseComparison
	p.infixFns[LOG_OR] = p.parseComparison

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
	return p.safeParseStmt()
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
	return p.parseExpr(precLOWEST)
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
	return p.parseStmt()
}

func (p *Parser) crashRecovery() {
	p.nextToken()
	for p.token.Type != EOF {
		switch p.token.Type {
		case SEMI:
			p.nextToken()
			return
		case IF, WHILE, RETURN, LET, BREAK, FUNC:
			return
		case RBRACE, RPAREN, RBRACKET:
			return
		}
		p.nextToken()
	}
}

func (p *Parser) parseStmt() Statement {
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
	case LET:
		return p.parseLet()
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
	case FUNC:
		return p.parseFunc()
	default:
		expr := p.ParseExpr()
		p.expect(SEMI)
		return &ExprStmt{pos: expr.Pos(), Expr: expr}
	}
}

func (p *Parser) parseExpr(prec int) Expression {
	var expr Expression
	prefixFn, ok := p.prefixFns[p.token.Type]
	if !ok {
		line, col := p.pos()
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
		expr = p.parseBool()
	case INT:
		expr = p.parseInt()
	case STRING:
		tok := p.token
		expr = &StringLitExpr{pos: newPos(tok.Line, tok.Column), Value: p.token.Lit}
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
		fn := &IdentExpr{pos: newPos(tok.Line, tok.Column), Id: tok.Lit}
		expr = p.parseCall(fn)
	case LPAREN:
		expr = p.parseGroup()
	case DOLLAR:
		expr = p.parseCtx()
	case NULL:
		tok := p.token
		p.nextToken()
		expr = &NullLitExpr{pos: newPos(tok.Line, tok.Column)}
	default:
		line, col := p.pos()
		p.errors.Report(ErrUnknownPrefixKind, line, col, p.token.Lit)
		panic(&parsingError{"unknown token: " + p.token.Lit})
	}
	return expr
}

func (p *Parser) parseCtx() Expression {
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(DOLLAR)
	return &CtxExpr{pos: pos}
}

func (p *Parser) parseFunc() Statement {
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(FUNC)
	tok := p.token
	p.expect(IDENT)
	name := &IdentExpr{pos: newPos(tok.Line, tok.Column), Id: tok.Lit}
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
	var retType *IdentExpr
	if p.token.Type == IDENT {
		retType = &IdentExpr{pos: newPos(p.token.Line, p.token.Column), Id: p.token.Lit}
		p.nextToken()
	}
	body := p.parseBlock()
	return &FuncStmt{pos: pos, Name: name, ArgNames: argNames, ArgTypes: argTypes, Body: body, RetType: retType}
}

func (p *Parser) parseVarDef() (name, typ *IdentExpr) {
	nameTok := p.token
	p.expect(IDENT)
	name = &IdentExpr{pos: newPos(nameTok.Line, nameTok.Column), Id: nameTok.Lit}
	if p.token.Type == IDENT {
		typ = &IdentExpr{pos: newPos(p.token.Line, p.token.Column), Id: p.token.Lit}
		p.nextToken()
	}
	return
}

func (p *Parser) parseBool() Expression {
	tok := p.token
	v, _ := strconv.ParseBool(p.token.Lit)
	expr := &BoolLitExpr{pos: newPos(tok.Line, tok.Column), Value: v}
	p.nextToken()
	return expr
}

func (p *Parser) parseInt() Expression {
	var expr Expression
	tok := p.token
	v64, err := strconv.ParseInt(p.token.Lit, 10, 64)
	if err != nil {
		line, col := p.pos()
		p.errors.Report(ErrIntLiteralOverflow, line, col, p.token.Lit)
		expr = &IntLitExpr{pos: newPos(tok.Line, tok.Column), Value: math.MaxInt32}
	} else {
		if v64 > math.MaxInt32 {
			p.errors.Report(ErrIntLiteralOverflow, tok.Line, tok.Column, p.token.Lit)
			v64 = math.MaxInt32
		}
		expr = &IntLitExpr{pos: newPos(tok.Line, tok.Column), Value: int32(v64)}
	}
	p.nextToken()
	return expr
}

func (p *Parser) parseUnary() Expression {
	tok := p.token
	p.nextToken()
	if tok.Type == MINUS && p.token.Type == INT { // 负数
		v := "-" + p.token.Lit
		v64, err := strconv.ParseInt(v, 10, 64)
		var expr *IntLitExpr
		if err != nil {
			p.errors.Report(ErrIntLiteralOverflow, tok.Line, tok.Column, v)
			expr = &IntLitExpr{pos: newPos(tok.Line, tok.Column), Value: math.MinInt32}
		} else {
			if v64 < math.MinInt32 {
				p.errors.Report(ErrIntLiteralOverflow, tok.Line, tok.Column, v)
				v64 = math.MinInt32
			}
			expr = &IntLitExpr{pos: newPos(tok.Line, tok.Column), Value: int32(v64)}
		}
		p.nextToken()
		return expr
	}
	expr := p.parseExpr(precPREFIX)
	return &UnaryExpr{pos: newPos(tok.Line, tok.Column), Op: tok.Type, Right: expr}
}

func (p *Parser) parseList() Expression {
	// [v1,v2,v3]
	tok := p.token
	p.expect(LBRACKET)
	v := []Expression{}
	if p.token.Type != RBRACKET {
		v = append(v, p.ParseExpr())
		for p.token.Type == COMMA {
			p.nextToken()
			if p.token.Type == RBRACKET { // 支持尾随逗号 [a,b,c,]
				break
			}
			v = append(v, p.ParseExpr())
		}
	}
	p.expect(RBRACKET)
	return &ListLitExpr{pos: newPos(tok.Line, tok.Column), Values: v}
}

func (p *Parser) parseMap() Expression {
	// {k:v,k:v,}
	tok := p.token
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
	return &MapLitExpr{pos: newPos(tok.Line, tok.Column), Keys: keys, Values: v}
}

func (p *Parser) parseGroup() Expression {
	// Paren: ( x )
	tok := p.token
	p.expect(LPAREN)
	expr := p.ParseExpr()
	p.expect(RPAREN)
	return &ParenExpr{pos: newPos(tok.Line, tok.Column), Sub: expr}
}

func (p *Parser) parseIdent() *IdentExpr {
	if p.token.Type != IDENT {
		line, col := p.pos()
		p.errors.Report(ErrExpectedToken, line, col, IDENT.String(), p.token.Lit)
		panic(&parsingError{"expect an ident, but got: " + p.token.Lit})
	}
	tok := p.token
	e := &IdentExpr{pos: newPos(tok.Line, tok.Column), Id: p.token.Lit}
	p.nextToken()
	return e
}

func (p *Parser) parseCall(left Expression) Expression {
	tok := p.token
	expr := &CallExpr{pos: newPos(tok.Line, tok.Column), Func: left}
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

	return &TernaryExpr{pos: newPos(tok.Line, tok.Column), Cond: left, Then: thn, Else: els}
}

func (p *Parser) parseDot(left Expression) Expression {
	// tok := p.token
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(DOT)
	tok := p.token
	if intLit, ok := left.(*IntLitExpr); ok && tok.Type == INT {
		p.nextToken()
		return &DecimalLitExpr{pos: intLit.pos, Value: fmt.Sprintf("%d.%s", intLit.Value, tok.Lit)}
	}
	if !isPropertyName(p.token.Type) {
		p.reportError(IDENT)
	}
	p.expect(IDENT)
	return &DotExpr{pos: pos, Object: left, Property: &IdentExpr{pos: newPos(tok.Line, tok.Column), Id: tok.Lit}}
}

func (p *Parser) parseIndex(left Expression) Expression {
	// tok := p.token
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(LBRACKET)
	idx := p.ParseExpr()
	p.expect(RBRACKET)
	return &IndexExpr{pos: pos, Node: left, Index: idx}
}

func (p *Parser) parseAssign(left Expression) Expression {
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(ASSIGN)
	val := p.ParseExpr()
	return &AssignExpr{pos: pos, Name: left, Value: val}
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

func (p *Parser) parseLet() *LetStmt {
	// let xxx;
	// let xxx type;
	// let xxx type = xxx;
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(LET)
	name := p.parseIdent()
	var typ *IdentExpr
	var val Expression

	if p.token.Type == IDENT {
		typ = &IdentExpr{pos: newPos(p.token.Line, p.token.Column), Id: p.token.Lit}
		p.nextToken()
	}

	switch p.token.Type {
	case ASSIGN:
		p.nextToken()
		val = p.ParseExpr()
		p.expect(SEMI)
	case SEMI:
		p.nextToken()
	default:
		p.reportError(p.token.Type)
	}
	return &LetStmt{pos: pos, Name: name, Type: typ, Value: val}
}

func (p *Parser) parseIf() *IfStmt {
	// tok := p.token
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(IF)
	p.expect(LPAREN)
	cond := p.ParseExpr()
	p.expect(RPAREN)
	then := p.parseBlock()

	s := &IfStmt{pos: pos, Cond: cond, Then: then}
	if p.token.Type == ELSE {
		p.nextToken()
		s.Else = p.parseBlock()
	}
	return s
}

func (p *Parser) parseWhile() *WhileStmt {
	// tok := p.token
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(WHILE)
	p.expect(LPAREN)
	cond := p.ParseExpr()
	p.expect(RPAREN)
	body := p.parseBlock()
	return &WhileStmt{pos: pos, Cond: cond, Body: body}
}

func (p *Parser) parseBreak() *BreakStmt {
	pos := newPos(p.token.Line, p.token.Column)
	p.nextToken()
	var label *IdentExpr
	if p.token.Type == IDENT {
		label = &IdentExpr{pos: newPos(p.token.Line, p.token.Column), Id: p.token.Lit}
		p.nextToken()
	}
	p.expect(SEMI)
	return &BreakStmt{pos: pos, Label: label}
}

func (p *Parser) parseBlock() *BlockStmt {
	// tok := p.token
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(LBRACE)
	stmts := []Statement{}
	for p.token.Type != RBRACE && p.token.Type != EOF {
		stmt := p.safeParseStmt()
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
	}
	p.expect(RBRACE)
	return &BlockStmt{pos: pos, Statements: stmts}
}

func (p *Parser) parseReturn() *ReturnStmt {
	// return;
	// return xxx;
	// tok := p.token
	pos := newPos(p.token.Line, p.token.Column)
	p.expect(RETURN)
	var expr Expression
	if p.token.Type != SEMI {
		expr = p.ParseExpr()
	}
	p.expect(SEMI)
	return &ReturnStmt{pos: pos, Value: expr}
}

func (p *Parser) expect(kind TokenType) {
	if p.token.Type == kind {
		p.nextToken()
		return
	}
	if kind == SEMI {
		line, col := p.pos()
		p.errors.Report(ErrMissingSemi, line, col, p.token.Lit)
		panic(&parsingError{fmt.Sprintf(ErrMissingSemi.Template, p.token.Lit)})
	}

	p.reportError(kind)
}

func (p *Parser) reportError(kind TokenType) {
	pos := p.token.Offset
	if p.token.Type == EOF {
		pos = p.prevToken.Offset + len(p.prevToken.Lit)
	}
	line, col := p.lexer.LineMap(pos)

	p.errors.Report(ErrExpectedToken, line, col, kind.String(), p.token.Lit)
	panic(&parsingError{"unknown token: " + p.token.Lit})
}

func (p *Parser) pos() (int, int) {
	if p.token.Line > 0 {
		return p.token.Line, p.token.Column
	}
	return p.lexer.LineMap(p.token.Offset)
}

func isPropertyName(t TokenType) bool {
	switch t {
	case LPAREN, RPAREN, LBRACE, RBRACE, LBRACKET, RBRACKET,
		COMMA, SEMI, COLON, DOT, ASSIGN,
		PLUS, MINUS, STAR, SLASH, PERCENT, BANG, TILDE, QUESTION,
		EQ, NE, LT, LE, GT, GE, LTLT, GTGT,
		BIT_XOR, BIT_AND, BIT_OR, LOG_AND, LOG_OR,
		ERR, EOF:
		return false
	case LET, AND, OR, IF, WHILE, BREAK, RETURN, TRUE, FALSE, ELSE, NULL, TYPEOF, SIZEOF, FUNC:
		return false
	}
	return true
}

type parsingError struct {
	msg string
}
