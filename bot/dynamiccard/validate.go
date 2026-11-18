package dynamiccard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Local precheck limits from the dynamic-card platform contract.
const (
	// MaxContentBytes is the maximum UTF-8 byte size of the encoded content.
	MaxContentBytes = 63000
	// MaxContentDepth is the maximum JSON depth of the encoded content: the
	// root object is layer 1 and every nested component adds two layers.
	MaxContentDepth = 14
	// RecommendedMaxComponents is the advisory component count; exceeding it
	// produces a warning only.
	RecommendedMaxComponents = 200
	// RecommendedMaxNesting is the advisory component nesting depth; exceeding
	// it produces a warning only.
	RecommendedMaxNesting = 3
)

// Warning codes returned by ValidateContent and Send for advisory (P1/P2)
// findings. Warnings never block a request.
const (
	WarningTooManyComponents = "too_many_components"
	WarningDeepNesting       = "deep_nesting"
	// WarningAtMentionTextMissing reports that a mentioned uuapId has no
	// "@<uuapId>" occurrence in the markdown text, so the client will not
	// highlight it or send a notification. Set AtOptions.AutoMentionText to
	// inject the missing mentions instead.
	WarningAtMentionTextMissing = "at_mention_text_missing"
)

// Warning is an advisory validation finding. Warnings never block a request;
// the server remains the final authority.
type Warning struct {
	Code    string
	Message string
}

// ValidationOptions controls the P1 prechecks. Both checks are enabled by
// default; a caller may disable one when the content is pre-encoded or the
// limit is enforced elsewhere.
type ValidationOptions struct {
	SkipSizeCheck  bool
	SkipDepthCheck bool
}

// ValidateContent applies the P0 and P1 local prechecks with default options.
func ValidateContent(content *DynamicCardContent) ([]Warning, error) {
	return ValidateContentWithOptions(content, ValidationOptions{})
}

// ValidateContentWithOptions applies the local prechecks and returns advisory
// warnings. A non-nil error is a *DynamicCardError with CategoryValidation:
// P0 findings (missing type, duplicate id, invalid markdown content), an
// invalid scheme, an encoded size above 63,000 bytes, or a JSON depth above 14.
func ValidateContentWithOptions(content *DynamicCardContent, options ValidationOptions) ([]Warning, error) {
	if content == nil {
		return nil, newValidationError("content", "must not be nil")
	}
	if content.Scheme != "" && content.Scheme != SchemeV1 {
		return nil, newValidationError("scheme", `must be "1"`)
	}

	stats := componentStats{}
	ids := make(map[string]struct{})
	if err := inspectComponents(content.Body.Children, 1, ids, &stats); err != nil {
		return nil, err
	}
	if content.Footer != nil {
		if err := inspectComponents(content.Footer.Children, 1, ids, &stats); err != nil {
			return nil, err
		}
	}

	raw, err := json.Marshal(content)
	if err != nil {
		return nil, newValidationError("content", "cannot be encoded: "+err.Error())
	}
	if !options.SkipSizeCheck && len(raw) > MaxContentBytes {
		return nil, newValidationError("content.size",
			fmt.Sprintf("%d UTF-8 bytes exceeds the %d byte limit", len(raw), MaxContentBytes))
	}
	if !options.SkipDepthCheck {
		depth, err := contentDepth(raw)
		if err != nil {
			return nil, newValidationError("content", "cannot compute JSON depth: "+err.Error())
		}
		if depth > MaxContentDepth {
			return nil, newValidationError("content.depth",
				fmt.Sprintf("%d levels exceeds the %d level limit", depth, MaxContentDepth))
		}
	}

	warnings := make([]Warning, 0, 2)
	if stats.components > RecommendedMaxComponents {
		warnings = append(warnings, Warning{
			Code: WarningTooManyComponents,
			Message: fmt.Sprintf("%d components exceeds the recommended %d",
				stats.components, RecommendedMaxComponents),
		})
	}
	if stats.nesting > RecommendedMaxNesting {
		warnings = append(warnings, Warning{
			Code:    WarningDeepNesting,
			Message: fmt.Sprintf("%d component levels exceeds the recommended %d", stats.nesting, RecommendedMaxNesting),
		})
	}
	return warnings, nil
}

type componentStats struct {
	components int
	nesting    int
}

type identifiedComponent interface {
	componentID() string
}

func inspectComponents(list ComponentList, level int, ids map[string]struct{}, stats *componentStats) error {
	if level > stats.nesting {
		stats.nesting = level
	}
	for _, component := range list {
		if component == nil {
			return newValidationError("children", "component must not be nil")
		}
		if strings.TrimSpace(component.ComponentType()) == "" {
			return newValidationError("component.type", "must not be empty")
		}
		stats.components++

		if identified, ok := component.(identifiedComponent); ok {
			id := strings.TrimSpace(identified.componentID())
			if id != "" {
				if _, exists := ids[id]; exists {
					return newValidationError("component.id", fmt.Sprintf("duplicate id %q", id))
				}
				ids[id] = struct{}{}
			}
		}

		var children ComponentList
		switch typed := component.(type) {
		case *RowComponent:
			children = typed.Children
		case *ColumnComponent:
			children = typed.Children
		case *ActionBarComponent:
			if err := validateActionBar(typed); err != nil {
				return err
			}
		case *RawComponent:
			if typed.Type == ComponentTypeMarkdown {
				if rawContent, exists := typed.Extra["content"]; exists {
					var text string
					if err := json.Unmarshal(rawContent, &text); err != nil {
						return newValidationError("markdown.content", "must be a string")
					}
				}
			}
		}
		if len(children) > 0 {
			if err := inspectComponents(children, level+1, ids, stats); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateActionBar enforces the structural minimum of an action_bar: a
// non-empty id, a non-nil actions array, a non-empty key for every action and a
// title for ai_tag. Unknown keys and unknown fields are intentionally accepted
// so a newer platform operation keeps working.
func validateActionBar(component *ActionBarComponent) error {
	if strings.TrimSpace(component.ID) == "" {
		return newValidationError("component.id", "action_bar id must not be empty")
	}
	if component.Actions == nil {
		return newValidationError("component.actions", "action_bar actions must not be nil")
	}
	for index, action := range component.Actions {
		field := fmt.Sprintf("component.actions[%d]", index)
		key := strings.TrimSpace(action.Key)
		if key == "" {
			return newValidationError(field+".key", "must not be empty")
		}
		if key == ActionKeyAITag && len(action.Title) == 0 {
			return newValidationError(field+".title", "ai_tag title is required")
		}
	}
	return nil
}

func contentDepth(raw []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	return jsonValueDepth(value), nil
}

// jsonValueDepth counts the root as layer 1 and every value below it as one
// more layer, so each nested component object plus its children array consumes
// two layers.
func jsonValueDepth(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		depth := 1
		for _, child := range typed {
			if candidate := 1 + jsonValueDepth(child); candidate > depth {
				depth = candidate
			}
		}
		return depth
	case []any:
		depth := 1
		for _, child := range typed {
			if candidate := 1 + jsonValueDepth(child); candidate > depth {
				depth = candidate
			}
		}
		return depth
	default:
		return 1
	}
}
