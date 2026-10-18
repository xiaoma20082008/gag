package script

import (
	"context"
	"errors"
	"testing"
)

func TestCompileAcceptsPolicy(t *testing.T) {
	prog, err := Compile("policy.mk", `if ($.method == "GET") { return true; }`)
	if err != nil {
		t.Fatalf("合法脚本编译失败: %v", err)
	}
	if prog.Name != "policy.mk" || prog.Source == "" {
		t.Fatalf("program = %+v", prog)
	}
}

func TestCompileRecoversFromInvalidSource(t *testing.T) {
	// 非法源码可能触发 mk 内部 panic，桥接层必须兜住，不能带崩网关进程。
	if _, err := Compile("bad.mk", `if {`); err == nil {
		t.Fatal("非法脚本应返回错误")
	}
}

func TestSet(t *testing.T) {
	s := NewSet()
	if err := s.Add("a", `return 1;`); err != nil {
		t.Fatalf("登记脚本: %v", err)
	}
	if _, ok := s.Get("a"); !ok {
		t.Fatal("脚本未登记")
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d", s.Len())
	}
}

func TestEvalNotImplemented(t *testing.T) {
	prog, err := Compile("p.mk", `return 1;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntime().Eval(context.Background(), prog, nil); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("执行器未实现时应返回明确错误，实际 %v", err)
	}
}
