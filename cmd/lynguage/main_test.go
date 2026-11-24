package main

import (
	"bytes"
	"context"
	goParser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 命令测试直接调用 runCLI 并捕获两个输出流，检查正常输出、帮助和错误的分流。

// TestCommandStreams 验证 VM 和源码导出命令的标准输出，以及帮助和参数错误的退出码。
func TestCommandStreams(t *testing.T) {
	script := filepath.Join("..", "..", "app", "example", "phase3.lyn")
	// 使用同一脚本覆盖默认 VM 和 emit-go，同时比较帮助与错误的输出流。
	tests := []struct {
		name       string
		args       []string
		code       int
		wantOutput string
		wantError  string
	}{
		{name: "VM", args: []string{"run", script}, wantOutput: "2 2\n3 4\n5 8\nOk(14)\nErr(score index out of range)\n"},
		{name: "emit", args: []string{"emit-go", script}, wantOutput: "package main"},
		{name: "help", args: []string{"run", "--help"}, wantOutput: usage},
		{name: "missing path", args: []string{"run", "--transpile"}, code: 2, wantError: "run 需要一个脚本路径"},
		{name: "unexpected flag", args: []string{"emit-go", "--transpile", script}, code: 2, wantError: "flag provided but not defined"},
	}
	for _, test := range tests {
		// 每个用例单独收集输出，防止其他命令的内容干扰比较。
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), test.args, nil, &stdout, &stderr)
			if code != test.code {
				t.Fatalf("exit code = %d, want %d; stderr = %q", code, test.code, stderr.String())
			}
			if test.wantError == "" && stderr.Len() != 0 || !strings.Contains(stderr.String(), test.wantError) {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantError)
			}
			if test.name == "emit" {
				// 直接解析整个 stdout，帮助文字或诊断混入源码时会使测试失败。
				file, err := goParser.ParseFile(token.NewFileSet(), "emitted.go", stdout.Bytes(), goParser.AllErrors)
				if err != nil || file.Name.Name != "main" {
					t.Fatalf("invalid Go output: %v", err)
				}
			} else if stdout.String() != test.wantOutput {
				t.Errorf("stdout = %q, want %q", stdout.String(), test.wantOutput)
			}
		})
	}
}

// TestTypeErrorKeepsStdoutEmpty 验证静态错误在选择后端前就结束命令。
func TestTypeErrorKeepsStdoutEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.lyn")
	if err := os.WriteFile(path, []byte("let value: int = \"text\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 三种模式共用检查入口，因此诊断都要包含原始文件名和位置，stdout 保持为空。
	for _, args := range [][]string{{"run", path}, {"run", "--transpile", path}, {"emit-go", path}} {
		var stdout, stderr bytes.Buffer
		code := runCLI(context.Background(), args, nil, &stdout, &stderr)
		if code != 1 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), path+": 1:") {
			t.Errorf("args %v gave exit %d, stdout %q, stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
}
