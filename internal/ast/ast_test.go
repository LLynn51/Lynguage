package ast_test

import (
	"fmt"
	"strings"
	"testing"

	"lynguage/internal/ast"
	"lynguage/internal/lexer"
	"lynguage/internal/parser"
)

// 节点测试先验证 AST 接口的分类和公共区间，再从真实源码解析整棵树，
// 检查声明、类型、语句和表达式之间的层级及其区间。

// TestNodeCategoriesAndSpan 检查各节点实现的接口及其保存的源码区间。
func TestNodeCategoriesAndSpan(t *testing.T) {
	span := lexer.Span{
		Start: lexer.Position{Offset: 3, Line: 2, Column: 1},
		End:   lexer.Position{Offset: 6, Line: 2, Column: 2},
	}
	location := ast.NodeSpan{Source: span}
	cases := []struct {
		node     ast.Node
		category string
	}{
		{&ast.Program{NodeSpan: location}, "辅助节点"},
		{&ast.TypeRef{NodeSpan: location}, "辅助节点"},
		{&ast.TypedName{NodeSpan: location}, "辅助节点"},
		{&ast.Receiver{NodeSpan: location}, "辅助节点"},
		{&ast.Block{NodeSpan: location}, "辅助节点"},
		{&ast.ConditionalBranch{NodeSpan: location}, "辅助节点"},
		{&ast.Argument{NodeSpan: location}, "辅助节点"},
		{&ast.StructDecl{NodeSpan: location}, "声明"},
		{&ast.FuncDecl{NodeSpan: location}, "声明"},
		{&ast.BindingStmt{NodeSpan: location}, "语句"},
		{&ast.IfStmt{NodeSpan: location}, "语句"},
		{&ast.WhileStmt{NodeSpan: location}, "语句"},
		{&ast.ReturnStmt{NodeSpan: location}, "语句"},
		{&ast.ExprStmt{NodeSpan: location}, "语句"},
		{&ast.AssignStmt{NodeSpan: location}, "语句"},
		{&ast.Identifier{NodeSpan: location}, "表达式"},
		{&ast.LiteralExpr{NodeSpan: location}, "表达式"},
		{&ast.GroupExpr{NodeSpan: location}, "表达式"},
		{&ast.UnaryExpr{NodeSpan: location}, "表达式"},
		{&ast.BinaryExpr{NodeSpan: location}, "表达式"},
		{&ast.CallExpr{NodeSpan: location}, "表达式"},
		{&ast.IndexExpr{NodeSpan: location}, "表达式"},
		{&ast.MemberExpr{NodeSpan: location}, "表达式"},
		{&ast.TryExpr{NodeSpan: location}, "表达式"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%T", tc.node), func(t *testing.T) {
			// 分别检查三个接口。语句也是顶层项，声明只实现顶层项接口。
			_, item := tc.node.(ast.Item)
			_, stmt := tc.node.(ast.Stmt)
			_, expr := tc.node.(ast.Expr)
			if item != (tc.category == "声明" || tc.category == "语句") ||
				stmt != (tc.category == "语句") || expr != (tc.category == "表达式") {
				t.Errorf("期望%s，实际接口为 Item=%t、Stmt=%t、Expr=%t", tc.category, item, stmt, expr)
			}
			if got := tc.node.Span(); got != span {
				t.Errorf("源码区间为 %+v，期望 %+v", got, span)
			}
		})
	}
}

// TestParsedTreeSpans 检查解析所得节点的嵌套关系以及子节点在源码中的边界。
func TestParsedTreeSpans(t *testing.T) {
	const declaration = "struct Box<T> { value: Result<T, str> }"
	const signature = "def (const self: Box<T>) get() -> Result<T, str> "
	const block = "{\r\n    return Ok(self.value?) # 行尾注释\r\n}"
	source := "# 前言\r\n" + declaration + "\r\n" + signature + block + "\r\n"
	program, err := parser.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Items) != 2 {
		t.Fatalf("有 %d 个顶层节点，期望 2 个", len(program.Items))
	}

	// 从声明向下读取类型引用、返回语句、调用实参和错误传播表达式。
	box := requireNode[*ast.StructDecl](t, program.Items[0])
	method := requireNode[*ast.FuncDecl](t, program.Items[1])
	body := requireNode[*ast.Block](t, method.Body)
	if len(box.Fields) != 1 || len(body.Statements) != 1 {
		t.Fatalf("字段数为 %d，方法体语句数为 %d，两者都应为 1", len(box.Fields), len(body.Statements))
	}
	result := requireNode[*ast.TypeRef](t, box.Fields[0].Type)
	if result.Name.Text != "Result" || len(result.Args) != 2 ||
		result.Args[0] == nil || result.Args[1] == nil ||
		result.Args[0].Name.Text != "T" || result.Args[1].Name.Text != "str" {
		t.Fatalf("字段类型应为 Result<T, str>，实际为 %+v", result)
	}
	ret := requireNode[*ast.ReturnStmt](t, body.Statements[0])
	call := requireNode[*ast.CallExpr](t, ret.Value)
	if len(call.Args) != 1 || call.Args[0].Name != nil {
		t.Fatalf("调用应有一个位置实参，实际为 %+v", call.Args)
	}
	propagate := requireNode[*ast.TryExpr](t, call.Args[0].Value)
	member := requireNode[*ast.MemberExpr](t, propagate.Operand)

	// 各区间应能独立还原对应片段，尤其是行尾注释前的 return 和问号本身。
	for _, region := range []struct {
		label string
		span  lexer.Span
		text  string
	}{
		{"程序", program.Span(), source},
		{"结构体", box.Span(), declaration},
		{"字段", box.Fields[0].Span(), "value: Result<T, str>"},
		{"字段类型", result.Span(), "Result<T, str>"},
		{"首个类型实参", result.Args[0].Span(), "T"},
		{"方法", method.Span(), signature + block},
		{"方法体", body.Span(), block},
		{"返回语句", ret.Span(), "return Ok(self.value?)"},
		{"调用", call.Span(), "Ok(self.value?)"},
		{"位置实参", call.Args[0].Span(), "self.value?"},
		{"错误传播", propagate.Span(), "self.value?"},
		{"问号", propagate.Question, "?"},
		{"成员访问", member.Span(), "self.value"},
	} {
		assertSpanText(t, source, region.label, region.span, region.text)
	}
	wantQuestion := lexer.Position{Offset: strings.IndexByte(source, '?'), Line: 4, Column: 25}
	if got := propagate.Question.Start; got != wantQuestion || got != member.Span().End {
		t.Errorf("问号起点为 %+v，期望 %+v 且紧接成员访问", got, wantQuestion)
	}
}

// 树结构检查通过 requireNode 逐层取得具体节点，再用 assertSpanText
// 独立核对节点保存的区间及其对应的源文本。

// requireNode 验证节点类型后返回该节点，供测试继续读取子节点。
func requireNode[T interface {
	ast.Node
	comparable
}](t *testing.T, node ast.Node) T {
	t.Helper()
	value, ok := node.(T)
	var zero T
	// 拒绝类型匹配的空指针，以便把故障定位到当前节点。
	if !ok || value == zero {
		t.Fatalf("节点为 %T (%v)，期望非空的 %T", node, node, zero)
	}
	return value
}

// assertSpanText 检查半开区间是否有效，以及它截取出的源码是否正确。
func assertSpanText(t *testing.T, source, label string, span lexer.Span, want string) {
	t.Helper()
	start, end := span.Start.Offset, span.End.Offset
	// 先校验边界，避免位置回归引发切片越界并掩盖具体节点的信息。
	if start < 0 || end < start || end > len(source) {
		t.Errorf("%s的区间 [%d, %d) 超出源码范围", label, start, end)
		return
	}
	if got := source[start:end]; got != want {
		t.Errorf("%s的源码为 %q，期望 %q", label, got, want)
	}
}
