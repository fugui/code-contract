package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"

	"code-contract/config"
	"code-contract/models"

	"gorm.io/gorm"
)

// ResetOptions 定义本地业务数据清空选项。
type ResetOptions struct {
	ResetSettings bool
}

// ResetResult 记录本次本地数据清空与基线恢复的统计结果。
type ResetResult struct {
	TruncatedTables    int
	SettingsReset      bool
	RestoredCheckRules int64
	RestoredVocabTerms int64
}

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	resetSettings := flag.Bool("reset-settings", false, "同时清空 contract_settings 并恢复默认治理参数")
	force := flag.Bool("force", false, "跳过仅限本机数据库的安全熔断校验（谨慎使用）")
	flag.Parse()

	if err := config.LoadConfig(*configPath); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	dsn := config.AppConfig.Database.GetDSN()
	host, port, dbname, err := validateResetTarget(dsn, *force)
	if err != nil {
		log.Fatalf("安全熔断拦截: %v", err)
	}

	log.Println("==========================================================")
	log.Printf(" 开始清空本地 Code-Contract 业务数据 (目标库: %s:%s/%s)", host, port, dbname)
	log.Println("==========================================================")

	if err := models.InitDB(); err != nil {
		log.Fatalf("初始化数据库连接失败: %v", err)
	}

	res, err := resetLocalData(models.DB, ResetOptions{
		ResetSettings: *resetSettings,
	})
	if err != nil {
		log.Fatalf("清空本地数据失败: %v", err)
	}

	log.Printf("  • 已清空 %d 张 contract_* 业务表并重置自增序列", res.TruncatedTables)
	if res.SettingsReset {
		log.Printf("  • 已重置 %s 并恢复默认治理参数", (models.ModuleSetting{}).TableName())
	} else {
		log.Printf("  • 已保留 %s 模块配置及平台共享表 (users / departments / sys_audit_logs)", (models.ModuleSetting{}).TableName())
	}
	log.Printf("  • 已自动恢复内置静态检查规则 %d 条、基础标准词根 %d 条", res.RestoredCheckRules, res.RestoredVocabTerms)
	log.Println("  • 当前为纯净冷启动状态：未预置任何 Agent 或子系统")
	log.Println("----------------------------------------------------------")
	log.Println(" 下一步提示：")
	log.Println("   1. 若 code-contract-server 正在运行，请重启服务以刷新内存缓存；")
	log.Println("   2. 在 /agents 页面纳管本地 Agent 后，可通过 CSG 自发现一键导入拓扑。")
	log.Println("==========================================================")
}

// resetLocalData 在单事务内原子清空全部业务表并恢复内置静态规则与基础词库，不预置任何 Agent 或子系统。
func resetLocalData(db *gorm.DB, opts ResetOptions) (*ResetResult, error) {
	if db == nil {
		return nil, errors.New("数据库连接为空")
	}

	tables := models.BusinessTableNames()
	if len(tables) == 0 {
		return nil, errors.New("未获取到待清理业务表清单")
	}

	res := &ResetResult{
		TruncatedTables: len(tables),
		SettingsReset:   opts.ResetSettings,
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		truncateSQL := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", strings.Join(tables, ", "))
		if err := tx.Exec(truncateSQL).Error; err != nil {
			return fmt.Errorf("清空业务数据表失败: %w", err)
		}

		if opts.ResetSettings {
			settingsTable := (models.ModuleSetting{}).TableName()
			if err := tx.Exec(fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", settingsTable)).Error; err != nil {
				return fmt.Errorf("清空配置表 %s 失败: %w", settingsTable, err)
			}
			if err := models.EnsureDefaultSettings(tx); err != nil {
				return fmt.Errorf("恢复默认治理参数失败: %w", err)
			}
		}

		if err := models.EnsureBuiltinCheckRules(tx); err != nil {
			return fmt.Errorf("恢复内置静态检查规则失败: %w", err)
		}
		if err := models.EnsureBuiltinVocabularyTerms(tx); err != nil {
			return fmt.Errorf("恢复基础标准词根库失败: %w", err)
		}

		if err := tx.Model(&models.CheckRule{}).Count(&res.RestoredCheckRules).Error; err != nil {
			return fmt.Errorf("统计内置静态检查规则失败: %w", err)
		}
		if err := tx.Model(&models.VocabularyTerm{}).Count(&res.RestoredVocabTerms).Error; err != nil {
			return fmt.Errorf("统计基础标准词根失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return res, nil
}

// validateResetTarget 校验 DSN 目标是否属于本机回环地址，防止误清空远程共享库或生产库。
func validateResetTarget(dsn string, force bool) (host, port, dbname string, err error) {
	host, port, dbname = parseDSNTarget(dsn)
	if port == "" {
		port = "5432"
	}
	if dbname == "" {
		dbname = "(default)"
	}

	if force {
		if host == "" {
			host = "(unspecified)"
		}
		return host, port, dbname, nil
	}

	if strings.TrimSpace(host) == "" {
		return "", port, dbname, errors.New("未能从 DSN 解析数据库主机地址，拒绝执行（如确需执行请显式传入 -force）")
	}

	for _, part := range strings.Split(host, ",") {
		if !isLocalHost(part) {
			return host, port, dbname, fmt.Errorf("目标数据库主机 %q 非本机回环地址，默认仅允许清理本机数据库（如确需执行请显式传入 -force）", host)
		}
	}

	return host, port, dbname, nil
}

// parseDSNTarget 从 postgres URL 或 libpq key=value DSN 中提取 host、port 与 dbname。
func parseDSNTarget(dsn string) (host, port, dbname string) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", "", ""
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		if u, err := url.Parse(trimmed); err == nil {
			host = u.Hostname()
			port = u.Port()
			dbname = strings.TrimPrefix(u.Path, "/")
			q := u.Query()
			if host == "" {
				host = q.Get("host")
			}
			if port == "" {
				port = q.Get("port")
			}
			if dbname == "" {
				dbname = q.Get("dbname")
			}
			return host, port, dbname
		}
	}

	for _, token := range splitLibpqTokens(trimmed) {
		idx := strings.IndexByte(token, '=')
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(token[:idx]))
		val := unquoteLibpqValue(strings.TrimSpace(token[idx+1:]))
		switch key {
		case "host":
			host = val
		case "port":
			port = val
		case "dbname":
			dbname = val
		}
	}
	return host, port, dbname
}

func splitLibpqTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuotes := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && inQuotes {
			escaped = true
			cur.WriteByte(ch)
			continue
		}
		if ch == '\'' {
			inQuotes = !inQuotes
			cur.WriteByte(ch)
			continue
		}
		if (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') && !inQuotes {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func unquoteLibpqValue(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\'`, `'`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	return v
}

func isLocalHost(host string) bool {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if strings.HasPrefix(h, "/") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
