// Command layout_check validates the identity-first package and wire-name boundaries.
package main

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const modulePath = "github.com/dolfly/infoflow"

type allowlist struct {
	Entries []entry `json:"entries"`
}

type entry struct {
	Path        string `json:"path"`
	Line        int    `json:"line"`
	MatchedText string `json:"matched_text"`
}

func main() {
	root, err := repositoryRoot()
	if err != nil {
		fail(err)
	}
	for _, path := range []string{"bot/im", "bot/robot", "bot/events", "bot/webhook", "bot/websocket", "bot/internal", "user/im", "user/calendar", "user/meeting", "user/internal", "userauthorization", "internal/transport", "internal/runtime"} {
		if info, err := os.Stat(filepath.Join(root, path)); err != nil || !info.IsDir() {
			fail(fmt.Errorf("required package directory missing: %s", path))
		}
	}
	if _, err := os.Stat(filepath.Join(root, "src")); !os.IsNotExist(err) {
		fail(fmt.Errorf("Go module must not contain src/"))
	}
	for _, name := range []string{"group_robot.go", "message.go", "message_operations.go", "user.go", "webhook_service.go", "websocket_service.go"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			fail(fmt.Errorf("legacy root implementation remains: %s", name))
		}
	}
	if err := checkRootImports(root); err != nil {
		fail(err)
	}
	if err := checkTestOwnership(root); err != nil {
		fail(err)
	}
	if err := checkPersonalAllowlist(root); err != nil {
		fail(err)
	}
	fmt.Println("layout_check: passed")
}

func checkTestOwnership(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, item := range entries {
		if item.IsDir() || !strings.HasSuffix(item.Name(), "_test.go") {
			continue
		}
		if !rootTestOwnerAllowed(item.Name()) {
			return fmt.Errorf("test does not belong at module root: %s", item.Name())
		}
		if strings.HasPrefix(item.Name(), "bot_") || item.Name() == "user_test.go" {
			return fmt.Errorf("capability test remains at module root: %s", item.Name())
		}
	}

	for _, required := range []string{"integration", "internal/testkit"} {
		if info, statErr := os.Stat(filepath.Join(root, required)); statErr != nil || !info.IsDir() {
			return fmt.Errorf("required test ownership directory missing: %s", required)
		}
	}
	if err := rejectImport(filepath.Join(root, "internal", "testkit"), modulePath); err != nil {
		return fmt.Errorf("internal/testkit must stay independent of aggregate root: %w", err)
	}

	return filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil || item.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, imported := range parsed.Imports {
			if imported.Name != nil && imported.Name.Name == "." {
				return fmt.Errorf("dot import is forbidden in tests: %s imports %s", path, imported.Path.Value)
			}
		}
		if strings.Contains(filepath.ToSlash(path), "/integration/") && parsed.Name.Name != "integration_test" {
			return fmt.Errorf("integration test must use external package integration_test: %s", path)
		}
		return nil
	})
}

func rootTestOwnerAllowed(name string) bool {
	if name == "example_test.go" {
		return true
	}
	for _, owner := range []string{"client", "config", "error", "errors"} {
		if name == owner+"_test.go" || strings.HasPrefix(name, owner+"_") {
			return true
		}
	}
	return false
}

func rejectImport(root, forbidden string) error {
	return filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil || item.IsDir() || filepath.Ext(path) != ".go" {
			return walkErr
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, imported := range parsed.Imports {
			if strings.Trim(imported.Path.Value, `"`) == forbidden {
				return fmt.Errorf("%s imports %s", path, forbidden)
			}
		}
		return nil
	})
}

func repositoryRoot() (string, error) {
	output, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	return strings.TrimSpace(string(output)), err
}

func checkRootImports(root string) error {
	for _, identity := range []string{"bot", "user", "userauthorization"} {
		err := filepath.WalkDir(filepath.Join(root, identity), func(path string, item os.DirEntry, walkErr error) error {
			if walkErr != nil || item.IsDir() || filepath.Ext(path) != ".go" {
				return walkErr
			}
			// External consumer tests (package <pkg>_test) legitimately import
			// the aggregate root to exercise the public `infoflow.NewClient`
			// facade against a mock server (e.g. bot/robot/service_test.go).
			// In-package tests cannot import the root either way because the
			// import cycle is rejected at compile time. Skip *_test.go so the
			// check enforces the ARCHITECTURE.md rule "capability packages
			// never import the aggregate root" for production code only.
			if strings.HasSuffix(item.Name(), "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), `"`+modulePath+`"`) {
				return fmt.Errorf("capability imports aggregate root: %s", path)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func checkPersonalAllowlist(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "spec", "personal-wire-allowlist.json"))
	if err != nil {
		return err
	}
	var allowed allowlist
	if err := json.Unmarshal(data, &allowed); err != nil {
		return err
	}
	expected := map[string]int{}
	for _, item := range allowed.Entries {
		expected[item.Path+"\x00"+item.MatchedText]++
	}
	actual := map[string]int{}
	output, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return err
	}
	for _, relative := range strings.Split(string(output), "\x00") {
		if relative == "" || strings.HasPrefix(relative, ".sdk-factory/") || relative == "spec/personal-wire-allowlist.json" {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if readErr != nil || !strings.Contains(strings.ToLower(string(contents)), "personal") {
			continue
		}
		lower := strings.ToLower(string(contents))
		for _, item := range allowed.Entries {
			if item.Path != relative {
				continue
			}
			actual[item.Path+"\x00"+item.MatchedText] += strings.Count(lower, strings.ToLower(item.MatchedText))
		}
	}
	if fmt.Sprint(sortedCounts(actual)) != fmt.Sprint(sortedCounts(expected)) {
		return fmt.Errorf("personal allowlist mismatch: actual=%v expected=%v", sortedCounts(actual), sortedCounts(expected))
	}
	return nil
}

func sortedCounts(values map[string]int) []string {
	result := make([]string, 0, len(values))
	for key, count := range values {
		result = append(result, fmt.Sprintf("%s=%d", key, count))
	}
	sort.Strings(result)
	return result
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "layout_check:", err)
	os.Exit(1)
}
