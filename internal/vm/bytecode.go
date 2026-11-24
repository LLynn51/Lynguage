// vm 将经过类型检查的 Lynguage 程序编译成字节码，并通过栈式虚拟机执行。
package vm

import "lynguage/internal/lexer"

// Opcode 表示一条栈式虚拟机指令。
type Opcode uint8

// 指令按操作数来源分组。加载和存储使用槽位，跳转使用当前函数内的
// 指令下标，构造和调用使用当前函数的元数据表下标。
const (
	// 常量、变量和栈操作。
	OP_CONST Opcode = iota
	OP_LOAD_LOCAL
	OP_STORE_LOCAL
	OP_LOAD_GLOBAL
	OP_STORE_GLOBAL
	OP_POP
	OP_DUP

	// 一元运算消费一个值，二元运算消费左、右两个值，均压回结果。
	OP_NEGATE
	OP_NOT
	OP_ADD
	OP_SUBTRACT
	OP_MULTIPLY
	OP_DIVIDE
	OP_REMAINDER
	OP_EQUAL
	OP_NOT_EQUAL
	OP_LESS
	OP_LESS_EQUAL
	OP_GREATER
	OP_GREATER_EQUAL
	OP_CONVERT

	// 条件跳转消费一个布尔值，无条件跳转只更新指令位置。
	OP_JUMP
	OP_JUMP_IF_FALSE
	OP_JUMP_IF_TRUE

	// 字段和索引读取压回值，写入消费目标及新值。
	OP_NEW_STRUCT
	OP_GET_FIELD
	OP_SET_FIELD
	OP_GET_INDEX
	OP_SET_INDEX

	// 调用消费实参并留下返回值。方法调用的实参包含接收者。
	OP_CALL
	OP_CALL_METHOD
	OP_BUILTIN

	// 构造结果、展开成功值或传播错误，以及结束当前调用帧。
	OP_MAKE_RESULT
	OP_TRY
	OP_RETURN
)

// Instruction 携带整数操作数和用于运行时诊断的源码区间。
type Instruction struct {
	Op      Opcode
	Operand int
	Span    lexer.Span
}

// Program 是 Compile 生成的字节码及常量和调用信息。每次执行独立分配全局槽位。
type Program struct {
	functions  []function // 第 0 个函数执行顶层语句。
	globals    []string
	structures []structure
}

// function 保存单个函数的代码和元数据，locals 包含形参、接收者和块内局部变量的槽位。
type function struct {
	code         []Instruction
	constants    []any
	calls        []callSite
	constructors []constructor
	locals       int
}

// structure 保存结构体名和声明顺序的字段名。运行时实例按相同顺序存放字段值。
type structure struct {
	name   string
	fields []string
}

// constructor 记录一次结构体构造使用的类型和字段映射，以便按源码顺序求值。
type constructor struct {
	structure int
	fields    []int // 依实参求值顺序到结构体字段槽位的映射。
}

// callSite 描述一次调用的目标、实参数量和需要传给被调用方的泛型零值。
type callSite struct {
	target    int // 函数编号或内置函数编号。
	arguments int // 方法调用包含接收者。
	types     []zeroValue
}

// 泛型代码只需要在创建切片元素时计算类型零值。调用帧以零值传递
// 类型实参，Result 的零值递归使用错误类型的零值。
type zeroValue struct {
	value     any
	parameter int // 非零时为当前调用帧中的类型形参编号加一。
	failure   *zeroValue
}

// numericKind 约定 OP_CONVERT 的操作数，编译器也用它选择数值常量的 Go 类型。
type numericKind int

const (
	numberInt32 numericKind = iota
	numberInt64
	numberFloat32
	numberFloat64
)

// builtin 约定内置函数的编号，由编译器写入调用记录，VM 据此分派。
type builtin int

const (
	builtinPrintln builtin = iota
	builtinClock
	builtinRandInt
	builtinMakeSlice
	builtinLen
	builtinCap
	builtinAppend
	builtinReadFile
	builtinWriteFile
)

// OP_MAKE_RESULT 的操作数同时决定结果分支以及是否从栈上取载荷。
const (
	resultErr = iota
	resultOk
	resultOkVoid
)
