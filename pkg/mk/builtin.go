package mk

// 内置函数签名表
var Builtins = map[string]*TypeDef{
	// ---------- §6.1 通用操作 ----------
	"len":       fn("len", SysInt32, 1, 1, SysObject),
	"has":       fn("has", SysBool, 2, 2, SysObject, SysString),
	"keys":      fn("keys", SysList, 1, 1, SysObject),
	"values":    fn("values", SysList, 1, 1, SysObject),
	"is_string": fn("is_string", SysBool, 1, 1, SysObject),
	"is_number": fn("is_number", SysBool, 1, 1, SysObject),
	"is_bool":   fn("is_bool", SysBool, 1, 1, SysObject),
	"is_map":    fn("is_map", SysBool, 1, 1, SysObject),
	"is_array":  fn("is_array", SysBool, 1, 1, SysObject),
	"string":    fn("string", SysString, 1, 1, SysObject),
	"int":       fn("int", SysInt32, 1, 1, SysObject),
	"float":     fn("float", SysDecimal, 1, 1, SysObject),
	"bool":      fn("bool", SysBool, 1, 1, SysObject),

	// ---------- §6.2 字符串操作 ----------
	"contains":    fn("contains", SysBool, 2, 2, SysString, SysString),
	"starts_with": fn("starts_with", SysBool, 2, 2, SysString, SysString),
	"ends_with":   fn("ends_with", SysBool, 2, 2, SysString, SysString),
	"trim":        fn("trim", SysString, 1, 1, SysString),
	"lower":       fn("lower", SysString, 1, 1, SysString),
	"upper":       fn("upper", SysString, 1, 1, SysString),
	"replace":     fn("replace", SysString, 3, 3, SysString, SysString, SysString),
	"split":       fn("split", SysList, 2, 2, SysString, SysString),
	"join":        fn("join", SysString, 2, 2, SysList, SysString),
	"substr":      fn("substr", SysString, 2, 3, SysString, SysInt32, SysInt32),
	"regex_match": fn("regex_match", SysBool, 2, 2, SysString, SysString),

	// ---------- §6.3 请求访问辅助 ----------
	"header":     fn("header", SysString, 1, 2, SysString, SysString),
	"query":      fn("query", SysString, 1, 2, SysString, SysString),
	"cookie":     fn("cookie", SysString, 1, 2, SysString, SysString),
	"body_field": fn("body_field", SysObject, 1, 2, SysString, SysObject),

	// ---------- §6.4 HTTP 请求 ----------
	"http_get":    fn("http_get", SysObject, 1, 2, SysString, SysMap),
	"http_post":   fn("http_post", SysObject, 2, 3, SysString, SysObject, SysMap),
	"http_put":    fn("http_put", SysObject, 2, 3, SysString, SysObject, SysMap),
	"http_delete": fn("http_delete", SysObject, 1, 2, SysString, SysMap),

	// ---------- §6.5 安全与加密 ----------
	"jwt_parse":     fn("jwt_parse", SysObject, 2, 2, SysString, SysString),
	"sha256":        fn("sha256", SysString, 1, 1, SysString),
	"md5":           fn("md5", SysString, 1, 1, SysString),
	"base64_encode": fn("base64_encode", SysString, 1, 1, SysString),
	"base64_decode": fn("base64_decode", SysString, 1, 1, SysString),

	// ---------- §6.6 时间函数 ----------
	"now":         fn("now", SysInt64, 0, 0),
	"now_ms":      fn("now_ms", SysInt64, 0, 0),
	"format_time": fn("format_time", SysString, 2, 2, SysInt64, SysString),

	// ---------- §6.7 日志函数 ----------
	"log":       fn("log", SysVoid, 1, 1, SysString),
	"log_info":  fn("log_info", SysVoid, 1, 1, SysString),
	"log_warn":  fn("log_warn", SysVoid, 1, 1, SysString),
	"log_error": fn("log_error", SysVoid, 1, 1, SysString),

	// ---------- §6.8 错误处理 ----------
	"error": fn("error", SysObject, 1, 1, SysString),
	"throw": fn("throw", SysVoid, 1, 1, SysString),

	// ---------- §6.9 其它函数 ----------
	"uuid":   fn("uuid", SysString, 0, 0),
	"random": fn("random", SysDecimal, 0, 0),
	"typeof": fn("typeof", SysString, 1, 1, SysObject),
	"sizeof": fn("sizeof", SysInt32, 1, 1, SysObject),
}

func fn(name string, ret *TypeDef, min, max int, params ...*TypeDef) *TypeDef {
	return &TypeDef{
		Name: "fn " + name,
		Kind: TypeFunction,
		Size: 0,

		FunctionMetadata: &FunctionMetadata{
			Name:           name,
			ReturnType:     ret,
			ParameterTypes: params,
			MinArgs:        min,
			MaxArgs:        max,
		},
	}
}
