package transpiler

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Run 用系统 go build 将生成的源码编译为本机可执行文件，然后连接传入的输入输出流执行。
func Run(ctx context.Context, source []byte, stdin io.Reader, stdout, stderr io.Writer) error {
	// 构建产物放在独立临时目录，成功、失败和取消都走同一清理路径。
	dir, err := os.MkdirTemp("", "lynguage-go-")
	if err != nil {
		return fmt.Errorf("create native build directory: %w", err)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, source, 0o600); err != nil {
		return fmt.Errorf("write generated Go source: %w", err)
	}
	executable := filepath.Join(dir, "lynguage-native")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}

	// 构建只针对单个 Go 文件，并为当前系统生成可直接运行的二进制文件。
	build := exec.CommandContext(ctx, "go", "build", "-o", executable, file)
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	build.Stdout, build.Stderr = stderr, stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build generated Go: %w", err)
	}

	// 保留调用者的工作目录，使脚本通过相对路径进行文件读写时沿用 VM 的路径语义。
	program := exec.CommandContext(ctx, executable)
	program.Stdin, program.Stdout, program.Stderr = stdin, stdout, stderr
	return program.Run()
}
