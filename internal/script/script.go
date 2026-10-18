package script

import (
	"context"
	"gag/internal/core"
	"gag/pkg/mk"
)

type Env struct {
	Method   string
	Path     string
	Host     string
	ClientIP string
	Tenant   string
	User     string
	Region   string
	Header   string
	Query    string
}

type Script struct {
	Name   string
	Source string

	prog *mk.Program
	code []byte
}

type ScriptManager struct {
	scripts map[string]*Script
}

func (s *Script) Eval(ctx context.Context, req *core.RequestMeta) {
}

func (m *ScriptManager) Compile(name, code string) *Script {
	if script, exists := m.scripts[name]; exists {
		return script
	}
	ctx := mk.NewCompilationContext(name, code)
	ctx.Compile()
	script := &Script{
		Name:   name,
		Source: code,
		prog:   ctx.Program(),
		code:   ctx.Bytecode(),
	}
	m.scripts[name] = script
	return script
}

func NewScriptManager() *ScriptManager {
	return &ScriptManager{
		scripts: map[string]*Script{},
	}
}
