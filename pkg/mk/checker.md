# mk 语义分析设计规范（checker.md）

> 本文档定义 mk 语言**语义分析阶段（Checker）**的规范。

---

## 1. 设计背景

### 1.1 在编译流水线中的位置

```text
源码 ──► Lexer ──► Parser ──► AST ──►【Checker】──► Codegen ──► VM
        词法       语法                语义分析      代码生成    执行
```

Checker 是 AST 之后、代码生成之前的**独立一趟**。

| 项 | 内容 |
| --- | --- |
| 输入 | AST（`Node`） |
| 输出 | 诊断（`DiagnosticBag`） |
| 副作用 | **无**——不改写 AST、不求值、不产生字节码 |

### 1.2 职责

只做静态检查，共三类：

1. **名字解析**——标识符能否在当前作用域链中解析到（S / N 组规则）。
2. **类型检查**——运算符、赋值、函数调用的类型相容性（C 组规则）。
3. **控制流合法性**——`break` 是否在循环内、`return` 之后是否不可达（F 组规则）。

**明确不做**：求值、常量折叠、AST 改写、代码生成、优化、跨文件分析。

### 1.3 设计目标

| 目标 | 含义 |
| --- | --- |
| **不崩** | 任何输入都不得把 panic 抛给调用方。检查器是库，不是入口程序 |
| **不误报** | 静态推不出的类型一律放行。mk 是网关脚本语言，`$` 的成员、HTTP/JWT 的返回值都要运行期才知道 |
| **报全** | 发现错误后**继续检查**，一次运行报出尽可能多的诊断，而不是遇到第一个错误就退出 |

### 1.4 错误处理策略：三条不变式

**不变式 1 —— 结果通道先清空**

表达式的静态类型只通过检查器的 `returnType` 字段传递（Visitor 的 `Visit*` 方法统一返回 `(Node, error)`，不返回类型）。检查任何子表达式**之前**必须先把 `returnType` 置 `nil`：

```go
func (c *Checker) checkExpr(e Expression) *TypeDef {
    if e == nil { return SysObject }
    c.returnType = nil            // ← 陈旧值防护
    e.Accept(c)
    if c.returnType == nil { return SysObject }   // 忘记汇报 = 放行
    return c.returnType
}
```

否则"某个 visitor 忘记写类型"会**静默复用上一个表达式的类型**，产生随机误报。

**不变式 2 —— 未知即放行**

`nil` 与 `SysObject` 类型的值**永不报错**。"检查不出来" ≠ "类型错误"。这是避免假阳性的总闸门（见 D2）。

**不变式 3 —— 错误即毒化，绝不 panic**

发现错误时：报诊断 → 把该表达式的类型设为 `SysObject`（毒化）→ **继续检查**。外层 `Check` 再套一层 `recover` 兜底：

```go
func (c *Checker) Check(n Node) {
    if n == nil { return }
    defer func() {
        if r := recover(); r != nil {
            c.errors.Report(ErrCheckerInternal, 0, 0, fmt.Sprint(r))   // E3999
        }
    }()
    n.Accept(c)
}
```

效果：`undefined1 + 1 + undefined2` 一次报出**两个** `E3007`，而不是在第一个就崩。

### 1.5 非目标

- 不做泛型实例化（`TypeTypeParam`、`GenericMetadata` 保留但本阶段不启用）。
- 不做控制流敏感的精确分析（`if` 两分支不合并类型，取公共类型见 C17）。
- 不做跨模块/跨文件分析。
- 不做字符串长度、数组越界等运行期属性检查。

---

## 2. 类型系统

### 2.1 类型载体

```go
type TypeKind byte

const (
    TypePrimitive TypeKind = iota   // 基本类型
    TypeStruct                      // 结构体
    TypeClass                       // 类
    TypeFunction                    // 函数
    TypeTypeParam                   // 泛型参数
)

type TypeDef struct {
    Name string
    Kind TypeKind
    Size int
    Bitmask uint64

    ClassMetadata    *ClassMetadata
    FunctionMetadata *FunctionMetadata
    GenericMetadata  *GenericMetadata
}
```

### 2.2 类型全集

所有内置类型都是**全局唯一指针**（单例）。

| 类型 | `Kind` | `Size` | 源码名 |
| --- | --- | --- | --- |
| `SysInt8` | `TypePrimitive` | 1 | `byte` |
| `SysInt32` | `TypePrimitive` | 4 | `int` |
| `SysInt64` | `TypePrimitive` | 8 | `long` |
| `SysBool` | `TypePrimitive` | 1 | `bool` |
| `SysVoid` | `TypePrimitive` | 0 | `void`（仅作函数返回类型） |
| `SysDecimal` | `TypeClass` | 8 | `decimal` |
| `SysString` | `TypeClass` | 0 | `string` |
| `SysObject` | `TypeClass` | 0 | `object` |
| `SysMap` | `TypeClass` | 0 | `map` |
| `SysList` | `TypeClass` | 0 | `list` |
| `SysFunction` | `TypeFunction` | 0 | `fn` |

**类型别名表**（`LookupType` 查表，供 `let x <T> ...` 使用）：

| 源码名 | 指向 |
| --- | --- |
| `byte` | `SysInt8` |
| `int` | `SysInt32` |
| `long` | `SysInt64` |
| `bool` | `SysBool` |
| `void` | `SysVoid` |
| `decimal` / `float` | `SysDecimal` |
| `string` | `SysString` |
| `list` | `SysList` |
| `map` | `SysMap` |
| `fn` | `SysFunction` |
| `any` / `object` | `SysObject` |

> ⚠️ **注意 `SysDecimal` 的 `Kind` 是 `TypeClass` 而不是 `TypePrimitive`**。因此"是否数值"的判定**不能按 `Kind` 判断**，必须按指针枚举（见 2.6 的 `IsNumeric`），否则 `float()` 的返回值不参与数值运算。

### 2.3 `SysObject`：万能顶级对象

`SysObject` 同时承担两个角色：**类层次的根** 与 **"未知/不检查"**。`"any"` 与 `"object"` 都指向它。

```
引用类型:
SysObject
  ├── SysString
  ├── SysList
  ├── SysMap
  ├── SysDecimal
  └── SysFunction

值类型: SysInt8 / SysInt32 / SysInt64 / SysBool / SysVoid
```

整个检查器的"未知即放行"只靠一行开关：

```go
func (t *TypeDef) IsDynamic() bool { return t == SysObject }
```

### 2.4 `null`

**`null`**，表示"这个值是 null"。它的静态类型就是 `SysObject`。

- 类型系统中**不存在** `TypeNull` / `SysNull` / `IsNull()`。
- `let x = null;`、`let y string = null;`、`let a = 10; a = null;` 全部放行。
- `null()` → `E3012`（不可调用）。

### 2.5 函数签名即类型

函数的签名即：`TypeDef.FunctionMetadata`：

```go
type FunctionMetadata struct {
    Name           string        // 函数名（诊断文案用）
    ReturnType     *TypeDef
    ParameterTypes []*TypeDef   // 形参类型；写 SysObject 表示"不检查"
    MinArgs        int          // 最少实参个数（可选参数）
    MaxArgs        int          // 最多实参个数；-1 = 不限（变参）
}
```

一个函数类型就是：

```go
&TypeDef{
    Name: "fn " + name,
    Kind: TypeFunction,
    FunctionMetadata: &FunctionMetadata{ ReturnType: ret, ParameterTypes: params, MinArgs: min, MaxArgs: max },
}
```

### 2.6 类型运算

| 运算 | 签名 | 语义 |
| --- | --- | --- |
| `IsDynamic` | `(t) bool` | `t == SysObject`。万能/未知的总开关 |
| `IsNumeric` | `(t) bool` | `t ∈ {Int8, Int32, Int64, Decimal}` |
| `IsInteger` | `(t) bool` | `t ∈ {Int8, Int32, Int64}`（位运算/取模/移位用） |
| `Equal` | `(a, b) bool` | `a == b` ∨ (a、b 同为数值族) |
| `AssignableTo` | `(src, dst) bool` | 见下表 |
| `Unify` | `(a, b) *TypeDef` | 三元表达式两分支的公共类型 |
| `Signature` | `(t) *FunctionMetadata` | `t.Kind == TypeFunction ? t.FunctionMetadata : nil` |

**`AssignableTo(src, dst)` —— 判定顺序即语义：**

```
1. src == nil ∨ dst == nil              → true     信息不足，不报假阳性
2. src.IsDynamic() ∨ dst.IsDynamic()    → true     万能顶级，双向放行
3. Equal(src, dst)                      → true     同类型 / 数值族内互通
4. src 与 dst 同为 TypeFunction         → true     函数类型之间互赋
5. 其余                                 → false
```

> 第 2 条必须**双向**。只做一半会造成错误级联（实测：`undefinedName == ""` 从 1 条诊断变 2 条）。

**`Unify(a, b)` —— 三元分支公共类型：**

```
1. a == nil → b；b == nil → a
2. Equal(a, b)          → a
3. 同为数值族           → Decimal 优先，否则 Int32
4. 其余                 → SysObject
```

### 2.7 表达式的静态类型推导

| AST 节点 | 静态类型 |
| --- | --- |
| `IntLitExpr` | `SysInt32` |
| `DecimalLitExpr` | `SysDecimal` |
| `StringLitExpr` | `SysString` |
| `BoolLitExpr` | `SysBool` |
| `NullLitExpr` | `SysObject` |
| `MapLitExpr` | `SysMap` |
| `ListLitExpr` | `SysList` |
| `FuncStmt`（`fn` 声明） | `&TypeDef{Kind: TypeFunction, FunctionMetadata: ...}` |
| `CtxExpr`（`$`） | `SysObject` |
| `DotExpr`（`a.b`） | `SysObject`（无字段类型表，运行期才知道） |
| `IndexExpr`（`a[i]`） | Map → `SysObject`；List/String → 元素类型 |
| `CallExpr` | 被调用者的 `ReturnType`；签名未知时 → `SysObject` |

---

## 3. 规则列表

### R —— 结构与遍历约定

| 编号 | 规则 | 说明 |
| --- | --- | --- |
| **R1** | 单一职责 | Checker 只做静态检查：**不求值、不改写 AST、不产生字节码**。任何"计算出的值"都不应写回节点 |
| **R2** | 结果通道唯一 | 表达式类型**只**通过 `returnType` 传递；每个 Expr visitor **必须**汇报类型。这是本设计最重要的不变式 |
| **R3** | 陈旧值防护 | `checkExpr(e)` 在 `Accept` **之前**先把 `returnType` 置 `nil`，使"忘记设置"退化为放行，而不是静默复用上一个表达式的类型 |

### S —— 作用域与声明

| 编号 | 规则 | 触发条件 | 诊断码 |
| --- | --- | --- | --- |
| **S1** | `let` 是唯一声明形式 | 只有 `let` 引入新名字；裸赋值不隐式声明（严格模式） | — |
| **S2** | 同作用域重名禁止 | 同一 `SymbolTable` 层内重复 `Define` 同名 | `E3005` |
| **S3** | 块即作用域 | 每个 `{ }`（`BlockStmt`）`Enter` / `Leave` 一层 | — |
| **S4** | 函数名登记在外层 | `fn f(){}` 的 `f` 声明在**当前**（外层）作用域，先登记后查体 → 支持递归 | `E3005` |
| **S5** | 形参登记在函数作用域 | 形参 `KindParam`，随函数体 `Leave` 销毁 | `E3005` |
| **S6** | 内置函数注入根作用域 | `Builtins` 以 `KindBuiltin` 注入 root 作用域，可被内层同名遮蔽 | — |

### N —— 名字解析与赋值

| 编号 | 规则 | 触发条件 | 诊断码 |
| --- | --- | --- | --- |
| **N1** | 读未声明的名字 | `IdentExpr` 且 `Resolve` 失败 | `E3007` |
| **N2** | 写未声明的名字 | `IdentExpr` 是赋值目标且未声明 | 严格模式 `E3007`；宽松模式（`ImplicitDeclare = true`）隐式声明 |
| **N3** | 不可变绑定禁止赋值 | `sym.Constant == true` | `E3008` |
| **N4** | 左值判定 | 合法左值：`IdentExpr` / `DotExpr` / `IndexExpr`；`$` 本身、字面量不是 | `E3018` |
| **N5** | 非法左值仍继续检查 | 报 `E3018` 后**不中断**，继续查右值（一次报全） | — |

> **`$` 的可写性**：`$` 本身只读，但 `$.header[...] = ...`、`$.context.x = ...` 合法——它们是 `DotExpr` / `IndexExpr` 左值，走 N4 的合法分支。

### C —— 类型与运算检查

| 编号 | 规则 | 触发条件 | 诊断码 |
| --- | --- | --- | --- |
| **C1** | 类型名必须可解析 | `let x <T> ...` 中 `<T>` 不在类型表 | `E3010` |
| **C2** | `let` 初值可赋给标注类型 | `let x int = "s";` | `E3001` |
| **C3** | 赋值两侧类型相容 | `x = <不可赋给 x 的类型>` | `E3001` |
| **C4** | 条件必须是布尔 | `if` / `while` / 三元 的条件非 bool | `E3011` |
| **C5** | `==` / `!=` 需可比 | 两侧类型既不相同、也不可比较 | `E3002` |
| **C6** | `<` `<=` `>` `>=` 需有序 | 非「同为数值」且非「同为字符串」 | `E3002` |
| **C7** | 算术运算数类型 | `+ - * /`：`+` 允许字符串拼接，其余要求数值 | `E3002` |
| **C8** | 位运算/取模/移位要求整数 | `%` `&` `\|` `^` `<<` `>>` 两侧必须整数 | `E3002` |
| **C9** | 一元运算符 | `!` 要 bool；`+` `-` 要数值；`~` 要整数或 bool | `E3002` |
| **C10** | 实参个数符合签名 | 静态可确定签名时，实参个数须落在 `[MinArgs, MaxArgs]` | `E3014` |
| **C11** | 被调用者必须可调用 | `let x = 1; x();` | `E3012` |
| **C12** | 实参类型符合形参 | 形参类型非 `SysObject` 且不可赋 | `E3003` |
| **C13** | 函数必须已定义 | 调用未声明的函数名 | `E3013` |
| **C14** | 只有结构化/动态类型有字段 | `(1).foo` | `E3015` |
| **C15** | 下标访问需可索引 | `(1)[0]`；Map 要字符串键，List/String 要整数下标 | `E3016` / `E3002` |
| **C16** | map 字面量键不重复 | `{a: 1, a: 2}` | `E3017` |
| **C17** | 三元：条件 bool + 分支求公共类型 | 条件非 bool | `E3011`（分支不一致时不报，取 `SysObject`） |
| **C18** | 返回值类型匹配声明 | `fn f() int { return "s"; }`——`return` 的值须可赋给函数的声明返回类型 | `E3004` |

**运算符分类**（与 `token.go` 的优先级档位一致）：

```
逻辑：  and  or  &&  ||          → 要求 bool
相等：  ==   !=                  → 要求可比（C5）
有序：  <    <=  >   >=          → 要求有序（C6）
算术：  +    -   *   /           → 数值；+ 额外支持字符串拼接（C7）
整型：  %    &   |   ^   <<  >>  → 要求整数（C8）
一元：  !    +   -   ~           → C9
```

> **注意**：`and` / `or` / `&&` / `||` 在 AST 里也是 `ComparisonExpr`，与 `==` / `<` 共用 `VisitComparison`。**必须按 `n.Op` 分流**，否则 `a && b` 会被当成"相等比较"。

> **C18 的实现约定**：声明返回类型进 `fn f() <T>`，函数体检查期间要能随时读到"当前函数的期望返回类型"。用 **`returnStack []*TypeDef` 栈**而不是单个字段——否则 `fn outer() int { fn inner() string { return "x"; } return 1; }` 里 inner 的 `string` 会污染 outer 的 `int`。不写返回类型（`fn f() { }`）时期望类型取 `SysObject`，于是任何 `return` 都合法。

### F —— 控制流

| 编号 | 规则 | 触发条件 | 诊断码 |
| --- | --- | --- | --- |
| **F1** | `break` 必须在循环内 | `loopDepth <= 0` | `E3019` |
| **F2** | `return` 之后不可达 | 同层语句序列中 `return` 之后仍有语句 | `E3009` |
| **F3** | 顶层 `return` 合法 | 网关脚本响应契约（规范 §9 / §10） | — |
| **F4** | 检查器绝不 panic | 任何内部异常 → `recover` → 报内部错误 | `E3999` |

> **F2 的取舍**：报一次后**继续检查**后续语句，以便一次运行暴露更多错误。这与"报完就停"相反，是刻意的。

### D —— 动态类型策略

这四条决定"误报率"。mk 是网关脚本语言，`$` 的成员、HTTP/JWT 的返回值都是运行期才知道的，静态检查必须**宽松**。

| 编号 | 规则 | 说明 |
| --- | --- | --- |
| **D1** | `$` 是动态根对象 | `VisitCtx` 返回 `SysObject`；`$.anything`、`$.x[i]`、`$.f()` 一律放行 |
| **D2** | **未知即放行** | 类型为 `nil` 或 `SysObject` 时**不报错**——"检查不出来" ≠ "类型错误"。避免假阳性的总闸门 |
| **D3** | 内置函数有签名表 | `builtin.go` 覆盖规范全部内置函数；`MaxArgs = -1` 表示不限 |
| **D4** | 成员/方法调用返回 `SysObject` | `a.b`、`a.b(...)` 的静态类型未知（无字段类型表），一律 `SysObject`，但**实参照查** |

### T —— 类型系统

| 编号 | 规则 | 说明 |
| --- | --- | --- |
| **T1** | 内置类型是单例指针 | 但类型运算**必须**走 `Equal` / `AssignableTo`，不要直接 `==`，否则将来泛型实例化会静默失效 |
| **T2** | 可赋值关系 | `SysObject` ↔ 任意（万能顶级）；同类型 / 数值族内互赋；函数类型之间互赋；其余拒绝 |
| **T3** | 函数签名即类型 | 函数类型 = `TypeDef{Kind: TypeFunction, FunctionMetadata}`；**不引入 `FuncSig`**（见 2.5） |

### 3.1 诊断码总表

下表与 `diagnostics.go` **逐条对齐**：码、`Kind`、文案模板全部照抄代码。「可达」一栏标注该描述符是否有真实调用点——标 **不可达** 的表示描述符存在但当前没有任何路径能触发，规则表里不要挂靠它。

#### 词法错误（E1000–E1999，`KindLexical`）

| 码 | 文案模板 | 触发 | 可达 |
| --- | --- | --- | --- |
| `E1001` | `invalid character '%s'` | Lexer 遇非法字符 | 可达 |
| `E1002` | `unterminated string literal` | 字符串未闭合 | 可达 |
| `E1003` | `unterminated multi line comment` | 多行注释未闭合 | 可达 |
| `E1004` | `unknown escape sequence '\x' in string literal` | 字符串里出现无法识别的转义序列 | 可达 |

#### 语法错误（E2000–E2999，`KindSyntax`）

| 码 | 文案模板 | 触发 | 可达 |
| --- | --- | --- | --- |
| `E2001` | `expected '%s', got '%s'` | `expect()` / `reportError()` | 可达 |
| `E2002` | `expected ';', got '%s'` | `expect(SEMI)` 专用文案（缺分号） | 可达 |
| `E2003` | `expression '%s' cannot be used as a standalone statement` | — | **不可达**（无调用点） |
| `E2004` | `integer literal '%s' is out of range for int32 (-2147483648 ~ 2147483647)` | 整数字面量超过 int32 范围 | 可达 |
| `E2005` | `expected an expression before '%s'` | `parseExpr` 找不到前缀解析函数 | 可达 |
| `E2006` | `unknown token '%s'` | `parsePrefix` 的 `default` 分支 | **不可达** |
| `E2007` | `no infix parse function for '%s' found` | — | **不可达**（无调用点） |
| `E2008` | `unknown infix type '%s'` | — | **不可达**（无调用点） |

#### 语义错误（E3000–E3999，`KindSemantic`）

| 码 | 文案模板（节选） | 规则 | 可达 |
| --- | --- | --- | --- |
| `E3001` | `type mismatch in assignment: cannot assign type '%s' to variable of type '%s'` | C2 / C3 | 可达 |
| `E3002` | `type mismatch for operator '%s': unsupported operand types '%s' and '%s'` | C5–C9 / C15 | 可达 |
| `E3003` | `type mismatch in function call: argument %d expects type '%s', got '%s'` | C12 | 可达 |
| `E3004` | `type mismatch in return: expected type '%s', got '%s'` | **C18** | 可达 |
| `E3005` | `semantic error: identifier '%s' has already been declared in this scope` | S2 / S4 / S5 | 可达 |
| `E3006` | `type inference error: cannot determine the type of variable '%s'; please provide more context` | （保留，暂不触发） | **不可达**（无调用点） |
| `E3007` | `compiler error: name '%s' is not defined in this scope` | N1 / N2 | 可达 |
| `E3008` | `cannot assign to immutable variable/binding '%s'` | N3 | 暂不实现 |
| `E3009` | `cannot reachable the code` | F2 | 可达 |
| `E3010` | `unknown type '%s'` | C1 | 可达 |
| `E3011` | `condition must be bool, got '%s'` | C4 / C17 | 可达 |
| `E3012` | `'%s' is not callable` | C11 | 可达 |
| `E3013` | `function '%s' is not defined` | C13 | 可达 |
| `E3014` | `wrong number of arguments in call to '%s': expected %d, got %d` | C10 | 可达 |
| `E3015` | `type '%s' has no fields` | C14 | 可达 |
| `E3016` | `type '%s' is not indexable` | C15 | 可达 |
| `E3017` | `duplicate key '%s' in map literal` | C16 | 可达 |
| `E3018` | `cannot assign to '%s'; invalid left-hand side in assignment` | N4 | 可达 |
| `E3019` | `'break' outside of a loop` | F1 | 可达 |
| `E3999` | `internal checker error: %v`（出现即 bug） | F4 | 可达（recover 兜底） |

#### 运行时错误（E4000–E4999，`KindRuntime`）

| 码 | 文案模板 | 说明 |
| --- | --- | --- |
| `E4001` | `division by zero` | 除零 |
| `E4002` | `undefined variable '%s'` | 运行期未定义变量 |
| `E4003` | `'%s' is not a function` | 调用了非函数 |
| `E4004` | `wrong number of arguments; expected %d, got %d` | 实参个数不符 |
| `E4005` | `index out of bounds: %d` | 越界 |

> **码段归属说明**：`E3018`（非法左值）与 `E3019`（break 越界）历史上曾占用语法段的 `E2004` / `E2009`。**现在的归属是**：`E2004` = 整数溢出，`E3018` = 非法左值，`E3019` = break 越界；`E2009` 这个码已从 `diagnostics.go` 删除，**任何文档都不应再引用它**。

---

## 4. 内置函数

### 4.1 约定

- 内置函数表是 `map[string]*TypeDef`，每个条目都是一个**函数类型**（见 2.5）。
- **形参类型写 `SysObject` = 不检查**（规范未约束，或运行期才知道）。
- `MinArgs` / `MaxArgs` 驱动 C10；`MaxArgs = -1` 表示不限（变参）。
- 可选用 `?` 标注，例如 `header(name, defaultValue?)` 的 `MinArgs = 1, MaxArgs = 2`。

### 4.2 签名表

**§6.1 通用操作**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `len` | `len(obj)` | `int` |
| `has` | `has(obj, key)` | `bool` |
| `keys` | `keys(obj)` | `list` |
| `values` | `values(obj)` | `list` |
| `is_string` | `is_string(v)` | `bool` |
| `is_number` | `is_number(v)` | `bool` |
| `is_bool` | `is_bool(v)` | `bool` |
| `is_map` | `is_map(v)` | `bool` |
| `is_array` | `is_array(v)` | `bool` |
| `string` | `string(v)` | `string` |
| `int` | `int(v)` | `int` |
| `float` | `float(v)` | `decimal` |
| `bool` | `bool(v)` | `bool` |

**§6.2 字符串操作**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `contains` | `contains(str, sub)` | `bool` |
| `starts_with` | `starts_with(str, prefix)` | `bool` |
| `ends_with` | `ends_with(str, suffix)` | `bool` |
| `trim` | `trim(str)` | `string` |
| `lower` | `lower(str)` | `string` |
| `upper` | `upper(str)` | `string` |
| `replace` | `replace(str, old, new)` | `string` |
| `split` | `split(str, sep)` | `list` |
| `join` | `join(arr, sep)` | `string` |
| `regex_match` | `regex_match(str, pattern)` | `bool` |
| `substr` | `substr(str, left, right?)` | `string` |

**§6.3 请求访问辅助（4）**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `header` | `header(name, defaultValue?)` | `string` |
| `query` | `query(name, defaultValue?)` | `string` |
| `cookie` | `cookie(name, defaultValue?)` | `string` |
| `body_field` | `body_field(name, defaultValue?)` | `any` |

**§6.4 HTTP 请求**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `http_get` | `http_get(url, headers?)` | `any` |
| `http_post` | `http_post(url, data, headers?)` | `any` |
| `http_put` | `http_put(url, data, headers?)` | `any` |
| `http_delete` | `http_delete(url, headers?)` | `any` |

**§6.5 安全与加密**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `jwt_parse` | `jwt_parse(token, secret)` | `any` |
| `sha256` | `sha256(str)` | `string` |
| `md5` | `md5(str)` | `string` |
| `base64_encode` | `base64_encode(str)` | `string` |
| `base64_decode` | `base64_decode(str)` | `string` |

**§6.6 时间函数**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `now` | `now()` | `long` |
| `now_ms` | `now_ms()` | `long` |
| `format_time` | `format_time(ts, layout)` | `string` |

**§6.7 日志函数**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `log` | `log(msg)` | `void` |
| `log_info` | `log_info(msg)` | `void` |
| `log_warn` | `log_warn(msg)` | `void` |
| `log_error` | `log_error(msg)` | `void` |

**§6.8 错误处理**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `error` | `error(msg)` | `any`（错误对象） |
| `throw` | `throw(msg)` | `void` |

**§6.9 其它函数**

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `random` | `random()` | `decimal`（`[0, 1)` 随机数） |
| `uuid` | `uuid()` | `string` |
| `sizeof` | `sizeof(obj)` | `int`（对象大小） |
| `typeof` | `typeof(obj)` | `string`（对象类型名） |
