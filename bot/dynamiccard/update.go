package dynamiccard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// UpdateType discriminates the two update modes. The modes are mutually
// exclusive in both the type and the wire request.
type UpdateType string

const (
	// UpdateTypeFull replaces the whole card content.
	UpdateTypeFull UpdateType = "full"
	// UpdateTypeElement replaces individual components by id.
	UpdateTypeElement UpdateType = "element"
)

// Extra advisory codes returned by Update. Warnings never block a request.
const (
	// WarningSequenceMissing reports that a successful update response did not
	// carry a sequence; Sequence is 0 and the caller should keep its local value.
	WarningSequenceMissing = "sequence_missing"
	// WarningContainerChildrenMissing reports that a row/column update omits
	// children while the optional baseline had children; the platform removes
	// the original children.
	WarningContainerChildrenMissing = "container_children_missing"
)

// UpdateRequest is one content update. UpdateType selects exactly one mode:
//
//   - UpdateTypeFull: CardContent is required and Updates must be empty.
//   - UpdateTypeElement: Updates is required and CardContent must be nil.
//
// CurrentContent is an optional baseline used only for local element checks
// (target existence, unchanged type, cross-level ancestor/descendant conflict,
// container children completeness). When it is nil those checks are skipped and
// the server remains the authority.
//
// The request never carries a sequence: the update route rejects one with
// 1001.
type UpdateRequest struct {
	CardID         string
	UpdateType     UpdateType
	CardContent    *DynamicCardContent
	Updates        ComponentList
	CurrentContent *DynamicCardContent
}

// UpdateResult is the normalized successful update response. Sequence is the
// new card version returned by the platform; it can be passed as the
// enable.sequence of a subsequent stream on the same card. It is 0 together
// with a WarningSequenceMissing warning when the platform omitted it.
type UpdateResult struct {
	CardID   string
	Sequence int64
	Message  string
	Warnings []Warning
	Raw      json.RawMessage
}

// RawJSON returns the original update response, defensively copied.
func (r UpdateResult) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), r.Raw...))
}

type updatePayload struct {
	CardID      string              `json:"card_id"`
	UpdateType  UpdateType          `json:"update_type"`
	CardContent *DynamicCardContent `json:"card_content,omitempty"`
	Updates     ComponentList       `json:"updates,omitempty"`
}

// Update replaces full card content or individual components of an existing
// dynamic card. Local structural and baseline validation failures return a
// *DynamicCardError with CategoryValidation without sending a request. A 1010
// response is returned as CategoryStreamState whose message contains
// "请先 finish 流式消息" and, when present, carries CurrentSequence and
// CurrentContent. Updates are never retried automatically, including 1001,
// 3003, 6001 and 9001.
func (s Service) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	payload, warnings, err := buildUpdatePayload(request)
	if err != nil {
		return UpdateResult{}, err
	}
	response, err := s.post(ctx, dcinternal.RouteUpdate, payload, OperationUpdate)
	if err != nil {
		if failure, ok := err.(*DynamicCardError); ok {
			enrichStreamStateFailure(failure, response)
		}
		return UpdateResult{}, err
	}
	return parseUpdateResult(response, request, warnings)
}

func buildUpdatePayload(request UpdateRequest) (updatePayload, []Warning, error) {
	if strings.TrimSpace(request.CardID) == "" {
		return updatePayload{}, nil, newValidationError("cardId", "must not be blank")
	}
	switch request.UpdateType {
	case UpdateTypeFull:
		return buildFullUpdate(request)
	case UpdateTypeElement:
		return buildElementUpdate(request)
	default:
		return updatePayload{}, nil, newValidationError("updateType", `must be "full" or "element"`)
	}
}

func buildFullUpdate(request UpdateRequest) (updatePayload, []Warning, error) {
	if request.CardContent == nil {
		return updatePayload{}, nil, newValidationError("cardContent", "is required for a full update")
	}
	if len(request.Updates) != 0 {
		return updatePayload{}, nil, newValidationError("updates", "must be empty for a full update")
	}
	if request.CardContent.Scheme != "" && request.CardContent.Scheme != SchemeV1 {
		return updatePayload{}, nil, newValidationError("cardContent.scheme", `must be "1"`)
	}
	if len(request.CardContent.Body.Children) == 0 {
		return updatePayload{}, nil, newValidationError("cardContent.body.children", "must not be empty")
	}
	content := DynamicCardContent{Scheme: SchemeV1, Body: request.CardContent.Body, Footer: request.CardContent.Footer}
	warnings, err := ValidateContent(&content)
	if err != nil {
		return updatePayload{}, nil, err
	}
	payload := updatePayload{
		CardID:      request.CardID,
		UpdateType:  UpdateTypeFull,
		CardContent: &content,
	}
	return payload, warnings, nil
}

func buildElementUpdate(request UpdateRequest) (updatePayload, []Warning, error) {
	if request.CardContent != nil {
		return updatePayload{}, nil, newValidationError("cardContent", "must be empty for an element update")
	}
	if len(request.Updates) == 0 {
		return updatePayload{}, nil, newValidationError("updates", "must contain at least one component")
	}
	objects, err := elementUpdateObjects(request.Updates)
	if err != nil {
		return updatePayload{}, nil, err
	}

	var warnings []Warning
	if request.CurrentContent != nil {
		baselineWarnings, err := checkElementAgainstBaseline(objects, request.CurrentContent)
		if err != nil {
			return updatePayload{}, nil, err
		}
		warnings = append(warnings, baselineWarnings...)
	}
	payload := updatePayload{
		CardID:     request.CardID,
		UpdateType: UpdateTypeElement,
		Updates:    request.Updates,
	}
	return payload, warnings, nil
}

// elementUpdateObjects validates the element-update item rules shared by
// Update and the ACK card_update: every item is a complete component with a
// non-empty type and id, ids are globally unique in the request, and no
// ancestor/descendant pair is updated together. It returns the rendered objects
// for the optional baseline checks.
func elementUpdateObjects(updates ComponentList) ([]map[string]any, error) {
	return elementUpdateObjectsAt("updates", updates)
}

// elementUpdateObjectsAt is elementUpdateObjects with a caller-supplied field
// prefix so nested diagnostics name the real request path.
func elementUpdateObjectsAt(path string, updates ComponentList) ([]map[string]any, error) {
	objects := make([]map[string]any, len(updates))
	topLevel := make(map[string]struct{}, len(updates))
	for index, component := range updates {
		field := fmt.Sprintf("%s[%d]", path, index)
		if component == nil {
			return nil, newValidationError(field, "component must not be nil")
		}
		object, err := componentObject(component, field)
		if err != nil {
			return nil, err
		}
		if text, _ := object["type"].(string); strings.TrimSpace(text) == "" {
			return nil, newValidationError(field+".type", "must not be empty")
		}
		id, _ := object["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, newValidationError(field+".id", "must not be empty")
		}
		if _, exists := topLevel[id]; exists {
			return nil, newValidationError(field+".id", fmt.Sprintf("duplicate id %q", id))
		}
		topLevel[id] = struct{}{}
		objects[index] = object
	}

	updated := make(map[string]struct{}, len(objects))
	seen := make(map[string]struct{}, len(topLevel))
	for id := range topLevel {
		seen[id] = struct{}{}
	}
	for index, object := range objects {
		field := fmt.Sprintf("%s[%d]", path, index)
		for _, descendant := range descendantIDs(object) {
			if _, conflicts := topLevel[descendant]; conflicts {
				return nil, newValidationError(field+".children",
					fmt.Sprintf("ancestor and descendant %q updated together", descendant))
			}
		}
		if err := checkNestedComponents(object, field, seen, updated); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// componentObject renders one component as a generic JSON object so nested
// completeness and id checks also cover RawComponent and caller-defined
// Component implementations.
func componentObject(component Component, field string) (map[string]any, error) {
	raw, err := json.Marshal(component)
	if err != nil {
		return nil, newValidationError(field, "cannot be encoded: "+err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, newValidationError(field, "must encode a JSON object")
	}
	return object, nil
}

// descendantIDs returns every id nested under object's children arrays.
func descendantIDs(object map[string]any) []string {
	children, _ := object["children"].([]any)
	ids := make([]string, 0, len(children))
	for _, child := range children {
		childObject, ok := child.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := childObject["id"].(string); ok && strings.TrimSpace(id) != "" {
			ids = append(ids, strings.TrimSpace(id))
		}
		ids = append(ids, descendantIDs(childObject)...)
	}
	return ids
}

// checkNestedComponents enforces that every nested component carries a
// non-empty type and id, and that no id repeats within the request.
func checkNestedComponents(object map[string]any, field string, seen, updated map[string]struct{}) error {
	children, _ := object["children"].([]any)
	for index, child := range children {
		childObject, ok := child.(map[string]any)
		if !ok {
			return newValidationError(fmt.Sprintf("%s.children[%d]", field, index), "component must be a JSON object")
		}
		childField := fmt.Sprintf("%s.children[%d]", field, index)
		if text, _ := childObject["type"].(string); strings.TrimSpace(text) == "" {
			return newValidationError(childField+".type", "must not be empty")
		}
		id, _ := childObject["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return newValidationError(childField+".id", "must not be empty")
		}
		if _, exists := seen[id]; exists {
			return newValidationError(childField+".id", fmt.Sprintf("duplicate id %q", id))
		}
		seen[id] = struct{}{}
		updated[id] = struct{}{}
		if err := checkNestedComponents(childObject, childField, seen, updated); err != nil {
			return err
		}
	}
	return nil
}

type baselineComponent struct {
	componentType string
	ancestors     map[string]struct{}
	childCount    int
}

func checkElementAgainstBaseline(updates []map[string]any, current *DynamicCardContent) ([]Warning, error) {
	if current.Scheme != "" && current.Scheme != SchemeV1 {
		return nil, newValidationError("currentContent.scheme", `must be "1"`)
	}
	index := make(map[string]baselineComponent)
	if err := indexBaselineChildren(current.Body.Children, nil, index); err != nil {
		return nil, err
	}
	if current.Footer != nil {
		if err := indexBaselineChildren(current.Footer.Children, nil, index); err != nil {
			return nil, err
		}
	}
	updated := make(map[string]struct{}, len(updates))
	var warnings []Warning
	for position, object := range updates {
		field := fmt.Sprintf("updates[%d]", position)
		id, _ := object["id"].(string)
		id = strings.TrimSpace(id)
		entry, exists := index[id]
		if !exists {
			return nil, newValidationError(field+".id", fmt.Sprintf("%q does not exist in currentContent", id))
		}
		updateType, _ := object["type"].(string)
		if strings.TrimSpace(updateType) != entry.componentType {
			return nil, newValidationError(field+".type",
				fmt.Sprintf("component %q is %q in currentContent and cannot change to %q",
					id, entry.componentType, strings.TrimSpace(updateType)))
		}
		for ancestor := range entry.ancestors {
			if _, conflicts := updated[ancestor]; conflicts {
				return nil, newValidationError(field+".id",
					fmt.Sprintf("ancestor and descendant %q updated together", ancestor))
			}
		}
		updated[id] = struct{}{}

		children, _ := object["children"].([]any)
		if entry.childCount > 0 && len(children) == 0 && isContainerType(entry.componentType) {
			warnings = append(warnings, Warning{
				Code: WarningContainerChildrenMissing,
				Message: fmt.Sprintf("component %q has %d children in currentContent but the update omits children",
					id, entry.childCount),
			})
		}
	}
	return warnings, nil
}

func indexBaselineChildren(children ComponentList, ancestors map[string]struct{}, index map[string]baselineComponent) error {
	for _, component := range children {
		if component == nil {
			return newValidationError("currentContent.children", "component must not be nil")
		}
		object, err := componentObject(component, "currentContent.children")
		if err != nil {
			return err
		}
		componentType, _ := object["type"].(string)
		componentType = strings.TrimSpace(componentType)
		if componentType == "" {
			return newValidationError("currentContent.children.type", "must not be empty")
		}
		id, _ := object["id"].(string)
		id = strings.TrimSpace(id)
		if id != "" {
			if _, exists := index[id]; exists {
				return newValidationError("currentContent.id", fmt.Sprintf("duplicate id %q", id))
			}
			nested, _ := object["children"].([]any)
			index[id] = baselineComponent{
				componentType: componentType,
				ancestors:     ancestors,
				childCount:    len(nested),
			}
		}
		nested, _ := object["children"].([]any)
		if len(nested) > 0 {
			next := make(map[string]struct{}, len(ancestors)+1)
			for ancestor := range ancestors {
				next[ancestor] = struct{}{}
			}
			if id != "" {
				next[id] = struct{}{}
			}
			childList, err := componentListFromJSON(nested, "currentContent.children")
			if err != nil {
				return err
			}
			if err := indexBaselineChildren(childList, next, index); err != nil {
				return err
			}
		}
	}
	return nil
}

func componentListFromJSON(values []any, field string) (ComponentList, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, newValidationError(field, "cannot be encoded: "+err.Error())
	}
	var list ComponentList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, newValidationError(field, "cannot be decoded: "+err.Error())
	}
	return list, nil
}

func isContainerType(componentType string) bool {
	return componentType == ComponentTypeRow || componentType == ComponentTypeColumn
}

func parseUpdateResult(response CardResponse, request UpdateRequest, warnings []Warning) (UpdateResult, error) {
	if len(response.Payload) == 0 {
		return UpdateResult{}, missingResponseField("business payload", OperationUpdate, response)
	}
	var payload struct {
		CardID   json.RawMessage `json:"card_id"`
		Sequence json.RawMessage `json:"sequence"`
		Message  json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		return UpdateResult{}, missingResponseField("business data", OperationUpdate, response)
	}
	result := UpdateResult{
		CardID:   scalarJSONValue(payload.CardID),
		Message:  response.Message,
		Warnings: warnings,
		Raw:      response.RawJSON(),
	}
	if result.CardID == "" {
		result.CardID = request.CardID
	}
	if message := scalarJSONValue(payload.Message); message != "" {
		result.Message = message
	}
	sequence, present, err := parseSequence(payload.Sequence)
	if err != nil {
		return UpdateResult{}, &DynamicCardError{
			Category:   CategoryServer,
			Message:    "response sequence is invalid",
			HTTPStatus: response.HTTPStatus,
			Operation:  OperationUpdate,
			LogID:      response.LogID,
			Raw:        response.RawJSON(),
		}
	}
	if present {
		result.Sequence = sequence
	} else {
		result.Warnings = append(result.Warnings, Warning{
			Code:    WarningSequenceMissing,
			Message: "the update response did not carry a sequence; keep the previous value",
		})
	}
	return result, nil
}

func parseSequence(raw json.RawMessage) (int64, bool, error) {
	text := scalarJSONValue(raw)
	if text == "" {
		return 0, false, nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

// enrichStreamStateFailure adds the contract's fixed finish-first hint and any
// current_sequence / current_msg_content that accompanied a 1010 response.
func enrichStreamStateFailure(failure *DynamicCardError, response CardResponse) {
	if failure == nil || !failure.IsStreamState() {
		return
	}
	const hint = "请先 finish 流式消息"
	switch {
	case failure.Message == "":
		failure.Message = hint + "后再更新"
	case !strings.Contains(failure.Message, hint):
		failure.Message = hint + "后再更新: " + failure.Message
	}
	source := response.Payload
	if len(source) == 0 {
		source = response.Raw
	}
	if raw := firstObjectField(source, "current_sequence"); raw != nil {
		if value, err := strconv.ParseInt(scalarJSONValue(raw), 10, 64); err == nil {
			failure.CurrentSequence = &value
		}
	}
	if raw := firstObjectField(source, "current_msg_content"); raw != nil {
		var content DynamicCardContent
		if err := json.Unmarshal(raw, &content); err == nil {
			failure.CurrentContent = &content
		}
	}
}
