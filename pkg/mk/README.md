# mk 网关脚本语言设计规范

## 1. 背景

本文档为 mk 语言定义了一套网关导向的脚本模型，允许脚本通过 `$` 访问当前 HTTP 请求，类似于：

```javascript
$.header.authorization
$.cookie.session_id
$.query["user_id"]
```

目标是将 mk 从通用脚本语言改造为网关策略语言，支持请求检查、字段提取、路由决策、请求转换和外部调用。

---

## 2. 设计目标

- 通过 `$` 提供统一的请求上下文对象
- 支持访问请求头、Cookie、Query、路径、方法、Body 和远程信息
- 允许脚本化的请求路由和转换
- 保持语法简洁，与当前 mk 语言风格一致
- 提供网关侧的内置函数支持 HTTP、验证和安全检查
- 保持清晰的评估器/运行时模型

---

## 3. 核心设计

### 3.1 `$` 作为当前请求上下文

`$` 是一个保留的运行时对象，表示"当前请求"。

```javascript
$.method
$.path
$.header
$.cookie
$.query
$.body
$.context
```

该对象在脚本执行前被注入到评估器中。

---

## 4. 请求上下文结构

```javascript
$ = {
  // 请求基本信息
  method: "GET",
  path: "/api/users/123",
  
  // 查询参数
  query: {
    page: "1",
    user_id: "42",
    debug: "true"
  },
  
  // 请求头
  header: {
    "content-type": "application/json",
    "authorization": "Bearer abc.def.ghi",
    "x-trace-id": "trace-001"
  },
  
  // 请求 Cookie
  cookie: {
    session_id: "abc123",
    user_id: "42"
  },
  
  // 请求体（通常为 JSON 对象）
  body: {
    name: "alice",
    role: "admin"
  },
  
  // 远程客户端信息
  remote: {
    ip: "192.168.1.10",
    port: 52435
  },
  
  // URL 信息
  url: {
    scheme: "https",
    host: "gateway.example.com",
    port: 443,
    path: "/api/users/123",
    rawQuery: "page=1&user_id=42&debug=true"
  },
  
  // 自定义上下文（网关可注入）
  context: {
    tenant: "acme",
    trace_id: "trace-001",
    user_id: "42"
  }
}
```

### 规则

- `$` 从脚本角度是只读根对象，但可以修改嵌套值如 `$.header["x-trace-id"] = "..."` 和 `$.context.user_id = "..."`
- 动态 Key 必须支持，如 `"content-type"` 或 `"x-custom-header"`
- 嵌套映射是普通的类对象值
- 缺失的 Key 应返回 `null` 或在使用辅助函数时触发安全默认值

---

## 5. 访问语法

### 5.1 基础属性访问

```javascript
$.method
$.path
$.header.authorization
$.cookie.session_id
$.context.user_id
```

### 5.2 索引访问

```javascript
$.header["content-type"]
$.query["user_id"]
$.header["x-trace-id"]
```

### 5.3 链式访问

```javascript
$.header.authorization
$.cookie.session_id
$.context.user_id
$.remote.ip
```

### 5.4 安全访问模式（推荐）

```javascript
let token = $.header.get("authorization", "")
let page = $.query.get("page", "1")
let session = $.cookie.get("session_id", "")
```

---

## 6. 内置辅助函数

### 6.1 通用操作

```javascript
len(obj)              // 获取长度
has(obj, key)         // 检查 key 是否存在
keys(obj)             // 获取所有 key
values(obj)           // 获取所有 value
is_string(v)          // 检查是否为字符串
is_number(v)          // 检查是否为数字
is_bool(v)            // 检查是否为布尔值
is_map(v)             // 检查是否为对象
is_array(v)           // 检查是否为数组
string(v)             // 转换为字符串
int(v)                // 转换为整数
float(v)              // 转换为浮点数
bool(v)               // 转换为布尔值
```

### 6.2 字符串操作

```javascript
contains(str, sub)        // 包含子字符串
starts_with(str, prefix)  // 以前缀开始
ends_with(str, suffix)    // 以后缀结束
trim(str)                 // 去除空格
lower(str)                // 转小写
upper(str)                // 转大写
replace(str, old, new)    // 替换字符串
split(str, sep)           // 按分隔符分割
join(arr, sep)            // 用分隔符连接数组
regex_match(str, pattern) // 正则匹配
substr(str, left, right)  // 字符串切割
```

### 6.3 请求访问辅助

```javascript
header(name, defaultValue)       // 安全获取请求头
query(name, defaultValue)        // 安全获取查询参数
cookie(name, defaultValue)       // 安全获取 Cookie
body_field(name, defaultValue)   // 安全获取 Body 字段
```

### 6.4 HTTP 请求

```javascript
http_get(url, headers)
http_post(url, data, headers)
http_put(url, data, headers)
http_delete(url, headers)
```

### 6.5 安全与加密

```javascript
jwt_parse(token, secret)    // 解析 JWT
sha256(str)                 // SHA256 哈希
md5(str)                    // MD5 哈希
base64_encode(str)          // Base64 编码
base64_decode(str)          // Base64 解码
```

### 6.6 时间函数

```javascript
now()                      // 当前时间戳（秒）
now_ms()                   // 当前时间戳（毫秒）
format_time(ts, layout)    // 格式化时间
```

### 6.7 日志函数

```javascript
log(msg)           // 打印日志
log_info(msg)      // 信息日志
log_warn(msg)      // 警告日志
log_error(msg)     // 错误日志
```

### 6.8 错误处理

```javascript
error(msg)    // 返回错误对象
throw(msg)    // 抛出异常
```

### 6.9 其它函数

```javascript
random()    // 返回随机数
uuid()      // 返回string
sizeof(obj) // 返回对象大小
typeof(obj) // 返回对象类型
```

---

## 7. 语法示例

### 7.1 验证请求方法

```javascript
if ($.method != "POST") {
  return error("只允许 POST 方法");
}
```

### 7.2 访问请求头和 Cookie

```javascript
let token = $.header.get("authorization", "");
let session = $.cookie.get("session_id", "");

if (token == "" && session == "") {
  return error("未授权");
}
```

### 7.3 查询参数处理

```javascript
let page = int($.query.get("page", "1"));
let userId = int($.query.get("user_id", "0"));

if (userId <= 0) {
  return error("无效的 user_id");
}
```

### 7.4 请求体处理

```javascript
if (!has($.body, "username") || !has($.body, "password")) {
  return error("缺少凭证");
}
```

### 7.5 路由决策

```javascript
if ($.context.role == "admin") {
  $.header["x-route"] = "admin-service";
} else {
  $.header["x-route"] = "user-service";
}
```

---

## 8. 网关策略用例

### 8.1 身份认证

```javascript
let token = $.header.get("authorization", "");

if (!starts_with(token, "Bearer ")) {
  return error("缺少 Bearer Token");
}

let payload = jwt_parse(substr(token, 7), "gateway-secret");

if (!payload) {
  return error("无效的 Token");
}

$.context.user_id = payload.sub;
$.context.role = payload.role;
$.header["x-user-id"] = payload.sub;
```

### 8.2 请求转换

```javascript
$.header["x-client-ip"] = $.remote.ip;
$.header["x-trace-id"] = $.header.get("x-trace-id", uuid());
$.header["x-forwarded-host"] = $.url.host;
```

### 8.3 访问控制

```javascript
if ($.path == "/admin" && $.context.role != "admin") {
  return error("禁止访问");
}
```

### 8.4 上游调用

```javascript
let userInfo = http_get("http://svc-user/users/" + $.context.user_id);

if (userInfo.code != 200) {
  return error("用户查询失败");
}

$.context.user_name = userInfo.body.name;
```

---

## 9. 执行模型

解释器应在脚本执行前注入请求对象。

```text
HTTP 请求到达
   ↓
构建 $ 对象
   ↓
解析 mk 脚本
   ↓
使用注入的运行时上下文评估 AST
   ↓
应用 $ 的修改或返回响应
   ↓
继续网关管道
```

### 执行语义

- `$` 在全局作用域中可用
- 脚本可以修改 `$.header`、`$.cookie`、`$.context` 等
- 最终修改后的请求上下文由网关使用
- 脚本可以返回：
  - 响应对象
  - 修改后的请求
  - 错误对象
  - 布尔值或 Map 值（取决于引擎契约）

---

## 10. 响应契约

网关脚本应能产生如下结果：

```javascript
return {
  status: 200,
  headers: {
    "x-trace-id": $.header["x-trace-id"]
  },
  body: {
    ok: true
  }
}
```

或简单地：

```javascript
return {
  ok: true
}
```

运行时可能会将内部结果转换为网关原生响应对象。

---

## 11. 安全和隐私

### 11.1 脚本限制

为防止滥用，网关运行时应支持：

- 超时控制
- 内存使用限制
- 限制文件系统访问
- 阻止网络调用（除非显式启用）
- 默认禁止策略处理特权操作

### 11.2 危险操作

这些操作应默认被禁止：

```javascript
open_file()
exec_shell()
read_env()
write_config()
```

### 11.3 安全第一策略

任何脚本都应在沙箱式环境中执行，并具有明确的权限。

---

## 12. 性能考虑

### 12.1 脚本预编译

运行时应将 mk 编译为内部字节码形式，并按脚本哈希进行缓存。

### 12.2 保持低访问成本

`$` 应实现为轻量级运行时对象，加载为根上下文，而不是按字段进行深层重新分配。

### 12.3 限制递归和循环

网关脚本必须受限于复杂度和执行时间。

---

## 13. 运行时架构

```text
mk 语言
  ├── 词法分析器 (Lexer)
  ├── 语法解析器 (Parser)
  ├── 抽象语法树 (AST)
  ├── 评估器 (Evaluator)
  ├── 请求上下文运行时对象
  ├── 内置网关函数
  ├── 字节码虚拟机 (Bytecode VM)
  └── 安全沙箱
```

---

## 14. 语法规则

```antlr4

Program 
  : Statement*
  ;

Statement
  : IfStmt
  | WhileStmt
  | LetStmt
  | BlockStmt
  | BreakStmt
  | FuncStmt
  | ReturnStmt
  | ExprStmt
  ;

IfStmt
  : IF '(' Expression ')' BlockStmt (ELSE BlockStmt)?
  ;

WhileStmt
  : WHILE '(' Expression ')' BlockStmt
  ;

LetStmt
  : LET IdentExpr (IdentExpr)? ('=' Expression)? ';'
  ;

BlockStmt
  : '{' Statement* '}'
  ;

BreakStmt
  : BREAK IdentExpr? ';'
  ;

FuncStmt
  : FUNC IdentExpr '(' ParamList ')' IdentExpr? BlockStmt
  ;

ParamList
  : (Ident IdentExpr? (',' IdentExpr IdentExpr?)*)? 
  ;

ReturnStmt
  : RETURN Expression? ';'
  ;


ExprStmt
  : Expression? ';'
  ;

Expression
  : ValueExpr
  | IdentExpr
  | CallExpr
  | IndexExpr
  | UnaryExpr
  | ArithmeticExpr
  | ComparisonExpr
  | DotExpr
  | CtxExpr
  | ParenExpr
  | TernaryExpr
  | AssignExpr
  ;

ValueExpr
  : IntExpr
  | DecimalExpr
  | BoolExpr
  | StringExpr
  | MapExpr
  | ListExpr
  | NullExpr
  ;

CallExpr
  : Expression '(' Expression? (',' Expression)* ')'
  ;

IndexExpr
  : Expression '[' Expression ']'
  ;

ArithmeticExpr
  : Expression ('+' | '-' | '*' | '/' | '%' | '<<' | '>>' | '^' | '&' | '|') Expression
  ;

ComparisonExpr
  : Expression ('>' | '>=' | '<' | '<=' | '==' | '!=' | '&&' | '||' | 'and' | 'or') Expression
  ;

DotExpr
  : Expression '.' IdentExpr
  ;

CtxExpr
  : '$'
  ;

ParenExpr
  : '(' Expression ')'
  ;

TernaryExpr
  : Expression '?' Expression ':' Expression
  ;

AssignExpr
  : Expression '=' Expression
  ; 
```

### 14.1 类型名拼写

类型名只出现在三处：`let x <T> = ...` 的标注、`fn` 的返回类型、`fn` 的形参类型。

| 类别 | 可用拼写 |
| --- | --- |
| 整数 | `byte` / `int` / `long` |
| 布尔 | `bool` |
| 无返回值 | `void` |
| 小数 | `decimal`（`float` 同义） |
| 字符串 | `string` |
| 列表 | `list` |
| 字典 | `map` |
| 任意对象 | `object`（`any` 同义） |
| 函数 | `fn` |

---

## 15. 实现计划

### 第一阶段：最小支持

- 将 `$` 添加为运行时值
- 支持 `$.header`、`$.cookie`、`$.query`、`$.body`
- 支持属性和索引访问
- 添加基础 `get` 辅助函数

### 第二阶段：网关内置函数

- 添加 `jwt_parse`、`http_get`、`http_post`、`match`、`log`、`error`
- 添加 `has`、`len`、`keys`、`values`

### 第三阶段：策略引擎

- 添加请求修改语义
- 添加脚本返回契约
- 添加默认超时和沙箱限制

### 第四阶段：优化

- 缓存编译脚本
- 添加 JIT 或字节码优化
- 添加指标和追踪

---

## 16. 总结

让 `$` 成为单一的统一运行时对象，代表当前 HTTP 请求。这保持语言的表达力和最小化，同时支持强大的网关转换和路由决策。
