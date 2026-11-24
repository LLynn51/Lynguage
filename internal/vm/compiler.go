package vm

import (
	"fmt"
	"strconv"

	"lynguage/internal/ast"
	"lynguage/internal/checker"
	"lynguage/internal/lexer"
)

// methodKey 用结构体名和方法名定位方法，允许不同结构体使用相同的方法名。
type methodKey struct {
	structure string
	name      string
}

// compiler 持有整个程序的声明索引、产出的字节码和首个编译错误。
type compiler struct {
	info       *checker.Info
	program    Program
	functions  map[string]int
	methods    map[methodKey]int
	globals    map[string]int
	structures map[string]int
	fields     map[string]map[string]int
	err        error
}

// functionCompiler 持有单个函数的输出位置、局部作用域和类型形参槽位。
type functionCompiler struct {
	*compiler
	function       *function
	scopes         []map[string]int
	typeParameters map[string]int
}

// 编译入口与声明登记分两遍处理顶层项目。第一遍固定各项编号，
// 第二遍由 compileFunction 和顶层编译器使用这些编号生成指令。

// Compile 将 CheckWithInfo 检查过的 AST 转为可以重复执行的字节码。
func Compile(tree *ast.Program, info *checker.Info) (*Program, error) {
	if tree == nil || info == nil {
		return nil, fmt.Errorf("compilation requires a checked AST and type information")
	}
	c := compiler{
		info:       info,
		functions:  make(map[string]int),
		methods:    make(map[methodKey]int),
		globals:    make(map[string]int),
		structures: make(map[string]int),
		fields:     make(map[string]map[string]int),
	}
	// 预留第 0 个函数给顶层语句，其他函数和方法按声明顺序获取编号。
	c.program.functions = append(c.program.functions, function{})
	for _, item := range tree.Items {
		switch node := item.(type) {
		case *ast.StructDecl:
			c.registerStructure(node)
		case *ast.FuncDecl:
			index := len(c.program.functions)
			c.program.functions = append(c.program.functions, function{})
			if node.Receiver == nil {
				c.functions[node.Name.Text] = index
			} else {
				c.methods[methodKey{node.Receiver.Binding.Type.Name.Text, node.Name.Text}] = index
			}
		case *ast.BindingStmt:
			c.globals[node.Name.Text] = len(c.program.globals)
			c.program.globals = append(c.program.globals, node.Name.Text)
		}
	}
	// 全局绑定沿用源码执行顺序。函数体各自生成，调用指令使用已登记的编号。
	top := functionCompiler{compiler: &c, function: &c.program.functions[0]}
	for _, item := range tree.Items {
		switch node := item.(type) {
		case *ast.FuncDecl:
			c.compileFunction(node)
		case ast.Stmt:
			top.statement(node)
		}
		if c.err != nil {
			return nil, c.err
		}
	}
	top.implicitReturn(tree.Span())
	return &c.program, nil
}

// registerStructure 为结构体分配编号，并建立字段名到实例槽位的映射。
func (c *compiler) registerStructure(decl *ast.StructDecl) {
	name := decl.Name.Text
	shape := structure{name: name}
	fields := make(map[string]int, len(decl.Fields))
	// 声明顺序同时决定实例字段布局和输出时的字段顺序。
	for i, field := range decl.Fields {
		fields[field.Name.Text] = i
		shape.fields = append(shape.fields, field.Name.Text)
	}
	c.structures[name] = len(c.program.structures)
	c.fields[name] = fields
	c.program.structures = append(c.program.structures, shape)
}

// compileFunction 初始化函数或方法的编译状态，然后生成函数体和块尾返回指令。
func (c *compiler) compileFunction(decl *ast.FuncDecl) {
	index := c.functions[decl.Name.Text]
	if recv := decl.Receiver; recv != nil {
		index = c.methods[methodKey{recv.Binding.Type.Name.Text, decl.Name.Text}]
	}
	f := functionCompiler{
		compiler:       c,
		function:       &c.program.functions[index],
		scopes:         []map[string]int{make(map[string]int)},
		typeParameters: make(map[string]int),
	}
	// 对齐调用帧的排列顺序。方法接收者占第一个局部槽，其类型形参也排在前面。
	if recv := decl.Receiver; recv != nil {
		f.bindLocal(recv.Binding.Name.Text)
		for _, arg := range recv.Binding.Type.Args {
			f.bindType(arg.Name.Text)
		}
	}
	for _, param := range decl.TypeParams {
		f.bindType(param.Text)
	}
	for _, param := range decl.Params {
		f.bindLocal(param.Name.Text)
	}
	// 形参和函数体最外层声明共享作用域，内层块由 block 另开作用域。
	for _, stmt := range decl.Body.Statements {
		f.statement(stmt)
	}
	f.implicitReturn(decl.Body.Span())
}

// fail 记录首个编译错误及其源码位置，由 Compile 统一返回。
func (c *compiler) fail(span lexer.Span, format string, args ...any) {
	if c.err == nil {
		c.err = fmt.Errorf("%d:%d: %s", span.Start.Line, span.Start.Column, fmt.Sprintf(format, args...))
	}
}

// 槽位和指令辅助函数为后续语句、表达式编译提供统一的写入方式。
// bindLocal 与 variable 管理局部槽位，emit 与 patch 共同处理前向跳转。

// bindType 按声明顺序分配从 1 开始的形参编号，供 zeroValue 使用。
func (f *functionCompiler) bindType(name string) {
	f.typeParameters[name] = len(f.typeParameters) + 1
}

// bindLocal 在当前作用域登记变量，并为它在调用帧中预留一个槽位。
func (f *functionCompiler) bindLocal(name string) int {
	slot := f.function.locals
	f.function.locals++
	f.scopes[len(f.scopes)-1][name] = slot
	return slot
}

// emit 追加一条指令并返回其下标，便于后续回填操作数。
func (f *functionCompiler) emit(op Opcode, operand int, span lexer.Span) int {
	index := len(f.function.code)
	f.function.code = append(f.function.code, Instruction{Op: op, Operand: operand, Span: span})
	return index
}

// patch 将预留跳转的目标设为下一条即将生成的指令。
func (f *functionCompiler) patch(index int) {
	f.function.code[index].Operand = len(f.function.code)
}

// constant 把值写入当前函数的常量表，并生成对应的加载指令。
func (f *functionCompiler) constant(value any, span lexer.Span) {
	f.emit(OP_CONST, len(f.function.constants), span)
	f.function.constants = append(f.function.constants, value)
}

// implicitReturn 为裸 return 和可自然结束的块尾生成带空值的返回指令。
func (f *functionCompiler) implicitReturn(span lexer.Span) {
	f.constant(nil, span)
	f.emit(OP_RETURN, 0, span)
}

// variable 按词法作用域查找变量，生成调用方指定的局部或全局读写指令。
func (f *functionCompiler) variable(id *ast.Identifier, local, global Opcode) {
	// 从最近的块向外查找，优先使用遮蔽同名绑定的局部槽位。
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if slot, ok := f.scopes[i][id.Text]; ok {
			f.emit(local, slot, id.Span())
			return
		}
	}
	// 类型检查已经验证全局绑定在当前源码位置可见，编译器只需取得编号。
	if slot, ok := f.globals[id.Text]; ok {
		f.emit(global, slot, id.Span())
		return
	}
	f.fail(id.Span(), "no slot for variable %q", id.Text)
}

// 语句编译负责安排控制流，并消费 expression 留在栈上的求值结果。
// statement 分派各类语句，block 管理分支和循环体各自的作用域。

// block 为嵌套语句块建立作用域，块结束时移除名称映射。
func (f *functionCompiler) block(block *ast.Block) {
	f.scopes = append(f.scopes, make(map[string]int))
	for _, stmt := range block.Statements {
		f.statement(stmt)
	}
	f.scopes = f.scopes[:len(f.scopes)-1]
}

// statement 根据语句类型生成绑定、赋值、返回或控制流指令。
func (f *functionCompiler) statement(stmt ast.Stmt) {
	switch node := stmt.(type) {
	case *ast.BindingStmt:
		// 先生成初始值，再登记新绑定，使初始值可以引用同名的外层变量。
		f.expression(node.Value)
		if len(f.scopes) == 0 {
			f.emit(OP_STORE_GLOBAL, f.globals[node.Name.Text], node.Span())
		} else {
			f.emit(OP_STORE_LOCAL, f.bindLocal(node.Name.Text), node.Span())
		}
	case *ast.AssignStmt:
		f.assign(node)
	case *ast.ExprStmt:
		// 普通值和 void 调用的空值都遵循表达式的入栈约定。
		f.expression(node.Expr)
		f.emit(OP_POP, 0, node.Span())
	case *ast.ReturnStmt:
		if node.Value == nil {
			f.implicitReturn(node.Span())
		} else {
			f.expression(node.Value)
			f.emit(OP_RETURN, 0, node.Span())
		}
	case *ast.IfStmt:
		// 条件为假时跳到下一个分支，命中分支后跳到整个条件语句之后。
		ends := make([]int, 0, len(node.Branches))
		for _, branch := range node.Branches {
			f.expression(branch.Condition)
			next := f.emit(OP_JUMP_IF_FALSE, 0, branch.Condition.Span())
			f.block(branch.Body)
			ends = append(ends, f.emit(OP_JUMP, 0, branch.Span()))
			f.patch(next)
		}
		if node.Else != nil {
			f.block(node.Else)
		}
		// 全部分支连同 else 生成后，才能确定公共出口的位置。
		for _, end := range ends {
			f.patch(end)
		}
	case *ast.WhileStmt:
		// 循环体完成后回到条件位置重新求值，条件为假时跳出循环。
		start := len(f.function.code)
		f.expression(node.Condition)
		end := f.emit(OP_JUMP_IF_FALSE, 0, node.Condition.Span())
		f.block(node.Body)
		f.emit(OP_JUMP, start, node.Span())
		f.patch(end)
	default:
		f.fail(stmt.Span(), "unsupported statement %T", stmt)
	}
}

// ungroup 剥去外层括号，供赋值、调用和表达式分派识别实际节点。
func ungroup(expr ast.Expr) ast.Expr {
	for {
		group, ok := expr.(*ast.GroupExpr)
		if !ok {
			return expr
		}
		expr = group.Inner
	}
}

// assign 按赋值目标生成存储指令，并保持目标、索引和右值的求值顺序。
func (f *functionCompiler) assign(node *ast.AssignStmt) {
	switch target := ungroup(node.Target).(type) {
	case *ast.Identifier:
		f.expression(node.Value)
		f.variable(target, OP_STORE_LOCAL, OP_STORE_GLOBAL)
	case *ast.MemberExpr:
		// 保留接收者，等右值算完后由 OP_SET_FIELD 一起取走。
		f.expression(target.Target)
		f.expression(node.Value)
		f.emit(OP_SET_FIELD, f.field(target), target.Span())
	case *ast.IndexExpr:
		// 切片和索引各求值一次，OP_SET_INDEX 从栈顶依次取得右值、索引和切片。
		f.expression(target.Target)
		f.expression(target.Index)
		f.expression(node.Value)
		f.emit(OP_SET_INDEX, 0, target.Span())
	default:
		f.fail(node.Target.Span(), "unsupported assignment target %T", node.Target)
	}
}

// 表达式正常完成时留下一个栈值。expression 负责递归分派，
// 常量和运算辅助函数负责保留静态类型及语言规定的求值顺序。

// expression 生成一个表达式的指令，把求值结果留在操作数栈顶。
func (f *functionCompiler) expression(expr ast.Expr) {
	switch node := ungroup(expr).(type) {
	case *ast.Identifier:
		f.variable(node, OP_LOAD_LOCAL, OP_LOAD_GLOBAL)
	case *ast.LiteralExpr:
		// 数值按类型检查结果解析，布尔值和字符串可直接写入常量表。
		switch node.Kind {
		case lexer.True, lexer.False:
			f.constant(node.Kind == lexer.True, node.Span())
		case lexer.String:
			f.constant(node.StringValue, node.Span())
		case lexer.Integer, lexer.Float:
			f.number(node, node.Raw)
		}
	case *ast.UnaryExpr:
		f.unary(node)
	case *ast.BinaryExpr:
		f.binary(node)
	case *ast.CallExpr:
		f.call(node)
	case *ast.MemberExpr:
		// 先求接收者，再用编译时确定的字段槽位读取。
		f.expression(node.Target)
		f.emit(OP_GET_FIELD, f.field(node), node.Span())
	case *ast.IndexExpr:
		f.expression(node.Target)
		f.expression(node.Index)
		f.emit(OP_GET_INDEX, 0, node.Span())
	case *ast.TryExpr:
		// VM 在成功时留下内部值，在失败时直接退出当前调用帧。
		f.expression(node.Operand)
		f.emit(OP_TRY, 0, node.Question)
	default:
		f.fail(expr.Span(), "unsupported expression %T", expr)
	}
}

// numericKinds 将内置数值类型及别名映射到实际运行宽度。
var numericKinds = map[string]numericKind{
	"int32": numberInt32, "int": numberInt64, "int64": numberInt64,
	"float32": numberFloat32, "float": numberFloat64, "float64": numberFloat64,
}

// number 按表达式的静态数值类型解析字面量并写入常量表。
func (f *functionCompiler) number(expr ast.Expr, raw string) {
	typ := f.info.ExprTypes[expr]
	if typ == nil {
		f.fail(expr.Span(), "missing type for numeric literal %s", raw)
		return
	}
	// 整数按十进制和目标位宽解析，浮点常量按目标精度舍入。
	var value any
	var err error
	switch numericKinds[typ.Name.Text] {
	case numberInt32:
		var n int64
		n, err = strconv.ParseInt(raw, 10, 32)
		value = int32(n)
	case numberInt64:
		value, err = strconv.ParseInt(raw, 10, 64)
	case numberFloat32:
		var n float64
		n, err = strconv.ParseFloat(raw, 32)
		value = float32(n)
	case numberFloat64:
		value, err = strconv.ParseFloat(raw, 64)
	}
	// 理论上范围已由类型检查器验证，这里仍把解析失败关联到原表达式。
	if err != nil {
		f.fail(expr.Span(), "invalid numeric constant %s: %v", raw, err)
	}
	f.constant(value, expr.Span())
}

// unary 编译数字正负号和布尔取反，并把直接带负号的字面量作为整体解析。
func (f *functionCompiler) unary(node *ast.UnaryExpr) {
	if node.Operator.Kind == lexer.Minus {
		// 负号和字面量一起解析时，可以表示对应宽度的最小有符号整数。
		if literal, ok := node.Operand.(*ast.LiteralExpr); ok && (literal.Kind == lexer.Integer || literal.Kind == lexer.Float) {
			f.number(node, "-"+literal.Raw)
			return
		}
	}
	// 单目正号沿用操作数原值，负号和 not 由 VM 替换栈顶值。
	f.expression(node.Operand)
	switch node.Operator.Kind {
	case lexer.Minus:
		f.emit(OP_NEGATE, 0, node.Operator.Span)
	case lexer.Not:
		f.emit(OP_NOT, 0, node.Operator.Span)
	}
}

// binaryOps 覆盖两侧都需要求值的二元运算，逻辑短路由 binary 生成跳转。
var binaryOps = map[lexer.Kind]Opcode{
	lexer.Plus: OP_ADD, lexer.Minus: OP_SUBTRACT, lexer.Star: OP_MULTIPLY,
	lexer.Slash: OP_DIVIDE, lexer.Percent: OP_REMAINDER,
	lexer.Equal: OP_EQUAL, lexer.NotEqual: OP_NOT_EQUAL,
	lexer.Less: OP_LESS, lexer.LessEqual: OP_LESS_EQUAL,
	lexer.Greater: OP_GREATER, lexer.GreaterEqual: OP_GREATER_EQUAL,
}

// binary 按从左到右的顺序编译二元运算，为 and 和 or 保留短路行为。
func (f *functionCompiler) binary(node *ast.BinaryExpr) {
	f.expression(node.Left)
	span := node.Operator.Span
	kind := node.Operator.Kind
	if kind == lexer.And || kind == lexer.Or {
		// 条件跳转会弹出测试值，先复制左值以便短路时保留原结果。
		f.emit(OP_DUP, 0, span)
		jump := OP_JUMP_IF_FALSE
		if kind == lexer.Or {
			jump = OP_JUMP_IF_TRUE
		}
		end := f.emit(jump, 0, span)
		// 执行右侧时用右值替换左值，两条路径最后都只留一个栈值。
		f.emit(OP_POP, 0, span)
		f.expression(node.Right)
		f.patch(end)
		return
	}
	f.expression(node.Right)
	f.emit(binaryOps[kind], 0, span)
}

// field 按接收者的静态结构体类型解析字段槽位，供读写指令共用。
func (f *functionCompiler) field(node *ast.MemberExpr) int {
	// 同名字段在不同结构体中可能占据不同槽位，先从类型信息确定字段所属结构体。
	typ := f.info.ExprTypes[node.Target]
	if typ != nil {
		if slot, ok := f.fields[typ.Name.Text][node.Name.Text]; ok {
			return slot
		}
	}
	f.fail(node.Name.Span(), "no field slot for %q", node.Name.Text)
	return 0
}

// 调用编译先确定目标并按源码顺序压入实参，再生成专用指令或调用记录。
// call 处理函数、内置操作和构造器，methodCall 提前压入接收者，
// 两者通过 emitCall 统一存放调用点和泛型零值信息。

// builtins 将静态检查通过的内置函数名映射到 VM 的分派编号。
var builtins = map[string]builtin{
	"println": builtinPrintln, "clock_ns": builtinClock, "rand_int": builtinRandInt,
	"make_slice": builtinMakeSlice, "len": builtinLen, "cap": builtinCap, "append": builtinAppend,
	"read_file": builtinReadFile, "write_file": builtinWriteFile,
}

// call 识别调用的类别，按实参顺序生成求值指令并发出对应的操作。
func (f *functionCompiler) call(node *ast.CallExpr) {
	// 方法调用有额外的接收者求值步骤，交给 methodCall 处理。
	if member, ok := ungroup(node.Callee).(*ast.MemberExpr); ok {
		f.methodCall(node, member)
		return
	}
	callee := ungroup(node.Callee).(*ast.Identifier)
	for _, arg := range node.Args {
		f.expression(arg.Value)
	}
	name := callee.Text
	// Result 构造与数值类型转换都有专用指令，直接消费已入栈的实参。
	switch name {
	case "Ok":
		kind := resultOk
		if len(node.Args) == 0 {
			kind = resultOkVoid
		}
		f.emit(OP_MAKE_RESULT, kind, node.Span())
		return
	case "Err":
		f.emit(OP_MAKE_RESULT, resultErr, node.Span())
		return
	}
	if kind, ok := numericKinds[name]; ok {
		f.emit(OP_CONVERT, int(kind), node.Span())
		return
	}
	if index, ok := f.structures[name]; ok {
		// 命名实参按源码顺序求值，字段映射将它们放进声明顺序的实例槽位。
		site := constructor{structure: index, fields: make([]int, len(node.Args))}
		for i, arg := range node.Args {
			site.fields[i] = f.fields[name][arg.Name.Text]
		}
		f.emit(OP_NEW_STRUCT, len(f.function.constructors), node.Span())
		f.function.constructors = append(f.function.constructors, site)
		return
	}
	if target, ok := builtins[name]; ok {
		// make_slice 需要元素零值来初始化长度范围，其余内置函数直接使用值实参。
		var types []*ast.TypeRef
		if target == builtinMakeSlice {
			types = f.info.TypeArgs[node]
			if len(types) != 1 {
				f.fail(node.Span(), "missing element type for make_slice")
			}
		}
		f.emitCall(OP_BUILTIN, int(target), len(node.Args), node.Span(), types)
		return
	}
	if target, ok := f.functions[name]; ok {
		f.emitCall(OP_CALL, target, len(node.Args), node.Span(), f.info.TypeArgs[node])
		return
	}
	f.fail(node.Span(), "no call target for %q", name)
}

// methodCall 按接收者的静态类型定位方法，并将接收者排在普通实参之前。
func (f *functionCompiler) methodCall(node *ast.CallExpr, member *ast.MemberExpr) {
	// 方法表用结构体类型和方法名查找，实际对象在运行时作为值传入。
	receiver := f.info.ExprTypes[member.Target]
	if receiver == nil {
		f.fail(member.Span(), "missing receiver type for method %q", member.Name.Text)
		return
	}
	target, ok := f.methods[methodKey{receiver.Name.Text, member.Name.Text}]
	if !ok {
		f.fail(member.Span(), "no method target for %s.%s", receiver.Name.Text, member.Name.Text)
		return
	}
	f.expression(member.Target)
	for _, arg := range node.Args {
		f.expression(arg.Value)
	}
	// 类型实参排列与方法帧的声明顺序一致，接收者类型实参排在方法自己的实参前。
	types := append(append([]*ast.TypeRef(nil), receiver.Args...), f.info.TypeArgs[node]...)
	f.emitCall(OP_CALL_METHOD, target, len(node.Args)+1, node.Span(), types)
}

// emitCall 记录调用目标、值实参数量和泛型零值计划，再写入引用该记录的指令。
func (f *functionCompiler) emitCall(op Opcode, target, count int, span lexer.Span, types []*ast.TypeRef) {
	site := callSite{target: target, arguments: count}
	for _, typ := range types {
		site.types = append(site.types, f.zero(typ))
	}
	f.emit(op, len(f.function.calls), span)
	f.function.calls = append(f.function.calls, site)
}

// zero 将已解析的类型转成零值计划，运行时由 zeroValue.resolve 求出具体值。
func (f *functionCompiler) zero(typ *ast.TypeRef) zeroValue {
	name := typ.Name.Text
	// 类型形参引用调用帧中的零值槽位，预置类型和结构体使用各自的零值规则。
	if !typ.GlobalName {
		index := f.typeParameters[name]
		if index == 0 {
			f.fail(typ.Span(), "no type parameter %q in this function", name)
		}
		return zeroValue{parameter: index}
	}
	switch name {
	case "int32":
		return zeroValue{value: int32(0)}
	case "int", "int64":
		return zeroValue{value: int64(0)}
	case "float32":
		return zeroValue{value: float32(0)}
	case "float", "float64":
		return zeroValue{value: float64(0)}
	case "str":
		return zeroValue{value: ""}
	case "bool":
		return zeroValue{value: false}
	case "Slice":
		return zeroValue{value: SliceHeader{}}
	case "Result":
		// Result 的零态是 Err，错误载荷的零值也可能依赖当前调用的类型形参。
		failure := f.zero(typ.Args[1])
		return zeroValue{failure: &failure}
	case "void":
		return zeroValue{}
	default:
		// 保留空结构体引用的指针类型，便于 VM 识别对象和全局槽位的状态。
		return zeroValue{value: (*structValue)(nil)}
	}
}
