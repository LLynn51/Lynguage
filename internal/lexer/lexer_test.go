package lexer_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"lynguage/internal/lexer"
)

// 正常输入用例分别检查记号分类、原始拼写、字符串解码和源码坐标。
// 共用的 scan 和 assertLexemes 直接验证 Lex 返回的记号序列。

// TestLexemeKinds 检查关键字、标识符、数字和符号的分类边界。
func TestLexemeKinds(t *testing.T) {
	t.Run("关键字和标识符", func(t *testing.T) {
		// 关键字必须完整匹配，Unicode 数字可出现在标识符的后续位置。
		assertLexemes(t, "def let const struct if elif else while return true false and or not define Def _名٢ Result Slice",
			[]lexeme{
				{lexer.Def, "def"}, {lexer.Let, "let"}, {lexer.Const, "const"}, {lexer.Struct, "struct"},
				{lexer.If, "if"}, {lexer.Elif, "elif"}, {lexer.Else, "else"}, {lexer.While, "while"},
				{lexer.Return, "return"}, {lexer.True, "true"}, {lexer.False, "false"},
				{lexer.And, "and"}, {lexer.Or, "or"}, {lexer.Not, "not"},
				{lexer.Identifier, "define"}, {lexer.Identifier, "Def"}, {lexer.Identifier, "_名٢"},
				{lexer.Identifier, "Result"}, {lexer.Identifier, "Slice"},
			})
	})
	t.Run("符号最长匹配", func(t *testing.T) {
		// 相邻的两个 > 仍是两个记号，复合比较符和箭头各占一个记号。
		assertLexemes(t, "()[]{} ,:.? +-*/% = == != < <= >> >= ->",
			[]lexeme{
				{lexer.LParen, "("}, {lexer.RParen, ")"}, {lexer.LBracket, "["}, {lexer.RBracket, "]"},
				{lexer.LBrace, "{"}, {lexer.RBrace, "}"}, {lexer.Comma, ","}, {lexer.Colon, ":"},
				{lexer.Dot, "."}, {lexer.Question, "?"}, {lexer.Plus, "+"}, {lexer.Minus, "-"},
				{lexer.Star, "*"}, {lexer.Slash, "/"}, {lexer.Percent, "%"}, {lexer.Assign, "="},
				{lexer.Equal, "=="}, {lexer.NotEqual, "!="}, {lexer.Less, "<"}, {lexer.LessEqual, "<="},
				{lexer.Greater, ">"}, {lexer.Greater, ">"}, {lexer.GreaterEqual, ">="}, {lexer.Arrow, "->"},
			})
	})
	t.Run("数字与相邻符号", func(t *testing.T) {
		// 正负号独立成记号。小数点只有在后面接 ASCII 数字时才属于数字。
		assertLexemes(t, "-12 +0 0.25 7e2 4.5E-3 6e+4 99.field 2. .5",
			[]lexeme{
				{lexer.Minus, "-"}, {lexer.Integer, "12"}, {lexer.Plus, "+"}, {lexer.Integer, "0"},
				{lexer.Float, "0.25"}, {lexer.Float, "7e2"}, {lexer.Float, "4.5E-3"}, {lexer.Float, "6e+4"},
				{lexer.Integer, "99"}, {lexer.Dot, "."}, {lexer.Identifier, "field"},
				{lexer.Integer, "2"}, {lexer.Dot, "."}, {lexer.Dot, "."}, {lexer.Integer, "5"},
			})
	})
	t.Run("空文件和注释", func(t *testing.T) {
		assertLexemes(t, "", nil)
		assertLexemes(t, " \t# 行尾注释", nil)
	})
}

// TestTokenPositions 检查字节偏移、码点列号和三种换行的完整记号序列。
func TestTokenPositions(t *testing.T) {
	source := "let 名٢=1 # 注释\r\n\t(x[\r2])\n"
	want := []struct {
		kind       lexer.Kind
		raw        string
		start, end lexer.Position
	}{
		{lexer.Let, "let", position(0, 1, 1), position(3, 1, 4)},
		{lexer.Identifier, "名٢", position(4, 1, 5), position(9, 1, 7)},
		{lexer.Assign, "=", position(9, 1, 7), position(10, 1, 8)},
		{lexer.Integer, "1", position(10, 1, 8), position(11, 1, 9)},
		{lexer.Newline, "\r\n", position(20, 1, 14), position(22, 2, 1)},
		{lexer.LParen, "(", position(23, 2, 2), position(24, 2, 3)},
		{lexer.Identifier, "x", position(24, 2, 3), position(25, 2, 4)},
		{lexer.LBracket, "[", position(25, 2, 4), position(26, 2, 5)},
		{lexer.Newline, "\r", position(26, 2, 5), position(27, 3, 1)},
		{lexer.Integer, "2", position(27, 3, 1), position(28, 3, 2)},
		{lexer.RBracket, "]", position(28, 3, 2), position(29, 3, 3)},
		{lexer.RParen, ")", position(29, 3, 3), position(30, 3, 4)},
		{lexer.Newline, "\n", position(30, 3, 4), position(31, 4, 1)},
		{lexer.EOF, "", position(31, 4, 1), position(31, 4, 1)},
	}
	tokens := scan(t, source)
	if len(tokens) != len(want) {
		t.Fatalf("得到 %d 个记号，期望 %d 个", len(tokens), len(want))
	}
	// 注释与制表符影响位置。即使在圆括号和方括号内，词法层也保留换行。
	for i, tc := range want {
		expected := lexer.Token{
			Kind:   tc.kind,
			Lexeme: tc.raw,
			Span:   lexer.Span{Start: tc.start, End: tc.end},
		}
		if got := tokens[i]; got != expected {
			t.Errorf("第 %d 个记号为 %+v，期望 %+v", i+1, got, expected)
		}
	}
}

// TestStringLexemes 检查双引号、转义原文和解码值。
func TestStringLexemes(t *testing.T) {
	const raw = `"中文�#\"\\\n\r\t"`
	tokens := scan(t, "\"\" "+raw+" # 字符串后的注释")
	if len(tokens) != 3 {
		t.Fatalf("期望两个字符串和 EOF，得到 %+v", tokens)
	}
	// 字符串内的 # 是文本，合法编码的 U+FFFD 也应进入解码结果。
	for i, tc := range []struct{ raw, value string }{
		{`""`, ""},
		{raw, "中文�#\"\\\n\r\t"},
	} {
		token := tokens[i]
		if token.Kind != lexer.String || token.Lexeme != tc.raw || token.StringValue != tc.value {
			t.Errorf("第 %d 个字符串为 (%s, %q, %q)，期望 (%s, %q, %q)",
				i+1, token.Kind, token.Lexeme, token.StringValue, lexer.String, tc.raw, tc.value)
		}
	}
	if tokens[2].Kind != lexer.EOF {
		t.Errorf("字符串和注释之后应为 EOF，实际为 %+v", tokens[2])
	}
}

// TestLexErrors 检查无效字符、字面量和 UTF-8 的错误类型与范围。
func TestLexErrors(t *testing.T) {
	cases := []struct {
		name, source string
		start        lexer.Position
		raw, detail  string
	}{
		{"非法分号", "x;", position(1, 1, 2), ";", "unexpected character"},
		{"单独的感叹号", "!", position(0, 1, 1), "!", "unexpected character"},
		{"换行后缺少指数数字", "?\r\n1e+", position(4, 2, 2), "e+", "expected digits"},
		{"指数标记后直接接字母", "1evalue", position(1, 1, 2), "e", "expected digits"},
		{"未知转义", `"前\q"`, position(4, 1, 3), `\q`, "unknown string escape"},
		{"未闭合字符串", "\"前", position(0, 1, 1), "\"前", "unterminated string"},
		{"字符串内的物理换行", "\"前\n\"", position(0, 1, 1), "\"前", "unterminated string"},
		{"反斜杠后遇到文件末尾", "\"\\", position(0, 1, 1), "\"\\", "unterminated string"},
		{"反斜杠后遇到换行", "\"\\\r\n", position(0, 1, 1), "\"\\", "unterminated string"},
		{"未转义的控制字符", "\"\x7f\"", position(1, 1, 2), "\x7f", "unescaped control character"},
		{"普通源码中的非法 UTF-8", "名\xff", position(3, 1, 2), "\xff", "invalid UTF-8"},
		{"注释中的非法 UTF-8", "# 注\xff", position(5, 1, 4), "\xff", "invalid UTF-8"},
		{"字符串中的非法 UTF-8", "\"前\xff\"", position(4, 1, 3), "\xff", "invalid UTF-8"},
		{"反斜杠后的非法 UTF-8", "\"\\\xff\"", position(2, 1, 3), "\xff", "invalid UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 错误应能由调用方辨别，且扫描失败时不交付已读取的前缀记号。
			tokens, err := lexer.Lex(tc.source)
			var diagnostic *lexer.Error
			if !errors.As(err, &diagnostic) {
				t.Fatalf("期望词法错误，得到 %T (%v)", err, err)
			}
			if len(tokens) != 0 {
				t.Errorf("扫描失败时得到部分记号 %+v", tokens)
			}
			end := position(tc.start.Offset+len(tc.raw), tc.start.Line, tc.start.Column+utf8.RuneCountInString(tc.raw))
			expected := lexer.Span{Start: tc.start, End: end}
			if diagnostic.Span != expected {
				t.Errorf("错误区间为 %+v，期望 %+v", diagnostic.Span, expected)
			}
			if !strings.Contains(diagnostic.Message, tc.detail) {
				t.Errorf("诊断为 %q，期望包含 %q", diagnostic.Message, tc.detail)
			}
			formatted := fmt.Sprintf("%d:%d: %s", tc.start.Line, tc.start.Column, diagnostic.Message)
			if err.Error() != formatted {
				t.Errorf("格式化诊断为 %q，期望 %q", err.Error(), formatted)
			}
		})
	}
}

// 分类断言通过 scan 获取记号，再比较类别、原文和 EOF。
// 位置用例用 position 构造坐标，显式区分字节偏移、行号和列号。

type lexeme struct {
	kind lexer.Kind
	raw  string
}

// scan 读取成功用例的全部记号，在意外失败时报告对应源码。
func scan(t *testing.T, source string) []lexer.Token {
	t.Helper()
	tokens, err := lexer.Lex(source)
	if err != nil {
		t.Fatalf("扫描 %q 失败，得到 %v", source, err)
	}
	return tokens
}

// assertLexemes 比较记号类别与原文，并检查末尾的 EOF。
func assertLexemes(t *testing.T, source string, want []lexeme) {
	t.Helper()
	tokens := scan(t, source)
	// 除了用例列出的记号，扫描结果只应额外包含一个 EOF。
	if len(tokens) != len(want)+1 {
		t.Fatalf("扫描 %q 得到 %d 个记号，期望 %d 个（含 EOF）", source, len(tokens), len(want)+1)
	}
	for i, expected := range want {
		if got := tokens[i]; got.Kind != expected.kind || got.Lexeme != expected.raw {
			t.Errorf("第 %d 个记号为 (%s, %q)，期望 (%s, %q)",
				i+1, got.Kind, got.Lexeme, expected.kind, expected.raw)
		}
	}
	eof := tokens[len(want)]
	if eof.Kind != lexer.EOF || eof.Lexeme != "" || eof.Span.Start != eof.Span.End || eof.Span.End.Offset != len(source) {
		t.Errorf("EOF 应是源码末尾的空区间，实际为 %+v", eof)
	}
}

// position 构造带字节偏移的源码坐标。
func position(offset, line, column int) lexer.Position {
	return lexer.Position{Offset: offset, Line: line, Column: column}
}
