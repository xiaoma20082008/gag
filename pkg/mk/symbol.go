package mk

type SymbolKind int

const SymbolTableKey = "SymbolTable"

const (
	KindVariable SymbolKind = iota // 变量
	KindConstant                   // 常量
	KindFunction                   // 函数
	KindParam                      // 函数参数
	KindType                       // 类型
	KindField                      // 字段
	KindMethod                     // 实例方法
	KindBuiltin                    // 内置函数/内置类型
	KindModule                     // 模块
)

func (k SymbolKind) String() string {
	switch k {
	case KindVariable:
		return "variable"
	case KindConstant:
		return "constant"
	case KindFunction:
		return "function"
	case KindParam:
		return "parameter"
	case KindType:
		return "type"
	case KindField:
		return "field"
	case KindMethod:
		return "method"
	case KindBuiltin:
		return "builtin"
	case KindModule:
		return "module"
	default:
		return "unknown"
	}
}

type Symbol struct {
	Name   string
	Kind   SymbolKind
	Type   *TypeDef
	Index  int
	Depth  int
	Offset int // 栈帧偏移

	File   string
	Line   int
	Column int

	Constant bool
	Extern   bool
	Comments string

	Extra map[string]any
}

type SymbolTable struct {
	data map[string]*Symbol

	current *SymbolTable
	outer   *SymbolTable
}

func (st *SymbolTable) Define(name string, v any) {}

func (st *SymbolTable) Resolve(name string) *Symbol {
	for t := st.current; t != nil; t = t.outer {
		if v, ok := t.data[name]; ok {
			return v
		}
	}
	return nil
}

func (st *SymbolTable) Enter() {}

func (st *SymbolTable) Leave() {}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{
		data: make(map[string]*Symbol),
	}
}
