// Command upstream_push prepares the GitHub upstream mirror.
//
// The source repository is the source of truth and keeps its internal module
// path. The GitHub mirror must declare github.com/dolfly/infoflow so that
// `go get github.com/dolfly/infoflow` resolves. This command rewrites the
// module path in a throwaway worktree, regenerates the protobuf bindings from
// the rewritten .proto sources, verifies the rewritten tree, and pushes a
// single root commit only when -push is given.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultNewModulePath = "github.com/dolfly/infoflow"

	// The mirror is a public artifact; use a neutral identity instead of the
	// release operator's local git identity.
	mirrorAuthorName  = "InfoFlow SDK"
	mirrorAuthorEmail = "610232+dolfly@users.noreply.github.com"
)

// defaultExcludes removes iCode-only material from the GitHub mirror: internal
// release docs, the internal Go proxy environment, the internal CI config, the
// agent/review guide, and worker evidence. They stay in the iCode trunk.
const defaultExcludes = "docs/internal,go.env,ci.yml,.claude,.sdk-factory,.sdk-release,.sdk-test,scripts/regression_test"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "upstream_push:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("upstream_push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "HEAD", "source revision to mirror")
	newModule := fs.String("new-module", defaultNewModulePath, "module path declared by the upstream mirror")
	remote := fs.String("remote", "upstream", "git remote that hosts the mirror")
	branch := fs.String("branch", "main", "remote branch to publish")
	protocFlag := fs.String("protoc", "protoc", "protoc executable (protobuf compiler)")
	protocGenGoFlag := fs.String("protoc-gen-go", "protoc-gen-go", "protoc-gen-go plugin executable")
	excludeFlag := fs.String("exclude", defaultExcludes, "comma-separated paths kept out of the mirror")
	push := fs.Bool("push", false, "commit the rewrite and push it to the remote")
	if err := fs.Parse(args); err != nil {
		return err
	}

	excludes, err := parseExcludes(*excludeFlag)
	if err != nil {
		return err
	}

	protocPath, err := exec.LookPath(*protocFlag)
	if err != nil {
		return fmt.Errorf("protoc not found (%q): install protobuf compiler 33.4+ or pass -protoc: %w", *protocFlag, err)
	}
	pluginPath, err := exec.LookPath(*protocGenGoFlag)
	if err != nil {
		return fmt.Errorf("protoc-gen-go not found (%q): run `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11` or pass -protoc-gen-go: %w", *protocGenGoFlag, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	root, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("locate repository root: %w", err)
	}
	if err := ensureClean(root); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "infoflow-upstream-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	worktree := filepath.Join(tmp, "repo")
	if err := runGit(root, "worktree", "add", "--detach", worktree, *from); err != nil {
		return fmt.Errorf("create throwaway worktree: %w", err)
	}
	defer func() {
		if err := runGitQuiet(root, "worktree", "remove", "--force", worktree); err != nil {
			_ = runGitQuiet(root, "worktree", "prune")
		}
	}()

	goMod, err := os.ReadFile(filepath.Join(worktree, "go.mod"))
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	oldModule, err := parseModulePath(goMod)
	if err != nil {
		return err
	}
	if oldModule == *newModule {
		return fmt.Errorf("go.mod at %s already declares %s", *from, *newModule)
	}

	// HEAD may already contain gofmt violations; the rewrite must not add new ones.
	unformattedBefore, err := unformattedFiles(ctx, worktree)
	if err != nil {
		return err
	}

	changed, err := rewriteWorktree(ctx, worktree, oldModule, *newModule)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		return fmt.Errorf("no tracked file references %s at %s", oldModule, *from)
	}
	removed, err := removeExcluded(worktree, excludes)
	if err != nil {
		return err
	}
	changed = filterChanged(changed, excludes)
	if err := regenerateBindings(ctx, worktree, *newModule, protocPath, pluginPath); err != nil {
		return err
	}

	if err := ensureNoReferences(ctx, worktree, oldModule); err != nil {
		return err
	}
	if err := verifyTree(ctx, worktree, unformattedBefore); err != nil {
		return err
	}

	if len(unformattedBefore) > 0 {
		fmt.Fprintf(stdout, "note: %s already has gofmt violations (left unchanged): %s\n", *from, strings.Join(unformattedBefore, ", "))
	}
	fmt.Fprintf(stdout, "rewrote %d file(s) from %s to %s in a throwaway worktree\n", len(changed), oldModule, *newModule)
	for _, file := range changed {
		fmt.Fprintf(stdout, "  %s\n", file)
	}
	if len(removed) > 0 {
		fmt.Fprintf(stdout, "removed %d internal path(s) from the mirror: %s\n", len(removed), strings.Join(removed, ", "))
	}

	if !*push {
		fmt.Fprintf(stdout, "\ndry run: nothing was committed or pushed.\nrerun with -push to commit and push to %s/%s\n", *remote, *branch)
		return nil
	}

	head, err := commitMirror(ctx, worktree, commitMessage(*newModule))
	if err != nil {
		return err
	}
	if err := runGit(worktree, pushArgs(*remote, *branch)...); err != nil {
		return fmt.Errorf("push to %s: %w", *remote, err)
	}
	fmt.Fprintf(stdout, "pushed %s to %s/%s\n", head, *remote, *branch)
	return nil
}

// parseExcludes splits a comma-separated path list and rejects paths that
// could escape the throwaway worktree.
func parseExcludes(spec string) ([]string, error) {
	var excludes []string
	for _, raw := range strings.Split(spec, ",") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if filepath.IsAbs(trimmed) {
			return nil, fmt.Errorf("invalid exclude path %q: must be relative", trimmed)
		}
		cleaned := path.Clean(filepath.ToSlash(trimmed))
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("invalid exclude path %q: must stay inside the worktree", trimmed)
		}
		excludes = append(excludes, cleaned)
	}
	return excludes, nil
}

// removeExcluded deletes internal-only paths from the worktree. Missing paths
// are ignored so the same default list works across revisions.
func removeExcluded(dir string, excludes []string) ([]string, error) {
	var removed []string
	for _, rel := range excludes {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("stat %s: %w", rel, err)
		}
		if err := os.RemoveAll(full); err != nil {
			return nil, fmt.Errorf("remove %s: %w", rel, err)
		}
		removed = append(removed, rel)
	}
	sort.Strings(removed)
	return removed, nil
}

// filterChanged drops rewritten files that were then removed from the mirror.
func filterChanged(changed, excludes []string) []string {
	var kept []string
	for _, rel := range changed {
		if !isExcluded(rel, excludes) {
			kept = append(kept, rel)
		}
	}
	return kept
}

func isExcluded(rel string, excludes []string) bool {
	for _, exclude := range excludes {
		if rel == exclude || strings.HasPrefix(rel, exclude+"/") {
			return true
		}
	}
	return false
}

// parseModulePath returns the module path declared by go.mod.
func parseModulePath(goMod []byte) (string, error) {
	for _, raw := range strings.Split(string(goMod), "\n") {
		line := strings.TrimSpace(raw)
		if line != "module" && !strings.HasPrefix(line, "module ") && !strings.HasPrefix(line, "module\t") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		if i := strings.Index(path, "//"); i >= 0 {
			path = strings.TrimSpace(path[:i])
		}
		path = strings.Trim(path, `"`)
		if path == "" {
			return "", errors.New("go.mod has an empty module directive")
		}
		if strings.ContainsAny(path, " \t") {
			return "", fmt.Errorf("go.mod module directive is malformed: %q", line)
		}
		return path, nil
	}
	return "", errors.New("go.mod has no module directive")
}

// rewriteModulePath replaces every occurrence of oldPath with newPath.
func rewriteModulePath(content []byte, oldPath, newPath string) ([]byte, bool) {
	if oldPath == "" || oldPath == newPath || !bytes.Contains(content, []byte(oldPath)) {
		return content, false
	}
	return bytes.ReplaceAll(content, []byte(oldPath), []byte(newPath)), true
}

// rewriteFileContent rewrites one tracked file. Go files marked as generated
// only get their import paths rewritten: generated protobuf descriptors embed
// length-prefixed byte strings that would be corrupted by a plain text edit.
func rewriteFileContent(rel string, content []byte, oldPath, newPath string) ([]byte, bool) {
	if isGeneratedGoFile(rel, content) {
		return rewriteImportsOnly(content, oldPath, newPath)
	}
	return rewriteModulePath(content, oldPath, newPath)
}

func isGeneratedGoFile(rel string, content []byte) bool {
	if !strings.HasSuffix(rel, ".go") {
		return false
	}
	firstLine, _, _ := strings.Cut(string(content), "\n")
	return strings.HasPrefix(firstLine, "// Code generated ") && strings.Contains(firstLine, "DO NOT EDIT")
}

// rewriteImportsOnly rewrites module-path import literals without touching any
// other byte in the file.
func rewriteImportsOnly(content []byte, oldPath, newPath string) ([]byte, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", content, parser.ImportsOnly)
	if err != nil {
		return content, false
	}
	type edit struct {
		start int
		end   int
		text  string
	}
	var edits []edit
	for _, spec := range file.Imports {
		path, ok := importPath(spec)
		if !ok || !matchesModulePath(path, oldPath) {
			continue
		}
		edits = append(edits, edit{
			start: fset.Position(spec.Path.Pos()).Offset,
			end:   fset.Position(spec.Path.End()).Offset,
			text:  strconv.Quote(newPath + strings.TrimPrefix(path, oldPath)),
		})
	}
	if len(edits) == 0 {
		return content, false
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), content...)
	for _, e := range edits {
		next := make([]byte, 0, len(out)-(e.end-e.start)+len(e.text))
		next = append(next, out[:e.start]...)
		next = append(next, e.text...)
		next = append(next, out[e.end:]...)
		out = next
	}
	return out, true
}

func importPath(spec *ast.ImportSpec) (string, bool) {
	path, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return "", false
	}
	return path, true
}

func matchesModulePath(path, modulePath string) bool {
	return path == modulePath || strings.HasPrefix(path, modulePath+"/")
}

// rewriteWorktree rewrites every tracked file in dir that references oldPath
// and returns the rewritten paths, sorted.
func rewriteWorktree(ctx context.Context, dir, oldPath, newPath string) ([]string, error) {
	files, err := grepFiles(ctx, dir, oldPath)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, rel := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", rel, err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		rewritten, ok := rewriteFileContent(rel, content, oldPath, newPath)
		if !ok {
			continue
		}
		if err := os.WriteFile(path, rewritten, info.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
		changed = append(changed, rel)
	}
	sort.Strings(changed)
	return changed, nil
}

// grepFiles lists tracked files in dir whose content contains needle.
func grepFiles(ctx context.Context, dir, needle string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "grep", "-l", "-z", "-F", "-e", needle, "--", ".")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil // git grep exits 1 when nothing matches
		}
		return nil, fmt.Errorf("git grep: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for _, file := range strings.Split(stdout.String(), "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

func ensureClean(root string) error {
	status, err := gitOutput(root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("check working tree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("working tree is not clean; commit or stash first:\n%s", status)
	}
	return nil
}

func ensureNoReferences(ctx context.Context, dir, needle string) error {
	remaining, err := grepFiles(ctx, dir, needle)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("rewritten tree still references %s in %s", needle, strings.Join(remaining, ", "))
	}
	return nil
}

// regenerateBindings regenerates the checked-in protobuf bindings from the
// rewritten .proto sources, so their embedded descriptors carry the mirror
// module path instead of the source module path.
func regenerateBindings(ctx context.Context, dir, modulePath, protocPath, pluginPath string) error {
	protos, err := protoFiles(dir)
	if err != nil {
		return err
	}
	if len(protos) == 0 {
		return nil
	}
	args := []string{
		"-I", ".",
		"--go_out=.",
		"--go_opt=module=" + modulePath,
		"--plugin=protoc-gen-go=" + pluginPath,
	}
	args = append(args, protos...)
	if err := runCommand(ctx, dir, protocPath, args...); err != nil {
		return fmt.Errorf("regenerate protobuf bindings: %w", err)
	}
	return nil
}

func protoFiles(dir string) ([]string, error) {
	out, err := gitOutput(dir, "ls-files", "-z", "--", "*.proto")
	if err != nil {
		return nil, fmt.Errorf("list proto files: %w", err)
	}
	var files []string
	for _, file := range strings.Split(out, "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

func verifyTree(ctx context.Context, dir string, unformattedBefore []string) error {
	unformattedAfter, err := unformattedFiles(ctx, dir)
	if err != nil {
		return err
	}
	if added := newUnformatted(unformattedBefore, unformattedAfter); len(added) > 0 {
		return fmt.Errorf("rewrite introduced gofmt violations:\n%s", strings.Join(added, "\n"))
	}
	for _, args := range [][]string{
		{"vet", "./..."},
		{"test", "./..."},
	} {
		if err := runCommand(ctx, dir, "go", args...); err != nil {
			return err
		}
	}
	return nil
}

// unformattedFiles lists the files gofmt would change, relative to dir.
func unformattedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := outputCommand(ctx, dir, "gofmt", "-l", ".")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files, nil
}

// newUnformatted returns the files that became unformatted because of the
// rewrite, ignoring violations that already existed at the source revision.
func newUnformatted(before, after []string) []string {
	existing := make(map[string]bool, len(before))
	for _, file := range before {
		existing[file] = true
	}
	var added []string
	for _, file := range after {
		if !existing[file] {
			added = append(added, file)
		}
	}
	sort.Strings(added)
	return added
}

// pushArgs force-updates the mirror branch: it is regenerated from trunk on
// every publish, so its history is intentionally not a fast-forward. The lease
// fails the push when the remote ref moved since the last fetch.
func pushArgs(remote, branch string) []string {
	return []string{"push", "--force-with-lease", remote, "HEAD:refs/heads/" + branch}
}

// commitMirror stages the rewritten tree and creates a root commit. The
// mirror is published without the source repository's commit history, so no
// internal commit messages or authors reach the upstream branch.
func commitMirror(ctx context.Context, dir, message string) (string, error) {
	if err := runGit(dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage rewritten tree: %w", err)
	}
	tree, err := gitOutput(dir, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write mirror tree: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "commit-tree", tree, "-F", "-")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+mirrorAuthorName,
		"GIT_AUTHOR_EMAIL="+mirrorAuthorEmail,
		"GIT_COMMITTER_NAME="+mirrorAuthorName,
		"GIT_COMMITTER_EMAIL="+mirrorAuthorEmail,
	)
	cmd.Stdin = strings.NewReader(message)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("create mirror root commit: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	commit := strings.TrimSpace(stdout.String())
	if commit == "" {
		return "", errors.New("git commit-tree returned an empty commit")
	}
	if err := runGit(dir, "update-ref", "HEAD", commit); err != nil {
		return "", fmt.Errorf("point worktree HEAD at mirror commit: %w", err)
	}
	return commit, nil
}

func commitMessage(newModule string) string {
	return fmt.Sprintf(`chore(release): publish upstream module path %s

Generated from the source tree by scripts/upstream_push; do not edit the
mirror by hand.

Co-Authored-By: Claude Code <noreply@anthropic.com>`, newModule)
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runGitQuiet(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Run()
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runCommand(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func outputCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
