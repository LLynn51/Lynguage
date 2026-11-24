// lexer 包将 UTF-8 编码的 Lynguage 源码扫描为记号。
package lexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind 表示记号的类别。
type Kind string

const (
	EOF        Kind = "end of input"
	Newline    Kind = "newline"
	Identifier Kind = "identifier"
	Integer    Kind = "integer"
	Float      Kind = "float"
	String     Kind = "string"

	Def    Kind = "def"
	Let    Kind = "let"
	Const  Kind = "const"
	Struct Kind = "struct"
	If     Kind = "if"
	Elif   Kind = "elif"
	Else   Kind = "else"
	While  Kind = "while"
	Return Kind = "return"
	True   Kind = "true"
	False  Kind = "false"
	And    Kind = "and"
	Or     Kind = "or"
	Not    Kind = "not"

	LParen       Kind = "("
	RParen       Kind = ")"
	LBracket     Kind = "["
	RBracket     Kind = "]"
	LBrace       Kind = "{"
	RBrace       Kind = "}"
	Comma        Kind = ","
	Colon        Kind = ":"
	Dot          Kind = "."
	Question     Kind = "?"
	Plus         Kind = "+"
	Minus        Kind = "-"
	Star         Kind = "*"
	Slash        Kind = "/"
	Percent      Kind = "%"
	Assign       Kind = "="
	Equal        Kind = "=="
	NotEqual     Kind = "!="
	Less         Kind = "<"
	LessEqual    Kind = "<="
	Greater      Kind = ">"
	GreaterEqual Kind = ">="
	Arrow        Kind = "->"
)

var keywords = map[string]Kind{
	"def": Def, "let": Let, "const": Const, "struct": Struct,
	"if": If, "elif": Elif, "else": Else, "while": While, "return": Return,
	"true": True, "false": False, "and": And, "or": Or, "not": Not,
}

var symbols = map[string]Kind{
	"(": LParen, ")": RParen, "[": LBracket, "]": RBracket, "{": LBrace, "}": RBrace,
	",": Comma, ":": Colon, ".": Dot, "?": Question,
	"+": Plus, "-": Minus, "*": Star, "/": Slash, "%": Percent,
	"=": Assign, "==": Equal, "!=": NotEqual,
	"<": Less, "<=": LessEqual, ">": Greater, ">=": GreaterEqual, "->": Arrow,
}

// Position 的字节偏移从 0 开始，行号和列号从 1 开始，列号按 Unicode 码点计数。
type Position struct {
	Offset int
	Line   int
	Column int
}

// Span 表示左闭右开的源码区间。
type Span struct {
	Start Position
	End   Position
}

// Token 保留记号的原始拼写，字符串记号还保留解码后的值。
type Token struct {
	Kind        Kind
	Lexeme      string
	StringValue string
	Span        Span
}

// Error 记录词法错误的源码区间与说明。
type Error struct {
	Span    Span
	Message string
}

// Error 按行号、列号和错误说明的顺序格式化诊断。
func (e *Error) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Span.Start.Line, e.Span.Start.Column, e.Message)
}

type scanner struct {
	source string
	pos    Position
}

// Lex 驱动扫描循环，next 处理空白、注释和换行，再交给 scanToken
// 识别标识符、字面量或符号。各扫描函数共用同一个源码游标。

// Lex 返回全部记号，包括源码中的换行和末尾的 EOF 记号。
// 遇到词法错误时，返回扫描到的第一个错误。
func Lex(source string) ([]Token, error) {
	s := scanner{source: source, pos: Position{Line: 1, Column: 1}}
	var tokens []Token
	for {
		// 只有扫描到 EOF 才交付完整的记号序列。
		token, err := s.next()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
		if token.Kind == EOF {
			return tokens, nil
		}
	}
}

// next 跳过横向空白和行注释，交付下一个换行、普通记号或 EOF。
func (s *scanner) next() (Token, error) {
	for {
		start := s.pos
		switch s.byteAt(0) {
		case ' ', '\t':
			s.advanceASCII(1)
		case '#':
			// 保留注释末尾的换行，供语句分隔规则使用。
			if err := s.skipComment(); err != nil {
				return Token{}, err
			}
		case '\n', '\r':
			// CRLF 占两个字节，但只产生一个换行记号。
			s.pos.Offset++
			if s.source[start.Offset] == '\r' && s.byteAt(0) == '\n' {
				s.pos.Offset++
			}
			s.pos.Line++
			s.pos.Column = 1
			return s.token(Newline, start), nil
		default:
			if start.Offset == len(s.source) {
				return s.token(EOF, start), nil
			}
			return s.scanToken(start)
		}
	}
}

// scanToken 根据首字符选择扫描方式，并按最长匹配识别符号。
func (s *scanner) scanToken(start Position) (Token, error) {
	r, width := s.runeAt()
	// 宽度为 1 的替换字符表示编码错误，合法的 U+FFFD 占三个字节。
	switch {
	case r == utf8.RuneError && width == 1:
		return Token{}, s.invalidUTF8()
	case r == '_' || unicode.IsLetter(r):
		return s.scanIdentifier(start, width), nil
	case isDigit(s.byteAt(0)):
		return s.scanNumber(start)
	case r == '"':
		return s.scanString(start)
	}
	// 先尝试两个 ASCII 字节组成的符号，例如 >= 和 ->。
	if start.Offset+2 <= len(s.source) {
		if kind, ok := symbols[s.source[start.Offset:start.Offset+2]]; ok {
			s.advanceASCII(2)
			return s.token(kind, start), nil
		}
	}
	s.advanceRune(width)
	if kind, ok := symbols[s.source[start.Offset:s.pos.Offset]]; ok {
		return s.token(kind, start), nil
	}
	return Token{}, s.errorFrom(start, fmt.Sprintf("unexpected character %q", r))
}

// skipComment 扫描从 # 到行尾的注释，并检查其中的 UTF-8 编码。
func (s *scanner) skipComment() error {
	for s.pos.Offset < len(s.source) && s.byteAt(0) != '\n' && s.byteAt(0) != '\r' {
		r, width := s.runeAt()
		// 注释内容也属于源码，编码错误需要定位到对应字节。
		if r == utf8.RuneError && width == 1 {
			return s.invalidUTF8()
		}
		s.advanceRune(width)
	}
	return nil
}

// 标识符和数字都先读取完整拼写再确定记号类别。标识符用关键字表分类，
// 数字由 takeDigits 读取各个数字段，根据小数点和指数决定是否为浮点数。

// scanIdentifier 读取标识符，并在完整拼写匹配时将它归类为关键字。
func (s *scanner) scanIdentifier(start Position, firstWidth int) Token {
	s.advanceRune(firstWidth)
	// 首字符已由调用方判断，后续位置还允许 Unicode 十进制数字。
	for {
		r, width := s.runeAt()
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			break
		}
		s.advanceRune(width)
	}
	token := s.token(Identifier, start)
	// 完整拼写查表可使 define 等带关键字前缀的名称保留为标识符。
	if kind, ok := keywords[token.Lexeme]; ok {
		token.Kind = kind
	}
	return token
}

// scanNumber 读取十进制整数或浮点数，检查指数的拼写。
func (s *scanner) scanNumber(start Position) (Token, error) {
	s.takeDigits()
	kind := Integer
	// 小数点后必须紧接数字，其他点号留给下一次扫描。
	if s.byteAt(0) == '.' && isDigit(s.byteAt(1)) {
		kind = Float
		s.advanceASCII(1)
		s.takeDigits()
	}
	if s.byteAt(0) == 'e' || s.byteAt(0) == 'E' {
		// 读到指数标记后，允许一个符号，并要求至少一位指数数字。
		kind = Float
		exponent := s.pos
		s.advanceASCII(1)
		if s.byteAt(0) == '+' || s.byteAt(0) == '-' {
			s.advanceASCII(1)
		}
		if !isDigit(s.byteAt(0)) {
			return Token{}, s.errorFrom(exponent, "expected digits in numeric exponent")
		}
		s.takeDigits()
	}
	return s.token(kind, start), nil
}

// takeDigits 连续读取 ASCII 数字，游标停在数字段之后。
func (s *scanner) takeDigits() {
	for isDigit(s.byteAt(0)) {
		s.advanceASCII(1)
	}
}

// isDigit 判断单个字节是否为数字字面量允许的 ASCII 数字。
func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

// scanString 负责匹配引号和收集解码后的文本，scanEscape 负责解释
// 反斜杠后的字符。两个函数共用游标，所以最终记号可以保留完整原文。

// scanString 读取双引号字符串，同时保留源码拼写和解码值。
func (s *scanner) scanString(start Position) (Token, error) {
	s.advanceASCII(1)
	var value strings.Builder
	for s.pos.Offset < len(s.source) {
		charStart := s.pos
		r, width := s.runeAt()
		// 先检查编码和原始换行，遇到换行时将错误定位到起始引号。
		switch {
		case r == utf8.RuneError && width == 1:
			return Token{}, s.invalidUTF8()
		case r == '\r' || r == '\n':
			return Token{}, s.errorFrom(start, "unterminated string")
		}
		s.advanceRune(width)
		// 普通码点直接写入，转义交给 scanEscape，闭引号结束扫描。
		switch {
		case r == '"':
			token := s.token(String, start)
			token.StringValue = value.String()
			return token, nil
		case r == '\\':
			escaped, err := s.scanEscape(start, charStart)
			if err != nil {
				return Token{}, err
			}
			value.WriteByte(escaped)
		case r < 0x20 || r == 0x7f:
			return Token{}, s.errorFrom(charStart, "unescaped control character in string")
		default:
			value.WriteRune(r)
		}
	}
	return Token{}, s.errorFrom(start, "unterminated string")
}

// scanEscape 读取反斜杠之后的字符，并返回转义后的单字节值。
func (s *scanner) scanEscape(stringStart, escapeStart Position) (byte, error) {
	r, width := s.runeAt()
	// 未闭合字符串从引号开始定位，非法字节和未知转义各自定位到出错处。
	switch {
	case width == 0 || r == '\r' || r == '\n':
		return 0, s.errorFrom(stringStart, "unterminated string")
	case r == utf8.RuneError && width == 1:
		return 0, s.invalidUTF8()
	}
	s.advanceRune(width)
	// 合法转义的解码结果都是单字节字符。
	switch r {
	case '"', '\\':
		return byte(r), nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	default:
		escape := s.source[escapeStart.Offset:s.pos.Offset]
		return 0, s.errorFrom(escapeStart, fmt.Sprintf("unknown string escape %q", escape))
	}
}

// 游标读取函数只查看源码，推进函数同步维护字节偏移和列号。
// 换行由 next 读取并更新行号，便于在一个位置处理三种行尾格式。

// byteAt 查看当前偏移之后指定距离的字节，越过末尾时返回 0。
func (s *scanner) byteAt(distance int) byte {
	offset := s.pos.Offset + distance
	if offset >= len(s.source) {
		return 0
	}
	return s.source[offset]
}

// runeAt 查看当前的 Unicode 码点及其 UTF-8 字节宽度。
func (s *scanner) runeAt() (rune, int) {
	return utf8.DecodeRuneInString(s.source[s.pos.Offset:])
}

// advanceRune 按码点的字节宽度推进偏移，并将列号加一。
func (s *scanner) advanceRune(width int) {
	s.pos.Offset += width
	s.pos.Column++
}

// advanceASCII 推进指定数量的 ASCII 字节，偏移和列号等量增加。
func (s *scanner) advanceASCII(count int) {
	s.pos.Offset += count
	s.pos.Column += count
}

// 记号和诊断均由扫描起点与当前游标确定区间。invalidUTF8 先读取一个
// 损坏字节，再复用 errorFrom 构造可精确定位到该字节的错误。

// token 根据扫描区间构造记号，并从源码切出原始拼写。
func (s *scanner) token(kind Kind, start Position) Token {
	return Token{
		Kind:   kind,
		Lexeme: s.source[start.Offset:s.pos.Offset],
		Span:   Span{Start: start, End: s.pos},
	}
}

// errorFrom 用指定起点和当前游标构造词法错误。
func (s *scanner) errorFrom(start Position, message string) *Error {
	return &Error{Span: Span{Start: start, End: s.pos}, Message: message}
}

// invalidUTF8 将当前的非法 UTF-8 字节标记为词法错误。
func (s *scanner) invalidUTF8() *Error {
	start := s.pos
	s.advanceRune(1)
	return s.errorFrom(start, "invalid UTF-8")
}
