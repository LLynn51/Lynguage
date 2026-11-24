package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"lynguage/internal/parser"
)

const demoSource = "app/bubble_sort.lyn"

// main 运行冒泡排序示例的 AST 查看入口，将错误写入标准错误流。
func main() {
	if err := printAST(demoSource, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// printAST 读取 Lynguage 源文件，将词法和语法分析得到的 AST 写成 JSON。
func printAST(path string, output io.Writer) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取脚本失败 %w", err)
	}
	// Parse 先扫描记号，再构建语法树。解析成功后才开始输出 JSON。
	program, err := parser.Parse(string(source))
	if err != nil {
		return fmt.Errorf("解析 %s 失败 %w", path, err)
	}

	// 缩进展示节点层级，直接保留泛型和比较符中的尖括号。
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(program); err != nil {
		return fmt.Errorf("输出 AST JSON 失败 %w", err)
	}
	return nil
}
