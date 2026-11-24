package vm_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lynguage/internal/checker"
	"lynguage/internal/parser"
	"lynguage/internal/vm"
)

// 测试辅助函数走实际编译和运行入口。compile 构造可复用的字节码，
// checkOutput 为每次运行收集 stdout 并比较完整输出。

// compile 从 Lynguage 源码生成字节码，任一前端或编译阶段出错立即终止测试。
func compile(t *testing.T, source string) *vm.Program {
	t.Helper()
	// 类型检查和编译使用同一棵语法树，以保持类型信息中的节点索引有效。
	tree, err := parser.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := checker.CheckWithInfo(tree)
	if err != nil {
		t.Fatal(err)
	}
	program, err := vm.Compile(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// checkOutput 执行一次字节码并逐字比较输出，包括空格和换行。
func checkOutput(t *testing.T, program *vm.Program, want string) {
	t.Helper()
	// 每次运行使用独立缓冲区，重复执行时也单独验证完整输出。
	var output bytes.Buffer
	if err := vm.Run(program, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// 端到端测试先验证仓库示例，再用短脚本覆盖类型精度、栈清理、内置函数和错误报告。

// TestExamples 验证示例程序的输出，以及同一字节码重复执行时的运行状态隔离。
func TestExamples(t *testing.T) {
	// Phase 3 示例覆盖方法、扩容和结果传播，冒泡排序覆盖循环和索引写入。
	tests := []struct {
		file string
		want string
	}{
		{"phase3.lyn", "2 2\n3 4\n5 8\nOk(14)\nErr(score index out of range)\n"},
		{"bubble_sort.lyn", "长度和容量 5 8\n-1\n0\n3\n3\n5\n"},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "app", "example", test.file))
			if err != nil {
				t.Fatal(err)
			}
			program := compile(t, string(source))
			checkOutput(t, program, test.want)
			// 第二次运行复用编译结果，检验全局绑定和可变对象会重新初始化。
			checkOutput(t, program, test.want)
		})
	}
}

// TestTypedValuesAndEarlyReturn 验证泛型零值、切片共享、运算精度和当前帧的提前退出。
func TestTypedValuesAndEarlyReturn(t *testing.T) {
	// 泛型方法生成 Result 切片。脚本同时检查扩容前后的别名行为和零容量追加。
	// early 把失败的 ? 放在嵌套表达式里，配合短路和递归检查操作数栈。
	source := `
struct Box<T> {
    value: T
}

def (const self: Box<A>) zeros<B>(value: B) -> Slice<Result<B, A>> {
    return make_slice(1, 2)
}

def sum(left: int, right: int) -> int {
    return left + right
}

def fail() -> Result<int, str> { return Err("stopped") }
def never() -> int {
    println("unreachable")
    return 0
}

def early() -> Result<bool, str> {
    return Ok(sum(1, fail()? + never()) == 0)
}

def factorial(n: int) -> int {
    if n <= 1 { return 1 }
    return n * factorial(n - 1)
}

let box = Box(value: "hello")
let zero = box.zeros(true)
println(zero[0])
let shared = append(zero, Ok(true))
let other = append(zero, Ok(false))
println(len(zero), cap(zero), shared[1], other[1])
let grown = append(shared, Ok(true))
grown[1] = Ok(true)
println(cap(grown), grown[1], shared[1])
let empty = make_slice<int>(0, 0)
let single = append(empty, 4)
println(len(empty), cap(single), single[0])
println(early(), factorial(6))
println(false and never() == 0, true or never() == 0)
let small: int32 = -2147483648
let narrow: float32 = 16777216.0 + 1.0
println(small - 1, small / -1, int(narrow), int32(int(4294967295)))
`
	// 整行比较还能捕捉不可达的 never 被误执行或数值宽度丢失造成的差异。
	checkOutput(t, compile(t, source), "Err()\n1 2 Ok(false) Ok(false)\n4 Ok(true) Ok(false)\n0 1 4\nErr(stopped) 720\nfalse true\n2147483647 -2147483648 16777216 -1\n")
}

// TestBuiltins 验证文件读写、UTF-8 校验、随机数的最小上界和时钟返回值。
func TestBuiltins(t *testing.T) {
	// 用临时文件准备无效编码，另一个文件由 Lynguage 脚本自行创建并读回。
	dir := filepath.ToSlash(t.TempDir())
	valid, invalid := dir+"/valid.txt", dir+"/invalid.txt"
	if err := os.WriteFile(invalid, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	// 随机上界取 1 以固定结果，时钟只比较正值，避免依赖具体执行时刻。
	source := fmt.Sprintf(`
def round_trip(path: str) -> Result<str, str> {
    write_file(path, "你好")?
    return read_file(path)
}
println(round_trip(%q))
println(read_file(%q))
println(rand_int(1), clock_ns() > 0)
`, valid, invalid)
	want := fmt.Sprintf("Ok(你好)\nErr(file %q contains invalid UTF-8)\n0 true\n", invalid)
	checkOutput(t, compile(t, source), want)
}

// TestRuntimeErrors 验证非法操作产生带源码位置的 RuntimeError。
func TestRuntimeErrors(t *testing.T) {
	// 每个脚本都能通过静态检查，错误需要执行到对应指令才会触发。
	tests := []struct {
		name    string
		source  string
		message string
		line    int
	}{
		{"index", "let values = make_slice<int>(0, 2)\nprintln(values[0])", "slice index 0 out of range", 2},
		{"length", "make_slice<int>(-1, 0)", "invalid slice length", 1},
		{"rand bound", "rand_int(0)", "upper bound must be positive", 1},
		{"division", "println(1 / 0)", "integer division by zero", 1},
		{"nil field", "struct Box { value: int }\nlet boxes = make_slice<Box>(1, 1)\nprintln(boxes[0].value)", "field access on nil struct reference", 3},
		{"nil method", "struct Box {}\ndef (const b: Box) read() -> int { return 1 }\nlet boxes = make_slice<Box>(1, 1)\nprintln(boxes[0].read())", "method call on nil struct reference", 4},
		{"global", "later()\nlet value = 1\ndef later() -> void { println(value) }", "global \"value\" has not been initialized", 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := vm.Run(compile(t, test.source), nil)
			// 核对错误类型、消息和原脚本行号，覆盖源码区间的传递。
			var runtimeErr *vm.RuntimeError
			if !errors.As(err, &runtimeErr) || !strings.Contains(runtimeErr.Message, test.message) || runtimeErr.Span.Start.Line != test.line {
				t.Errorf("Run() = %v, want line %d and %q", err, test.line, test.message)
			}
		})
	}
}
