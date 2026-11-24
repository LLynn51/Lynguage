package parser_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"lynguage/internal/ast"
	"lynguage/internal/lexer"
	"lynguage/internal/parser"
)

// 手册示例检查实际交付的文档。后面的结构断言直接调用公开的 Parse，
// 沿着返回的 AST 验证声明、语句、表达式及其源码位置。

// TestManualExamples 逐段解析手册中的完整示例。
func TestManualExamples(t *testing.T) {
	manual, err := os.ReadFile(filepath.Join("..", "..", "docs", "manual.md"))
	if err != nil {
		t.Fatal(err)
	}
	// 从文档提取示例，可在手册中的代码修改后直接验证最新内容。
	fence := regexp.MustCompile("(?ms)^```text[ \\t]*\\r?\\n(.*?)^```[ \\t]*\\r?$")
	examples := fence.FindAllSubmatch(manual, -1)
	if len(examples) == 0 {
		t.Fatal("手册中没有找到代码示例")
	}
	for i, example := range examples {
		t.Run(fmt.Sprintf("示例%d", i+1), func(t *testing.T) {
			program := parseProgram(t, string(example[1]))
			if len(program.Items) == 0 {
				t.Fatal("示例没有产生顶层节点")
			}
		})
	}
}

// TestDeclarationsAndConstructors 检查结构体、方法签名、泛型类型和命名构造实参。
func TestDeclarationsAndConstructors(t *testing.T) {
	program := parseProgram(t, `struct Empty {}
struct Box<
    T,
> {
    value: T
    history: Slice<Result<T, str>>
}
def (const self: Box<T>) get<
    E,
>(replacement: T, fallback: E,) -> Result<T, E> { return Ok(self.value) }
def (self: Box<T>) set(value: T) -> void {
    self.value = value
    return
}
let data: Box<int> = Box<int>(history: make_slice<Result<int, str>>(0, 0), value: 10,)
`)
	requireEqual(t, "顶层节点数量", len(program.Items), 5)

	// 空结构体和泛型结构体应保留各自的字段及类型形参。
	empty := requireNode[*ast.StructDecl](t, program.Items[0])
	requireEqual(t, "空结构体名", empty.Name.Text, "Empty")
	requireEqual(t, "空结构体字段数", len(empty.Fields), 0)
	box := requireNode[*ast.StructDecl](t, program.Items[1])
	requireEqual(t, "结构体名", box.Name.Text, "Box")
	requireEqual(t, "结构体形参数量", len(box.TypeParams), 1)
	requireEqual(t, "结构体形参", box.TypeParams[0].Text, "T")
	requireEqual(t, "字段数", len(box.Fields), 2)
	requireEqual(t, "字段名", box.Fields[1].Name.Text, "history")
	requireEqual(t, "嵌套字段类型", typeName(box.Fields[1].Type), "Slice<Result<T, str>>")

	// 分别检查只读与可变接收者，区分接收者的类型实参与方法自己的类型形参。
	get := requireNode[*ast.FuncDecl](t, program.Items[2])
	receiver := requireNode[*ast.Receiver](t, get.Receiver)
	requireEqual(t, "方法名", get.Name.Text, "get")
	requireEqual(t, "接收者只读标记", receiver.Const, true)
	requireEqual(t, "接收者名称", receiver.Binding.Name.Text, "self")
	requireEqual(t, "接收者类型", typeName(receiver.Binding.Type), "Box<T>")
	requireEqual(t, "方法形参数量", len(get.TypeParams), 1)
	requireEqual(t, "方法形参", get.TypeParams[0].Text, "E")
	requireEqual(t, "方法参数数", len(get.Params), 2)
	requireEqual(t, "方法参数名", get.Params[1].Name.Text, "fallback")
	requireEqual(t, "方法参数类型", typeName(get.Params[1].Type), "E")
	requireEqual(t, "方法返回类型", typeName(get.ReturnType), "Result<T, E>")
	set := requireNode[*ast.FuncDecl](t, program.Items[3])
	requireEqual(t, "可变接收者标记", requireNode[*ast.Receiver](t, set.Receiver).Const, false)
	requireEqual(t, "方法体语句数", len(set.Body.Statements), 2)
	write := requireNode[*ast.AssignStmt](t, set.Body.Statements[0])
	requireEqual(t, "写入字段", requireNode[*ast.MemberExpr](t, write.Target).Name.Text, "value")
	requireEqual(t, "裸 return", requireNode[*ast.ReturnStmt](t, set.Body.Statements[1]).Value, nil)

	// 命名实参保留源码中的顺序，嵌套调用也保留自己的泛型实参。
	data := requireNode[*ast.BindingStmt](t, program.Items[4])
	requireEqual(t, "显式绑定类型", typeName(data.Type), "Box<int>")
	constructor := requireNode[*ast.CallExpr](t, data.Value)
	requireEqual(t, "构造目标", requireNode[*ast.Identifier](t, constructor.Callee).Text, "Box")
	requireEqual(t, "构造类型实参数量", len(constructor.TypeArgs), 1)
	requireEqual(t, "构造类型实参", typeName(constructor.TypeArgs[0]), "int")
	requireEqual(t, "构造实参数量", len(constructor.Args), 2)
	requireEqual(t, "首个命名实参", requireNode[*ast.Identifier](t, constructor.Args[0].Name).Text, "history")
	requireEqual(t, "第二个命名实参", requireNode[*ast.Identifier](t, constructor.Args[1].Name).Text, "value")
	makeSlice := requireNode[*ast.CallExpr](t, constructor.Args[0].Value)
	requireEqual(t, "切片构造函数", requireNode[*ast.Identifier](t, makeSlice.Callee).Text, "make_slice")
	requireEqual(t, "切片类型实参数量", len(makeSlice.TypeArgs), 1)
	requireEqual(t, "切片元素类型", typeName(makeSlice.TypeArgs[0]), "Result<int, str>")
}

// TestResultPropagation 检查 Result 返回类型、构造器和后缀错误传播节点。
func TestResultPropagation(t *testing.T) {
	source := `def copy_text(path: str, dest: str) -> Result<void, str> {
    let text = read_file(path)?
    write_file(dest, text)?
    return Ok<void, str>()
}
def fail() -> Result<int, str> { return Err<int, str>("bad") }
`
	program := parseProgram(t, source)
	requireEqual(t, "函数数量", len(program.Items), 2)
	copyText := requireNode[*ast.FuncDecl](t, program.Items[0])
	requireEqual(t, "普通函数的接收者", copyText.Receiver, nil)
	requireEqual(t, "函数参数数", len(copyText.Params), 2)
	requireEqual(t, "首个参数名", copyText.Params[0].Name.Text, "path")
	requireEqual(t, "首个参数类型", typeName(copyText.Params[0].Type), "str")
	requireEqual(t, "函数返回类型", typeName(copyText.ReturnType), "Result<void, str>")
	requireEqual(t, "函数体语句数", len(copyText.Body.Statements), 3)

	// 问号紧跟被包装的调用，独立语句和初始化表达式使用同一种错误传播节点。
	load := requireNode[*ast.BindingStmt](t, copyText.Body.Statements[0])
	write := requireNode[*ast.ExprStmt](t, copyText.Body.Statements[1])
	for _, expr := range []struct {
		value    ast.Expr
		callText string
	}{
		{load.Value, "read_file(path)"},
		{write.Expr, "write_file(dest, text)"},
	} {
		try := requireNode[*ast.TryExpr](t, expr.value)
		call := requireNode[*ast.CallExpr](t, try.Operand)
		requireSpanText(t, source, call.Span(), expr.callText)
		requireSpanText(t, source, try.Question, "?")
		requireSpanText(t, source, try.Span(), expr.callText+"?")
		requireEqual(t, "调用结束位置与问号起点", call.Span().End, try.Question.Start)
	}

	// Ok 和 Err 保留显式类型实参，Ok 的值实参列表可以为空。
	okReturn := requireNode[*ast.ReturnStmt](t, copyText.Body.Statements[2])
	requireEqual(t, "Ok 构造表达式", expressionShape(t, okReturn.Value), "(call Ok<void, str>)")
	fail := requireNode[*ast.FuncDecl](t, program.Items[1])
	requireEqual(t, "失败函数语句数", len(fail.Body.Statements), 1)
	errReturn := requireNode[*ast.ReturnStmt](t, fail.Body.Statements[0])
	requireEqual(t, "Err 构造表达式", expressionShape(t, errReturn.Value), "(call Err<int, str> \"bad\")")
}

// TestControlFlowAndAssignments 检查分行、条件分支、循环和赋值目标。
func TestControlFlowAndAssignments(t *testing.T) {
	program := parseProgram(t, `let value = 0
const LIMIT: int = 4
while value < LIMIT {
    if value == 0 { value = 1 }
    # 注释与换行仍可出现在分支之间
    elif value == 1 { data[
        value
    ] = 2 }
    elif value == 2 { value = 3 } else { value = value + 1 }
    if false {}
    value = value + 1
}
`)
	requireEqual(t, "顶层节点数量", len(program.Items), 3)

	// 检查推断型 let 和显式标注的 const 各自携带的语法信息。
	mutable := requireNode[*ast.BindingStmt](t, program.Items[0])
	fixed := requireNode[*ast.BindingStmt](t, program.Items[1])
	requireEqual(t, "let 标记", mutable.Const, false)
	requireEqual(t, "省略类型标注", mutable.Type, nil)
	requireEqual(t, "const 标记", fixed.Const, true)
	requireEqual(t, "const 类型", typeName(fixed.Type), "int")

	// 有后续分支时跨过换行，独立的 if 则把后面的语句留给外层循环体。
	loop := requireNode[*ast.WhileStmt](t, program.Items[2])
	requireEqual(t, "循环条件", expressionShape(t, loop.Condition), "(< value LIMIT)")
	requireEqual(t, "循环体语句数", len(loop.Body.Statements), 3)
	conditional := requireNode[*ast.IfStmt](t, loop.Body.Statements[0])
	requireEqual(t, "带条件分支数", len(conditional.Branches), 3)
	requireEqual(t, "第二个分支条件", expressionShape(t, conditional.Branches[1].Condition), "(== value 1)")
	indexWrite := requireNode[*ast.AssignStmt](t, conditional.Branches[1].Body.Statements[0])
	requireEqual(t, "跨行索引目标", expressionShape(t, indexWrite.Target), "([] data value)")
	otherwise := requireNode[*ast.Block](t, conditional.Else)
	requireEqual(t, "else 语句数", len(otherwise.Statements), 1)
	standalone := requireNode[*ast.IfStmt](t, loop.Body.Statements[1])
	requireEqual(t, "独立 if 的分支数", len(standalone.Branches), 1)
	requireEqual(t, "空分支语句数", len(standalone.Branches[0].Body.Statements), 0)
	requireEqual(t, "独立 if 的 else", standalone.Else, nil)
	lastWrite := requireNode[*ast.AssignStmt](t, loop.Body.Statements[2])
	requireEqual(t, "普通赋值目标", expressionShape(t, lastWrite.Target), "value")
	requireEqual(t, "普通赋值表达式", expressionShape(t, lastWrite.Value), "(+ value 1)")
}

// 表达式用例将解析结果转成简短的括号形式。两个测试共用相同断言，
// 分别检查运算符如何结合，以及尖括号何时开启泛型调用。

// TestOperatorPrecedence 检查二元优先级、左结合、括号及前缀和后缀的结合。
func TestOperatorPrecedence(t *testing.T) {
	// 混合表达式同时锁定优先级层次，同级链和分组表达式单独定位结合问题。
	checkExpressions(t, []expressionCase{
		{"混合优先级", "not a or b and c != d < 2 + 3 * 4 % 5", "(or (not a) (and b (!= c (< d (+ 2 (% (* 3 4) 5))))))"},
		{"左结合", "a - b - c / d / e", "(- (- a b) (/ (/ c d) e))"},
		{"右侧前缀和后缀", "-+f(1)? + g", "(+ (- (+ (? (call f 1)))) g)"},
		{"括号分组", "(a + b) * c", "(* (分组 (+ a b)) c)"},
		{"复合比较符", "a <= b == c >= d", "(== (<= a b) (>= c d))"},
	})
}

// TestGenericCallDisambiguation 检查泛型调用和比较表达式的消歧边界。
func TestGenericCallDisambiguation(t *testing.T) {
	// 覆盖贴邻规则、试读回退和会忽略换行的上下文，再验证后缀链的完整结合顺序。
	checkExpressions(t, []expressionCase{
		{"嵌套实参", "f<Box<Slice<int>>, str>(x)", "(call f<Box<Slice<int>>, str> x)"},
		{"尖括号与调用间有空格", "a<b> (c)", "(call a<b> c)"},
		{"左尖括号前有空格", "a < b > (c)", "(> (< a b) (分组 c))"},
		{"试读失败后回退", "x<y<z", "(< (< x y) z)"},
		{"试读成功后回退", "a<b>c", "(> (< a b) c)"},
		{"分组作为比较左侧", "(a)<b>(c)", "(> (< (分组 a) b) (分组 c))"},
		{"类型实参中有换行与注释", "f<# 类型实参\n Slice<int>,\n>(x)", "(call f<Slice<int>> x)"},
		{"调用实参中忽略换行", "f(a<b>\n(c))", "(call f (call a<b> c))"},
		{"命名实参内的比较", "Pair<int>(left: a < b, right: x)", "(call Pair<int> left: (< a b) right: x)"},
		{"成员泛型调用及后缀链", "reader.get<Result<int, str>>(x)?[\n0\n].value", "(. ([] (? (call (. reader get)<Result<int, str>> x)) 0) value)"},
	})
}

// TestSourceSpansAndLiterals 检查 UTF-8 位置、行尾格式、字面量原文及解码值。
func TestSourceSpansAndLiterals(t *testing.T) {
	source := "# 标题\r\nlet 变量٢ = \"中文\\n\\r\\t\\\"\\\\#\"\rlet value = 1.25e-2\nlet big = 9223372036854775808"
	program := parseProgram(t, source)
	requireEqual(t, "绑定数", len(program.Items), 3)
	requireEqual(t, "程序起始位置", program.Span().Start, lexer.Position{Line: 1, Column: 1})
	requireEqual(t, "程序结束偏移", program.Span().End.Offset, len(source))
	requireEqual(t, "程序结束行", program.Span().End.Line, 4)

	// 多字节名称的偏移按字节计算，列号按 Unicode 码点计算。
	text := requireNode[*ast.BindingStmt](t, program.Items[0])
	nameStart := lexer.Position{Offset: len("# 标题\r\nlet "), Line: 2, Column: 5}
	nameEnd := lexer.Position{Offset: nameStart.Offset + len("变量٢"), Line: 2, Column: 8}
	requireEqual(t, "名称区间", text.Name.Span(), lexer.Span{Start: nameStart, End: nameEnd})
	requireSpanText(t, source, text.Name.Span(), "变量٢")
	str := requireNode[*ast.LiteralExpr](t, text.Value)
	requireEqual(t, "字符串记号类别", str.Kind, lexer.String)
	requireEqual(t, "字符串解码值", str.StringValue, "中文\n\r\t\"\\#")
	requireEqual(t, "字符串原始拼写", str.Raw, `"中文\n\r\t\"\\#"`)
	requireSpanText(t, source, str.Span(), str.Raw)

	// 数值按原文入树，保留指数拼写和可能超出默认类型范围的整数。
	floating := requireNode[*ast.BindingStmt](t, program.Items[1])
	value := requireNode[*ast.LiteralExpr](t, floating.Value)
	requireEqual(t, "浮点数类别", value.Kind, lexer.Float)
	requireEqual(t, "浮点数拼写", value.Raw, "1.25e-2")
	requireEqual(t, "CR 之后的行号", value.Span().Start.Line, 3)
	big := requireNode[*ast.BindingStmt](t, program.Items[2])
	integer := requireNode[*ast.LiteralExpr](t, big.Value)
	requireEqual(t, "大整数类别", integer.Kind, lexer.Integer)
	requireEqual(t, "大整数拼写", integer.Raw, "9223372036854775808")
}

// TestEmptyPrograms 检查空文件以及仅含空白与注释的源码。
func TestEmptyPrograms(t *testing.T) {
	requireEqual(t, "空文件项目数", len(parseProgram(t, "").Items), 0)
	requireEqual(t, "空白和注释文件项目数", len(parseProgram(t, "\n# 注释\r\n \t# 行尾").Items), 0)
}

// 两组错误用例都从公开入口触发诊断，分别验证语法错误和词法错误的类型。
// requireDiagnostic 共用位置、源码区间和消息断言。

// TestSyntaxErrors 检查常见无效语法和错误定位。
func TestSyntaxErrors(t *testing.T) {
	cases := []diagnosticCase{
		{"缺少参数类型", "def f(x) -> int {}", 1, 8, ")", "expected :"},
		{"缺少返回箭头", "def f() {}", 1, 9, "{", "expected ->"},
		{"缺少返回类型", "def f() -> {}", 1, 12, "{", "expected identifier"},
		{"缺少闭花括号", "def f() -> int {", 1, 17, "", "expected }"},
		{"空类型形参", "struct Box<> {}", 1, 12, ">", "expected identifier"},
		{"类型闭合符和等号相连", "let x: Box<int>=1", 1, 15, ">=", "expected >"},
		{"语句缺少换行", "let a = 1 let b = 2", 1, 11, "let", "expected newline"},
		{"字段缺少换行", "struct P { a: int b: int }", 1, 19, "b", "expected newline"},
		{"没有 if 的 else", "else {}", 1, 1, "else", "expected expression"},
		{"独立泛型调用跨行", "a<b>\n(c)", 1, 5, "\n", "expected expression"},
		{"位置实参列表混入命名实参", "f(1, a: 2)", 1, 7, ":", "expected )"},
		{"命名实参列表混入位置实参", "f(a: 1, 2)", 1, 9, "2", "expected identifier"},
		{"分组不能单独赋值", "(x) = 1", 1, 1, "(x)", "invalid assignment target"},
		{"传播表达式不能赋值", "f()? = 1", 1, 1, "f()?", "invalid assignment target"},
		{"连写赋值", "x = y = z", 1, 7, "=", "expected newline"},
		{"CRLF 后缺少表达式", "let a = 1\r\nlet b = )", 2, 9, ")", "expected expression"},
	}
	// 每个无效输入都要求返回可区分的错误类型及完整的故障区间。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, err := parser.Parse(tc.source)
			requireEqual(t, "语法失败时的程序", program, nil)
			var syntaxError *parser.Error
			if !errors.As(err, &syntaxError) {
				t.Fatalf("期望语法错误，得到 %T (%v)", err, err)
			}
			requireDiagnostic(t, tc, syntaxError.Span, syntaxError.Message)
		})
	}
}

// TestLexicalErrors 检查词法错误透过解析入口传递时的位置和消息。
func TestLexicalErrors(t *testing.T) {
	cases := []diagnosticCase{
		{"指数缺少数字", "1e+", 1, 2, "e+", "expected digits"},
		{"未知转义", `"bad\q"`, 1, 5, `\q`, "unknown string escape"},
		{"未结束字符串", "\"bad", 1, 1, "\"bad", "unterminated string"},
		{"裸控制字符", "\"a\tb\"", 1, 3, "\t", "unescaped control character"},
		{"不允许分号", "let x = 1;", 1, 10, ";", "unexpected character"},
		{"注释中的非法 UTF-8", "# 注\xff", 1, 4, "\xff", "invalid UTF-8"},
	}
	// 直接走 Parse，确保上层调用者仍可辨认词法错误并取得原始字节范围。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, err := parser.Parse(tc.source)
			requireEqual(t, "词法失败时的程序", program, nil)
			var lexicalError *lexer.Error
			if !errors.As(err, &lexicalError) {
				t.Fatalf("期望词法错误，得到 %T (%v)", err, err)
			}
			requireDiagnostic(t, tc, lexicalError.Span, lexicalError.Message)
		})
	}
}

// 解析和节点断言辅助函数在第一个先决条件失败时停止当前用例，
// 让后续的 AST 字段检查可以直接指出具体节点的差异。

// parseProgram 解析完整程序并报告意外的分析错误。
func parseProgram(t *testing.T, source string) *ast.Program {
	t.Helper()
	program, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("解析失败 %v\n%s", err, source)
	}
	return program
}

// requireNode 将节点转为期望的 AST 类型，实际类型不符时立即失败。
func requireNode[T interface {
	ast.Node
	comparable
}](t *testing.T, node ast.Node) T {
	t.Helper()
	value, ok := node.(T)
	var zero T
	// 接口可能装有类型正确的空指针，也要在读取节点字段前拒绝。
	if !ok || value == zero {
		t.Fatalf("节点为 %T (%v)，期望非空的 %T", node, node, zero)
	}
	return value
}

// requireEqual 检查具名字段或结果，失败时显示实际值和期望值。
func requireEqual[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s为 %v，期望 %v", field, got, want)
	}
}

// requireSpanText 用半开字节区间从原文切片，以验证记号和节点的范围。
func requireSpanText(t *testing.T, source string, span lexer.Span, want string) {
	t.Helper()
	start, end := span.Start.Offset, span.End.Offset
	if start < 0 || end < start || end > len(source) {
		t.Fatalf("无效的源码区间 [%d, %d)，源码共 %d 字节", start, end, len(source))
	}
	requireEqual(t, fmt.Sprintf("源码区间 [%d, %d) 的文本", start, end), source[start:end], want)
}

type diagnosticCase struct {
	name, source     string
	line, column     int
	spanText, detail string
}

// requireDiagnostic 核对错误起点、区间覆盖的原文及消息中的关键字。
func requireDiagnostic(t *testing.T, tc diagnosticCase, span lexer.Span, message string) {
	t.Helper()
	requireEqual(t, "错误行号", span.Start.Line, tc.line)
	requireEqual(t, "错误列号", span.Start.Column, tc.column)
	requireSpanText(t, tc.source, span, tc.spanText)
	if !strings.Contains(message, tc.detail) {
		t.Fatalf("消息为 %q，期望包含 %q", message, tc.detail)
	}
}

// 表达式断言先解析单条语句，再递归表示 AST 的层级。调用中的泛型类型
// 由 typeArguments 和 typeName 处理，以便在失败消息中显示嵌套结构。

type expressionCase struct {
	name, source, shape string
}

// checkExpressions 将每段源码单独解析为表达式语句并比较节点结构。
func checkExpressions(t *testing.T, cases []expressionCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program := parseProgram(t, tc.source)
			// 先确认表达式完整留在一个语句中，再比较它的子树。
			requireEqual(t, "顶层节点数量", len(program.Items), 1)
			stmt := requireNode[*ast.ExprStmt](t, program.Items[0])
			requireEqual(t, "表达式结构", expressionShape(t, stmt.Expr), tc.shape)
		})
	}
}

// expressionShape 用括号形式表现测试所检查的表达式节点及其子节点。
func expressionShape(t *testing.T, expr ast.Expr) string {
	t.Helper()
	// 叶节点记录原文，运算与后缀节点按实际的子树嵌套顺序展开。
	switch node := expr.(type) {
	case *ast.Identifier:
		return node.Text
	case *ast.LiteralExpr:
		return node.Raw
	case *ast.GroupExpr:
		return "(分组 " + expressionShape(t, node.Inner) + ")"
	case *ast.UnaryExpr:
		return "(" + string(node.Operator.Kind) + " " + expressionShape(t, node.Operand) + ")"
	case *ast.BinaryExpr:
		return "(" + string(node.Operator.Kind) + " " + expressionShape(t, node.Left) + " " + expressionShape(t, node.Right) + ")"
	case *ast.TryExpr:
		return "(? " + expressionShape(t, node.Operand) + ")"
	case *ast.IndexExpr:
		return "([] " + expressionShape(t, node.Target) + " " + expressionShape(t, node.Index) + ")"
	case *ast.MemberExpr:
		return "(. " + expressionShape(t, node.Target) + " " + node.Name.Text + ")"
	case *ast.CallExpr:
		// 类型实参附在调用目标上，值实参按源码顺序展开。
		parts := []string{"call", expressionShape(t, node.Callee) + typeArguments(node.TypeArgs)}
		for _, arg := range node.Args {
			value := expressionShape(t, arg.Value)
			if arg.Name != nil {
				value = arg.Name.Text + ": " + value
			}
			parts = append(parts, value)
		}
		return "(" + strings.Join(parts, " ") + ")"
	default:
		t.Fatalf("表达式结构断言遇到未支持的节点 %T", expr)
		return ""
	}
}

// typeName 表示类型引用的名称及递归嵌套的实参。
func typeName(ref *ast.TypeRef) string {
	if ref == nil {
		return "<无类型>"
	}
	return ref.Name.Text + typeArguments(ref.Args)
}

// typeArguments 把非空类型实参列表表示为尖括号内用逗号分隔的类型。
func typeArguments(args []*ast.TypeRef) string {
	if len(args) == 0 {
		return ""
	}
	// 逐项递归表示类型，保留嵌套层次和原来的参数顺序。
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = typeName(arg)
	}
	return "<" + strings.Join(parts, ", ") + ">"
}
