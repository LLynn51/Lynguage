package vm

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"lynguage/internal/lexer"
)

// SliceHeader 按值复制头部并共享底层存储。Data 包含 Cap 个槽位，只有前 Len 项可访问。
type SliceHeader struct {
	Data []any
	Len  int
	Cap  int
}

// 每个槽位保存 Go 接口值，由两个机器字组成。先限制容量，避免槽位总字节数溢出。
const maxSliceCapacity = int64(int(^uint(0)>>1)) / (2 * strconv.IntSize / 8)

// 切片辅助函数共同维护头部和底层存储。makeSlice 和 append 管理容量，
// checkIndex 以 Len 为界，供索引读写指令共用。

// allocateSlice 为指定容量分配底层槽位，并把 make 的容量 panic 转成错误。
func allocateSlice(capacity int) (data []any, err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("slice capacity %d is too large", capacity)
		}
	}()
	return make([]any, capacity), nil
}

// makeSlice 检查长度与容量，并将可访问区间初始化为元素类型的零值。
func makeSlice(length, capacity int64, zero any) (SliceHeader, error) {
	if length < 0 || capacity < length {
		return SliceHeader{}, fmt.Errorf("invalid slice length %d and capacity %d: require 0 <= length <= capacity", length, capacity)
	}
	if capacity > maxSliceCapacity {
		return SliceHeader{}, fmt.Errorf("slice capacity %d is too large", capacity)
	}
	// 底层存储预留 Cap 个槽位，首次可见的前 Len 项需要写入正确的动态类型和零值。
	data, err := allocateSlice(int(capacity))
	if err != nil {
		return SliceHeader{}, err
	}
	for i := range data[:int(length)] {
		data[i] = zero
	}
	return SliceHeader{Data: data, Len: int(length), Cap: int(capacity)}, nil
}

// append 返回追加后的切片头，在容量用尽时分配两倍大小的新存储。
func (s SliceHeader) append(value any) (SliceHeader, error) {
	if s.Len == s.Cap {
		// 先检查翻倍后的容量，空切片第一次增长到一个槽位。
		if int64(s.Cap) > maxSliceCapacity/2 {
			return SliceHeader{}, fmt.Errorf("slice capacity %d cannot double", s.Cap)
		}
		capacity := max(1, s.Cap*2)
		data, err := allocateSlice(capacity)
		if err != nil {
			return SliceHeader{}, err
		}
		copy(data, s.Data[:s.Len])
		s.Data, s.Cap = data, capacity
	}
	// 容量足够时沿用共享存储，只有返回的头部记录新增元素。
	s.Data[s.Len] = value
	s.Len++
	return s, nil
}

// checkIndex 验证索引是否落在当前切片头可访问的长度范围内。
func (s SliceHeader) checkIndex(index int64) error {
	if index < 0 || index >= int64(s.Len) {
		return fmt.Errorf("slice index %d out of range for length %d", index, s.Len)
	}
	return nil
}

// Result 的 Value 按 Ok 区分为成功值或错误值。void 成功值使用 nil。
type Result struct {
	Ok    bool
	Value any
}

// structValue 保存结构体实例。堆上的实例由指针共享，字段按 shape 的槽位排列。
type structValue struct {
	shape  *structure
	fields []any
}

// RuntimeError 保存发生运行时错误的源码位置。
type RuntimeError struct {
	Span    lexer.Span
	Message string
}

// Error 将运行时错误显示为 Lynguage 源码的行号、列号和消息。
func (e *RuntimeError) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Span.Start.Line, e.Span.Start.Column, e.Message)
}

// frame 保存一次函数调用的独立状态。base 标记返回时应恢复到的操作数栈高度。
type frame struct {
	function  *function
	ip        int
	base      int
	locals    []any
	typeZeros []any
}

// machine 保存单次运行的全局变量、共享操作数栈和调用栈。
type machine struct {
	program *Program
	output  io.Writer
	globals []any
	stack   []any
	frames  []frame
}

// 执行循环从顶层帧开始取指。step 调用栈及帧辅助函数实现指令效果，
// enter 和 leave 负责在调用切换时保留调用方尚未消费的操作数。

// Run 执行字节码程序的顶层语句，并将 println 写到 output。
func Run(program *Program, output io.Writer) error {
	if program == nil || len(program.functions) == 0 {
		return fmt.Errorf("execution requires a compiled program")
	}
	if output == nil {
		output = io.Discard
	}
	// 每次运行重建全局槽位和调用栈，编译结果可供不同运行复用。
	m := machine{program: program, output: output, globals: make([]any, len(program.globals))}
	m.enter(0, nil, nil)
	for len(m.frames) != 0 {
		f := &m.frames[len(m.frames)-1]
		ins := f.function.code[f.ip]
		// 先推进指令位置，跳转才能覆盖它，函数返回后调用方也能从下一条继续。
		f.ip++
		if err := m.step(f, ins); err != nil {
			return &RuntimeError{Span: ins.Span, Message: err.Error()}
		}
	}
	return nil
}

// push 将一个值压到共享操作数栈顶。
func (m *machine) push(value any) {
	m.stack = append(m.stack, value)
}

// pop 弹出栈顶值，并释放栈槽持有的引用。
func (m *machine) pop() any {
	last := len(m.stack) - 1
	value := m.stack[last]
	m.stack[last] = nil
	m.stack = m.stack[:last]
	return value
}

// truncate 清空并移除指定基址之后的操作数，供调用和返回共同使用。
func (m *machine) truncate(base int) {
	clear(m.stack[base:])
	m.stack = m.stack[:base]
}

// enter 将实参迁移到新帧的局部槽，并记录调用方剩余操作数的边界。
func (m *machine) enter(index int, args, typeZeros []any) {
	function := &m.program.functions[index]
	locals := make([]any, function.locals)
	copy(locals, args)
	base := len(m.stack) - len(args)
	m.truncate(base)
	m.frames = append(m.frames, frame{function: function, base: base, locals: locals, typeZeros: typeZeros})
}

// leave 退出当前帧，恢复调用方栈高度，并将返回值交给调用方。
func (m *machine) leave(value any) {
	last := len(m.frames) - 1
	// 普通返回和 ? 失败共用这里，同时释放帧内未消费的临时值和局部引用。
	m.truncate(m.frames[last].base)
	m.frames[last] = frame{}
	m.frames = m.frames[:last]
	if last != 0 {
		m.push(value)
	}
}

// resolve 在当前调用帧中求出零值，按需读取类型形参槽位或构造嵌套的 Err。
func (z zeroValue) resolve(params []any) any {
	switch {
	case z.parameter != 0:
		return params[z.parameter-1]
	case z.failure != nil:
		// Result 本身的零态是失败，其载荷取错误类型的零值。
		return Result{Value: z.failure.resolve(params)}
	default:
		return z.value
	}
}

// 指令分派和运行时值操作共同实现字节码的栈约定。step 处理简单指令，
// field、index 和 call 分别处理需要检查引用、边界或切换调用帧的操作。

// step 执行一条已取出的指令，根据操作码更新栈、变量槽位或控制流。
func (m *machine) step(f *frame, ins Instruction) error {
	operand := ins.Operand
	switch ins.Op {
	case OP_CONST:
		m.push(f.function.constants[operand])
	case OP_LOAD_LOCAL:
		m.push(f.locals[operand])
	case OP_STORE_LOCAL:
		f.locals[operand] = m.pop()
	case OP_LOAD_GLOBAL:
		value := m.globals[operand]
		// 函数可被提前调用，读取时要检查全局绑定是否已经执行过写入。
		if value == nil {
			return fmt.Errorf("global %q has not been initialized", m.program.globals[operand])
		}
		m.push(value)
	case OP_STORE_GLOBAL:
		m.globals[operand] = m.pop()
	case OP_POP:
		m.pop()
	case OP_DUP:
		m.push(m.stack[len(m.stack)-1])
	case OP_NEGATE:
		m.push(negate(m.pop()))
	case OP_NOT:
		m.push(!m.pop().(bool))
	case OP_ADD, OP_SUBTRACT, OP_MULTIPLY, OP_DIVIDE, OP_REMAINDER,
		OP_EQUAL, OP_NOT_EQUAL, OP_LESS, OP_LESS_EQUAL, OP_GREATER, OP_GREATER_EQUAL:
		// 编译器先压左值再压右值，因此从栈顶逆序取出，以保留减法等运算的方向。
		right := m.pop()
		left := m.pop()
		value, err := binary(ins.Op, left, right)
		if err != nil {
			return err
		}
		m.push(value)
	case OP_CONVERT:
		// 操作数指定目标类型，源类型由弹出的 Go 值给出。
		value := m.pop()
		switch numericKind(operand) {
		case numberInt32:
			m.push(convert[int32](value))
		case numberInt64:
			m.push(convert[int64](value))
		case numberFloat32:
			m.push(convert[float32](value))
		case numberFloat64:
			m.push(convert[float64](value))
		}
	case OP_JUMP:
		f.ip = operand
	case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE:
		// 各分支消费测试值，跳转目标是当前函数中的绝对指令下标。
		if m.pop().(bool) == (ins.Op == OP_JUMP_IF_TRUE) {
			f.ip = operand
		}
	case OP_NEW_STRUCT:
		site := f.function.constructors[operand]
		obj := &structValue{shape: &m.program.structures[site.structure], fields: make([]any, len(site.fields))}
		base := len(m.stack) - len(site.fields)
		// 实参位于栈顶连续区间，按构造点映射重排到对象字段后用对象替换它们。
		for i, slot := range site.fields {
			obj.fields[slot] = m.stack[base+i]
		}
		m.truncate(base)
		m.push(obj)
	case OP_GET_FIELD, OP_SET_FIELD:
		return m.field(ins.Op, operand)
	case OP_GET_INDEX, OP_SET_INDEX:
		return m.index(ins.Op)
	case OP_CALL, OP_CALL_METHOD, OP_BUILTIN:
		return m.call(f, ins.Op, f.function.calls[operand])
	case OP_MAKE_RESULT:
		result := Result{Ok: operand != resultErr}
		// Ok() 代表 void 成功，其空载荷由 Go 零值提供。
		if operand != resultOkVoid {
			result.Value = m.pop()
		}
		m.push(result)
	case OP_TRY:
		result := m.pop().(Result)
		if result.Ok {
			m.push(result.Value)
		} else {
			// 直接返回原来的错误载荷，并丢弃当前表达式和帧内残留的操作数。
			m.leave(result)
		}
	case OP_RETURN:
		m.leave(m.pop())
	default:
		return fmt.Errorf("unknown opcode %d", ins.Op)
	}
	return nil
}

// field 读写结构体的指定字段，并报告对空结构体引用的访问。
func (m *machine) field(op Opcode, slot int) error {
	// 写入时新值位于接收者之上，读取只需要取出接收者。
	var value any
	if op == OP_SET_FIELD {
		value = m.pop()
	}
	obj := m.pop().(*structValue)
	if obj == nil {
		return fmt.Errorf("field access on nil struct reference")
	}
	if op == OP_SET_FIELD {
		obj.fields[slot] = value
	} else {
		m.push(obj.fields[slot])
	}
	return nil
}

// index 按切片头的 Len 验证下标，再读取或修改共享存储中的元素。
func (m *machine) index(op Opcode) error {
	// 写入比读取多弹出一个右值，然后依次取得索引和切片头。
	var value any
	if op == OP_SET_INDEX {
		value = m.pop()
	}
	index := m.pop().(int64)
	slice := m.pop().(SliceHeader)
	if err := slice.checkIndex(index); err != nil {
		return err
	}
	if op == OP_SET_INDEX {
		slice.Data[index] = value
	} else {
		m.push(slice.Data[index])
	}
	return nil
}

// call 收集栈顶实参，解析所需泛型零值，并分派到字节码函数或 Go 内置函数。
func (m *machine) call(f *frame, op Opcode, site callSite) error {
	base := len(m.stack) - site.arguments
	args := m.stack[base:]
	// 方法的首个实参是接收者，进入方法体前验证对象存在。
	if op == OP_CALL_METHOD && args[0].(*structValue) == nil {
		return fmt.Errorf("method call on nil struct reference")
	}
	var zeros []any
	if len(site.types) != 0 {
		// 类型实参可能引用调用方的类型形参，切换帧前先用当前帧求值。
		zeros = make([]any, len(site.types))
		for i, typ := range site.types {
			zeros[i] = typ.resolve(f.typeZeros)
		}
	}
	if op != OP_BUILTIN {
		m.enter(site.target, args, zeros)
		return nil
	}
	// 内置函数直接执行，用返回值替换栈上的实参，保持与字节码调用相同的栈效果。
	value, err := m.builtin(builtin(site.target), args, zeros)
	if err != nil {
		return err
	}
	m.truncate(base)
	m.push(value)
	return nil
}

// 内置函数桥接 Go 的时钟、随机数、文件和输出接口，并调用 VM 的切片实现。
// println 通过 formatValue 递归格式化聚合值，活动路径记录用于截断循环引用。

// builtin 执行指定的内置函数，返回脚本值或需要中止 VM 的运行时错误。
func (m *machine) builtin(target builtin, args, zeros []any) (any, error) {
	switch target {
	case builtinPrintln:
		// 先按语言格式组装整行，再一次写到调用方指定的输出流。
		var line strings.Builder
		seen := make(map[*structValue]bool)
		for i, value := range args {
			if i != 0 {
				line.WriteByte(' ')
			}
			formatValue(&line, value, seen)
		}
		line.WriteByte('\n')
		_, err := io.WriteString(m.output, line.String())
		return nil, err
	case builtinClock:
		return time.Now().UnixNano(), nil
	case builtinRandInt:
		// 检查上界后再调用 Go 随机数函数，将无效参数报告为源码运行时错误。
		upper := args[0].(int64)
		if upper <= 0 {
			return nil, fmt.Errorf("rand_int upper bound must be positive, got %d", upper)
		}
		return rand.Int63n(upper), nil
	case builtinMakeSlice:
		// 内置调用记录提供元素零值，长度和容量来自值实参。
		return makeSlice(args[0].(int64), args[1].(int64), zeros[0])
	case builtinLen:
		return int64(args[0].(SliceHeader).Len), nil
	case builtinCap:
		return int64(args[0].(SliceHeader).Cap), nil
	case builtinAppend:
		return args[0].(SliceHeader).append(args[1])
	case builtinReadFile:
		// 文件错误和编码错误都是脚本可传播的 Result.Err。
		path := args[0].(string)
		data, err := os.ReadFile(path)
		if err != nil {
			return Result{Value: err.Error()}, nil
		}
		if !utf8.Valid(data) {
			return Result{Value: fmt.Sprintf("file %q contains invalid UTF-8", path)}, nil
		}
		return Result{Ok: true, Value: string(data)}, nil
	case builtinWriteFile:
		// 写文件成功时产生无载荷的 Ok()，失败时保留宿主系统的错误消息。
		path, data := args[0].(string), args[1].(string)
		if err := os.WriteFile(path, []byte(data), 0o666); err != nil {
			return Result{Value: err.Error()}, nil
		}
		return Result{Ok: true}, nil
	default:
		return nil, fmt.Errorf("unknown builtin %d", target)
	}
}

// formatValue 按 Lynguage 的输出形式写入一个值，并递归展开聚合值。
func formatValue(out *strings.Builder, value any, seen map[*structValue]bool) {
	switch value := value.(type) {
	case SliceHeader:
		// 切片只显示当前头部可访问的元素。
		out.WriteByte('[')
		for i, item := range value.Data[:value.Len] {
			if i != 0 {
				out.WriteByte(' ')
			}
			formatValue(out, item, seen)
		}
		out.WriteByte(']')
	case *structValue:
		if value == nil {
			out.WriteString("nil")
			return
		}
		out.WriteString(value.shape.name)
		// seen 只标记当前递归路径，回到上层后移除，以便独立出现的别名完整显示。
		if seen[value] {
			out.WriteString("{...}")
			return
		}
		seen[value] = true
		out.WriteByte('{')
		for i, field := range value.fields {
			if i != 0 {
				out.WriteString(", ")
			}
			out.WriteString(value.shape.fields[i])
			out.WriteString(": ")
			formatValue(out, field, seen)
		}
		out.WriteByte('}')
		delete(seen, value)
	case Result:
		// void 成功输出 Ok()，错误分支按其载荷类型递归格式化。
		if value.Ok {
			out.WriteString("Ok(")
			if value.Value != nil {
				formatValue(out, value.Value, seen)
			}
		} else {
			out.WriteString("Err(")
			formatValue(out, value.Value, seen)
		}
		out.WriteByte(')')
	default:
		fmt.Fprint(out, value)
	}
}

// 算术和比较先按 Go 值的动态类型分派，再交给泛型辅助函数。
// 整数与浮点数共用运算分支，各自保持编译时确定的数值宽度。

// integer 限定支持取余和整数除零检查的数值类型。
type integer interface{ int32 | int64 }

// number 涵盖 VM 用于数值运算和转换的四种 Go 类型。
type number interface{ integer | float32 | float64 }

// negate 对数字取负并保留其原有位宽。
func negate(value any) any {
	// any 包装了具体数值类型，各分支在该类型上直接计算。
	switch value := value.(type) {
	case int32:
		return -value
	case int64:
		return -value
	case float32:
		return -value
	default:
		return -value.(float64)
	}
}

// convert 将一个运行时数值显式转换成指令指定的 Go 数值类型。
func convert[T number](value any) T {
	// 先取得源值的具体类型，再执行到目标类型 T 的 Go 转换。
	switch value := value.(type) {
	case int32:
		return T(value)
	case int64:
		return T(value)
	case float32:
		return T(value)
	default:
		return T(value.(float64))
	}
}

// binary 根据左操作数的动态类型选择数值、文本或布尔运算路径。
func binary(op Opcode, left, right any) (any, error) {
	// 类型检查已统一左右操作数的类型，数值分支据此断言右值的具体类型。
	switch left := left.(type) {
	case int32:
		return integerBinary(op, left, right.(int32))
	case int64:
		return integerBinary(op, left, right.(int64))
	case float32:
		return numberBinary(op, left, right.(float32)), nil
	case float64:
		return numberBinary(op, left, right.(float64)), nil
	case string:
		if op == OP_ADD {
			return left + right.(string), nil
		}
		return compare(op, left, right.(string)), nil
	default:
		// 剩下的布尔值只支持相等和不等比较。
		return (left == right) == (op == OP_EQUAL), nil
	}
}

// integerBinary 处理整数除零和取余，其余运算交给共用的数值实现。
func integerBinary[T integer](op Opcode, left, right T) (any, error) {
	if (op == OP_DIVIDE || op == OP_REMAINDER) && right == 0 {
		return nil, fmt.Errorf("integer division by zero")
	}
	if op == OP_REMAINDER {
		return left % right, nil
	}
	return numberBinary(op, left, right), nil
}

// numberBinary 执行同类型数值的四则运算，并将关系运算交给 compare。
func numberBinary[T number](op Opcode, left, right T) any {
	// 每个算术分支以 T 计算，比较分支则统一产生布尔结果。
	switch op {
	case OP_ADD:
		return left + right
	case OP_SUBTRACT:
		return left - right
	case OP_MULTIPLY:
		return left * right
	case OP_DIVIDE:
		return left / right
	default:
		return compare(op, left, right)
	}
}

// compare 比较两个同类型数值或字符串，覆盖相等与大小关系。
func compare[T interface{ number | string }](op Opcode, left, right T) bool {
	// 直接使用 Go 比较运算，保留浮点 NaN 的比较行为和字符串的字典序。
	switch op {
	case OP_EQUAL:
		return left == right
	case OP_NOT_EQUAL:
		return left != right
	case OP_LESS:
		return left < right
	case OP_LESS_EQUAL:
		return left <= right
	case OP_GREATER:
		return left > right
	default:
		return left >= right
	}
}
