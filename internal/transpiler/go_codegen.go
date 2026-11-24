// transpiler 将经过类型检查的 Lynguage 程序转成可独立编译的 Go 源码。
package transpiler

import (
	"fmt"
	"go/format"
	"math"
	"strconv"
	"strings"

	"lynguage/internal/ast"
	"lynguage/internal/checker"
	"lynguage/internal/lexer"
)

type methodKey struct {
	structure string
	name      string
}

type generator struct {
	output     strings.Builder
	info       *checker.Info
	structures map[string]*ast.StructDecl
	functions  map[string]*ast.FuncDecl
	methods    map[methodKey]*ast.FuncDecl
	graph      typeGraph
	erased     bool
	decl       ast.Item
	scopes     []map[string]string
	typeParams map[string]string
	typeIDs    map[string]int
	returnType *ast.TypeRef
	indent     int
	nextLocal  int
	nextValue  int
	err        error
}

// 先登记结构体和方法签名，再分别生成声明和入口。声明可前向引用，
// 入口中的全局初始化及其他顶层语句仍按 Lynguage 源码顺序执行。

// Generate 将 CheckWithInfo 检查过的 AST 生成一个格式化后的独立 Go 文件。
func Generate(tree *ast.Program, info *checker.Info) ([]byte, error) {
	if tree == nil || info == nil {
		return nil, fmt.Errorf("Go generation requires a checked AST and type information")
	}
	g := generator{
		info:       info,
		structures: make(map[string]*ast.StructDecl),
		functions:  make(map[string]*ast.FuncDecl),
		methods:    make(map[methodKey]*ast.FuncDecl),
		graph:      typeGraph{parameters: make(map[ast.Item][]int)},
	}
	g.register(tree)
	g.generate(tree)
	// 检测到增长循环时重新生成整份文件，让共享同一类型的声明使用一致的表示。
	if g.err == nil && g.graph.hasGrowingCycle() {
		g.erased = true
		g.output.Reset()
		g.generate(tree)
	}
	if g.err != nil {
		return nil, g.err
	}
	// format.Source 同时验证生成文件的 Go 语法并统一排版。
	source, err := format.Source([]byte(g.output.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated Go: %w", err)
	}
	return source, nil
}

// register 登记全局声明及形参顺序，供静态调用和实例化分析使用。
func (g *generator) register(tree *ast.Program) {
	// 方法调用根据接收者类型和方法声明决定使用 Go 方法还是泛型函数。
	for _, item := range tree.Items {
		switch node := item.(type) {
		case *ast.StructDecl:
			g.structures[node.Name.Text] = node
			g.graph.register(node, len(node.TypeParams))
		case *ast.FuncDecl:
			count := len(node.TypeParams)
			if node.Receiver != nil {
				key := methodKey{node.Receiver.Binding.Type.Name.Text, node.Name.Text}
				g.methods[key] = node
				count += len(node.Receiver.Binding.Type.Args)
			} else {
				g.functions[node.Name.Text] = node
			}
			g.graph.register(node, count)
		}
	}
}

// generate 为当前类型表示生成运行时辅助代码、包级声明及 Go 入口。
func (g *generator) generate(tree *ast.Program) {
	// 运行时辅助函数与脚本代码放在同一文件中，系统 Go 只需标准库即可编译。
	g.output.WriteString(goRuntime)
	if g.erased {
		g.output.WriteString(goErasedRuntime)
	}
	g.resetBody()
	for _, item := range tree.Items {
		switch node := item.(type) {
		case *ast.StructDecl:
			g.structure(node)
		case *ast.FuncDecl:
			g.function(node)
		case *ast.BindingStmt:
			g.line("var lynGlobal_%s lynGlobal[%s]", node.Name.Text, g.goType(node.Type))
		}
	}
	g.main(tree)
}

// main 生成 Go 入口，执行顶层语句并安装源码错误报告逻辑。
func (g *generator) main(tree *ast.Program) {
	g.resetBody()
	g.line("// main 按源码顺序执行 Lynguage 顶层语句。")
	g.line("func main() {")
	g.indent++
	g.line("defer lynReportFailure()")
	// 顶层声明已在包级生成，此处只执行顶层语句及全局绑定的初始值。
	for _, item := range tree.Items {
		if stmt, ok := item.(ast.Stmt); ok {
			if g.statement(stmt) {
				break
			}
		}
	}
	g.indent--
	g.line("}")
}

// 声明生成共用类型映射与命名规则。结构体映射为 Go 指针，接收者方法
// 依类型形参情况生成原生方法或接收者排在首个值实参位置的泛型函数。

// structure 生成 Go 结构体和供 println 使用的格式化方法。
func (g *generator) structure(decl *ast.StructDecl) {
	g.resetBody()
	g.decl = decl
	params := g.bindTypes(decl.TypeParams)
	if g.erased {
		params = nil
	}
	name := "lynStruct_" + decl.Name.Text
	// 字段保留声明顺序，并使用原生 Go 类型表示实际存储布局。
	g.line("type %s%s struct {", name, typeList(params, " any"))
	g.indent++
	for _, field := range decl.Fields {
		g.line("lynField_%s %s", field.Name.Text, g.goType(field.Type))
	}
	g.indent--
	g.line("}")
	// 格式化方法在输出字段前登记当前结构体引用，供嵌套值检测循环。
	g.line("// lynFormat 按声明顺序输出 %s 的字段，并检测当前路径中的循环引用。", decl.Name.Text)
	g.line("func (self *%s%s) lynFormat(out *strings.Builder, seen map[any]bool) {", name, typeList(params, ""))
	g.indent++
	g.line("if !lynBeginStruct(out, seen, self, self == nil, %q) { return }", decl.Name.Text)
	for i, field := range decl.Fields {
		g.line("lynWriteField(out, seen, %q, self.lynField_%s, %t)", field.Name.Text, field.Name.Text, i != 0)
	}
	g.line("lynEndStruct(out, seen, self)")
	g.indent--
	g.line("}")
	g.resetBody()
}

// function 生成函数或方法的 Go 签名、接收者检查和函数体。
func (g *generator) function(decl *ast.FuncDecl) {
	g.resetBody()
	g.decl = decl
	g.scopes = append(g.scopes, make(map[string]string))
	g.returnType = decl.ReturnType
	name := "lynFunc_" + decl.Name.Text
	var receiver, receiverName string
	var typeParams, params []string
	if recv := decl.Receiver; recv != nil {
		ref := recv.Binding.Type
		var receiverParams []string
		// 接收者位置声明的是新形参，Go 的原生泛型接收者和泛型函数都沿用此顺序。
		for _, arg := range ref.Args {
			receiverParams = append(receiverParams, g.bindType(arg.Name.Text))
		}
		g.recordReceiver(decl)
		receiverType := "*lynStruct_" + ref.Name.Text
		if !g.erased {
			receiverType += typeList(receiverParams, "")
		}
		receiverName = g.bindLocal(recv.Binding.Name.Text)
		receiverBinding := receiverName + " " + receiverType
		params = append(params, "lynCallAt lynLocation")
		if len(decl.TypeParams) == 0 {
			receiver = "(" + receiverBinding + ") "
			name = "lynMethod_" + decl.Name.Text
		} else {
			// Go 方法没有独立的类型形参，用泛型函数接收两组类型实参。
			name = genericMethodName(ref.Name.Text, decl.Name.Text)
			params = append(params, receiverBinding)
		}
		if receiver == "" || g.erased {
			typeParams = receiverParams
		}
	}
	typeParams = append(typeParams, g.bindTypes(decl.TypeParams)...)
	if g.erased {
		// 擦除后的声明在普通值参数前接收每个类型形参的零值。
		for _, param := range typeParams {
			params = append(params, param+" any")
		}
		typeParams = nil
	}
	for _, param := range decl.Params {
		params = append(params, g.bindLocal(param.Name.Text)+" "+g.goType(param.Type))
	}
	g.line("// %s 执行 Lynguage 声明 %s 的函数体。", name, decl.Name.Text)
	g.line("func %s%s%s(%s) %s {", receiver, name, typeList(typeParams, " any"), strings.Join(params, ", "), g.goType(decl.ReturnType))
	g.indent++
	if receiverName != "" {
		// 入体时实参已经求值，空引用的报错位置来自调用点。
		g.line("if %s == nil { lynFail(lynCallAt, %q) }", receiverName, "method call on nil struct reference")
	}
	if !g.statements(decl.Body.Statements) {
		// Go 的终止语句判定与 Lynguage 不同，只在 Go 需要时补上块尾路径。
		if g.goType(decl.ReturnType) == "lynVoid" {
			g.line("return lynVoid{}")
		} else {
			g.line("panic(%q)", "unreachable Lynguage function exit")
		}
	}
	g.indent--
	g.line("}")
	g.resetBody()
}

// genericMethodName 编码结构体名长度和方法名，避免下划线造成拼接冲突。
func genericMethodName(structure, method string) string {
	return fmt.Sprintf("lynGenericMethod_%d_%s_%s", len(structure), structure, method)
}

// 类型映射保留具体位宽、引用语义和类型形参。typeList 同时用于声明的
// 类型形参列表和调用或类型引用中的实参列表。

var primitiveTypes = map[string]string{
	"int": "int64", "int32": "int32", "int64": "int64",
	"float": "float64", "float32": "float32", "float64": "float64",
	"bool": "bool", "str": "string", "void": "lynVoid",
}

// goType 将一个 Lynguage 类型引用映射为 Go 类型表达式。
func (g *generator) goType(typ *ast.TypeRef) string {
	if typ == nil {
		g.fail(lexer.Span{}, "missing checked type")
		return "any"
	}
	name := typ.Name.Text
	// 源码类型名可能被形参遮蔽，类型检查器生成的全局名始终指向全局声明。
	if param := g.typeParams[name]; !typ.GlobalName && param != "" {
		if g.erased {
			return "any"
		}
		return param
	}
	if primitive := primitiveTypes[name]; primitive != "" {
		return primitive
	}
	// 值类型用原生切片或结果结构，Lynguage 结构体统一通过指针传递。
	if g.erased {
		switch name {
		case "Slice":
			return "lynSlice[any]"
		case "Result":
			return "lynResult[any, any]"
		default:
			return "*lynStruct_" + name
		}
	}
	g.recordInstantiation(g.structures[name], typ.Args)
	args := g.goTypeArgs(typ.Args)
	switch name {
	case "Slice":
		return "lynSlice" + args
	case "Result":
		return "lynResult" + args
	default:
		return "*lynStruct_" + name + args
	}
}

// goTypeArgs 将已解析的类型实参映射为 Go 方括号列表。
func (g *generator) goTypeArgs(types []*ast.TypeRef) string {
	args := make([]string, len(types))
	for i, typ := range types {
		args[i] = g.goType(typ)
	}
	return typeList(args, "")
}

// typeList 组装方括号列表，suffix 向每个类型形参附加约束。
func typeList(types []string, suffix string) string {
	if len(types) == 0 {
		return ""
	}
	return "[" + strings.Join(types, suffix+", ") + suffix + "]"
}

// bindType 为类型形参分配带前缀的合法 Go 名称。
func (g *generator) bindType(name string) string {
	goName := "lynType_" + name
	g.typeIDs[name] = g.graph.parameters[g.decl][len(g.typeParams)]
	g.typeParams[name] = goName
	return goName
}

// bindTypes 依声明顺序登记类型形参并返回其 Go 名称。
func (g *generator) bindTypes(types []ast.Identifier) []string {
	names := make([]string, len(types))
	for i, param := range types {
		names[i] = g.bindType(param.Text)
	}
	return names
}

// 生成器用独立编号为每个函数创建局部名称和临时值。line 与 temporary
// 负责发出 Go 语句，fail 保留首个带源码位置的生成错误。

// resetBody 恢复顶层状态，为下一个独立声明清除函数内的名字和返回类型。
func (g *generator) resetBody() {
	g.scopes = nil
	g.decl = nil
	g.returnType = nil
	g.typeParams = make(map[string]string)
	g.typeIDs = make(map[string]int)
	g.nextLocal, g.nextValue = 0, 0
}

// bindLocal 在当前词法作用域登记一个唯一的 Go 变量名。
func (g *generator) bindLocal(name string) string {
	goName := fmt.Sprintf("lynLocal_%d_%s", g.nextLocal, name)
	g.nextLocal++
	g.scopes[len(g.scopes)-1][name] = goName
	return goName
}

// local 从最内层作用域查找局部变量，空结果表示使用全局槽位。
func (g *generator) local(name string) string {
	for i := len(g.scopes) - 1; i >= 0; i-- {
		if goName := g.scopes[i][name]; goName != "" {
			return goName
		}
	}
	return ""
}

// line 在当前缩进写入一行 Go 源码。
func (g *generator) line(pattern string, args ...any) {
	g.output.WriteString(strings.Repeat("\t", g.indent))
	fmt.Fprintf(&g.output, pattern, args...)
	g.output.WriteByte('\n')
}

// temporary 把一个 Go 表达式求值到新的临时变量，并返回变量名。
func (g *generator) temporary(pattern string, args ...any) string {
	name := "lynValue" + strconv.Itoa(g.nextValue)
	g.nextValue++
	g.line("%s := %s", name, fmt.Sprintf(pattern, args...))
	return name
}

// location 将 Lynguage 源码区间的起点生成 Go 运行时位置值。
func location(span lexer.Span) string {
	return fmt.Sprintf("lynLocation{%d, %d}", span.Start.Line, span.Start.Column)
}

// fail 保存首个生成错误，供 Generate 在格式化前返回。
func (g *generator) fail(span lexer.Span, pattern string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf("%d:%d: %s", span.Start.Line, span.Start.Column, fmt.Sprintf(pattern, args...))
	}
}

// 语句生成负责建立作用域和安排控制流。value 可以在求值点插入临时变量
// 及提前返回语句，因此每个条件都在原本应求值的 Go 分支或循环中生成。

// block 在当前 Go 块内生成一个 Lynguage 词法作用域，并报告块是否终止。
func (g *generator) block(block *ast.Block) bool {
	g.scopes = append(g.scopes, make(map[string]string))
	ends := g.statements(block.Statements)
	g.scopes = g.scopes[:len(g.scopes)-1]
	return ends
}

// statements 依次生成语句，在 Go 控制流明确终止时结束当前块。
func (g *generator) statements(stmts []ast.Stmt) bool {
	for _, stmt := range stmts {
		if g.statement(stmt) {
			return true
		}
	}
	return false
}

// statement 生成一条语句，并报告该语句能否让 Go 认定当前块已经终止。
func (g *generator) statement(stmt ast.Stmt) bool {
	switch node := stmt.(type) {
	case *ast.BindingStmt:
		// 初始值在引入新名称前求值，以保留同名外层变量的可见性。
		value := g.value(node.Value)
		if len(g.scopes) == 0 {
			g.line("lynGlobal_%s.store(%s)", node.Name.Text, value)
		} else {
			name := g.bindLocal(node.Name.Text)
			g.line("%s := %s", name, value)
			g.line("_ = %s", name) // Lynguage 允许声明未使用的局部变量。
		}
	case *ast.AssignStmt:
		g.assign(node)
	case *ast.ExprStmt:
		g.line("_ = %s", g.value(node.Expr))
	case *ast.ReturnStmt:
		if node.Value == nil {
			g.line("return lynVoid{}")
		} else {
			g.line("return %s", g.value(node.Value))
		}
		return true
	case *ast.IfStmt:
		return g.conditional(node.Branches, node.Else)
	case *ast.WhileStmt:
		return g.loop(node)
	default:
		g.fail(stmt.Span(), "unsupported statement %T", stmt)
	}
	return false
}

// conditional 将 if 和 elif 展开为嵌套的 Go 条件块，并汇总各分支的终止状态。
func (g *generator) conditional(branches []ast.ConditionalBranch, otherwise *ast.Block) bool {
	if len(branches) == 0 {
		return g.block(otherwise)
	}
	branch := branches[0]
	g.line("if %s {", g.value(branch.Condition))
	g.indent++
	thenEnds := g.block(branch.Body)
	g.indent--
	var elseEnds bool
	// 后续条件的计算也位于 else 内，只有前面所有分支未命中时才运行。
	if len(branches) > 1 || otherwise != nil {
		g.line("} else {")
		g.indent++
		elseEnds = g.conditional(branches[1:], otherwise)
		g.indent--
	}
	g.line("}")
	return thenEnds && elseEnds
}

// loop 生成 while 循环，每轮求条件并在需要时退出。
func (g *generator) loop(node *ast.WhileStmt) bool {
	literal, _ := ungroup(node.Condition).(*ast.LiteralExpr)
	infinite := literal != nil && literal.Kind == lexer.True
	g.line("for {")
	g.indent++
	// 字面量 true 可使用 Go 的无限循环。其他条件每轮重新求值，允许 ? 提前返回。
	if !infinite {
		condition := g.value(node.Condition)
		g.line("if !%s { break }", condition)
	}
	g.block(node.Body)
	g.indent--
	g.line("}")
	return infinite
}

// assign 保留赋值目标和右值的求值顺序，然后生成局部、全局或聚合值写入。
func (g *generator) assign(stmt *ast.AssignStmt) {
	switch target := ungroup(stmt.Target).(type) {
	case *ast.Identifier:
		value := g.value(stmt.Value)
		if local := g.local(target.Text); local != "" {
			g.line("%s = %s", local, value)
		} else {
			g.line("lynGlobal_%s.store(%s)", target.Text, value)
		}
	case *ast.MemberExpr:
		// 先取得接收者，再求右值，最后检查写入的结构体引用。
		receiver := g.value(target.Target)
		value := g.value(stmt.Value)
		g.line("lynFieldReceiver(%s, %s).lynField_%s = %s", location(target.Span()), receiver, target.Name.Text, value)
	case *ast.IndexExpr:
		// 保存切片头和下标，等右值求完后由 lynSetIndex 检查边界并写入。
		slice, index := g.value(target.Target), g.value(target.Index)
		value := g.value(stmt.Value)
		name := "lynSetIndex"
		if g.erased {
			name += "[any]"
		}
		g.line("%s(%s, %s, %s, %s)", name, location(target.Span()), slice, index, value)
	default:
		g.fail(stmt.Target.Span(), "unsupported assignment target %T", stmt.Target)
	}
}

// 表达式按源码从左到右生成。每一步使用稳定的临时值，后面的调用
// 即使修改全局变量或别名，也只会影响尚未求值的表达式。

// ungroup 去掉外层括号，使分派逻辑取得实际的表达式节点。
func ungroup(expr ast.Expr) ast.Expr {
	for {
		group, ok := expr.(*ast.GroupExpr)
		if !ok {
			return expr
		}
		expr = group.Inner
	}
}

// value 生成表达式的求值语句并返回持有结果的 Go 临时变量名。
func (g *generator) value(expr ast.Expr) string {
	value := g.expression(expr)
	if g.erased {
		// 泛型存储槽可能装箱，静态已知的表达式在运算或传参之前恢复其 Go 类型。
		if typ := g.goType(g.info.ExprTypes[expr]); typ != "any" {
			return g.temporary("lynTyped[%s](%s)", typ, value)
		}
	}
	return value
}

// expression 按节点类别发出求值语句，产生按当前存储表示得到的原始值。
func (g *generator) expression(expr ast.Expr) string {
	switch node := ungroup(expr).(type) {
	case *ast.Identifier:
		// 将读取本身固定在当前步骤，避免后面的子表达式改变变量后才取值。
		if local := g.local(node.Text); local != "" {
			return g.temporary("%s", local)
		}
		return g.temporary("lynGlobal_%s.load(%s, %q)", node.Text, location(node.Span()), node.Text)
	case *ast.LiteralExpr:
		return g.literal(node)
	case *ast.UnaryExpr:
		return g.unary(node)
	case *ast.BinaryExpr:
		return g.binary(node)
	case *ast.CallExpr:
		return g.call(node)
	case *ast.MemberExpr:
		return g.temporary("lynFieldReceiver(%s, %s).lynField_%s", location(node.Span()), g.value(node.Target), node.Name.Text)
	case *ast.IndexExpr:
		// 切片表达式先于索引求值，之后读取该切片头所指向的元素。
		slice, index := g.value(node.Target), g.value(node.Index)
		return g.temporary("lynGetIndex(%s, %s, %s)", location(node.Span()), slice, index)
	case *ast.TryExpr:
		return g.tryValue(node)
	default:
		g.fail(expr.Span(), "unsupported expression %T", expr)
		return "lynVoid{}"
	}
}

// literal 生成布尔、字符串或已定型的数值常量。
func (g *generator) literal(node *ast.LiteralExpr) string {
	// 布尔值和解码后的字符串可直接生成，数值还需按检查器给出的位宽定型。
	switch node.Kind {
	case lexer.True, lexer.False:
		return g.temporary("%t", node.Kind == lexer.True)
	case lexer.String:
		return g.temporary("%q", node.StringValue)
	default:
		return g.numericLiteral(node, node.Raw)
	}
}

// numericLiteral 生成目标位宽的临时值，保留十进制整数和负浮点零。
func (g *generator) numericLiteral(expr ast.Expr, raw string) string {
	typ := g.goType(g.info.ExprTypes[expr])
	switch typ {
	case "int32", "int64":
		// Lynguage 前导零仍表示十进制，转换为 Go 字面量前先标准化拼写。
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			g.fail(expr.Span(), "invalid checked integer %q: %v", raw, err)
		}
		return g.temporary("%s(%d)", typ, value)
	case "float32", "float64":
		bits := 64
		if typ == "float32" {
			bits = 32
		}
		value, err := strconv.ParseFloat(raw, bits)
		if err != nil {
			g.fail(expr.Span(), "invalid checked float %q: %v", raw, err)
		}
		// Go 常量零没有符号，只有负零需要从其 IEEE 754 位模式构造。
		if value == 0 && math.Signbit(value) {
			return g.temporary("lynNegativeZero%d()", bits)
		}
		return g.temporary("%s(%s)", typ, strconv.FormatFloat(value, 'g', -1, bits))
	default:
		g.fail(expr.Span(), "unsupported numeric type %s", typ)
		return "lynVoid{}"
	}
}

// unary 生成一元运算，对直接带负号的数值字面量使用完整拼写定值。
func (g *generator) unary(node *ast.UnaryExpr) string {
	if node.Operator.Kind == lexer.Minus {
		// 最小有符号整数的绝对值无法先存进正数临时变量。
		if literal, ok := node.Operand.(*ast.LiteralExpr); ok && (literal.Kind == lexer.Integer || literal.Kind == lexer.Float) {
			return g.numericLiteral(node, "-"+literal.Raw)
		}
	}
	operand := g.value(node.Operand)
	switch node.Operator.Kind {
	case lexer.Not:
		return g.temporary("!%s", operand)
	case lexer.Minus:
		return g.temporary("-%s", operand)
	default:
		return operand
	}
}

// binary 生成二元运算，并为短路、除零诊断和浮点舍入保留 VM 语义。
func (g *generator) binary(node *ast.BinaryExpr) string {
	left := g.value(node.Left)
	op := node.Operator.Kind
	if op == lexer.And || op == lexer.Or {
		return g.shortCircuit(node, left)
	}
	right := g.value(node.Right)
	resultType := g.goType(g.info.ExprTypes[node])
	// Go 变量运算保留整数宽度，显式检查除数以报告 Lynguage 源码位置。
	if (op == lexer.Slash || op == lexer.Percent) && (resultType == "int32" || resultType == "int64") {
		g.line("if %s == 0 { lynFail(%s, %q) }", right, location(node.Operator.Span), "integer division by zero")
	}
	if resultType == "float32" || resultType == "float64" {
		// 显式转换保留每个浮点中间结果的舍入，避免 Go 融合乘加改变结果。
		return g.temporary("%s(%s %s %s)", resultType, left, op, right)
	}
	return g.temporary("%s %s %s", left, op, right)
}

// shortCircuit 仅在需要右值时进入 Go 条件块，并在块外保留最终布尔值。
func (g *generator) shortCircuit(node *ast.BinaryExpr, left string) string {
	result := g.temporary("%s", left)
	condition := result
	if node.Operator.Kind == lexer.Or {
		condition = "!" + result
	}
	// 右值可能包含调用及 ?，让所有右值语句都留在条件块内。
	g.line("if %s {", condition)
	g.indent++
	g.line("%s = %s", result, g.value(node.Right))
	g.indent--
	g.line("}")
	return result
}

// tryValue 展开 ?，错误分支返回当前函数的 Result，成功分支继续使用载荷。
func (g *generator) tryValue(node *ast.TryExpr) string {
	operand := g.value(node.Operand)
	// 错误类型由检查器保证相同，成功类型要换成当前函数的声明类型。
	typeArgs := "[any, any]"
	if !g.erased {
		typeArgs = g.goTypeArgs(g.returnType.Args)
	}
	g.line("if !%s.ok { return lynErr%s(%s.failure) }", operand, typeArgs, operand)
	return g.temporary("%s.value", operand)
}

// 调用生成分为实参求值和目标选择。普通函数与泛型方法显式使用检查器
// 得出的类型实参，结构体构造在所有值准备好后按字段名组装。

type builtinCall struct {
	name     string
	location bool
}

var builtins = map[string]builtinCall{
	"println": {"lynPrintln", true}, "clock_ns": {"lynClock", false}, "rand_int": {"lynRandInt", true},
	"make_slice": {"lynMakeSlice", true}, "len": {"lynLen", false}, "cap": {"lynCap", false},
	"append": {"lynAppend", true}, "read_file": {"lynReadFile", false}, "write_file": {"lynWriteFile", false},
}

// call 生成函数、内置操作、结构体构造或方法调用，并返回其结果。
func (g *generator) call(node *ast.CallExpr) string {
	switch callee := ungroup(node.Callee).(type) {
	case *ast.MemberExpr:
		return g.methodCall(node, callee)
	case *ast.Identifier:
		name := callee.Text
		args := g.arguments(node.Args)
		if g.structures[name] != nil {
			return g.constructor(node, args)
		}
		// Result 构造、数值转换与内置函数使用语言运行时约定的 Go 签名。
		types := g.info.TypeArgs[node]
		var typeArgs string
		if !g.erased {
			typeArgs = g.goTypeArgs(types)
		}
		switch name {
		case "Ok":
			if len(args) == 0 {
				args = append(args, "lynVoid{}")
			}
			return g.resultCall("lynOk", typeArgs, args)
		case "Err":
			return g.resultCall("lynErr", typeArgs, args)
		case "int", "int32", "int64", "float", "float32", "float64":
			return g.invocation(primitiveTypes[name], args)
		}
		if builtin, ok := builtins[name]; ok {
			return g.builtinCall(node, builtin, args, types, typeArgs)
		}
		g.recordInstantiation(g.functions[name], types)
		if g.erased {
			args = append(g.zeroArguments(types), args...)
		}
		// 原生泛型显式写出类型实参，覆盖只靠返回类型就能推断的调用。
		return g.invocation("lynFunc_"+name+typeArgs, args)
	default:
		g.fail(node.Callee.Span(), "unsupported call target %T", node.Callee)
		return "lynVoid{}"
	}
}

// resultCall 选择结果构造器的类型实参，擦除时统一使用两个 any 载荷槽。
func (g *generator) resultCall(name, typeArgs string, args []string) string {
	if g.erased {
		typeArgs = "[any, any]"
	}
	return g.invocation(name+typeArgs, args)
}

// builtinCall 补齐源码位置，并根据容器表示调用对应的内置辅助函数。
func (g *generator) builtinCall(node *ast.CallExpr, builtin builtinCall, args []string, types []*ast.TypeRef, typeArgs string) string {
	if builtin.location {
		args = append([]string{location(node.Span())}, args...)
	}
	if g.erased {
		// 切片擦除后要用真实元素零值填充，文件内置的固定签名则要转换结果载荷。
		switch builtin.name {
		case "lynMakeSlice":
			return g.invocation("lynMakeErasedSlice", append(args, g.zeroValue(types[0])))
		case "lynLen", "lynCap", "lynAppend":
			typeArgs = "[any]"
		case "lynReadFile", "lynWriteFile":
			return g.temporary("lynEraseResult(%s)", g.invocation(builtin.name, args))
		}
	}
	return g.invocation(builtin.name+typeArgs, args)
}

// arguments 按源码顺序逐项求值，返回供原生 Go 调用使用的临时变量名。
func (g *generator) arguments(args []ast.Argument) []string {
	values := make([]string, len(args))
	for i, arg := range args {
		values[i] = g.value(arg.Value)
	}
	return values
}

// invocation 生成一次 Go 调用并将其结果固定在新的临时变量中。
func (g *generator) invocation(target string, args []string) string {
	return g.temporary("%s(%s)", target, strings.Join(args, ", "))
}

// constructor 用已求值的命名实参生成 Go 结构体字面量并取地址。
func (g *generator) constructor(node *ast.CallExpr, args []string) string {
	for i, arg := range node.Args {
		args[i] = "lynField_" + arg.Name.Text + ": " + args[i]
	}
	typ := strings.TrimPrefix(g.goType(g.info.ExprTypes[node]), "*")
	return g.temporary("&%s{%s}", typ, strings.Join(args, ", "))
}

// methodCall 按接收者类型选定绑定方法，先求接收者，再求普通实参。
func (g *generator) methodCall(node *ast.CallExpr, member *ast.MemberExpr) string {
	receiverType := g.info.ExprTypes[member.Target]
	if receiverType == nil {
		g.fail(member.Span(), "missing checked receiver type")
		return "lynVoid{}"
	}
	key := methodKey{receiverType.Name.Text, member.Name.Text}
	decl := g.methods[key]
	if decl == nil {
		g.fail(member.Span(), "missing method %s.%s", key.structure, key.name)
		return "lynVoid{}"
	}
	// 先固定对象引用，即使某个实参再次赋值给原绑定，调用目标仍然相同。
	receiver := g.value(member.Target)
	at := location(node.Span())
	args := g.arguments(node.Args)
	// 实参顺序与声明一致，先传接收者的类型实参，再传方法自己的类型实参。
	types := append(append([]*ast.TypeRef(nil), receiverType.Args...), g.info.TypeArgs[node]...)
	g.recordInstantiation(decl, types)
	if g.erased {
		args = append(g.zeroArguments(types), args...)
	}
	if len(decl.TypeParams) == 0 {
		return g.invocation(receiver+".lynMethod_"+key.name, append([]string{at}, args...))
	}
	name := genericMethodName(key.structure, key.name)
	if !g.erased {
		name += g.goTypeArgs(types)
	}
	return g.invocation(name, append([]string{at, receiver}, args...))
}
