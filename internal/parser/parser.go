// parser 包使用递归下降分析 Lynguage 的语法结构，用 Pratt 算法分析表达式。
// 之所以用 Pratt 算法，是因为 Lynguage 的表达式语法中有很多二元运算符，且优先级和结合性不规则。
// Pratt 算法可以在不引入额外的语法规则的情况下处理这些运算符。
package parser

import (
	"fmt"

	"lynguage/internal/ast"
	"lynguage/internal/lexer"
)

// Error 记录语法错误的源码区间与说明。
type Error struct {
	Span    lexer.Span
	Message string
}

// Error 按行号、列号和错误说明的顺序格式化诊断。
func (e *Error) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Span.Start.Line, e.Span.Start.Column, e.Message)
}

type parser struct {
	tokens       []lexer.Token
	index        int
	typeArgCache map[int]typeArgumentParse
}

type typeArgumentParse struct {
	args []*ast.TypeRef
	end  lexer.Token
	next int
	err  error
}

// Parse 将词法分析、换行处理和顶层语法分析串起来。预处理保留可分隔
// 语句的换行，随后 lineList 逐项调用 parseTopItem，直到读到 EOF。

// Parse 返回程序的语法树，或分析过程中首先发现的词法或语法错误。
// 名称解析和类型检查在独立阶段进行。
func Parse(source string) (*ast.Program, error) {
	tokens, err := lexer.Lex(source)
	if err != nil {
		return nil, err
	}
	// 顶层以 EOF 为终止记号，复用代码块和字段列表的分行规则。
	p := parser{tokens: withoutGroupedNewlines(tokens)}
	items, eof, err := lineList(&p, lexer.EOF, p.parseTopItem)
	if err != nil {
		return nil, err
	}
	return &ast.Program{
		NodeSpan: sourceSpan(lexer.Position{Line: 1, Column: 1}, eof.Span.End),
		Items:    items,
	}, nil
}

// withoutGroupedNewlines 忽略圆括号和方括号内的换行。
// 尖括号也可用于比较，类型实参列表中的换行由列表解析逻辑处理。
func withoutGroupedNewlines(tokens []lexer.Token) []lexer.Token {
	// 用原记号切片就地保留需要的记号，depth 记录当前嵌套深度。
	result := tokens[:0]
	depth := 0
	for _, token := range tokens {
		switch token.Kind {
		case lexer.LParen, lexer.LBracket:
			depth++
		case lexer.RParen, lexer.RBracket:
			if depth > 0 {
				depth--
			}
		case lexer.Newline:
			// 只有外层换行才可能分隔语句、字段或顶层项目。
			if depth > 0 {
				continue
			}
		}
		result = append(result, token)
	}
	return result
}

// 顶层根据首个记号区分声明与普通语句。结构体和函数共用类型形参、
// 带类型名称和类型引用的解析规则，函数体交给语句解析函数处理。

// parseTopItem 读取一个顶层声明或语句。
func (p *parser) parseTopItem() (ast.Item, error) {
	// 声明只在顶层识别，其他首记号沿用代码块中的语句规则。
	switch p.current().Kind {
	case lexer.Struct:
		return p.parseStruct()
	case lexer.Def:
		return p.parseFunction()
	default:
		return p.parseStatement()
	}
}

// parseStruct 读取结构体的名称、可选类型形参和字段列表。
func (p *parser) parseStruct() (*ast.StructDecl, error) {
	start := p.advance()
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	typeParams, err := p.parseTypeParameters()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(lexer.LBrace); err != nil {
		return nil, err
	}
	// 每个字段是一个带类型的名称，整个字段列表使用换行分隔。
	fields, end, err := lineList(p, lexer.RBrace, p.parseTypedName)
	if err != nil {
		return nil, err
	}
	return &ast.StructDecl{
		NodeSpan:   sourceSpan(start.Span.Start, end.Span.End),
		Name:       name,
		TypeParams: typeParams,
		Fields:     fields,
	}, nil
}

// parseFunction 读取函数或方法的完整签名和函数体。
func (p *parser) parseFunction() (*ast.FuncDecl, error) {
	start := p.advance()
	// def 后若有接收者，后续签名仍与普通函数共用同一套解析规则。
	receiver, err := p.parseOptionalReceiver()
	if err != nil {
		return nil, err
	}
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	typeParams, err := p.parseTypeParameters()
	if err != nil {
		return nil, err
	}
	// 参数逐项要求类型标注，右括号之后必须出现返回类型。
	if _, err := p.expect(lexer.LParen); err != nil {
		return nil, err
	}
	params, _, err := commaList(p, lexer.RParen, p.parseTypedName)
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(lexer.Arrow); err != nil {
		return nil, err
	}
	returnType, err := p.parseType()
	if err != nil {
		return nil, err
	}
	// 完成签名后才解析函数体，用闭花括号确定声明的结束位置。
	body, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	return &ast.FuncDecl{
		NodeSpan:   sourceSpan(start.Span.Start, body.Span().End),
		Name:       name,
		TypeParams: typeParams,
		Receiver:   receiver,
		Params:     params,
		ReturnType: returnType,
		Body:       body,
	}, nil
}

// parseOptionalReceiver 读取 def 后的可选方法接收者及其 const 标记。
func (p *parser) parseOptionalReceiver() (*ast.Receiver, error) {
	start, ok := p.match(lexer.LParen)
	if !ok {
		return nil, nil
	}
	// 确定存在接收者后，先读可选的 const，再复用参数的标注语法。
	_, isConst := p.match(lexer.Const)
	binding, err := p.parseTypedName()
	if err != nil {
		return nil, err
	}
	end, err := p.expect(lexer.RParen)
	if err != nil {
		return nil, err
	}
	return &ast.Receiver{
		NodeSpan: sourceSpan(start.Span.Start, end.Span.End),
		Binding:  binding,
		Const:    isConst,
	}, nil
}

// 类型形参是声明引入的名称，类型实参是可能继续嵌套的类型引用。
// parseTypeParameters 读名称列表，parseType 与 parseTypeArguments
// 递归处理类型引用。调用处试读显式类型实参时也复用后者。

// parseTypeParameters 读取声明中的可选尖括号类型形参列表。
func (p *parser) parseTypeParameters() ([]ast.Identifier, error) {
	if _, ok := p.match(lexer.Less); !ok {
		return nil, nil
	}
	params, _, err := commaList(p, lexer.Greater, p.parseIdentifier)
	return params, err
}

// parseTypedName 读取字段、参数或接收者共用的名称与类型标注。
func (p *parser) parseTypedName() (ast.TypedName, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return ast.TypedName{}, err
	}
	// 冒号把名称位置和类型位置分开，后者允许嵌套泛型。
	if _, err := p.expect(lexer.Colon); err != nil {
		return ast.TypedName{}, err
	}
	ref, err := p.parseType()
	if err != nil {
		return ast.TypedName{}, err
	}
	return ast.TypedName{
		NodeSpan: sourceSpan(name.Span().Start, ref.Span().End),
		Name:     name,
		Type:     ref,
	}, nil
}

// parseType 读取类型名称和可选的嵌套类型实参。
func (p *parser) parseType() (*ast.TypeRef, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	ref := &ast.TypeRef{NodeSpan: name.NodeSpan, Name: name}
	// 类型位置的 < 总是开启类型实参列表，允许它与类型名之间有空白。
	if p.at(lexer.Less) {
		args := p.parseTypeArguments()
		if args.err != nil {
			return nil, args.err
		}
		ref.Args = args.args
		ref.NodeSpan = sourceSpan(name.Span().Start, args.end.Span.End)
	}
	return ref, nil
}

// parseTypeArguments 从当前的 < 读取类型实参列表，并按起点缓存结果。
func (p *parser) parseTypeArguments() typeArgumentParse {
	start := p.index
	// 命中时恢复原解析结束后的游标，让调用方得到与重新解析相同的状态。
	if cached, ok := p.typeArgCache[start]; ok {
		p.index = cached.next
		return cached
	}
	p.advance()
	args, end, err := commaList(p, lexer.Greater, p.parseType)
	// 成功和失败都缓存，避免在比较链试读时重复扫描嵌套列表。
	parsed := typeArgumentParse{args: args, end: end, next: p.index, err: err}
	if p.typeArgCache == nil {
		p.typeArgCache = make(map[int]typeArgumentParse)
	}
	p.typeArgCache[start] = parsed
	return parsed
}

// 语句解析从 parseStatement 分派。parseBlock 用 lineList 重复读取
// 语句，条件分支和循环又用 parseBlock 读取各自的主体。绑定、返回、
// 条件和赋值中的表达式都由 parseExpression 处理。

// parseBlock 读取由花括号围住的一组语句。
func (p *parser) parseBlock() (*ast.Block, error) {
	start, err := p.expect(lexer.LBrace)
	if err != nil {
		return nil, err
	}
	// 把闭花括号交给列表读取，使最后一条语句可以直接接在它前面。
	statements, end, err := lineList(p, lexer.RBrace, p.parseStatement)
	if err != nil {
		return nil, err
	}
	return &ast.Block{
		NodeSpan:   sourceSpan(start.Span.Start, end.Span.End),
		Statements: statements,
	}, nil
}

// parseStatement 根据首记号读取一条语句。
func (p *parser) parseStatement() (ast.Stmt, error) {
	switch p.current().Kind {
	case lexer.Let, lexer.Const:
		return p.parseBinding()
	case lexer.If:
		return p.parseIf()
	case lexer.While:
		// while 和带条件的 if 分支共享“条件表达式加代码块”的形状。
		branch, err := p.parseConditionalBranch()
		if err != nil {
			return nil, err
		}
		return &ast.WhileStmt{
			NodeSpan:  branch.NodeSpan,
			Condition: branch.Condition,
			Body:      branch.Body,
		}, nil
	case lexer.Return:
		return p.parseReturn()
	default:
		return p.parseExpressionStatement()
	}
}

// parseBinding 读取 let 或 const 绑定、可选类型标注和必需的初始值。
func (p *parser) parseBinding() (*ast.BindingStmt, error) {
	start := p.advance()
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	// 仅在冒号出现时读取类型，省略时留给后续类型推断。
	var ref *ast.TypeRef
	if _, ok := p.match(lexer.Colon); ok {
		ref, err = p.parseType()
		if err != nil {
			return nil, err
		}
	}
	// 两种绑定都要求等号及完整的初始值表达式。
	if _, err := p.expect(lexer.Assign); err != nil {
		return nil, err
	}
	value, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return nil, err
	}
	return &ast.BindingStmt{
		NodeSpan: sourceSpan(start.Span.Start, value.Span().End),
		Name:     name,
		Const:    start.Kind == lexer.Const,
		Type:     ref,
		Value:    value,
	}, nil
}

// parseIf 读取 if 及其后续的 elif 和 else 分支。
func (p *parser) parseIf() (*ast.IfStmt, error) {
	first, err := p.parseConditionalBranch()
	if err != nil {
		return nil, err
	}
	result := &ast.IfStmt{Branches: []ast.ConditionalBranch{first}}
	end := first.Span().End
	for {
		// 先跨过换行查看是否有后续分支，必要时交还换行给外层列表。
		beforeNewlines := p.index
		p.skipNewlines()
		switch p.current().Kind {
		case lexer.Elif:
			branch, err := p.parseConditionalBranch()
			if err != nil {
				return nil, err
			}
			result.Branches = append(result.Branches, branch)
			end = branch.Span().End
			continue
		case lexer.Else:
			// else 直接读取代码块，一旦读到就结束整条条件语句。
			p.advance()
			body, err := p.parseBlock()
			if err != nil {
				return nil, err
			}
			result.Else = body
			end = body.Span().End
		default:
			p.index = beforeNewlines
		}
		break
	}
	result.NodeSpan = sourceSpan(first.Span().Start, end)
	return result, nil
}

// parseConditionalBranch 读取 if、elif 或 while 后的条件和主体。
func (p *parser) parseConditionalBranch() (ast.ConditionalBranch, error) {
	start := p.advance()
	// 先读完整的条件表达式，随后的左花括号开启主体。
	condition, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return ast.ConditionalBranch{}, err
	}
	body, err := p.parseBlock()
	if err != nil {
		return ast.ConditionalBranch{}, err
	}
	return ast.ConditionalBranch{
		NodeSpan:  sourceSpan(start.Span.Start, body.Span().End),
		Condition: condition,
		Body:      body,
	}, nil
}

// parseReturn 读取裸 return 或带返回表达式的语句。
func (p *parser) parseReturn() (*ast.ReturnStmt, error) {
	start := p.advance()
	// 语句边界上的 return 没有值，下一个记号由外层列表处理。
	switch p.current().Kind {
	case lexer.Newline, lexer.RBrace, lexer.EOF:
		return &ast.ReturnStmt{NodeSpan: ast.NodeSpan{Source: start.Span}}, nil
	}
	value, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return nil, err
	}
	return &ast.ReturnStmt{
		NodeSpan: sourceSpan(start.Span.Start, value.Span().End),
		Value:    value,
	}, nil
}

// parseExpressionStatement 读取独立表达式，或把它作为赋值左侧继续解析。
func (p *parser) parseExpressionStatement() (ast.Stmt, error) {
	left, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return nil, err
	}
	// 等号把表达式语句转换成赋值语句，等号本身不参与表达式解析。
	if _, ok := p.match(lexer.Assign); !ok {
		return &ast.ExprStmt{NodeSpan: ast.NodeSpan{Source: left.Span()}, Expr: left}, nil
	}
	// 根据左侧最外层的语法节点判断它能否作为赋值目标。
	switch left.(type) {
	case *ast.Identifier, *ast.MemberExpr, *ast.IndexExpr:
	default:
		return nil, &Error{Span: left.Span(), Message: "invalid assignment target"}
	}
	right, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return nil, err
	}
	return &ast.AssignStmt{
		NodeSpan: sourceSpan(left.Span().Start, right.Span().End),
		Target:   left,
		Value:    right,
	}, nil
}

// Pratt 表达式解析先由 parsePrefix 建立左侧节点，再由 parsePostfix
// 处理紧跟的后缀。剩余记号交给 infixPrecedence 判断能否继续结合，
// 二元运算的右侧会递归调用 parseExpression。

const (
	lowestPrecedence = iota + 1
	orPrecedence
	andPrecedence
	equalityPrecedence
	comparisonPrecedence
	sumPrecedence
	productPrecedence
	prefixPrecedence
)

// infixPrecedence 返回二元运算符的优先级，其余记号返回 0。
func infixPrecedence(kind lexer.Kind) int {
	// 每组共享同一优先级，数值越大越先与操作数结合。
	switch kind {
	case lexer.Or:
		return orPrecedence
	case lexer.And:
		return andPrecedence
	case lexer.Equal, lexer.NotEqual:
		return equalityPrecedence
	case lexer.Less, lexer.LessEqual, lexer.Greater, lexer.GreaterEqual:
		return comparisonPrecedence
	case lexer.Plus, lexer.Minus:
		return sumPrecedence
	case lexer.Star, lexer.Slash, lexer.Percent:
		return productPrecedence
	default:
		return 0
	}
}

// parseExpression 读取由最低优先级界定的一个完整表达式。
func (p *parser) parseExpression(minPrecedence int) (ast.Expr, error) {
	left, err := p.parsePrefix()
	if err != nil {
		return nil, err
	}
	for {
		// 后缀比二元运算符优先。每识别一个后缀，就更新左侧节点并继续。
		postfix, err := p.parsePostfix(left)
		if err != nil {
			return nil, err
		}
		if postfix != nil {
			left = postfix
			continue
		}
		// 较低优先级的运算符留给外层。右侧用更高的门槛实现左结合。
		precedence := infixPrecedence(p.current().Kind)
		if precedence < minPrecedence {
			return left, nil
		}
		operator := p.advance()
		right, err := p.parseExpression(precedence + 1)
		if err != nil {
			return nil, err
		}
		left = &ast.BinaryExpr{
			NodeSpan: sourceSpan(left.Span().Start, right.Span().End),
			Left:     left,
			Operator: operator,
			Right:    right,
		}
	}
}

// parsePrefix 读取字面量、标识符、括号分组或前缀运算。
func (p *parser) parsePrefix() (ast.Expr, error) {
	token := p.current()
	switch token.Kind {
	case lexer.Not, lexer.Plus, lexer.Minus:
		// 前缀操作数可以包含后缀或另一个前缀，二元运算留给外层。
		p.advance()
		operand, err := p.parseExpression(prefixPrecedence)
		if err != nil {
			return nil, err
		}
		return &ast.UnaryExpr{
			NodeSpan: sourceSpan(token.Span.Start, operand.Span().End),
			Operator: token,
			Operand:  operand,
		}, nil
	case lexer.Identifier:
		p.advance()
		name := identifier(token)
		return &name, nil
	case lexer.Integer, lexer.Float, lexer.String, lexer.True, lexer.False:
		p.advance()
		return &ast.LiteralExpr{
			NodeSpan:    ast.NodeSpan{Source: token.Span},
			Kind:        token.Kind,
			Raw:         token.Lexeme,
			StringValue: token.StringValue,
		}, nil
	case lexer.LParen:
		// 括号内从最低优先级开始，并保留分组节点供后续判断。
		p.advance()
		inner, err := p.parseExpression(lowestPrecedence)
		if err != nil {
			return nil, err
		}
		end, err := p.expect(lexer.RParen)
		if err != nil {
			return nil, err
		}
		return &ast.GroupExpr{NodeSpan: sourceSpan(token.Span.Start, end.Span.End), Inner: inner}, nil
	default:
		return nil, p.unexpected("expression")
	}
}

// 后缀解析以已有的左侧节点为目标，调用和索引交给各自的读取函数。
// 对 < 先试读类型实参，失败则回到同一位置，让表达式循环识别比较符。

// parsePostfix 读取一个可附着到左侧表达式的后缀，无后缀时返回 nil。
func (p *parser) parsePostfix(left ast.Expr) (ast.Expr, error) {
	switch p.current().Kind {
	case lexer.LParen:
		return p.parseCall(left, nil)
	case lexer.Less:
		// 只有完整的类型实参列表和后续左括号一起出现时才进入调用解析。
		if args := p.callTypeArguments(left); args != nil {
			return p.parseCall(left, args)
		}
	case lexer.LBracket:
		return p.parseIndex(left)
	case lexer.Dot:
		p.advance()
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		return &ast.MemberExpr{
			NodeSpan: sourceSpan(left.Span().Start, name.Span().End),
			Target:   left,
			Name:     name,
		}, nil
	case lexer.Question:
		// 分别保存整个表达式和问号位置，方便后续定位错误传播诊断。
		question := p.advance()
		return &ast.TryExpr{
			NodeSpan: sourceSpan(left.Span().Start, question.Span.End),
			Operand:  left,
			Question: question.Span,
		}, nil
	}
	return nil, nil
}

// callTypeArguments 尝试识别表达式后紧跟的显式调用类型实参。
func (p *parser) callTypeArguments(left ast.Expr) []*ast.TypeRef {
	// 泛型调用的 < 必须紧接普通名称或成员名。
	switch left.(type) {
	case *ast.Identifier, *ast.MemberExpr:
	default:
		return nil
	}
	if left.Span().End.Offset != p.current().Span.Start.Offset {
		return nil
	}
	// 试读失败时还原游标，使同一个 < 能按二元运算符重新解释。
	beforeLess := p.index
	args := p.parseTypeArguments()
	if args.err != nil || !p.at(lexer.LParen) {
		p.index = beforeLess
		return nil
	}
	return args.args
}

// 调用由 parseCall 根据首个实参决定整组列表的读法，随后 commaList
// 重复调用选定的实参读取函数。位置实参直接读取表达式，命名实参先读名称。

// parseCall 读取调用的值实参，并将可选类型实参与目标一同保存。
func (p *parser) parseCall(callee ast.Expr, typeArgs []*ast.TypeRef) (ast.Expr, error) {
	p.advance()
	// 首个实参的“名称加冒号”决定整个列表是否使用命名形式。
	parseArgument := p.parsePositionalArgument
	if p.at(lexer.Identifier) && p.peek(1).Kind == lexer.Colon {
		parseArgument = p.parseNamedArgument
	}
	args, end, err := commaList(p, lexer.RParen, parseArgument)
	if err != nil {
		return nil, err
	}
	return &ast.CallExpr{
		NodeSpan: sourceSpan(callee.Span().Start, end.Span.End),
		Callee:   callee,
		TypeArgs: typeArgs,
		Args:     args,
	}, nil
}

// parsePositionalArgument 将一个表达式包装为无名称的实参。
func (p *parser) parsePositionalArgument() (ast.Argument, error) {
	value, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return ast.Argument{}, err
	}
	return ast.Argument{NodeSpan: ast.NodeSpan{Source: value.Span()}, Value: value}, nil
}

// parseNamedArgument 读取名称、冒号和实参表达式。
func (p *parser) parseNamedArgument() (ast.Argument, error) {
	name, err := p.parseIdentifier()
	if err != nil {
		return ast.Argument{}, err
	}
	// 每个命名实参都必须包含自己的冒号，由这里拒绝混入的位置实参。
	if _, err := p.expect(lexer.Colon); err != nil {
		return ast.Argument{}, err
	}
	value, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return ast.Argument{}, err
	}
	return ast.Argument{
		NodeSpan: sourceSpan(name.Span().Start, value.Span().End),
		Name:     &name,
		Value:    value,
	}, nil
}

// parseIndex 读取方括号内的索引表达式，并与已有目标合成索引节点。
func (p *parser) parseIndex(target ast.Expr) (ast.Expr, error) {
	p.advance()
	// 索引内部可以是任意优先级的表达式，必须由右方括号结束。
	index, err := p.parseExpression(lowestPrecedence)
	if err != nil {
		return nil, err
	}
	end, err := p.expect(lexer.RBracket)
	if err != nil {
		return nil, err
	}
	return &ast.IndexExpr{
		NodeSpan: sourceSpan(target.Span().Start, end.Span.End),
		Target:   target,
		Index:    index,
	}, nil
}

// 两种列表都由调用方提供终止记号和单项读取函数。
// lineList 管理换行边界，commaList 管理逗号、尾逗号和括号内的换行。

// lineList 解析程序、代码块或结构体中按换行分隔的项目。
func lineList[T any](p *parser, end lexer.Kind, element func() (T, error)) ([]T, lexer.Token, error) {
	var items []T
	for p.skipNewlines(); !p.at(end) && !p.at(lexer.EOF); p.skipNewlines() {
		item, err := element()
		if err != nil {
			return nil, lexer.Token{}, err
		}
		items = append(items, item)
		// 非末项之后必须有换行。提前遇到 EOF 时，由末尾的 expect 报出缺失终止符。
		if !p.at(lexer.Newline) && !p.at(end) && !p.at(lexer.EOF) {
			return nil, lexer.Token{}, p.unexpected(fmt.Sprintf("newline or %s", end))
		}
	}
	close, err := p.expect(end)
	return items, close, err
}

// commaList 解析圆括号或尖括号中以逗号分隔的项目，两者均允许尾逗号。
func commaList[T any](p *parser, end lexer.Kind, element func() (T, error)) ([]T, lexer.Token, error) {
	// 圆括号的换行已预处理，并允许直接结束。尖括号仍需跳过换行并读取首项。
	angleList := end == lexer.Greater
	if angleList {
		p.skipNewlines()
	} else if close, ok := p.match(end); ok {
		return nil, close, nil
	}
	var items []T
	for {
		item, err := element()
		if err != nil {
			return nil, lexer.Token{}, err
		}
		items = append(items, item)
		if angleList {
			p.skipNewlines()
		}
		// 有逗号时可继续读取下一项，也可直接闭合形成尾逗号。
		if _, ok := p.match(lexer.Comma); !ok {
			close, err := p.expect(end)
			return items, close, err
		}
		if angleList {
			p.skipNewlines()
		}
		if close, ok := p.match(end); ok {
			return items, close, nil
		}
	}
}

// 这些函数把词法层的记号和位置转换为语法树使用的节点数据。
// parseIdentifier 负责消费记号，identifier 也可转换已经读过的记号。

// parseIdentifier 要求下一个记号为标识符，并返回带源码区间的名称。
func (p *parser) parseIdentifier() (ast.Identifier, error) {
	token, err := p.expect(lexer.Identifier)
	if err != nil {
		return ast.Identifier{}, err
	}
	return identifier(token), nil
}

// identifier 用已识别的标识符记号构造名称节点。
func identifier(token lexer.Token) ast.Identifier {
	return ast.Identifier{NodeSpan: ast.NodeSpan{Source: token.Span}, Text: token.Lexeme}
}

// sourceSpan 将起止位置包装为可嵌入语法节点的源码区间。
func sourceSpan(start, end lexer.Position) ast.NodeSpan {
	return ast.NodeSpan{Source: lexer.Span{Start: start, End: end}}
}

// 记号游标函数提供查看、前进和按类别消费的操作。
// match 用于可选记号，expect 复用 match 并在必需记号缺失时生成诊断。

// current 返回当前记号，游标保持原位。
func (p *parser) current() lexer.Token {
	return p.tokens[p.index]
}

// peek 向前查看指定距离的记号，到达末尾后返回 EOF。
func (p *parser) peek(distance int) lexer.Token {
	if p.index+distance >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.index+distance]
}

// at 判断当前记号是否属于指定类别。
func (p *parser) at(kind lexer.Kind) bool {
	return p.current().Kind == kind
}

// advance 返回当前记号并向前一步，遇到 EOF 时停在末尾。
func (p *parser) advance() lexer.Token {
	token := p.current()
	if token.Kind != lexer.EOF {
		p.index++
	}
	return token
}

// match 仅在记号类别匹配时消费并返回当前记号。
func (p *parser) match(kind lexer.Kind) (lexer.Token, bool) {
	if p.at(kind) {
		return p.advance(), true
	}
	return lexer.Token{}, false
}

// expect 消费一个必需的记号，类别不符时返回带位置的语法错误。
func (p *parser) expect(kind lexer.Kind) (lexer.Token, error) {
	if token, ok := p.match(kind); ok {
		return token, nil
	}
	return lexer.Token{}, p.unexpected(string(kind))
}

// skipNewlines 跳过连续的换行记号，由调用处决定何时允许换行。
func (p *parser) skipNewlines() {
	for p.at(lexer.Newline) {
		p.advance()
	}
}

// unexpected 根据期望内容和当前记号构造语法诊断。
func (p *parser) unexpected(expected string) *Error {
	token := p.current()
	found := string(token.Kind)
	// 普通记号显示它的原始拼写，换行和 EOF 使用类别名以便阅读。
	if token.Kind != lexer.Newline && token.Kind != lexer.EOF {
		found = fmt.Sprintf("%q", token.Lexeme)
	}
	return &Error{
		Span:    token.Span,
		Message: fmt.Sprintf("expected %s, found %s", expected, found),
	}
}
