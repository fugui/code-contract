package protosandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code-common/backend/gitsync"
)

func runGitCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Sandbox Bot",
		"GIT_AUTHOR_EMAIL=sandbox@code-contract.local",
		"GIT_COMMITTER_NAME=Sandbox Bot",
		"GIT_COMMITTER_EMAIL=sandbox@code-contract.local",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s (%s): %w\n%s", strings.Join(args, " "), dir, err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func ensurePathWithinSandbox(codesDir, targetPath string) error {
	absCodes, err := filepath.Abs(filepath.Clean(codesDir))
	if err != nil {
		return err
	}
	sandboxRoot := filepath.Join(absCodes, SandboxCacheSubdir)
	absTarget, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(sandboxRoot, absTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("安全拦截：目标路径 %q 不在沙箱缓存根目录 %q 内", absTarget, sandboxRoot)
	}
	return nil
}

// WriteSharedBaseDir 将 R4 共享基座真实 .proto 写入 <codesDir>/sandbox-demo/shared-base/include/。
func WriteSharedBaseDir(codesDir string) (string, error) {
	if strings.TrimSpace(codesDir) == "" {
		return "", errors.New("codesDir 不能为空")
	}
	absCodes, err := filepath.Abs(filepath.Clean(codesDir))
	if err != nil {
		return "", err
	}
	baseDir := filepath.Join(absCodes, filepath.FromSlash(SandboxSharedBaseRel))
	if err := ensurePathWithinSandbox(absCodes, baseDir); err != nil {
		return "", err
	}
	if err := os.RemoveAll(baseDir); err != nil {
		return "", fmt.Errorf("清理旧共享基座目录失败: %w", err)
	}
	if err := writeFilesMap(baseDir, SharedBaseFiles()); err != nil {
		return "", err
	}
	return baseDir, nil
}

// InitSandboxGitRepo 按 ADR-26 ResolveRepoLocalPath 在宿主机共享 codesDir 下初始化沙箱本地 Git 缓存。
// 仅保留沙箱模拟分支与 refs/remotes/origin/<branch> 引用，不向真实远端发任何请求。
func InitSandboxGitRepo(codesDir string, fx RepoFixture) (localPath string, commitSHA string, err error) {
	if strings.TrimSpace(codesDir) == "" {
		return "", "", errors.New("codesDir 不能为空")
	}
	repoURL := fx.RepoURL()
	localPath, err = gitsync.ResolveRepoLocalPath(codesDir, repoURL)
	if err != nil {
		return "", "", err
	}
	if err := ensurePathWithinSandbox(codesDir, localPath); err != nil {
		return "", "", err
	}

	if err := os.RemoveAll(localPath); err != nil {
		return "", "", fmt.Errorf("清理旧沙箱仓目录失败: %w", err)
	}
	if fx.SkipLocalCache {
		return "", "", nil
	}

	if err := os.MkdirAll(localPath, 0755); err != nil {
		return "", "", fmt.Errorf("创建沙箱仓目录失败: %w", err)
	}
	if err := writeFilesMap(localPath, fx.Files); err != nil {
		return "", "", err
	}

	branch := strings.TrimSpace(fx.Branch)
	if branch == "" {
		branch = "main"
	}

	if _, err := runGitCmd(localPath, "init", "-b", branch); err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "config", "user.name", "Sandbox Bot"); err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "config", "user.email", "sandbox@code-contract.local"); err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "remote", "add", "origin", repoURL); err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "add", "-A"); err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "commit", "--allow-empty", "-m", "chore(sandbox): initial contract corpus (v1)"); err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "tag", "-f", SandboxTagV1); err != nil {
		return "", "", err
	}
	sha, err := runGitCmd(localPath, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	if _, err := runGitCmd(localPath, "update-ref", "refs/remotes/origin/"+branch, sha); err != nil {
		return "", "", err
	}
	return localPath, sha, nil
}

// CommitAndAdvanceOrigin 在本地沙箱仓写入更新文件并推进 HEAD 与 refs/remotes/origin/<branch>。
func CommitAndAdvanceOrigin(localPath, branch string, files map[string][]byte, message string) (string, error) {
	if strings.TrimSpace(localPath) == "" {
		return "", errors.New("localPath 不能为空")
	}
	if strings.TrimSpace(branch) == "" {
		branch = "main"
	}
	if strings.TrimSpace(message) == "" {
		message = "chore(sandbox): mutate contract corpus"
	}
	if err := writeFilesMap(localPath, files); err != nil {
		return "", err
	}
	if _, err := runGitCmd(localPath, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := runGitCmd(localPath, "commit", "--allow-empty", "-m", message); err != nil {
		return "", err
	}
	sha, err := runGitCmd(localPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := runGitCmd(localPath, "update-ref", "refs/remotes/origin/"+branch, sha); err != nil {
		return "", err
	}
	return sha, nil
}

// RollbackRepoToTag 将本地沙箱仓硬回滚至指定标签并同步更新 refs/remotes/origin/<branch>。
func RollbackRepoToTag(localPath, branch, tag string) (string, error) {
	if strings.TrimSpace(localPath) == "" {
		return "", errors.New("localPath 不能为空")
	}
	if strings.TrimSpace(branch) == "" {
		branch = "main"
	}
	if strings.TrimSpace(tag) == "" {
		tag = SandboxTagV1
	}
	if _, err := runGitCmd(localPath, "reset", "--hard", tag); err != nil {
		return "", err
	}
	if _, err := runGitCmd(localPath, "clean", "-fd"); err != nil {
		return "", err
	}
	sha, err := runGitCmd(localPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := runGitCmd(localPath, "update-ref", "refs/remotes/origin/"+branch, sha); err != nil {
		return "", err
	}
	return sha, nil
}

func writeFilesMap(root string, files map[string][]byte) error {
	for rel, data := range files {
		cleanRel := filepath.Clean(filepath.FromSlash(strings.TrimLeft(rel, "/")))
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("非法相对路径: %q", rel)
		}
		abs := filepath.Join(root, cleanRel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
