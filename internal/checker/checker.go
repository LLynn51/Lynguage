// checker 包对 Lynguage 的语法树进行名称解析和静态类型检查。
package checker

import (
	"fmt"
	"strconv"
	"strings"

	"lynguage/internal/ast"
	"lynguage/internal/lexer"
)

// Error 记录静态类型错误的源码区间。
type Error struct {
	Span    lexer.Span
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Span.Start.Line, e.Span.Start.Column, e.Message)
}

func errorAt(span lexer.Span, format string, args ...any) error {
	return &Error{Span: span, Message: fmt.Sprintf(format, args...)}
}

type typeKind uint8

const (
	namedType typeKind = iota
	typeParameter
	inferenceVariable
)

// 类型形参用指针区分声明。每次泛型调用使用独立的推断变量。
type valueType struct {
	kind typeKind
	name string
	args []*valueType

	solution *valueType // 推断变量的统一结果
	number   lexer.Kind // 字面量对推断变量施加的数值类别约束
	nonVoid  bool       // 类型形参是否用于存储或取值
}

func named(name string, args ...*valueType) *valueType {
	return &valueType{name: name, args: args}
}

var (
	intType     = named("int")
	int32Type   = named("int32")
	floatType   = named("float")
	float32Type = named("float32")
	boolType    = named("bool")
	strType     = named("str")
	voidType    = named("void")
)

var primitiveTypes = map[string]*valueType{
	"int": intType, "int32": int32Type, "int64": intType,
	"float": floatType, "float32": float32Type, "float64": floatType,
	"bool": boolType, "str": strType, "void": voidType,
}

func (t *valueType) resolved() *valueType {
	for t.solution != nil {
		t = t.solution
	}
	return t
}

func (t *valueType) String() string {
	t = t.resolved()
	if t.kind == inferenceVariable {
		if t.number != "" {
			return string(t.number) + " literal"
		}
		if t.name == "" {
			return "unknown type"
		}
	}
	if len(t.args) == 0 {
		return t.name
	}
	args := make([]string, len(t.args))
	for i, arg := range t.args {
		args[i] = arg.String()
	}
	return t.name + "<" + strings.Join(args, ", ") + ">"
}

func (t *valueType) numberKind() lexer.Kind {
	switch t.resolved() {
	case intType, int32Type:
		return lexer.Integer
	case floatType, float32Type:
		return lexer.Float
	default:
		return ""
	}
}

func (t *valueType) ref(span lexer.Span) *ast.TypeRef {
	t = t.resolved()
	source := ast.NodeSpan{Source: span}
	ref := &ast.TypeRef{
		NodeSpan:   source,
		Name:       ast.Identifier{NodeSpan: source, Text: t.name},
		GlobalName: t.kind == namedType,
	}
	for _, arg := range t.args {
		ref.Args = append(ref.Args, arg.ref(span))
	}
	return ref
}

func (t *valueType) substitute(types map[*valueType]*valueType) *valueType {
	if replacement := types[t]; replacement != nil {
		return replacement
	}
	if len(t.args) == 0 || len(types) == 0 {
		return t
	}
	args := make([]*valueType, len(t.args))
	for i, arg := range t.args {
		args[i] = arg.substitute(types)
	}
	return named(t.name, args...)
}

func substitutions(params, args []*valueType) map[*valueType]*valueType {
	result := make(map[*valueType]*valueType, len(params))
	for i, param := range params {
		result[param] = args[i]
	}
	return result
}

func (t *valueType) contains(other *valueType) bool {
	t = t.resolved()
	if t == other {
		return true
	}
	for _, arg := range t.args {
		if arg.contains(other) {
			return true
		}
	}
	return false
}

func (t *valueType) complete() bool {
	t = t.resolved()
	if t.kind == inferenceVariable {
		return false
	}
	for _, arg := range t.args {
		if !arg.complete() {
			return false
		}
	}
	return true
}

func unify(left, right *valueType) bool {
	left, right = left.resolved(), right.resolved()
	if left == right {
		return true
	}
	if left.kind != inferenceVariable && right.kind == inferenceVariable {
		left, right = right, left
	}
	if left.kind == inferenceVariable {
		if right.kind == inferenceVariable {
			if left.number != "" && right.number != "" && left.number != right.number {
				return false
			}
			if right.number == "" {
				right.number = left.number
			}
		} else if (left.number != "" && left.number != right.numberKind()) || right.contains(left) {
			return false
		}
		left.solution = right
		return true
	}
	if left.kind != namedType || right.kind != namedType || left.name != right.name || len(left.args) != len(right.args) {
		return false
	}
	for i, arg := range left.args {
		if !unify(arg, right.args[i]) {
			return false
		}
	}
	return true
}

func expectType(span lexer.Span, actual, expected *valueType, context string) error {
	if !unify(actual, expected) {
		return errorAt(span, "%s has type %s, expected %s", context, actual, expected)
	}
	return nil
}

// 根据是否处于返回位置或 Result 的成功位置，检查 void 的合法性。
func requireType(t *valueType, allowVoid bool, span lexer.Span) error {
	t = t.resolved()
	if !allowVoid {
		if t == voidType {
			return errorAt(span, "void cannot be used as a value or storage type")
		}
		if t.kind == typeParameter {
			t.nonVoid = true
		}
	}
	for i, arg := range t.args {
		if err := requireType(arg, t.name == "Result" && i == 0, span); err != nil {
			return err
		}
	}
	return nil
}

type binding struct {
	typ      *valueType
	constant bool
}

type scope struct {
	parent *scope
	names  map[string]binding
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, names: make(map[string]binding)}
}

func (s *scope) lookup(name string) (binding, bool) {
	for ; s != nil; s = s.parent {
		if binding, ok := s.names[name]; ok {
			return binding, true
		}
	}
	return binding{}, false
}

func (s *scope) define(name ast.Identifier, value binding) error {
	if _, ok := s.names[name.Text]; ok {
		return errorAt(name.Span(), "%q is already declared in this scope", name.Text)
	}
	s.names[name.Text] = value
	return nil
}

type structure struct {
	params  []*valueType
	fields  map[string]*valueType
	methods map[string]*signature
}

type signature struct {
	typeNames      map[string]*valueType
	typeParams     []*valueType
	receiverParams []*valueType
	receiver       *valueType
	params         []*valueType
	result         *valueType
	constReceiver  bool
}

type instantiation struct {
	span     lexer.Span
	param    *valueType
	argument *valueType
}

type inferredBinding struct {
	node *ast.BindingStmt
	typ  *valueType
}

type checker struct {
	structures     map[string]*structure
	functions      map[string]*signature
	declarations   map[*ast.FuncDecl]*signature
	global         *scope
	inferred       []inferredBinding
	instantiations []instantiation
}

// Check 检查已解析的程序，返回首个静态类型错误。检查成功后，省略的
// let 和 const 类型以规范类型名和完整的类型实参回填到 BindingStmt.Type。
func Check(program *ast.Program) error {
	c := checker{
		structures:   make(map[string]*structure),
		functions:    builtins(),
		declarations: make(map[*ast.FuncDecl]*signature),
		global:       newScope(nil),
	}
	if err := c.register(program); err != nil {
		return err
	}
	top := context{checker: &c, scope: c.global}
	for _, item := range program.Items {
		switch node := item.(type) {
		case *ast.FuncDecl:
			if err := c.function(node); err != nil {
				return err
			}
		case ast.Stmt:
			if _, err := top.statement(node); err != nil {
				return err
			}
		}
	}
	if err := c.checkInstantiations(); err != nil {
		return err
	}
	for _, inferred := range c.inferred {
		inferred.node.Type = inferred.typ.ref(inferred.node.Value.Span())
	}
	return nil
}

func (c *checker) reserved(name string) bool {
	return primitiveTypes[name] != nil || name == "Slice" || name == "Result" ||
		name == "println" || c.structures[name] != nil || c.functions[name] != nil
}

func (c *checker) resolve(ref *ast.TypeRef, typeNames map[string]*valueType) (*valueType, error) {
	name := ref.Name.Text
	typ := primitiveTypes[name]
	if param := typeNames[name]; !ref.GlobalName && param != nil {
		typ = param
	}
	arity := 0
	if typ == nil {
		switch name {
		case "Slice":
			arity = 1
		case "Result":
			arity = 2
		default:
			decl := c.structures[name]
			if decl == nil {
				return nil, errorAt(ref.Span(), "unknown type %q", name)
			}
			arity = len(decl.params)
		}
	}
	if len(ref.Args) != arity {
		return nil, errorAt(ref.Span(), "%s expects %d type arguments, got %d", name, arity, len(ref.Args))
	}
	if typ != nil {
		return typ, nil
	}
	args := make([]*valueType, arity)
	for i, ref := range ref.Args {
		arg, err := c.resolve(ref, typeNames)
		if err != nil {
			return nil, err
		}
		args[i] = arg
	}
	return named(name, args...), nil
}

func (c *checker) resolveUse(ref *ast.TypeRef, typeNames map[string]*valueType, allowVoid bool) (*valueType, error) {
	typ, err := c.resolve(ref, typeNames)
	if err != nil {
		return nil, err
	}
	return typ, requireType(typ, allowVoid, ref.Span())
}

func declareTypeParams(ids []ast.Identifier, names map[string]*valueType) ([]*valueType, error) {
	params := make([]*valueType, 0, len(ids))
	for _, id := range ids {
		if names[id.Text] != nil {
			return nil, errorAt(id.Span(), "duplicate type parameter %q", id.Text)
		}
		param := &valueType{kind: typeParameter, name: id.Text}
		names[id.Text] = param
		params = append(params, param)
	}
	return params, nil
}

func (c *checker) register(program *ast.Program) error {
	// 先登记所有结构体名称和形参数量，再解析彼此引用的字段和签名。
	for _, item := range program.Items {
		decl, ok := item.(*ast.StructDecl)
		if !ok {
			continue
		}
		name := decl.Name.Text
		if c.reserved(name) {
			return errorAt(decl.Name.Span(), "%q is already declared at top level", name)
		}
		params, err := declareTypeParams(decl.TypeParams, make(map[string]*valueType))
		if err != nil {
			return err
		}
		c.structures[name] = &structure{params: params, fields: make(map[string]*valueType), methods: make(map[string]*signature)}
	}
	for _, item := range program.Items {
		decl, ok := item.(*ast.StructDecl)
		if !ok {
			continue
		}
		st := c.structures[decl.Name.Text]
		names := make(map[string]*valueType, len(st.params))
		for _, param := range st.params {
			names[param.name] = param
		}
		for _, field := range decl.Fields {
			if st.fields[field.Name.Text] != nil {
				return errorAt(field.Name.Span(), "duplicate field %q", field.Name.Text)
			}
			typ, err := c.resolveUse(field.Type, names, false)
			if err != nil {
				return err
			}
			st.fields[field.Name.Text] = typ
		}
	}
	for _, item := range program.Items {
		if decl, ok := item.(*ast.FuncDecl); ok {
			if err := c.registerFunction(decl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *checker) registerFunction(decl *ast.FuncDecl) error {
	sig := &signature{typeNames: make(map[string]*valueType)}
	name := decl.Name.Text
	if decl.Receiver == nil {
		if c.reserved(name) {
			return errorAt(decl.Name.Span(), "%q is already declared at top level", name)
		}
		c.functions[name] = sig
	} else {
		ref := decl.Receiver.Binding.Type
		st := c.structures[ref.Name.Text]
		if st == nil {
			return errorAt(ref.Span(), "method receiver must be a declared struct")
		}
		if len(ref.Args) != len(st.params) {
			return errorAt(ref.Span(), "%s receiver expects %d type parameters, got %d", ref.Name.Text, len(st.params), len(ref.Args))
		}
		var ids []ast.Identifier
		for _, arg := range ref.Args {
			if len(arg.Args) != 0 {
				return errorAt(arg.Span(), "receiver type parameter must be a bare identifier")
			}
			ids = append(ids, arg.Name)
		}
		params, err := declareTypeParams(ids, sig.typeNames)
		if err != nil {
			return err
		}
		if st.fields[name] != nil || st.methods[name] != nil {
			return errorAt(decl.Name.Span(), "duplicate member %q on %s", name, ref.Name.Text)
		}
		sig.receiverParams = params
		sig.receiver = named(ref.Name.Text, params...)
		sig.constReceiver = decl.Receiver.Const
		st.methods[name] = sig
	}
	var err error
	sig.typeParams, err = declareTypeParams(decl.TypeParams, sig.typeNames)
	if err != nil {
		return err
	}
	for _, param := range decl.Params {
		typ, err := c.resolveUse(param.Type, sig.typeNames, false)
		if err != nil {
			return err
		}
		sig.params = append(sig.params, typ)
	}
	sig.result, err = c.resolveUse(decl.ReturnType, sig.typeNames, true)
	if err != nil {
		return err
	}
	c.declarations[decl] = sig
	return nil
}

func builtins() map[string]*signature {
	element := &valueType{kind: typeParameter, name: "T", nonVoid: true}
	slice := named("Slice", element)
	length := &signature{typeParams: []*valueType{element}, params: []*valueType{slice}, result: intType}
	okType := &valueType{kind: typeParameter, name: "T"}
	errType := &valueType{kind: typeParameter, name: "E", nonVoid: true}
	result := named("Result", okType, errType)
	return map[string]*signature{
		"clock_ns":   {result: intType},
		"rand_int":   {params: []*valueType{intType}, result: intType},
		"make_slice": {typeParams: []*valueType{element}, params: []*valueType{intType, intType}, result: slice},
		"len":        length,
		"cap":        length,
		"append":     {typeParams: []*valueType{element}, params: []*valueType{slice, element}, result: slice},
		"read_file":  {params: []*valueType{strType}, result: named("Result", strType, strType)},
		"write_file": {params: []*valueType{strType, strType}, result: named("Result", voidType, strType)},
		"Ok":         {typeParams: []*valueType{okType, errType}, params: []*valueType{okType}, result: result},
		"Err":        {typeParams: []*valueType{okType, errType}, params: []*valueType{errType}, result: result},
	}
}

// 泛型体可以晚于调用方分析，先传播形参的存储要求，再检查具体实例。
func (c *checker) checkInstantiations() error {
	for changed := true; changed; {
		changed = false
		for _, instance := range c.instantiations {
			arg := instance.argument.resolved()
			if instance.param.nonVoid && arg.kind == typeParameter && !arg.nonVoid {
				arg.nonVoid = true
				changed = true
			}
		}
	}
	for _, instance := range c.instantiations {
		if instance.param.nonVoid && instance.argument.resolved() == voidType {
			return errorAt(instance.span, "void cannot be used for type parameter %s in a value or storage position", instance.param)
		}
	}
	return nil
}

type context struct {
	*checker
	scope     *scope
	typeNames map[string]*valueType
	result    *valueType // 顶层语句没有函数返回类型
}

func (c *checker) function(decl *ast.FuncDecl) error {
	sig := c.declarations[decl]
	body := context{checker: c, scope: newScope(c.global), typeNames: sig.typeNames, result: sig.result}
	if recv := decl.Receiver; recv != nil {
		if err := body.scope.define(recv.Binding.Name, binding{typ: sig.receiver, constant: recv.Const}); err != nil {
			return err
		}
	}
	for i, param := range decl.Params {
		if err := body.scope.define(param.Name, binding{typ: sig.params[i]}); err != nil {
			return err
		}
	}
	exits, err := body.block(decl.Body)
	if err != nil {
		return err
	}
	if sig.result != voidType && !exits {
		return errorAt(decl.Name.Span(), "%s must return %s on every path that reaches the end of its body", decl.Name.Text, sig.result)
	}
	return nil
}

func (c context) nestedBlock(block *ast.Block) (bool, error) {
	c.scope = newScope(c.scope)
	return c.block(block)
}

// 返回值表示控制流是否无法正常到达块末尾。
func (c context) block(block *ast.Block) (bool, error) {
	exits := false
	for _, stmt := range block.Statements {
		stmtExits, err := c.statement(stmt)
		if err != nil {
			return false, err
		}
		exits = exits || stmtExits
	}
	return exits, nil
}

func (c context) statement(stmt ast.Stmt) (bool, error) {
	switch stmt := stmt.(type) {
	case *ast.BindingStmt:
		if c.scope == c.global && c.reserved(stmt.Name.Text) {
			return false, errorAt(stmt.Name.Span(), "%q is already declared at top level", stmt.Name.Text)
		}
		var expected *valueType
		if stmt.Type != nil {
			var err error
			expected, err = c.resolveUse(stmt.Type, c.typeNames, false)
			if err != nil {
				return false, err
			}
		}
		typ, err := c.checkExpr(stmt.Value, expected, false)
		if err != nil {
			return false, err
		}
		if err := c.scope.define(stmt.Name, binding{typ: typ, constant: stmt.Const}); err != nil {
			return false, err
		}
		if stmt.Type == nil {
			c.inferred = append(c.inferred, inferredBinding{node: stmt, typ: typ})
		}
		return false, nil
	case *ast.AssignStmt:
		return false, c.assign(stmt)
	case *ast.ExprStmt:
		_, err := c.checkExpr(stmt.Expr, nil, true)
		return false, err
	case *ast.ReturnStmt:
		if c.result == nil {
			return false, errorAt(stmt.Span(), "return requires an enclosing function or method")
		}
		if stmt.Value == nil {
			if c.result != voidType {
				return false, errorAt(stmt.Span(), "return requires a value of type %s", c.result)
			}
			return true, nil
		}
		if c.result == voidType {
			return false, errorAt(stmt.Span(), "cannot return a value from a void function or method")
		}
		_, err := c.checkExpr(stmt.Value, c.result, false)
		return true, err
	case *ast.IfStmt:
		canSkip := true
		canComplete := false
		for _, branch := range stmt.Branches {
			if _, err := c.checkExpr(branch.Condition, boolType, false); err != nil {
				return false, err
			}
			exits, err := c.nestedBlock(branch.Body)
			if err != nil {
				return false, err
			}
			known, truth := literalBool(branch.Condition)
			canComplete = canComplete || canSkip && (!known || truth) && !exits
			canSkip = canSkip && (!known || !truth)
		}
		if stmt.Else != nil {
			exits, err := c.nestedBlock(stmt.Else)
			if err != nil {
				return false, err
			}
			canSkip = canSkip && !exits
		}
		return !canComplete && !canSkip, nil
	case *ast.WhileStmt:
		if _, err := c.checkExpr(stmt.Condition, boolType, false); err != nil {
			return false, err
		}
		_, err := c.nestedBlock(stmt.Body)
		known, truth := literalBool(stmt.Condition)
		return known && truth, err
	default:
		return false, errorAt(stmt.Span(), "unsupported statement %T", stmt)
	}
}

func ungroup(expr ast.Expr) ast.Expr {
	for {
		group, ok := expr.(*ast.GroupExpr)
		if !ok {
			return expr
		}
		expr = group.Inner
	}
}

func literalBool(expr ast.Expr) (known, truth bool) {
	literal, ok := ungroup(expr).(*ast.LiteralExpr)
	if !ok {
		return false, false
	}
	return literal.Kind == lexer.True || literal.Kind == lexer.False, literal.Kind == lexer.True
}

func (c context) constantBinding(expr ast.Expr) *ast.Identifier {
	if id, ok := ungroup(expr).(*ast.Identifier); ok {
		if b, _ := c.scope.lookup(id.Text); b.constant {
			return id
		}
	}
	return nil
}

func (c context) assign(stmt *ast.AssignStmt) error {
	switch target := ungroup(stmt.Target).(type) {
	case *ast.Identifier:
		if id := c.constantBinding(target); id != nil {
			return errorAt(stmt.Target.Span(), "cannot assign to const binding %q", id.Text)
		}
	case *ast.MemberExpr:
		if id := c.constantBinding(target.Target); id != nil {
			return errorAt(stmt.Target.Span(), "cannot assign to field %q of const binding %q", target.Name.Text, id.Text)
		}
	case *ast.IndexExpr:
	default:
		return errorAt(stmt.Target.Span(), "invalid assignment target")
	}
	x := expressionCheck{context: c}
	target, err := x.value(stmt.Target)
	if err != nil {
		return err
	}
	value, err := x.value(stmt.Value)
	if err != nil {
		return err
	}
	if err := expectType(stmt.Value.Span(), value, target, "assignment value"); err != nil {
		return err
	}
	return x.finish()
}

type numericLiteral struct {
	span lexer.Span
	raw  string
	typ  *valueType
}

type typeUse struct {
	span      lexer.Span
	typ       *valueType
	allowVoid bool
}

type operation struct {
	operator lexer.Token
	typ      *valueType
	unary    bool
}

// 同一表达式的类型约束合并后才给数值字面量使用默认类型，避免实参
// 或运算数出现的先后顺序影响推断。
type expressionCheck struct {
	context
	literals  []numericLiteral
	uses      []typeUse
	operators []operation
	instances []instantiation
}

func (c context) checkExpr(expr ast.Expr, expected *valueType, allowVoid bool) (*valueType, error) {
	x := expressionCheck{context: c}
	typ, err := x.use(expr, allowVoid)
	if err != nil {
		return nil, err
	}
	if expected != nil {
		if err := expectType(expr.Span(), typ, expected, "expression"); err != nil {
			return nil, err
		}
	}
	if err := x.finish(); err != nil {
		return nil, err
	}
	return typ.resolved(), nil
}

func (x *expressionCheck) finish() error {
	for _, literal := range x.literals {
		typ := literal.typ.resolved()
		if typ.kind == inferenceVariable {
			typ.solution = intType
			if typ.number == lexer.Float {
				typ.solution = floatType
			}
		}
	}
	for _, instance := range x.instances {
		if !instance.argument.complete() {
			return errorAt(instance.span, "cannot infer type argument %s", instance.param)
		}
	}
	for _, literal := range x.literals {
		if err := literal.checkRange(); err != nil {
			return err
		}
	}
	for _, use := range x.uses {
		if err := requireType(use.typ, use.allowVoid, use.span); err != nil {
			return err
		}
	}
	for _, op := range x.operators {
		if err := op.check(); err != nil {
			return err
		}
	}
	x.checker.instantiations = append(x.checker.instantiations, x.instances...)
	return nil
}

func (n numericLiteral) checkRange() error {
	typ := n.typ.resolved()
	bits := 64
	if typ == int32Type || typ == float32Type {
		bits = 32
	}
	var err error
	if typ.numberKind() == lexer.Integer {
		_, err = strconv.ParseInt(n.raw, 10, bits)
	} else {
		_, err = strconv.ParseFloat(n.raw, bits)
	}
	if err != nil {
		return errorAt(n.span, "literal %s is out of range for %s", n.raw, typ)
	}
	return nil
}

func (x *expressionCheck) value(expr ast.Expr) (*valueType, error) {
	return x.use(expr, false)
}

func (x *expressionCheck) use(expr ast.Expr, allowVoid bool) (*valueType, error) {
	typ, err := x.expr(expr)
	if err == nil {
		x.uses = append(x.uses, typeUse{span: expr.Span(), typ: typ, allowVoid: allowVoid})
	}
	return typ, err
}

func (x *expressionCheck) expr(expr ast.Expr) (*valueType, error) {
	switch node := ungroup(expr).(type) {
	case *ast.Identifier:
		if b, ok := x.scope.lookup(node.Text); ok {
			return b.typ, nil
		}
		if x.reserved(node.Text) || x.typeNames[node.Text] != nil {
			return nil, errorAt(node.Span(), "%q cannot be used as a value", node.Text)
		}
		return nil, errorAt(node.Span(), "undefined name %q", node.Text)
	case *ast.LiteralExpr:
		switch node.Kind {
		case lexer.True, lexer.False:
			return boolType, nil
		case lexer.String:
			return strType, nil
		case lexer.Integer, lexer.Float:
			return x.number(node.Kind, node.Raw, node.Span()), nil
		}
	case *ast.UnaryExpr:
		return x.unary(node)
	case *ast.BinaryExpr:
		return x.binary(node)
	case *ast.CallExpr:
		return x.call(node)
	case *ast.MemberExpr:
		receiver, st, err := x.memberReceiver(node)
		if err != nil {
			return nil, err
		}
		if field := st.fields[node.Name.Text]; field != nil {
			return field.substitute(substitutions(st.params, receiver.args)), nil
		}
		if st.methods[node.Name.Text] != nil {
			return nil, errorAt(node.Name.Span(), "method %q must be called", node.Name.Text)
		}
		return nil, errorAt(node.Name.Span(), "%s has no member %q", receiver, node.Name.Text)
	case *ast.IndexExpr:
		return x.index(node)
	case *ast.TryExpr:
		return x.try(node)
	}
	return nil, errorAt(expr.Span(), "unsupported expression %T", expr)
}

func (x *expressionCheck) number(kind lexer.Kind, raw string, span lexer.Span) *valueType {
	typ := &valueType{kind: inferenceVariable, number: kind}
	x.literals = append(x.literals, numericLiteral{span: span, raw: raw, typ: typ})
	return typ
}

func (x *expressionCheck) unary(node *ast.UnaryExpr) (*valueType, error) {
	if node.Operator.Kind == lexer.Minus {
		if literal, ok := node.Operand.(*ast.LiteralExpr); ok && (literal.Kind == lexer.Integer || literal.Kind == lexer.Float) {
			return x.number(literal.Kind, "-"+literal.Raw, node.Span()), nil
		}
	}
	operand, err := x.value(node.Operand)
	if err != nil {
		return nil, err
	}
	if node.Operator.Kind == lexer.Not {
		return boolType, expectType(node.Operand.Span(), operand, boolType, "operand of not")
	}
	// 单目 + 和 - 只接受数字，与二元 + 的字符串规则不同。
	x.operators = append(x.operators, operation{operator: node.Operator, typ: operand, unary: true})
	return operand, nil
}

func (x *expressionCheck) binary(node *ast.BinaryExpr) (*valueType, error) {
	left, err := x.value(node.Left)
	if err != nil {
		return nil, err
	}
	right, err := x.value(node.Right)
	if err != nil {
		return nil, err
	}
	switch node.Operator.Kind {
	case lexer.And, lexer.Or:
		if err := expectType(node.Left.Span(), left, boolType, "left operand"); err != nil {
			return nil, err
		}
		return boolType, expectType(node.Right.Span(), right, boolType, "right operand")
	}
	if err := expectType(node.Right.Span(), right, left, "right operand"); err != nil {
		return nil, err
	}
	x.operators = append(x.operators, operation{operator: node.Operator, typ: left})
	switch node.Operator.Kind {
	case lexer.Equal, lexer.NotEqual, lexer.Less, lexer.LessEqual, lexer.Greater, lexer.GreaterEqual:
		return boolType, nil
	default:
		return left, nil
	}
}

func (op operation) check() error {
	typ := op.typ.resolved()
	number := typ.numberKind()
	valid := number != ""
	switch op.operator.Kind {
	case lexer.Plus, lexer.Less, lexer.LessEqual, lexer.Greater, lexer.GreaterEqual:
		valid = valid || !op.unary && typ == strType
	case lexer.Equal, lexer.NotEqual:
		valid = valid || typ == strType || typ == boolType
	case lexer.Percent:
		valid = number == lexer.Integer
	}
	if !valid {
		return errorAt(op.operator.Span, "operator %q does not accept %s", op.operator.Lexeme, typ)
	}
	return nil
}

func (x *expressionCheck) memberReceiver(node *ast.MemberExpr) (*valueType, *structure, error) {
	target, err := x.value(node.Target)
	if err != nil {
		return nil, nil, err
	}
	target = target.resolved()
	if target.kind == namedType {
		if st := x.structures[target.name]; st != nil {
			return target, st, nil
		}
	}
	return nil, nil, errorAt(node.Name.Span(), "cannot select member %q on %s", node.Name.Text, target)
}

func (x *expressionCheck) index(node *ast.IndexExpr) (*valueType, error) {
	target, err := x.value(node.Target)
	if err != nil {
		return nil, err
	}
	element := &valueType{kind: inferenceVariable}
	if !unify(target, named("Slice", element)) {
		return nil, errorAt(node.Target.Span(), "indexing requires Slice, got %s", target)
	}
	index, err := x.value(node.Index)
	if err != nil {
		return nil, err
	}
	return element, expectType(node.Index.Span(), index, intType, "slice index")
}

func (x *expressionCheck) try(node *ast.TryExpr) (*valueType, error) {
	operand, err := x.value(node.Operand)
	if err != nil {
		return nil, err
	}
	operand = operand.resolved()
	if operand.kind != inferenceVariable && (operand.kind != namedType || operand.name != "Result") {
		return nil, errorAt(node.Question, "? requires a Result operand, got %s", operand)
	}
	if x.result == nil || x.result.kind != namedType || x.result.name != "Result" {
		return nil, errorAt(node.Question, "? requires an enclosing function or method returning Result")
	}
	if operand.kind == inferenceVariable {
		success := &valueType{kind: inferenceVariable}
		if !unify(operand, named("Result", success, x.result.args[1])) {
			return nil, errorAt(node.Question, "? requires a Result operand, got %s", operand)
		}
		return success, nil
	}
	if !unify(operand.args[1], x.result.args[1]) {
		return nil, errorAt(node.Question, "? error type %s does not match enclosing Result error type %s", operand.args[1], x.result.args[1])
	}
	return operand.args[0], nil
}

func (x *expressionCheck) call(node *ast.CallExpr) (*valueType, error) {
	switch callee := ungroup(node.Callee).(type) {
	case *ast.Identifier:
		name := callee.Text
		if b, ok := x.scope.lookup(name); ok {
			return nil, errorAt(callee.Span(), "%q has type %s and is not callable", name, b.typ)
		}
		if st := x.structures[name]; st != nil {
			return x.construct(node, name, st)
		}
		if sig := x.functions[name]; sig != nil {
			if name == "Ok" && len(node.Args) == 0 {
				return x.emptyOk(node, sig)
			}
			return x.invoke(node, name, sig, nil)
		}
		if name == "println" {
			if len(node.TypeArgs) != 0 {
				return nil, errorAt(node.Span(), "println does not accept type arguments")
			}
			for _, arg := range node.Args {
				if arg.Name != nil {
					return nil, errorAt(arg.Span(), "println requires positional arguments")
				}
				if _, err := x.value(arg.Value); err != nil {
					return nil, err
				}
			}
			return voidType, nil
		}
		if typ := primitiveTypes[name]; x.typeNames[name] == nil && typ != nil && typ.numberKind() != "" {
			return x.convert(node, callee, typ)
		}
		if x.reserved(name) || x.typeNames[name] != nil {
			return nil, errorAt(callee.Span(), "%q is not callable", name)
		}
		return nil, errorAt(callee.Span(), "undefined function %q", name)
	case *ast.MemberExpr:
		receiver, st, err := x.memberReceiver(callee)
		if err != nil {
			return nil, err
		}
		name := callee.Name.Text
		sig := st.methods[name]
		if sig == nil {
			return nil, errorAt(callee.Name.Span(), "%s has no method %q", receiver, name)
		}
		if id := x.constantBinding(callee.Target); id != nil && !sig.constReceiver {
			return nil, errorAt(callee.Name.Span(), "cannot call mutable method %q on const binding %q", name, id.Text)
		}
		return x.invoke(node, name, sig, substitutions(sig.receiverParams, receiver.args))
	default:
		return nil, errorAt(node.Callee.Span(), "call requires a function, constructor, conversion, or method")
	}
}

func (x *expressionCheck) typeArguments(node *ast.CallExpr, name string, params []*valueType) ([]*valueType, error) {
	if n := len(node.TypeArgs); n != 0 && n != len(params) {
		return nil, errorAt(node.Span(), "%s expects %d type arguments, got %d", name, len(params), n)
	}
	args := make([]*valueType, len(params))
	for i, param := range params {
		span := node.Span()
		arg := &valueType{kind: inferenceVariable, name: param.name}
		if len(node.TypeArgs) != 0 {
			ref := node.TypeArgs[i]
			span = ref.Span()
			var err error
			arg, err = x.resolveUse(ref, x.typeNames, true)
			if err != nil {
				return nil, err
			}
		}
		x.instances = append(x.instances, instantiation{span: span, param: param, argument: arg})
		args[i] = arg
	}
	return args, nil
}

func (x *expressionCheck) argument(arg ast.Argument, expected *valueType) error {
	typ, err := x.value(arg.Value)
	if err != nil {
		return err
	}
	return expectType(arg.Value.Span(), typ, expected, "argument")
}

func (x *expressionCheck) invoke(node *ast.CallExpr, name string, sig *signature, types map[*valueType]*valueType) (*valueType, error) {
	if len(node.Args) != len(sig.params) {
		return nil, errorAt(node.Span(), "%s expects %d arguments, got %d", name, len(sig.params), len(node.Args))
	}
	args, err := x.typeArguments(node, name, sig.typeParams)
	if err != nil {
		return nil, err
	}
	if types == nil {
		types = substitutions(sig.typeParams, args)
	} else {
		for i, param := range sig.typeParams {
			types[param] = args[i]
		}
	}
	for i, arg := range node.Args {
		if arg.Name != nil {
			return nil, errorAt(arg.Span(), "%s requires positional arguments", name)
		}
		if err := x.argument(arg, sig.params[i].substitute(types)); err != nil {
			return nil, err
		}
	}
	return sig.result.substitute(types), nil
}

func (x *expressionCheck) construct(node *ast.CallExpr, name string, st *structure) (*valueType, error) {
	args, err := x.typeArguments(node, name, st.params)
	if err != nil {
		return nil, err
	}
	if len(node.Args) != len(st.fields) {
		return nil, errorAt(node.Span(), "%s constructor requires %d field initializers, got %d", name, len(st.fields), len(node.Args))
	}
	types := substitutions(st.params, args)
	initialized := make(map[string]bool, len(st.fields))
	for _, arg := range node.Args {
		if arg.Name == nil {
			return nil, errorAt(arg.Span(), "%s constructor requires named arguments", name)
		}
		field := arg.Name.Text
		if st.fields[field] == nil {
			return nil, errorAt(arg.Name.Span(), "%s has no field %q", name, field)
		}
		if initialized[field] {
			return nil, errorAt(arg.Name.Span(), "duplicate initializer for field %q", field)
		}
		initialized[field] = true
		if err := x.argument(arg, st.fields[field].substitute(types)); err != nil {
			return nil, err
		}
	}
	return named(name, args...), nil
}

func (x *expressionCheck) emptyOk(node *ast.CallExpr, sig *signature) (*valueType, error) {
	args, err := x.typeArguments(node, "Ok", sig.typeParams)
	if err != nil {
		return nil, err
	}
	if !unify(args[0], voidType) {
		return nil, errorAt(node.Span(), "Ok() requires a Result with void success type, got %s", args[0])
	}
	return named("Result", args...), nil
}

func (x *expressionCheck) convert(node *ast.CallExpr, callee *ast.Identifier, target *valueType) (*valueType, error) {
	if len(node.TypeArgs) != 0 || len(node.Args) != 1 || node.Args[0].Name != nil {
		return nil, errorAt(node.Span(), "%s conversion requires one positional argument and no type arguments", callee.Text)
	}
	arg, err := x.value(node.Args[0].Value)
	if err != nil {
		return nil, err
	}
	if typ := arg.resolved(); typ.kind == inferenceVariable && typ.number == target.numberKind() {
		unify(typ, target)
	}
	x.operators = append(x.operators, operation{operator: lexer.Token{Lexeme: callee.Text, Span: callee.Span()}, typ: arg})
	return target, nil
}
