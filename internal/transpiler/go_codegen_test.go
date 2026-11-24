package transpiler_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lynguage/internal/checker"
	"lynguage/internal/parser"
	"lynguage/internal/transpiler"
	"lynguage/internal/vm"
)

// 验证辅助函数以同一棵已检查的语法树构造两个后端，分别执行并收集输出。

// compileBoth 构造 Go 源码和 VM 字节码，便于比较两个后端的运行结果。
func compileBoth(t *testing.T, source string) ([]byte, *vm.Program) {
	t.Helper()
	tree, err := parser.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	// 两个后端共享上下文类型及泛型推断结果。
	info, err := checker.CheckWithInfo(tree)
	if err != nil {
		t.Fatal(err)
	}
	goSource, err := transpiler.Generate(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	program, err := vm.Compile(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	return goSource, program
}

// assertSameOutput 通过系统 Go 构建并执行生成结果，逐字节比较输出与预期值。
func assertSameOutput(t *testing.T, source, want string) {
	t.Helper()
	goSource, program := compileBoth(t, source)
	var vmOutput, nativeOutput, nativeErrors bytes.Buffer
	if err := vm.Run(program, &vmOutput); err != nil {
		t.Fatal(err)
	}
	// 实际构建可以发现语法格式化无法检查的 Go 类型错误。
	if err := transpiler.Run(context.Background(), goSource, nil, &nativeOutput, &nativeErrors); err != nil {
		t.Fatalf("native execution: %v\n%s", err, nativeErrors.String())
	}
	if nativeErrors.Len() != 0 {
		t.Errorf("native stderr = %q", nativeErrors.String())
	}
	if !bytes.Equal(nativeOutput.Bytes(), vmOutput.Bytes()) || nativeOutput.String() != want {
		t.Errorf("native = %q\nVM = %q\nwant = %q", nativeOutput.String(), vmOutput.String(), want)
	}
}

// 端到端用例覆盖仓库脚本和 Go 源码生成中容易改变语义的求值、类型与错误路径。

// TestExamples 运行两个示例，核对切片容量、方法调用、结果传播和排序输出。
func TestExamples(t *testing.T) {
	// Phase 3 示例检查 2→4→8 的增长，冒泡排序检查循环及共享存储的修改。
	tests := []struct{ file, want string }{
		{"phase3.lyn", "2 2\n3 4\n5 8\nOk(14)\nErr(score index out of range)\n"},
		{"bubble_sort.lyn", "长度和容量 5 8\n-1\n0\n3\n3\n5\n"},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "app", "example", test.file))
			if err != nil {
				t.Fatal(err)
			}
			assertSameOutput(t, string(source), test.want)
		})
	}
}

// TestTypesAndEvaluationOrder 验证泛型、数值边界、循环引用和表达式求值顺序。
func TestTypesAndEvaluationOrder(t *testing.T) {
	// 用改写全局的函数观察实参和命名字段的求值顺序，验证 ? 与短路会跳过后续副作用。
	source := `
struct Box<T> {
    type: T
    tag: int
}
def (const b: Box<Box>) get() -> Box { return b.type }
def (const b: Box<A>) zero<B>(value: B) -> Slice<Result<B, A>> { return make_slice(1, 2) }
def zero<T>() -> Slice<T> { return make_slice(1, 1) }
struct Node {
    next: Node
}

let state = 0
def bump() -> int {
    state = state + 1
    return state
}
def problem() -> Result<bool, str> { return Err("stop") }
def side_effect(value: bool, second: int) -> int { return second }
def guarded() -> Result<int, str> {
    if false and problem()? { return Ok(10) }
    elif true or problem()? { return Ok(side_effect(problem()?, bump())) }
    return Ok(20)
}
def loop() -> Result<str, str> {
    while problem()? { return Ok("unexpected") }
    return Ok("finished")
}
def scope(field: int) -> int {
    if true {
        let field = field + 1
        let unused = false
        return field
    }
}

let box = Box(tag: bump(), type: bump())
println(box.tag, box.get(), state, bump(), state)
let zeros = box.zero(true)
println(zeros[0], cap(zeros), guarded(), loop(), state, scope(3))
let flags: Slice<bool> = zero()
println(flags, len(flags), cap(flags))
let nils = make_slice<Node>(1, 1)
let node = Node(next: nils[0])
node.next = node
println(nils, node)
let min: int32 = -2147483648
let narrow: float32 = 16777216.0 + 1.0
let neg: float32 = -0.0
let zero_float = 0.0
println(min - 1, min / -1, 09, int(narrow), 1.0 / neg, -0.0, zero_float / zero_float)
`
	// 输出同时核对 Go 保留字段名、接收者形参遮蔽、负零和窄类型整数溢出。
	want := "1 2 2 3 3\nErr(0) 2 Err(stop) Err(stop) 3 4\n[false] 1 1\n[nil] Node{next: Node{...}}\n2147483647 -2147483648 9 16777216 -Inf -0 NaN\n"
	assertSameOutput(t, source, want)
}

// TestGrowingTypes 验证运行时有限的递归可以在不同层使用不断嵌套的类型实参。
func TestGrowingTypes(t *testing.T) {
	// Lynguage 允许这些声明，Go 的静态泛型实例化会尝试无限展开类型。
	t.Run("function", func(t *testing.T) {
		assertSameOutput(t, `
def grow<T>(value: T, n: int) -> int {
    if n == 0 { return 1 }
    let next = make_slice<T>(1, 1)
    return grow(next, n - 1) + len(next)
}
println(grow("seed", 3))
let nested = make_slice<Slice<Result<int, str>>>(1, 1)
nested[0] = append(nested[0], Ok(2))
let default_result = make_slice<Result<int, Result<bool, str>>>(1, 1)
println(nested, cap(nested[0]), default_result)
`, "4\n[[Ok(2)]] 1 [Err(Err())]\n")
	})
	// 普通 Go 接收者会随结构体实例化，单独验证方法体引入的增长依赖。
	t.Run("receiver", func(t *testing.T) {
		assertSameOutput(t, `
struct Box<T> {
    item: T
}
def (const self: Box<T>) depth(n: int) -> int {
    if n == 0 { return 0 }
    let larger = Box(item: make_slice<T>(0, 0))
    return larger.depth(n - 1) + 1
}
println(Box(item: 10).depth(4))
`, "4\n")
	})
	// 递归结构体和独立泛型方法共同验证擦除后的字段及返回类型转换。
	t.Run("structure and method", func(t *testing.T) {
		assertSameOutput(t, `
struct Tower<T> {
    more: Tower<Slice<T>>
    item: T
}
def (const self: Tower<A>) grow<B>(value: B, n: int) -> Result<A, str> {
    if n <= 0 { return Ok(self.item) }
    let next = make_slice<B>(1, 1)
    return self.grow(next, n - 1)
}
let zero = make_slice<Tower<Slice<int>>>(1, 1)
let tower = Tower(item: 5, more: zero[0])
println(tower, tower.grow("seed", 3))
`, "Tower{more: nil, item: 5} Ok(5)\n")
	})
}

// TestBuiltins 验证 Go 运行时内置函数的文件、随机数和时钟语义。
func TestBuiltins(t *testing.T) {
	// 无效 UTF-8 由测试准备，正常 UTF-8 由两个后端分别写入和读回。
	dir := filepath.ToSlash(t.TempDir())
	valid, invalid := dir+"/valid.txt", dir+"/invalid.txt"
	if err := os.WriteFile(invalid, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`
def round_trip(path: str) -> Result<str, str> {
    write_file(path, "你好")?
    return read_file(path)
}
println(round_trip(%q), write_file(%q, "你好"))
println(read_file(%q))
println(rand_int(1), clock_ns() > 0)
`, valid, valid, invalid)
	// 随机上界固定为 1，时钟只检查正值，方便逐字节比较。
	want := fmt.Sprintf("Ok(你好) Ok()\nErr(file %q contains invalid UTF-8)\n0 true\n", invalid)
	assertSameOutput(t, source, want)
}

// TestRuntimeError 核对原生进程的退出码、诊断位置及错误前已经写入的输出。
func TestRuntimeError(t *testing.T) {
	// 索引写入在右值求值之后检查边界，副作用应先于运行时错误发生。
	source := strings.TrimSpace(`
def right() -> int {
    println("value evaluated")
    return 1
}
let values = make_slice<int>(0, 2)
values[0] = right()
`)
	goSource, program := compileBoth(t, source)
	var vmOutput, nativeOutput, nativeErrors bytes.Buffer
	vmError := vm.Run(program, &vmOutput)
	if vmError == nil {
		t.Fatal("VM did not report a runtime error")
	}
	// 原生诊断直接来自可执行文件，Go 构建工具不会给它附加运行时消息。
	err := transpiler.Run(context.Background(), goSource, nil, &nativeOutput, &nativeErrors)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || nativeErrors.String() != vmError.Error()+"\n" {
		t.Fatalf("native error = %v, stderr = %q, VM = %v", err, nativeErrors.String(), vmError)
	}
	if got, want := nativeOutput.String(), vmOutput.String(); got != want || want != "value evaluated\n" {
		t.Errorf("native stdout = %q, VM = %q", got, want)
	}
}
