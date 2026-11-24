# Lynguage 语言设计手册

Lynguage 是一门静态类型语言。它用 `def`、`let`、`if` 等关键字表达常见操作，用 `{}` 标出作用域。局部变量可以从初始值推断类型，函数签名则显式写出参数和返回类型。可恢复的错误通过 `Result` 随返回值传递。

执行架构包含字节码虚拟机和 `--transpile` Go 转译模式。两种模式遵循同一套语言语义，并复用 Go 宿主的垃圾回收。Phase 1 的交付范围是语言规范、词法分析、AST 和语法分析。类型检查、字节码执行和 Go 转译属于后续实现。形式化规则见 [语法规范](grammar.ebnf)。

## 写一段程序

```text
# 顶层语句按源码顺序执行
def add(a: int, b: int) -> int {
    return a + b
}

let count = 0
const LIMIT = 3
while count < LIMIT {
    if count == 0 {
        println("start")
    } elif count == 1 {
        println("middle")
    } else {
        println("end")
    }
    count = add(count, 1)
}
```

一行通常写一条语句。块末尾的语句也可以紧挨着 `}`，例如 `def ping() -> void { println("pong") }`。`#` 从所在位置注释到行尾。条件和循环要求 `bool`，`and`、`or` 会短路求值。圆括号、方括号和已识别的类型实参列表内可以换行。

## 类型、绑定和函数

| 类型 | 含义 |
| --- | --- |
| `int`、`int32`、`int64` | 有符号整数，`int` 等同于 `int64` |
| `float`、`float32`、`float64` | 浮点数，`float` 等同于 `float64` |
| `bool` | `true` 或 `false` |
| `str` | UTF-8 文本，使用双引号，可写 `\n`、`\r`、`\t`、`\"`、`\\` |
| `void` | 无值，用于函数返回类型及 `Result<void, E>` |

```text
let sample = 10             # int
let retries: int32 = 3
const RATE: float32 = 0.5

def identity<T>(value: T) -> T {
    return value
}

def log_value(value: int) -> void {
    println(value)
}

let first = identity(sample)
let second = identity<int32>(retries)
log_value(int(second))
```

`let` 和 `const` 都要求初始值。没有更具体的类型上下文时，整数字面量推断为 `int`，浮点字面量推断为 `float`。已经带有具体类型的数值可用 `int(second)` 这类调用显式转换。`const` 绑定禁止重新赋值，也禁止通过该绑定写入结构体的直接字段。

泛型调用可以从实参或期望类型推断类型。需要显式传入类型时，`<` 紧跟被调用的名称，例如 `identity<int32>(retries)`。建议在比较符两边留空格，例如 `first < sample`。函数参数和返回类型都必须标注，返回 `void` 的函数可以自然执行到块尾，或使用裸 `return`。

## 结构体与方法

```text
struct Box<T> {
    value: T
}

def (const self: Box<T>) get() -> T {
    return self.value
}

def (self: Box<T>) set(value: T) -> void {
    self.value = value
}

let box = Box<int>(value: 10)
box.set(20)
const view = box
println(view.get())
```

构造结构体时按字段名提供初始值，每个字段提供一次。结构体按引用传递，`view` 和 `box` 指向同一个对象。`view.value = 30` 和 `view.set(30)` 都会在类型检查时报错，经 `box` 做出的修改仍然可以通过 `view` 读到。只读取接收者直接字段的方法适合声明 `const self`。用结构体字段和方法组合行为。

## Slice

```text
let scores = make_slice<int>(2, 2)
scores[0] = 7
scores[1] = 9
scores = append(scores, 11)
println(len(scores), cap(scores), scores[2])  # 3 4 11
```

`make_slice<T>(len, cap)` 要求 `0 <= len <= cap`，并用元素类型的零值初始化可访问区间。索引从零开始，可访问范围为 `0` 到 `len(s) - 1`。VM 的切片头记录 `{Data, Len, Cap}`。赋值或传参会复制切片头，底层数据可共享。`append` 返回更新后的切片，达到容量上限时会分配新底层存储并将容量翻倍。初始容量为零时，第一次增长到一。追加后使用返回值，已知数据规模时可以预留容量。

## Result 与错误传播

```text
def copy_text(source: str, destination: str) -> Result<int, str> {
    let text = read_file(source)?
    write_file(destination, text)?
    return Ok(1)
}

def positive(value: int) -> Result<int, str> {
    if value <= 0 {
        return Err("value must be positive")
    }
    return Ok(value)
}
```

`Result<T, E>` 有成功和失败两种状态。`Ok(value)` 携带 `T`，`Err(error)` 携带 `E`，缺失的类型可以从返回类型等上下文推断。`Result<void, E>` 用 `Ok()` 表示成功。后缀 `?` 在成功时取出值，在失败时立即把同一个错误从当前函数返回。当前函数也必须返回 `Result`，且错误类型必须相同。`write_file(...)` 成功后没有值，所以 `write_file(destination, text)?` 可以独立成句。

## 内置函数

| 函数 | 行为 |
| --- | --- |
| `println(values)` | 接受零个或多个有值实参，用空格分隔后输出一行，返回 `void` |
| `clock_ns() -> int` | 返回距 Unix 纪元的纳秒数 |
| `rand_int(upper: int) -> int` | 返回 `[0, upper)` 内的随机整数，`upper` 必须为正 |
| `make_slice<T>(length: int, capacity: int) -> Slice<T>` | 创建指定长度和容量的切片 |
| `len<T>(slice: Slice<T>) -> int` | 返回切片的可访问元素数 |
| `cap<T>(slice: Slice<T>) -> int` | 返回切片的容量 |
| `append<T>(slice: Slice<T>, item: T) -> Slice<T>` | 在末尾追加一个元素并返回新切片头 |
| `read_file(path: str) -> Result<str, str>` | 读取 UTF-8 文本，读取失败或内容编码无效时返回错误消息 |
| `write_file(path: str, data: str) -> Result<void, str>` | 创建或覆盖 UTF-8 文本文件，失败时返回错误消息 |
