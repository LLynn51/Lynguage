// lynguage 提供脚本执行和 Go 源码导出命令。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"lynguage/internal/ast"
	"lynguage/internal/checker"
	"lynguage/internal/parser"
	"lynguage/internal/transpiler"
	"lynguage/internal/vm"
)

const usage = `用法
  lynguage run <file.lyn>              使用字节码虚拟机执行脚本
  lynguage run --transpile <file.lyn>  编译为原生 Go 程序并执行
  lynguage emit-go <file.lyn>          将生成的 Go 源码写到标准输出
`

type mode uint8

const (
	runVM mode = iota
	runNative
	emitGo
)

type command struct {
	mode mode
	path string
}

// 命令入口与参数解析一起处理用户交互。parseCommand 选择执行模式，
// runCLI 将帮助、用法错误和运行结果分别送到约定的输出流并返回退出码。

// main 连接进程的标准输入输出，执行命令并设置退出状态。
func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// runCLI 分派一次命令，区分用法错误、脚本错误与原生子进程的退出状态。
func runCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd, err := parseCommand(args)
	// 帮助输出走标准输出，参数有误时将诊断和用法一同写到标准错误流。
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprint(stderr, usage)
		return 2
	}

	err = execute(ctx, cmd, stdin, stdout, stderr)
	if err == nil {
		return 0
	}
	// 原生程序直接报告自己的运行时诊断。原样传递它的退出码，构建错误仍由 CLI 报告。
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	fmt.Fprintln(stderr, err)
	return 1
}

// parseCommand 解析子命令及其选项，并要求传入一个脚本路径。
func parseCommand(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New("缺少子命令")
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return command{}, flag.ErrHelp
	}
	name := args[0]
	if name != "run" && name != "emit-go" {
		return command{}, fmt.Errorf("未知子命令 %q", name)
	}

	// 每个子命令仅登记自己的选项，标准 flag 解析器也支持用 -- 传递以连字符开头的路径。
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var native bool
	if name == "run" {
		flags.BoolVar(&native, "transpile", false, "编译为原生 Go 程序并执行")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return command{}, err
	}
	if flags.NArg() != 1 {
		return command{}, fmt.Errorf("%s 需要一个脚本路径", name)
	}

	// run 默认进入 VM，选项和 emit-go 分别选择两个使用 Go 源码的路径。
	cmd := command{mode: runVM, path: flags.Arg(0)}
	if name == "emit-go" {
		cmd.mode = emitGo
	} else if native {
		cmd.mode = runNative
	}
	return cmd, nil
}

// 脚本处理先由 loadScript 完成读取、解析和类型检查。execute 使用同一份
// 检查结果选择字节码或 Go 后端，Go 后端的产物可以执行，也可以直接输出。

// loadScript 读取脚本并返回检查过的 AST 与后端需要的类型信息。
func loadScript(path string) (*ast.Program, *checker.Info, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取脚本失败 %w", err)
	}
	// 解析和类型检查的错误已带行列号，此处加上源文件路径以便定位。
	tree, err := parser.Parse(string(source))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	info, err := checker.CheckWithInfo(tree)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return tree, info, nil
}

// execute 将已解析的命令送给对应后端，并输出脚本结果或生成的 Go 源码。
func execute(ctx context.Context, cmd command, stdin io.Reader, stdout, stderr io.Writer) error {
	tree, info, err := loadScript(cmd.path)
	if err != nil {
		return err
	}
	if cmd.mode == runVM {
		// 默认路径在当前进程里编译和执行字节码，println 直接连接指定的输出流。
		program, err := vm.Compile(tree, info)
		if err != nil {
			return fmt.Errorf("编译 %s 失败 %w", cmd.path, err)
		}
		return vm.Run(program, stdout)
	}

	// 原生执行和源码导出共用生成步骤，只有执行模式需要启动 Go 工具链。
	source, err := transpiler.Generate(tree, info)
	if err != nil {
		return fmt.Errorf("转译 %s 失败 %w", cmd.path, err)
	}
	if cmd.mode == emitGo {
		written, err := stdout.Write(source)
		if err == nil && written != len(source) {
			err = io.ErrShortWrite
		}
		return err
	}
	return transpiler.Run(ctx, source, stdin, stdout, stderr)
}
