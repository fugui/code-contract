package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code-contract/config"
	"code-contract/demo/pkg/protosandbox"
	"code-contract/models"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		printUsage()
		return
	}

	subcmd := os.Args[1]
	args := os.Args[2:]
	ctx := context.Background()

	switch subcmd {
	case "up":
		fs := flag.NewFlagSet("up", flag.ExitOnError)
		cfgPath := fs.String("config", "", "config.yaml 路径（默认依次查找 ./config.yaml, ../config.yaml, ../../config.yaml）")
		scale := fs.Int("scale", 0, "额外生成的规模压测仓数量（如 500）")
		syncFlag := fs.Bool("sync", true, "初始化后自动执行 CACHE_ONLY 同步与 Stage C+ 全池协调")
		activate := fs.Bool("activate", true, "自动激活 Group A 描述集并重建活体文档符号")
		_ = fs.Parse(args)

		mustInitEnv(*cfgPath)
		res, err := protosandbox.Up(ctx, models.DB, &config.AppConfig, protosandbox.UpOptions{
			ScaleCount:     *scale,
			RunSync:        *syncFlag,
			ActivateGroupA: *activate,
		})
		if err != nil {
			fatalErr("up 失败", err)
		}
		printJSON("proto-sandbox up 完成", res)

	case "down":
		fs := flag.NewFlagSet("down", flag.ExitOnError)
		cfgPath := fs.String("config", "", "config.yaml 路径")
		_ = fs.Parse(args)

		mustInitEnv(*cfgPath)
		res, err := protosandbox.Down(ctx, models.DB, &config.AppConfig)
		if err != nil {
			fatalErr("down 失败", err)
		}
		printJSON("proto-sandbox down 清理完成", res)

	case "reset":
		fs := flag.NewFlagSet("reset", flag.ExitOnError)
		cfgPath := fs.String("config", "", "config.yaml 路径")
		scale := fs.Int("scale", 0, "额外生成的规模压测仓数量")
		syncFlag := fs.Bool("sync", true, "重建后自动执行同步与全池协调")
		activate := fs.Bool("activate", true, "重建后自动激活 Group A 描述集")
		_ = fs.Parse(args)

		mustInitEnv(*cfgPath)
		res, err := protosandbox.Reset(ctx, models.DB, &config.AppConfig, protosandbox.UpOptions{
			ScaleCount:     *scale,
			RunSync:        *syncFlag,
			ActivateGroupA: *activate,
		})
		if err != nil {
			fatalErr("reset 失败", err)
		}
		printJSON("proto-sandbox reset 完成", res)

	case "mutate":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "错误: 请指定演进剧本: breaking | lint-and-dup | promote-no-proto | doc-update | rollback")
			os.Exit(1)
		}
		scenario := args[0]
		fs := flag.NewFlagSet("mutate", flag.ExitOnError)
		cfgPath := fs.String("config", "", "config.yaml 路径")
		syncFlag := fs.Bool("sync", true, "变更后自动执行增量同步/文档刷新")
		_ = fs.Parse(args[1:])

		mustInitEnv(*cfgPath)
		res, err := protosandbox.Mutate(ctx, models.DB, &config.AppConfig, scenario, *syncFlag)
		if err != nil {
			fatalErr("mutate 失败", err)
		}
		printJSON("proto-sandbox mutate 完成", res)

	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		cfgPath := fs.String("config", "", "config.yaml 路径")
		_ = fs.Parse(args)

		mustInitEnv(*cfgPath)
		st, err := protosandbox.Status(ctx, models.DB, &config.AppConfig)
		if err != nil {
			fatalErr("status 查询失败", err)
		}
		printJSON("proto-sandbox status", st)

	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", subcmd)
		printUsage()
		os.Exit(1)
	}
}

func resolveConfigPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("指定的配置文件不存在: %s", abs)
		}
		return abs, nil
	}
	candidates := []string{
		"config.yaml",
		filepath.Join("..", "config.yaml"),
		filepath.Join("..", "..", "config.yaml"),
		"config.yaml.example",
		filepath.Join("..", "config.yaml.example"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("未找到 config.yaml 或 config.yaml.example，请通过 --config 显式指定")
}

func mustInitEnv(explicitCfg string) {
	cfgPath, err := resolveConfigPath(explicitCfg)
	if err != nil {
		fatalErr("定位配置文件失败", err)
	}
	if err := config.LoadConfig(cfgPath); err != nil {
		fatalErr("加载配置文件失败", err)
	}

	// 将相对 storage 路径固定锚定到 config.yaml 所在目录，保证从 demo/ 或仓库根目录执行均指向同一 data/ 目录
	baseDir := filepath.Dir(cfgPath)
	resolveRelToBase := func(p string) string {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" || filepath.IsAbs(trimmed) {
			return trimmed
		}
		return filepath.Clean(filepath.Join(baseDir, trimmed))
	}
	config.AppConfig.Storage.Root = resolveRelToBase(config.AppConfig.Storage.Root)
	config.AppConfig.Storage.CodesDir = resolveRelToBase(config.AppConfig.Storage.CodesDir)
	config.AppConfig.Storage.ProtosDir = resolveRelToBase(config.AppConfig.Storage.ProtosDir)

	if err := models.InitDB(); err != nil {
		fatalErr("连接并迁移本地数据库失败", err)
	}
}

func printJSON(title string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Printf("=== %s ===\n%s\n", title, string(b))
}

func fatalErr(prefix string, err error) {
	fmt.Fprintf(os.Stderr, "[proto-sandbox] %s: %v\n", prefix, err)
	os.Exit(1)
}

func printUsage() {
	fmt.Print(`proto-sandbox — Code-Contract 本地 Proto 与契约治理全链路沙箱工具

用法:
  proto-sandbox <command> [flags]

子命令:
  up       生成本地 Git 缓存 + 播种平台仓库/架构数字孪生/词库，并默认同步与激活 Group A
           参数: --config <path> --scale <N> --sync=true --activate=true
  down     安全彻底清理全部沙箱数据 (sandbox-* / SBX_* / sbx-* / SANDBOX_DEMO) 与本地沙箱缓存
           参数: --config <path>
  reset    先 down 再 up
           参数: --config <path> --scale <N> --sync=true --activate=true
  mutate   触发 Group C 动态演进剧本:
             breaking          破坏性字段删改 -> 触发 MR 门禁阻断与影响面分析
             lint-and-dup      命名违规 + AST 重复结构体簇 -> 触发静态检查报告
             promote-no-proto  往 NO_PROTO 仓新增小写 pg/ -> 验证自动晋升 HAS_PROTO
             doc-update        新增 RPC 与 @error_code -> 验证活体文档增量刷新
             rollback          将演进仓回滚至 sandbox-v1 基线标签
           参数: --config <path> --sync=true
  status   查看当前沙箱仓库、缓存、编译描述集与活体文档状态
`)
}
