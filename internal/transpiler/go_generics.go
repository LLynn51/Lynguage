package transpiler

import "lynguage/internal/ast"

// Go 会在编译时展开泛型实参。递归地将 T 替换为 Slice<T> 等类型时，
// 静态实例化无法终止。形参图标记这种循环，生成器此时改用运行时零值参数，
// 函数和控制流仍然直接编译为 Go。
type typeGraph struct {
	parameters map[ast.Item][]int
	edges      []map[typeEdge]struct{}
}

type typeEdge struct {
	to    int
	grows bool
}

// register 为声明中的每个形参分配图节点，顺序与 Go 类型实参一致。
func (graph *typeGraph) register(node ast.Item, count int) {
	for range count {
		graph.parameters[node] = append(graph.parameters[node], len(graph.edges))
		graph.edges = append(graph.edges, nil)
	}
}

// link 记录从实参中的源形参到目标形参的依赖，合并重复的边。
func (graph *typeGraph) link(from, to int, grows bool) {
	if graph.edges[from] == nil {
		graph.edges[from] = make(map[typeEdge]struct{})
	}
	graph.edges[from][typeEdge{to: to, grows: grows}] = struct{}{}
}

// hasGrowingCycle 检查带有类型嵌套的依赖边是否处于一个循环内。
func (graph *typeGraph) hasGrowingCycle() bool {
	// 平行传递形参的循环可以用同一个 Go 实例。增长边能回到起点时才会无限展开。
	for from, edges := range graph.edges {
		for edge := range edges {
			if edge.grows && graph.reaches(edge.to, from) {
				return true
			}
		}
	}
	return false
}

// reaches 判断从一个形参节点出发能否到达另一个形参节点。
func (graph *typeGraph) reaches(from, target int) bool {
	pending := []int{from}
	seen := make([]bool, len(graph.edges))
	seen[from] = true
	// 迭代遍历，遇到重复节点时跳过，避免依赖链消耗 Go 调用栈。
	for len(pending) != 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		if current == target {
			return true
		}
		for edge := range graph.edges[current] {
			if !seen[edge.to] {
				seen[edge.to] = true
				pending = append(pending, edge.to)
			}
		}
	}
	return false
}

// 生成原生类型时顺便记录形参依赖。结构体引用、函数调用和方法调用
// 共用 recordInstantiation，Go 接收者类型的隐式关联则由 recordReceiver 补全。

// recordInstantiation 根据已解析的实参类型登记一次实例化的依赖。
func (g *generator) recordInstantiation(target ast.Item, types []*ast.TypeRef) {
	if g.erased || len(g.typeIDs) == 0 {
		return
	}
	for i, param := range g.graph.parameters[target] {
		g.recordReferences(param, types[i], false)
	}
}

// recordReferences 递归查找类型引用中的形参，并区分直接传递与嵌套传递。
func (g *generator) recordReferences(target int, typ *ast.TypeRef, nested bool) {
	if source, ok := g.typeIDs[typ.Name.Text]; ok && !typ.GlobalName {
		g.graph.link(source, target, nested)
		return
	}
	// 进入容器或结构体的类型实参时，形参每经过本条边都会外包一层类型。
	for _, arg := range typ.Args {
		g.recordReferences(target, arg, true)
	}
}

// recordReceiver 关联方法形参和接收者结构体形参，模拟 Go 方法随类型实例化。
func (g *generator) recordReceiver(decl *ast.FuncDecl) {
	if g.erased {
		return
	}
	structure := g.structures[decl.Receiver.Binding.Type.Name.Text]
	for i, slot := range g.graph.parameters[structure] {
		param := g.graph.parameters[decl][i]
		g.graph.link(param, slot, false)
		// Go 原生方法和结构体共享实例，带独立形参的方法则是独立的 Go 函数。
		if len(decl.TypeParams) == 0 {
			g.graph.link(slot, param, false)
		}
	}
}

// 类型擦除后的泛型形参用值参数传播。调用端计算类型零值，函数可据此
// 初始化切片或转发形参，而结构体、切片与 Result 仍沿用相同的 Go 宿主类型。

// zeroArguments 将调用的类型实参转换为按形参顺序传递的零值参数。
func (g *generator) zeroArguments(types []*ast.TypeRef) []string {
	values := make([]string, len(types))
	for i, typ := range types {
		values[i] = g.zeroValue(typ)
	}
	return values
}

// zeroValue 生成擦除类型的零值，用当前形参的传入值保留具体零值语义。
func (g *generator) zeroValue(typ *ast.TypeRef) string {
	if param := g.typeParams[typ.Name.Text]; !typ.GlobalName && param != "" {
		return param
	}
	// 擦除后的 Result 载荷是 any，需要显式恢复失败载荷的具体类型。
	if typ.Name.Text == "Result" {
		return "lynErr[any, any](" + g.zeroValue(typ.Args[1]) + ")"
	}
	return "lynZero[" + g.goType(typ) + "]()"
}
