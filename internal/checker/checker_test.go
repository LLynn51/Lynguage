package checker_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"lynguage/internal/ast"
	"lynguage/internal/checker"
	"lynguage/internal/parser"
)

func parse(t *testing.T, source string) *ast.Program {
	t.Helper()
	program, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	return program
}

func typeName(ref *ast.TypeRef) string {
	if ref == nil {
		return "<nil>"
	}
	if len(ref.Args) == 0 {
		return ref.Name.Text
	}
	args := make([]string, len(ref.Args))
	for i, arg := range ref.Args {
		args[i] = typeName(arg)
	}
	return ref.Name.Text + "<" + strings.Join(args, ", ") + ">"
}

func checkBindings(t *testing.T, source string, want map[string]string) {
	t.Helper()
	program := parse(t, source)
	if err := checker.Check(program); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	bindings := make(map[string]*ast.BindingStmt)
	var visit func(ast.Item)
	visitBlock := func(block *ast.Block) {
		for _, stmt := range block.Statements {
			visit(stmt)
		}
	}
	visit = func(item ast.Item) {
		switch node := item.(type) {
		case *ast.BindingStmt:
			bindings[node.Name.Text] = node
		case *ast.FuncDecl:
			visitBlock(node.Body)
		case *ast.IfStmt:
			for _, branch := range node.Branches {
				visitBlock(branch.Body)
			}
			if node.Else != nil {
				visitBlock(node.Else)
			}
		case *ast.WhileStmt:
			visitBlock(node.Body)
		}
	}
	for _, item := range program.Items {
		visit(item)
	}
	for name, expected := range want {
		binding := bindings[name]
		if binding == nil {
			t.Errorf("binding %q missing from AST", name)
			continue
		}
		if got := typeName(binding.Type); got != expected {
			t.Errorf("%s.Type = %s, want %s", name, got, expected)
		}
	}
	if err := checker.Check(program); err != nil {
		t.Fatalf("Check() on annotated AST = %v", err)
	}
}

func TestCheckInferenceAndScopes(t *testing.T) {
	checkBindings(t, `let top = 3
let called = forward(top)
def forward(value: int64) -> int {
    let local = value
    if true {
        const local = "text"
        let branchRead = local
    } elif false {
        let alternative = false
    } else {
        const fallback = 1.5
    }
    while false {
        let local = local + 1
        let loopRead = local
        top = loopRead
    }
    const functionRead = local
    return later(functionRead + top)
}
def later(value: int) -> int { return value }
`, map[string]string{
		"top": "int", "called": "int", "branchRead": "str", "alternative": "bool",
		"fallback": "float", "loopRead": "int", "functionRead": "int",
	})
}

func TestCheckGenericInstantiation(t *testing.T) {
	checkBindings(t, `let first: int32 = -2147483648
let box = Box(values: make_slice(1, 1), item: first)
let fromIndex = box.values[0]
const fromMethod = box.get()
box.set(2)
let fromResult = box.wrap("fallback")
let explicit = box.wrap<str>("fallback")
let chosen = choose(9, first)
let reversed = choose(first, 9)
let choices = append(make_slice(1, 1), box)
let again = choices[0].get()
let number = 1 + first
let floating = 1.5 + float32(2.5)
let contextual: Slice<int64> = make_slice(1, 2)
let inferredFromAlias = contextual[0]

def (const self: Box<U>) get() -> U { return self.item }
def (self: Box<V>) set(value: V) -> void { self.item = value }
def (const self: Box<U>) wrap<E>(fallback: E) -> Result<U, E> { return Ok(self.item) }
def choose<T>(first: T, second: T) -> T {
    const chosenInside = first
    return chosenInside
}
struct Box<T> {
    item: T
    values: Slice<T>
}
`, map[string]string{
		"first": "int32", "box": "Box<int32>", "fromIndex": "int32", "fromMethod": "int32",
		"fromResult": "Result<int32, str>", "explicit": "Result<int32, str>", "chosen": "int32",
		"reversed": "int32", "choices": "Slice<Box<int32>>", "again": "int32", "number": "int32",
		"floating": "float32", "contextual": "Slice<int64>", "inferredFromAlias": "int", "chosenInside": "T",
	})
}

func TestCheckTryUnwrapsSuccessAndPreservesErrorType(t *testing.T) {
	checkBindings(t, `def copy(path: str) -> Result<int, str> {
    let text = read_file(path)?
    write_file(path, text)?
    const values = fallback()?
    let item = values[0]
    const contextual: int32 = Ok(9)?
    return Ok(len(values))
}
def fallback() -> Result<Slice<int32>, str> { return Ok(make_slice(1, 1)) }
def counter() -> Result<int, int> { return Ok(1) }
def fromAlias() -> Result<bool, int64> {
    let count = counter()?
    return Ok(count > 0)
}
def relay<E>(source: Result<int, E>) -> Result<bool, E> {
    let relayed = source?
    return Ok(relayed > 0)
}
def fail<T>() -> Result<T, str> { return Err("failed") }
def empty() -> Result<void, str> { return Ok() }
const failure = fail<void>()
let propagated = relay(counter())
`, map[string]string{
		"text": "str", "values": "Slice<int32>", "item": "int32", "contextual": "int32",
		"count": "int", "relayed": "int", "failure": "Result<void, str>", "propagated": "Result<bool, int>",
	})
}

func TestInferredTypesDistinguishGlobalNamesFromTypeParameters(t *testing.T) {
	program := parse(t, `def f<int>(value: int) -> void {
    let builtin = 1
    const parameter = value
}
f(true)
`)
	if err := checker.Check(program); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	body := program.Items[0].(*ast.FuncDecl).Body
	for i, wantGlobal := range []bool{true, false} {
		ref := body.Statements[i].(*ast.BindingStmt).Type
		if ref == nil || ref.Name.Text != "int" || ref.GlobalName != wantGlobal {
			t.Errorf("binding %d inferred type = %+v, want int with GlobalName=%t", i, ref, wantGlobal)
		}
	}
	if err := checker.Check(program); err != nil {
		t.Fatalf("Check() on annotated AST = %v", err)
	}
}

func TestCheckConstAllowsAliasNestedAndIndexedMutation(t *testing.T) {
	checkBindings(t, `struct Entry {
    value: int
}
struct Holder {
    entry: Entry
}
def (self: Entry) set(value: int) -> void { self.value = value }
def (const self: Entry) read() -> int { return self.value }
const holder = Holder(entry: Entry(value: 1))
holder.entry.value = 2
holder.entry.set(3)
const view = holder.entry
let alias = view
alias.value = 4
const items = make_slice<Entry>(1, 1)
items[0] = alias
items[0].value = 5
const readBack = (view).read()
if true {
    let view = alias
    view.set(6)
}
`, map[string]string{"holder": "Holder", "alias": "Entry", "items": "Slice<Entry>", "readBack": "int"})
}

type invalidProgram struct {
	name, source, message string
	line, column          int
}

func checkErrors(t *testing.T, cases []invalidProgram) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checker.Check(parse(t, tc.source))
			var diagnostic *checker.Error
			if !errors.As(err, &diagnostic) {
				t.Fatalf("Check() = %v, want *checker.Error", err)
			}
			pos := diagnostic.Span.Start
			if pos.Line != tc.line || tc.column != 0 && pos.Column != tc.column {
				t.Errorf("Check() = %v, want line %d, column %d (0 means any)", err, tc.line, tc.column)
			}
			if !strings.Contains(diagnostic.Message, tc.message) || !strings.HasPrefix(err.Error(), fmt.Sprintf("%d:%d: ", pos.Line, pos.Column)) {
				t.Errorf("Check() = %v, want position and message containing %q", err, tc.message)
			}
		})
	}
}

func TestCheckRejectsConstMutation(t *testing.T) {
	const entry = "struct Entry { value: int }\n"
	const atTop = entry + "const x = Entry(value: 1)\n"
	const inReceiver = entry + "def (const self: Entry) update() -> void {\n"
	checkErrors(t, []invalidProgram{
		{name: "binding", source: "const x = 1\nx = 2", message: `cannot assign to const binding "x"`, line: 2, column: 1},
		{name: "field", source: atTop + "(x).value = 2", message: `field "value" of const binding "x"`, line: 3},
		{name: "receiver", source: inReceiver + "    self = Entry(value: 2)\n}", message: `cannot assign to const binding "self"`, line: 3},
		{name: "receiver field", source: inReceiver + "    self.value = 2\n}", message: `field "value" of const binding "self"`, line: 3},
		{name: "receiver calling mutable method", source: inReceiver + "    (self).set(2)\n}\ndef (s: Entry) set(n: int) -> void { s.value = n }", message: `mutable method "set" on const binding "self"`, line: 3},
		{name: "const binding calling mutable method", source: atTop + "x.set(2)\ndef (s: Entry) set(n: int) -> void { s.value = n }", message: `mutable method "set" on const binding "x"`, line: 3},
	})
}

func TestCheckRejectsInvalidTry(t *testing.T) {
	checkErrors(t, []invalidProgram{
		{name: "error type mismatch", source: `def check() -> Result<int, int> {
    let text = read_file("x")?
    return Ok(1)
}`, message: "? error type str does not match enclosing Result error type int", line: 2, column: 30},
		{name: "nested error type mismatch", source: `def check(source: Result<int, Slice<int>>) -> Result<int, Slice<int32>> {
    return Ok(source?)
}`, message: "? error type Slice<int> does not match enclosing Result error type Slice<int32>", line: 2},
		{name: "distinct generic error parameters", source: `def check<E, F>(source: Result<int, E>) -> Result<int, F> {
    return Ok(source?)
}`, message: "? error type E does not match enclosing Result error type F", line: 2},
		{name: "wrong success type", source: `def check() -> Result<int, str> {
    let value: int = read_file("x")?
    return Ok(value)
}`, message: "has type str, expected int", line: 2},
		{name: "operand", source: `def check() -> Result<int, str> { return Ok(true?) }`, message: "requires a Result operand", line: 1},
		{name: "number operand", source: `def check() -> Result<int, str> { return Ok(1?) }`, message: "requires a Result operand", line: 1},
		{name: "return type", source: `def check() -> str { return read_file("x")? }`, message: "returning Result", line: 1},
		{name: "top level", source: `let text = read_file("x")?`, message: "requires an enclosing function or method", line: 1},
		{name: "void success as value", source: `def check() -> Result<void, str> {
    let value = write_file("x", "text")?
    return Ok()
}`, message: "void cannot be used as a value", line: 2},
	})
}

func TestCheckRejectsInvalidGenericUses(t *testing.T) {
	const box = "struct Box<T> { item: T }\n"
	const method = box + "def (self: Box<U>) set(value: U) -> void { self.item = value }\n"
	checkErrors(t, []invalidProgram{
		{name: "Slice arity", source: "let v: Slice<int, int> = make_slice<int>(1, 1)", message: "Slice expects 1 type arguments", line: 1},
		{name: "Result arity", source: "def f() -> Result<int> { return Ok(1) }", message: "Result expects 2 type arguments", line: 1},
		{name: "struct arity", source: box + "let v = Box<int, str>(item: 1)", message: "Box expects 1 type arguments", line: 2},
		{name: "unknown argument type", source: box + "let v = Box<Missing>(item: 1)", message: `unknown type "Missing"`, line: 2},
		{name: "wrong field", source: box + `let v = Box<int>(item: "x")`, message: "has type str, expected int", line: 2},
		{name: "unnamed field", source: box + "let v = Box(1)", message: "requires named arguments", line: 2},
		{name: "missing field", source: box + "let v = Box<int>()", message: "requires 1 field initializers", line: 2},
		{name: "duplicate field", source: "struct Pair<T> { left: T\nright: T }\nlet v = Pair(left: 1, left: 2)", message: `duplicate initializer for field "left"`, line: 3},
		{name: "wrong method argument", source: method + "let box = Box(item: 1)\nbox.set(true)", message: "has type bool, expected int", line: 4},
		{name: "extra method type argument", source: method + "let box = Box(item: 1)\nbox.set<int>(1)", message: "set expects 0 type arguments", line: 4},
		{name: "receiver argument is not a parameter", source: box + "def (b: Box<Slice<int>>) get() -> void {}", message: "receiver type parameter must be a bare identifier", line: 2},
		{name: "receiver argument count", source: box + "def (b: Box) get() -> void {}", message: "receiver expects 1 type parameters", line: 2},
		{name: "receiver parameter collision", source: box + "def (b: Box<T>) get<T>() -> void {}", message: `duplicate type parameter "T"`, line: 2},
		{name: "member collision", source: box + "def (b: Box<T>) item() -> T { return b.item }", message: `duplicate member "item"`, line: 2},
		{name: "missing type context", source: "let v = make_slice(1, 1)", message: "cannot infer type argument T", line: 1},
		{name: "conflicting inference", source: "def f<T>(a: T, b: T) -> T { return a }\nlet v = f(true, \"x\")", message: "has type str, expected bool", line: 2},
		{name: "void Slice element", source: "let v = make_slice<void>(1, 1)", message: "void cannot be used", line: 1},
		{name: "void error type", source: "def f() -> Result<int, void> { return Ok(1) }", message: "void cannot be used", line: 1},
		{name: "void use in generic body", source: `f<void>()
def f<T>() -> void { g<T>() }
def g<T>() -> void { let stored = Err<int, T>(generate<T>()) }
def generate<T>() -> T { while true {} }`, message: "void cannot be used for type parameter T", line: 1},
	})
}

func TestCheckRejectsInvalidScopesAndTypes(t *testing.T) {
	checkErrors(t, []invalidProgram{
		{name: "scope ends at block", source: "if true { let local = 1 }\nprintln(local)", message: `undefined name "local"`, line: 2},
		{name: "sibling branch scope", source: "if true { let local = 1 }\nelse { println(local) }", message: `undefined name "local"`, line: 2},
		{name: "initializer before name", source: "let x = x", message: `undefined name "x"`, line: 1},
		{name: "function cannot see later binding", source: "def f() -> int { return top }\nlet top = 1", message: `undefined name "top"`, line: 1},
		{name: "duplicate local", source: "def f(x: int) -> void { let x = 1 }", message: `"x" is already declared in this scope`, line: 1},
		{name: "shadowing applies to calls", source: "def f() -> void {\n  let len = true\n  len()\n}", message: "is not callable", line: 3},
		{name: "top name reserved", source: "let read_file = 1", message: `"read_file" is already declared at top level`, line: 1},
		{name: "assignment type", source: "let x = 1\nx = true", message: "assignment value has type bool, expected int", line: 2},
		{name: "invalid index", source: "let v = make_slice<int>(1, 1)\nlet x = v[true]", message: "slice index has type bool, expected int", line: 2},
		{name: "invalid condition", source: `while "x" {}`, message: "has type str, expected bool", line: 1},
		{name: "invalid operator", source: `let value = +"x"`, message: `operator "+" does not accept str`, line: 1},
		{name: "int32 range", source: "let x: int32 = 2147483648", message: "out of range for int32", line: 1},
		{name: "no implicit conversion", source: "let x: int32 = 1\nlet y: int = x", message: "has type int32, expected int", line: 2},
		{name: "missing return path", source: "def f(condition: bool) -> int { if condition { return 1 } }", message: "must return int on every path", line: 1},
	})
}

func TestCheckLeavesASTUnannotatedOnError(t *testing.T) {
	program := parse(t, "let value = 1\nvalue = true")
	if err := checker.Check(program); err == nil {
		t.Fatal("Check() succeeded for an invalid assignment")
	}
	if got := program.Items[0].(*ast.BindingStmt).Type; got != nil {
		t.Errorf("binding was annotated despite a failed check: %s", typeName(got))
	}
}

func TestCheckBubbleSortExample(t *testing.T) {
	source, err := os.ReadFile("../../app/example/bubble_sort.lyn")
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Check(parse(t, string(source))); err != nil {
		t.Fatalf("Check(bubble_sort.lyn) = %v", err)
	}
}
