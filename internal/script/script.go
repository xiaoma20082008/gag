// Package script 是网关与 mk 脚本引擎之间的桥接层。
//
// 设计文档 §9 要求用声明式 DSL 表达治理策略，其中动态逻辑（条件路由、
// 字段改写）交给 mk 脚本。当前 mk 已具备词法/语法/语义检查，
// 但字节码执行器尚未完成，因此这里只提供编译期能力：
//
//   - 配置加载时编译脚本，把语法错误挡在下发之前；
//   - 暴露 Runtime 接口，等 mk VM 完成后直接替换实现，不影响调用方。
package script

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"gag/pkg/mk"
)

// ErrRuntimeUnavailable 表示脚本执行引擎尚未就绪。
var ErrRuntimeUnavailable = errors.New("script: mk 运行时尚未实现")

// Program 是编译后的策略脚本。
type Program struct {
	Name     string
	Source   string
	Bytecode []byte
}

// Compile 使用 mk 引擎编译脚本。
//
// mk 在遇到语法错误时可能直接 panic（解析器尚未统一返回错误），
// 这里统一 recover 并转成 error，保证配置加载不会把进程带崩。
func Compile(name, source string) (prog *Program, err error) {
	defer func() {
		if r := recover(); r != nil {
			prog = nil
			err = fmt.Errorf("script %q 编译失败: %v", name, r)
		}
	}()
	ctx := mk.NewCompilationContext(name, source)
	code := ctx.Compile()
	if diags := ctx.Diagnostics(); len(diags) > 0 {
		msgs := make([]string, 0, len(diags))
		for _, d := range diags {
			msgs = append(msgs, d.String())
		}
		return nil, fmt.Errorf("script %q 编译失败: %s", name, strings.Join(msgs, "; "))
	}
	return &Program{Name: name, Source: source, Bytecode: code}, nil
}

// Runtime 执行已编译脚本。
type Runtime interface {
	Eval(ctx context.Context, prog *Program, env map[string]any) (any, error)
}

// MKRuntime 是 Runtime 的 mk 实现。
type MKRuntime struct{}

// NewRuntime 返回当前可用的运行时。
func NewRuntime() Runtime { return &MKRuntime{} }

// Eval 暂未实现：mk 的 AST/字节码解释器仍在完善中。
func (*MKRuntime) Eval(_ context.Context, _ *Program, _ map[string]any) (any, error) {
	return nil, ErrRuntimeUnavailable
}

// Set 是按名字索引的脚本集合，供配置引用。
type Set struct {
	mu       sync.RWMutex
	programs map[string]*Program
}

// NewSet 创建脚本集合。
func NewSet() *Set { return &Set{programs: make(map[string]*Program, 4)} }

// Add 编译并登记脚本；同名脚本会覆盖。
func (s *Set) Add(name, source string) error {
	prog, err := Compile(name, source)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.programs[name] = prog
	s.mu.Unlock()
	return nil
}

// Get 查找脚本。
func (s *Set) Get(name string) (*Program, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.programs[name]
	return p, ok
}

// Len 返回脚本数量。
func (s *Set) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.programs)
}
