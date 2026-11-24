// ast 包定义 Lynguage 的抽象语法树。
package ast

import "lynguage/internal/lexer"

// Node 表示带有左闭右开源码区间的语法节点。
type Node interface {
	Span() lexer.Span
}

// NodeSpan 保存语法节点共有的源码区间。
type NodeSpan struct {
	Source lexer.Span
}

// Span 返回节点保存的源码区间，嵌入 NodeSpan 的节点会共享此实现。
func (n NodeSpan) Span() lexer.Span { return n.Source }

// Item 表示可出现在顶层的声明或语句。
type Item interface {
	Node
	itemNode()
}

// Stmt 表示也可出现在代码块中的语句。
type Stmt interface {
	Item
	stmtNode()
}

// Expr 表示表达式节点。
type Expr interface {
	Node
	exprNode()
}

type Program struct {
	NodeSpan
	Items []Item
}

type Identifier struct {
	NodeSpan
	Text string
}

type TypeRef struct {
	NodeSpan
	Name       Identifier
	Args       []*TypeRef
	GlobalName bool `json:",omitempty"` // 推断结果中的预置类型或顶层结构体名称。
}

// TypedName 表示带类型标注的字段、参数或接收者绑定。
type TypedName struct {
	NodeSpan
	Name Identifier
	Type *TypeRef
}

type StructDecl struct {
	NodeSpan
	Name       Identifier
	TypeParams []Identifier
	Fields     []TypedName
}

type Receiver struct {
	NodeSpan
	Binding TypedName
	Const   bool
}

// FuncDecl 表示函数声明，Receiver 有值时表示方法声明。
type FuncDecl struct {
	NodeSpan
	Name       Identifier
	TypeParams []Identifier
	Receiver   *Receiver
	Params     []TypedName
	ReturnType *TypeRef
	Body       *Block
}

type Block struct {
	NodeSpan
	Statements []Stmt
}

type BindingStmt struct {
	NodeSpan
	Name  Identifier
	Const bool
	Type  *TypeRef // 省略类型标注时为 nil，由类型检查器推断。
	Value Expr
}

type ConditionalBranch struct {
	NodeSpan
	Condition Expr
	Body      *Block
}

// IfStmt 依次保存 if 分支和后续的 elif 分支。
type IfStmt struct {
	NodeSpan
	Branches []ConditionalBranch
	Else     *Block
}

type WhileStmt struct {
	NodeSpan
	Condition Expr
	Body      *Block
}

type ReturnStmt struct {
	NodeSpan
	Value Expr // 无返回表达式时为 nil。
}

type ExprStmt struct {
	NodeSpan
	Expr Expr
}

type AssignStmt struct {
	NodeSpan
	Target Expr
	Value  Expr
}

// LiteralExpr 保留数值字面量的原始拼写，供上下文定型和范围检查使用。
// StringValue 保存字符串字面量解码后的内容。
type LiteralExpr struct {
	NodeSpan
	Kind        lexer.Kind
	Raw         string
	StringValue string
}

type GroupExpr struct {
	NodeSpan
	Inner Expr
}

type UnaryExpr struct {
	NodeSpan
	Operator lexer.Token
	Operand  Expr
}

type BinaryExpr struct {
	NodeSpan
	Left     Expr
	Operator lexer.Token
	Right    Expr
}

// Argument 仅在命名实参列表中设置 Name。
type Argument struct {
	NodeSpan
	Name  *Identifier
	Value Expr
}

type CallExpr struct {
	NodeSpan
	Callee   Expr
	TypeArgs []*TypeRef
	Args     []Argument
}

type IndexExpr struct {
	NodeSpan
	Target Expr
	Index  Expr
}

type MemberExpr struct {
	NodeSpan
	Target Expr
	Name   Identifier
}

type TryExpr struct {
	NodeSpan
	Operand  Expr
	Question lexer.Span
}

// 这些空方法在编译期为节点标记语法类别。itemNode 允许节点进入
// Program.Items，stmtNode 在此基础上允许节点进入 Block.Statements。
// exprNode 标记可出现在操作数、实参和其他表达式位置的节点。

// itemNode 允许结构体声明出现在程序顶层。
func (*StructDecl) itemNode() {}

// itemNode 允许函数和方法声明出现在程序顶层。
func (*FuncDecl) itemNode() {}

// itemNode 允许绑定语句出现在程序顶层。
func (*BindingStmt) itemNode() {}

// itemNode 允许条件语句出现在程序顶层。
func (*IfStmt) itemNode() {}

// itemNode 允许循环语句出现在程序顶层。
func (*WhileStmt) itemNode() {}

// itemNode 允许语法树记录顶层的 return，供后续语义检查诊断。
func (*ReturnStmt) itemNode() {}

// itemNode 允许表达式语句出现在程序顶层。
func (*ExprStmt) itemNode() {}

// itemNode 允许赋值语句出现在程序顶层。
func (*AssignStmt) itemNode() {}

// stmtNode 允许绑定语句出现在代码块中。
func (*BindingStmt) stmtNode() {}

// stmtNode 允许条件语句出现在代码块中。
func (*IfStmt) stmtNode() {}

// stmtNode 允许循环语句出现在代码块中。
func (*WhileStmt) stmtNode() {}

// stmtNode 允许返回语句出现在代码块中。
func (*ReturnStmt) stmtNode() {}

// stmtNode 允许表达式语句出现在代码块中。
func (*ExprStmt) stmtNode() {}

// stmtNode 允许赋值语句出现在代码块中。
func (*AssignStmt) stmtNode() {}

// exprNode 允许标识符作为名称引用出现在表达式中。
func (*Identifier) exprNode() {}

// exprNode 将字面量标记为表达式。
func (*LiteralExpr) exprNode() {}

// exprNode 将带括号的分组标记为表达式。
func (*GroupExpr) exprNode() {}

// exprNode 将一元运算标记为表达式。
func (*UnaryExpr) exprNode() {}

// exprNode 将二元运算标记为表达式。
func (*BinaryExpr) exprNode() {}

// exprNode 将调用操作标记为表达式。
func (*CallExpr) exprNode() {}

// exprNode 将索引访问标记为表达式。
func (*IndexExpr) exprNode() {}

// exprNode 将成员访问标记为表达式。
func (*MemberExpr) exprNode() {}

// exprNode 将后缀 ? 错误传播操作标记为表达式。
func (*TryExpr) exprNode() {}
