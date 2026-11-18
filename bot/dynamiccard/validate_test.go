package dynamiccard

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func contentWithMarkdownLength(t *testing.T, length int) *DynamicCardContent {
	t.Helper()
	return NewContent(NewMarkdown("desc", strings.Repeat("a", length)))
}

// T-14: exactly 63,000 UTF-8 bytes passes; 63,001 fails.
func TestContentSizeBoundary(t *testing.T) {
	base := len(mustMarshal(t, contentWithMarkdownLength(t, 0)))
	padding := MaxContentBytes - base

	atLimit := contentWithMarkdownLength(t, padding)
	if size := len(mustMarshal(t, atLimit)); size != MaxContentBytes {
		t.Fatalf("constructed size = %d, want %d", size, MaxContentBytes)
	}
	if _, err := ValidateContent(atLimit); err != nil {
		t.Fatalf("%d bytes rejected: %v", MaxContentBytes, err)
	}

	overLimit := contentWithMarkdownLength(t, padding+1)
	if size := len(mustMarshal(t, overLimit)); size != MaxContentBytes+1 {
		t.Fatalf("constructed size = %d, want %d", size, MaxContentBytes+1)
	}
	_, err := ValidateContent(overLimit)
	if err == nil {
		t.Fatalf("%d bytes accepted", MaxContentBytes+1)
	}
	failure, ok := err.(*DynamicCardError)
	if !ok || !failure.IsValidation() {
		t.Fatalf("error = %T %v, want a validation DynamicCardError", err, err)
	}

	if _, err := ValidateContentWithOptions(overLimit, ValidationOptions{SkipSizeCheck: true}); err != nil {
		t.Fatalf("SkipSizeCheck still failed: %v", err)
	}
}

func nestedContent(levels int, meta json.RawMessage) *DynamicCardContent {
	var component Component = NewMarkdown("md", "x")
	if len(meta) > 0 {
		markdown := component.(*MarkdownComponent)
		markdown.Extra = map[string]json.RawMessage{"meta": meta}
	}
	for index := 0; index < levels; index++ {
		component = NewRow(fmt.Sprintf("row-%d", index), component)
	}
	return NewContent(component)
}

// T-15: depth 14 passes and depth 15 fails, with each nested component adding
// two layers.
func TestContentDepthBoundary(t *testing.T) {
	atLimit := nestedContent(4, json.RawMessage(`{"k":1}`))
	depth, err := contentDepth(mustMarshal(t, atLimit))
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != MaxContentDepth {
		t.Fatalf("depth = %d, want %d", depth, MaxContentDepth)
	}
	if _, err := ValidateContent(atLimit); err != nil {
		t.Fatalf("depth %d rejected: %v", MaxContentDepth, err)
	}

	overLimit := nestedContent(4, json.RawMessage(`{"k":{"z":1}}`))
	depth, err = contentDepth(mustMarshal(t, overLimit))
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != MaxContentDepth+1 {
		t.Fatalf("depth = %d, want %d", depth, MaxContentDepth+1)
	}
	if _, err := ValidateContent(overLimit); err == nil {
		t.Fatalf("depth %d accepted", MaxContentDepth+1)
	}

	if _, err := ValidateContentWithOptions(overLimit, ValidationOptions{SkipDepthCheck: true}); err != nil {
		t.Fatalf("SkipDepthCheck still failed: %v", err)
	}
}

// T-16: ids must be unique across the whole card, including nested components.
func TestDuplicateNestedID(t *testing.T) {
	content := NewContent(NewRow("dup", NewMarkdown("dup", "x")))
	_, err := ValidateContent(content)
	if err == nil {
		t.Fatal("duplicate nested id accepted")
	}
	failure, ok := err.(*DynamicCardError)
	if !ok || !failure.IsValidation() {
		t.Fatalf("error = %T %v, want validation", err, err)
	}

	deep := NewContent(
		NewColumn("col", NewRow("row", NewText("same", "a"))),
		NewText("same", "b"),
	)
	if _, err := ValidateContent(deep); err == nil {
		t.Fatal("duplicate id across branches accepted")
	}

	unique := NewContent(NewRow("row", NewText("a", "1"), NewText("b", "2")), NewText("c", "3"))
	if _, err := ValidateContent(unique); err != nil {
		t.Fatalf("unique ids rejected: %v", err)
	}
}

func TestMissingComponentTypeRejected(t *testing.T) {
	content := NewContent(NewRaw("", "", nil))
	if _, err := ValidateContent(content); err == nil {
		t.Fatal("empty component type accepted")
	}

	rawMarkdown := NewRaw(ComponentTypeMarkdown, "md", map[string]json.RawMessage{
		"content": json.RawMessage(`123`),
	})
	if _, err := ValidateContent(NewContent(rawMarkdown)); err == nil {
		t.Fatal("non-string markdown content accepted")
	}

	if _, err := ValidateContent(NewContent(NewRaw(ComponentTypeMarkdown, "md", map[string]json.RawMessage{
		"content": json.RawMessage(`"ok"`),
	}))); err != nil {
		t.Fatalf("raw markdown string content rejected: %v", err)
	}
}

func TestValidateRejectsInvalidInputs(t *testing.T) {
	if _, err := ValidateContent(nil); err == nil {
		t.Fatal("nil content accepted")
	}
	if _, err := ValidateContent(&DynamicCardContent{Scheme: "2"}); err == nil {
		t.Fatal("scheme 2 accepted")
	}
	withNil := &DynamicCardContent{Scheme: SchemeV1, Body: CardBody{Children: ComponentList{nil}}}
	if _, err := ValidateContent(withNil); err == nil {
		t.Fatal("nil component accepted")
	}
}

// Advisory findings never block a request.
func TestValidationWarningsOnly(t *testing.T) {
	components := make([]Component, 0, RecommendedMaxComponents+1)
	for index := 0; index <= RecommendedMaxComponents; index++ {
		components = append(components, NewText(fmt.Sprintf("t%d", index), "x"))
	}
	warnings, err := ValidateContent(NewContent(components...))
	if err != nil {
		t.Fatalf("too many components blocked: %v", err)
	}
	if !hasWarning(warnings, WarningTooManyComponents) {
		t.Fatalf("warnings = %v, want %s", warnings, WarningTooManyComponents)
	}

	warnings, err = ValidateContent(nestedContent(4, nil))
	if err != nil {
		t.Fatalf("deep nesting blocked: %v", err)
	}
	if !hasWarning(warnings, WarningDeepNesting) {
		t.Fatalf("warnings = %v, want %s", warnings, WarningDeepNesting)
	}

	warnings, err = ValidateContent(nestedContent(RecommendedMaxNesting-1, nil))
	if err != nil {
		t.Fatalf("recommended nesting rejected: %v", err)
	}
	for _, warning := range warnings {
		if warning.Code == WarningDeepNesting {
			t.Fatalf("recommended nesting produced %v", warning)
		}
	}
}

func hasWarning(warnings []Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
