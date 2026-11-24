package transpiler

// goRuntime 是生成文件的标准库支持代码，保持源码独立于 Lynguage 模块。
const goRuntime = `// 此文件由 Lynguage 转译器生成。
package main

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 两种负浮点零使用各自的符号位构造，供数值字面量生成使用。

// lynNegativeZero32 返回符号位置位的 float32 零值。
func lynNegativeZero32() float32 { return math.Float32frombits(1 << 31) }

// lynNegativeZero64 返回符号位置位的 float64 零值。
func lynNegativeZero64() float64 { return math.Float64frombits(1 << 63) }

type lynVoid struct{}

type lynLocation struct {
	line, column int
}

type lynRuntimeError struct {
	at      lynLocation
	message string
}

// 运行时检查用 lynFail 携带源码位置退出，再由 Go 入口统一报告。

// lynFail 终止脚本，并把源码位置和消息传给入口的错误处理函数。
func lynFail(at lynLocation, pattern string, args ...any) {
	panic(lynRuntimeError{at: at, message: fmt.Sprintf(pattern, args...)})
}

// lynReportFailure 输出 Lynguage 运行时诊断，并用非零状态结束进程。
func lynReportFailure() {
	if recovered := recover(); recovered != nil {
		if failure, ok := recovered.(lynRuntimeError); ok {
			fmt.Fprintf(os.Stderr, "%d:%d: %s\n", failure.at.line, failure.at.column, failure.message)
			os.Exit(1)
		}
		// 保留未预期的宿主 panic 和它的 Go 调用栈，便于诊断实现问题。
		panic(recovered)
	}
}

// 全局槽位保存实际值及初始化状态。入口执行声明时调用 store，
// 函数可通过 load 发现前向调用期间尚未初始化的绑定。
type lynGlobal[T any] struct {
	value T
	ready bool
}

// store 写入全局槽位，并标记该绑定已经获得值。
func (global *lynGlobal[T]) store(value T) {
	global.value, global.ready = value, true
}

// load 读取全局槽位，提前读取时按照标识符位置报告错误。
func (global *lynGlobal[T]) load(at lynLocation, name string) T {
	if !global.ready {
		lynFail(at, "global %q has not been initialized", name)
	}
	return global.value
}

// lynFieldReceiver 检查字段读写的结构体引用，再交给原生 Go 字段选择。
func lynFieldReceiver[T any](at lynLocation, receiver *T) *T {
	if receiver == nil {
		lynFail(at, "field access on nil struct reference")
	}
	return receiver
}

// 切片沿用 Go 的值语义与共享存储。构造和追加用独立的容量规则，
// 索引读写在访问底层元素前使用相同的 Lynguage 边界检查。
type lynSlice[T any] []T

// VM 的切片槽位存放接口值。这里沿用相同的容量上界。
const lynMaxSliceCapacity = int64(int(^uint(0)>>1)) / (2 * strconv.IntSize / 8)

// lynAllocate 在参数已通过检查后分配切片，并把宿主的容量 panic 转成脚本诊断。
func lynAllocate[T any](at lynLocation, length, capacity int64) lynSlice[T] {
	// defer 只包住这次分配，保留调用者的源码位置。
	defer func() {
		if recover() != nil {
			lynFail(at, "slice capacity %d is too large", capacity)
		}
	}()
	return make(lynSlice[T], int(length), int(capacity))
}

// lynMakeSlice 验证长度和容量，并利用 Go 的零值初始化可访问的元素。
func lynMakeSlice[T any](at lynLocation, length, capacity int64) lynSlice[T] {
	if length < 0 || capacity < length {
		lynFail(at, "invalid slice length %d and capacity %d: require 0 <= length <= capacity", length, capacity)
	}
	if capacity > lynMaxSliceCapacity {
		lynFail(at, "slice capacity %d is too large", capacity)
	}
	return lynAllocate[T](at, length, capacity)
}

// lynLen 将原生切片长度转换为 Lynguage 的 int。
func lynLen[T any](slice lynSlice[T]) int64 { return int64(len(slice)) }

// lynCap 将原生切片容量转换为 Lynguage 的 int。
func lynCap[T any](slice lynSlice[T]) int64 { return int64(cap(slice)) }

// lynAppend 追加一个元素，满容量时显式翻倍，空容量时增长到一。
func lynAppend[T any](at lynLocation, slice lynSlice[T], value T) lynSlice[T] {
	if len(slice) == cap(slice) {
		capacity := int64(cap(slice))
		if capacity > lynMaxSliceCapacity/2 {
			lynFail(at, "slice capacity %d cannot double", capacity)
		}
		// 用明确的新容量分配并复制，随后 Go append 只负责写入已有空槽。
		grown := lynAllocate[T](at, int64(len(slice)), max(int64(1), capacity*2))
		copy(grown, slice)
		slice = grown
	}
	return append(slice, value)
}

// lynCheckIndex 为读写操作验证下标，边界由当前切片头的长度决定。
func lynCheckIndex(at lynLocation, length int, index int64) {
	if index < 0 || index >= int64(length) {
		lynFail(at, "slice index %d out of range for length %d", index, length)
	}
}

// lynGetIndex 检查下标后读取切片元素。
func lynGetIndex[T any](at lynLocation, slice lynSlice[T], index int64) T {
	lynCheckIndex(at, len(slice), index)
	return slice[index]
}

// lynSetIndex 检查下标后将已求值的右值写入切片。
func lynSetIndex[T any](at lynLocation, slice lynSlice[T], index int64, value T) {
	lynCheckIndex(at, len(slice), index)
	slice[index] = value
}

// lynResult 的 Go 零值自然表示 Err(E 的零值)。lynOk 与 lynErr 提供
// 显式构造，生成的 ? 直接检查 ok 并把 failure 传给外围 Result。
type lynResult[T, E any] struct {
	ok      bool
	value   T
	failure E
}

// lynOk 构造成功结果，void 使用 lynVoid 值作为载荷。
func lynOk[T, E any](value T) lynResult[T, E] {
	return lynResult[T, E]{ok: true, value: value}
}

// lynErr 构造失败结果，保留原错误值。
func lynErr[T, E any](failure E) lynResult[T, E] {
	return lynResult[T, E]{failure: failure}
}

// 聚合类型通过 lynFormat 接入与 VM 一致的输出。结构体登记活动路径，
// 切片和结果递归传递同一份路径状态，使相互引用的值也能完整结束输出。
type lynPrintable interface {
	lynFormat(*strings.Builder, map[any]bool)
}

// lynFormatValue 输出一个值，聚合类型递归格式化，标量沿用 Go 的显示格式。
func lynFormatValue(out *strings.Builder, seen map[any]bool, value any) {
	if printable, ok := value.(lynPrintable); ok {
		printable.lynFormat(out, seen)
		return
	}
	fmt.Fprint(out, value)
}

// lynFormat 输出切片头当前可访问的元素，元素之间用空格分隔。
func (slice lynSlice[T]) lynFormat(out *strings.Builder, seen map[any]bool) {
	out.WriteByte('[')
	// 元素沿用外层的路径状态，便于识别通过切片又指回结构体的循环。
	for i, value := range slice {
		if i != 0 {
			out.WriteByte(' ')
		}
		lynFormatValue(out, seen, value)
	}
	out.WriteByte(']')
}

// lynFormat 按 Ok 或 Err 显示结果和载荷，成功的 void 显示为 Ok()。
func (result lynResult[T, E]) lynFormat(out *strings.Builder, seen map[any]bool) {
	if result.ok {
		out.WriteString("Ok(")
		// void 只占类型参数位置，输出时省略其载荷。
		if _, isVoid := any(result.value).(lynVoid); !isVoid {
			lynFormatValue(out, seen, result.value)
		}
	} else {
		out.WriteString("Err(")
		lynFormatValue(out, seen, result.failure)
	}
	out.WriteByte(')')
}

// lynBeginStruct 输出对象前缀，并判断是否需要继续展开字段。
func lynBeginStruct(out *strings.Builder, seen map[any]bool, identity any, isNil bool, name string) bool {
	if isNil {
		out.WriteString("nil")
		return false
	}
	out.WriteString(name)
	// 只截断当前递归路径上的重复对象，其他位置仍可完整显示同一引用。
	if seen[identity] {
		out.WriteString("{...}")
		return false
	}
	seen[identity] = true
	out.WriteByte('{')
	return true
}

// lynWriteField 输出一个字段名和递归格式化后的字段值。
func lynWriteField(out *strings.Builder, seen map[any]bool, name string, value any, separate bool) {
	if separate {
		out.WriteString(", ")
	}
	out.WriteString(name)
	out.WriteString(": ")
	lynFormatValue(out, seen, value)
}

// lynEndStruct 完成对象输出，并将它从当前递归路径移除。
func lynEndStruct(out *strings.Builder, seen map[any]bool, identity any) {
	out.WriteByte('}')
	delete(seen, identity)
}

// 系统内置函数桥接 Go 的输出、时间、随机数和文件操作。
// 参数错误会结束脚本，文件系统和编码错误作为 Result 值交还调用方。

// lynPrintln 将参数拼成一行并写入标准输出。
func lynPrintln(at lynLocation, values ...any) lynVoid {
	var line strings.Builder
	seen := make(map[any]bool)
	// 每对相邻实参间精确写入一个分隔空格，字符串本身按原内容输出。
	for i, value := range values {
		if i != 0 {
			line.WriteByte(' ')
		}
		lynFormatValue(&line, seen, value)
	}
	line.WriteByte('\n')
	if _, err := io.WriteString(os.Stdout, line.String()); err != nil {
		lynFail(at, "%s", err)
	}
	return lynVoid{}
}

// lynClock 返回 Unix 纪元以来的纳秒数。
func lynClock() int64 { return time.Now().UnixNano() }

// lynRandInt 验证上界，并生成位于半开区间 [0, upper) 的随机整数。
func lynRandInt(at lynLocation, upper int64) int64 {
	if upper <= 0 {
		lynFail(at, "rand_int upper bound must be positive, got %d", upper)
	}
	return rand.Int63n(upper)
}

// lynReadFile 读取 UTF-8 文本，将文件或编码错误作为失败结果返回。
func lynReadFile(path string) lynResult[string, string] {
	data, err := os.ReadFile(path)
	if err != nil {
		return lynErr[string](err.Error())
	}
	// 读取成功后验证编码，避免把无效文本作为 Lynguage 字符串返回。
	if !utf8.Valid(data) {
		return lynErr[string](fmt.Sprintf("file %q contains invalid UTF-8", path))
	}
	return lynOk[string, string](string(data))
}

// lynWriteFile 创建或覆盖文本文件，并用空成功载荷表示写入完成。
func lynWriteFile(path, data string) lynResult[lynVoid, string] {
	if err := os.WriteFile(path, []byte(data), 0o666); err != nil {
		return lynErr[lynVoid](err.Error())
	}
	return lynOk[lynVoid, string](lynVoid{})
}

`

// goErasedRuntime 只在存在增长型泛型循环的程序中补充载荷转换和零值操作。
const goErasedRuntime = `// 擦除后的载荷仍保留实际 Go 值，并在类型确定的操作前转换回来。

// lynTyped 将装箱值还原成由 Lynguage 类型检查确定的 Go 类型。
func lynTyped[T any](value any) T { return value.(T) }

// lynZero 返回一个具体 Go 类型的零值，供擦除后的泛型调用传递类型信息。
func lynZero[T any]() T {
	var zero T
	return zero
}

// lynMakeErasedSlice 创建切片并显式写入元素零值，保持装箱后的动态类型。
func lynMakeErasedSlice(at lynLocation, length, capacity int64, zero any) lynSlice[any] {
	slice := lynMakeSlice[any](at, length, capacity)
	for i := range slice {
		slice[i] = zero
	}
	return slice
}

// lynEraseResult 将内置函数返回的具体结果转换为擦除后的载荷表示。
func lynEraseResult[T, E any](result lynResult[T, E]) lynResult[any, any] {
	if result.ok {
		return lynOk[any, any](result.value)
	}
	return lynErr[any, any](result.failure)
}

`
