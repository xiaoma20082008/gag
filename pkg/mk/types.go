package mk

type TypeKind byte

const (
	TypePrimitive TypeKind = iota
	TypeStruct
	TypeClass
	TypeFunction  // 函数
	TypeTypeParam // 泛型
)

type AccessFlags uint16

const (
	FlagStatic      AccessFlags = 1 << 0 // 静态方法
	FlagVirtual     AccessFlags = 1 << 1 // 虚方法
	FlagOverride    AccessFlags = 1 << 2 // 重写方法
	FlagGeneric     AccessFlags = 1 << 3 // 泛型方法定义
	FlagConstructor AccessFlags = 1 << 4 // 构造函数
)

type TypeDef struct {
	Name string
	Kind TypeKind
	Size int

	Bitmask uint64

	ClassMetadata    *ClassMetadata
	FunctionMetadata *FunctionMetadata
	GenericMetadata  *GenericMetadata

	// Type any
}

type ClassMetadata struct {
	Base    *TypeDef
	Fields  []FieldDef
	Methods []MethodDef
	VMT     []*MethodDef
}

type FunctionMetadata struct {
	ReturnType     *TypeDef
	ParameterTypes []*TypeDef
}

type GenericMetadata struct {
	Params []string
	Args   []*TypeDef
	Cache  map[string]*TypeDef
}

type FieldDef struct {
	Name   string
	Type   *TypeDef
	Offset int
	Static bool
}

type MethodDef struct {
	Name  string
	Flags AccessFlags
	Index int

	// 区分是实例方法还是方法
	Instance bool

	// 函数签名
	ReturnType *TypeDef
	ParamTypes []*TypeDef
	LocalTypes []*TypeDef

	// 泛型信息
	GenericParams []*GenericParamDef
	GenericArgs   []*TypeDef
	DeclaringType *TypeDef

	// 执行体
	Code []byte
}

type GenericParamDef struct {
	Name        string   // 泛型参数名称，例如 "T"
	Index       int      // 泛型参数的索引位置（第几个泛型参数，从 0 开始）
	Constraints []string // 泛型约束（例如：必须实现某个接口，或必须是 class/struct）
}

type ClosureEnv struct {
	Type *TypeDef
	Data []byte
}

// ClosureDef 闭包
type ClosureDef struct {
	Type *TypeDef
	Func *MethodDef
	Env  *ClosureEnv
}

type ModuleDef struct {
	// 1. 基础识别信息
	Name    string
	Version string

	// 2. 常量池
	ConstantPool []string

	// 3.
	Types    []*TypeDef
	Closures []*ClosureDef

	// 4. 全局静态变量
	StaticFields []*FieldDef

	// 5. 依赖
	DependencyNames []string
	Dependencies    []*ModuleDef

	// 6. 入口函数
	EntryPoint *MethodDef
}

var (
	SysInt8  = &TypeDef{Name: "System.Int8", Kind: TypePrimitive, Size: 1}
	SysInt32 = &TypeDef{Name: "System.Int32", Kind: TypePrimitive, Size: 4}
	SysInt64 = &TypeDef{Name: "System.Int64", Kind: TypePrimitive, Size: 8}
	SysBool  = &TypeDef{Name: "System.Bool", Kind: TypePrimitive, Size: 1}
	SysVoid  = &TypeDef{Name: "System.Void", Kind: TypePrimitive, Size: 0}

	SysString   = &TypeDef{Name: "System.String", Kind: TypeClass, Size: 0}
	SysObject   = &TypeDef{Name: "System.Object", Kind: TypeClass, Size: 0}
	SysMap      = &TypeDef{Name: "System.Map", Kind: TypeClass, Size: 0}
	SysList     = &TypeDef{Name: "System.List", Kind: TypeClass, Size: 0}
	SysFunction = &TypeDef{Name: "System.Function", Kind: TypeFunction, Size: 0}
)

var InternalTypes = map[string]*TypeDef{
	"System.Int8":  SysInt8,
	"System.Int32": SysInt32,
	"System.Int64": SysInt64,
	"System.Bool":  SysBool,
	"System.Void":  SysVoid,

	"System.String":   SysString,
	"System.Object":   SysObject,
	"System.List":     SysList,
	"System.Map":      SysMap,
	"System.Function": SysFunction,

	"byte":   SysInt8,
	"int":    SysInt32,
	"long":   SysInt64,
	"bool":   SysBool,
	"void":   SysVoid,
	"string": SysString,
	"object": SysObject,
	"fn":     SysFunction,
}
