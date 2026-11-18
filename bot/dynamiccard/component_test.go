package dynamiccard

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

// T-10: all seven components serialize with their wire names and discriminators.
func TestComponentsSerialize(t *testing.T) {
	button := NewButton("btn", "Go")
	button.Style = ButtonStylePrimary
	button.Width = ButtonWidthWrap
	button.Enabled = Bool(true)
	button.Action = &ButtonAction{Method: ButtonMethodDataSubmit, Views: []string{"inp"}, Silent: Bool(false)}

	input := NewInput("inp")
	input.Placeholder = "type here"
	input.Max = Int(200)
	input.Lines = Int(1)
	input.Enabled = Bool(true)

	radio := NewRadioGroup("rg",
		RadioOption{Text: "A", Value: "a", Enabled: Bool(true)},
		RadioOption{Text: "B"},
	)
	radio.Multiple = Bool(false)
	radio.Value = &RadioValue{Multiple: []string{"a"}}
	radio.Direction = DirectionRow
	radio.Enabled = Bool(true)

	row := NewRow("row", NewText("t1", "x"))
	row.MainAxis = "center"
	row.CrossAxis = "stretch"
	row.Spacing = Int(0)
	row.Wrap = Bool(true)

	column := NewColumn("col", NewText("t2", "y"))
	column.MainAxis = "start"
	column.CrossAxis = "start"
	column.Spacing = Int(4)

	content := NewContent(
		NewMarkdown("md", "# hi"),
		NewText("txt", "plain"),
		button,
		input,
		radio,
		row,
		column,
	)

	decoded := decodeObject(t, mustMarshal(t, content))
	if decoded["scheme"] != SchemeV1 {
		t.Fatalf("scheme = %v, want %q", decoded["scheme"], SchemeV1)
	}
	body := decoded["body"].(map[string]any)
	children := body["children"].([]any)
	if len(children) != 7 {
		t.Fatalf("children = %d, want 7", len(children))
	}
	wantTypes := []string{
		ComponentTypeMarkdown, ComponentTypeText, ComponentTypeButton, ComponentTypeInput,
		ComponentTypeRadioGroup, ComponentTypeRow, ComponentTypeColumn,
	}
	for index, want := range wantTypes {
		if got := children[index].(map[string]any)["type"]; got != want {
			t.Fatalf("child %d type = %v, want %q", index, got, want)
		}
	}

	markdown := children[0].(map[string]any)
	if markdown["content"] != "# hi" || markdown["id"] != "md" {
		t.Fatalf("markdown = %v", markdown)
	}
	buttonObject := children[2].(map[string]any)
	action := buttonObject["action"].(map[string]any)
	if action["method"] != ButtonMethodDataSubmit {
		t.Fatalf("action.method = %v", action["method"])
	}
	if views := action["views"].([]any); len(views) != 1 || views[0] != "inp" {
		t.Fatalf("action.views = %v", action["views"])
	}
	if silent, ok := action["silent"].(bool); !ok || silent {
		t.Fatalf("action.silent = %v, want explicit false", action["silent"])
	}
	if width := buttonObject["width"]; width != ButtonWidthWrap {
		t.Fatalf("button.width = %v", width)
	}

	inputObject := children[3].(map[string]any)
	if _, ok := inputObject["max"]; !ok {
		t.Fatal("input.max missing")
	}
	if _, ok := inputObject["lines"]; !ok {
		t.Fatal("input.lines missing")
	}

	radioObject := children[4].(map[string]any)
	options := radioObject["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("options = %d, want 2", len(options))
	}
	if value := radioObject["value"].([]any); len(value) != 1 || value[0] != "a" {
		t.Fatalf("radio value = %v", radioObject["value"])
	}
	if direction := radioObject["direction"]; direction != DirectionRow {
		t.Fatalf("direction = %v", direction)
	}

	rowObject := children[5].(map[string]any)
	if _, ok := rowObject["main_axis"]; !ok {
		t.Fatalf("row missing snake_case main_axis: %v", rowObject)
	}
	if _, ok := rowObject["mainAxis"]; ok {
		t.Fatal("row emitted camelCase mainAxis")
	}
	if wrap, ok := rowObject["wrap"].(bool); !ok || !wrap {
		t.Fatalf("row wrap = %v", rowObject["wrap"])
	}

	columnObject := children[6].(map[string]any)
	if _, ok := columnObject["wrap"]; ok {
		t.Fatalf("column emitted wrap: %v", columnObject)
	}
}

// T-11: extra fields pass through and known fields win on collision.
func TestComponentExtraPassthrough(t *testing.T) {
	button := NewButton("btn", "real")
	button.Extra = map[string]json.RawMessage{
		"text":    json.RawMessage(`"override"`),
		"style":   json.RawMessage(`"danger"`),
		"custom":  json.RawMessage(`{"nested":[1,2]}`),
		"ignored": json.RawMessage(` `),
	}
	decoded := decodeObject(t, mustMarshal(t, button))
	if decoded["text"] != "real" {
		t.Fatalf("text = %v, want the known field to win", decoded["text"])
	}
	if decoded["style"] != "danger" {
		t.Fatalf("style = %v, want the extra value for an unset known field", decoded["style"])
	}
	custom, ok := decoded["custom"].(map[string]any)
	if !ok {
		t.Fatalf("custom = %v, want a passthrough object", decoded["custom"])
	}
	if nested := custom["nested"].([]any); len(nested) != 2 {
		t.Fatalf("custom.nested = %v", custom["nested"])
	}
	if _, ok := decoded["ignored"]; ok {
		t.Fatal("blank extra value was emitted")
	}
}

// T-12: unknown components round-trip through RawComponent untouched.
func TestRawComponentRoundTrip(t *testing.T) {
	const wire = `{"type":"chart","id":"c1","series":[1,2],"nested":{"x":true}}`
	var list ComponentList
	if err := json.Unmarshal([]byte(`[`+wire+`]`), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("components = %d, want 1", len(list))
	}
	raw, ok := list[0].(*RawComponent)
	if !ok {
		t.Fatalf("component = %T, want *RawComponent", list[0])
	}
	if raw.Type != "chart" || raw.ID != "c1" {
		t.Fatalf("raw = %+v", raw)
	}
	if raw.ComponentType() != "chart" {
		t.Fatalf("ComponentType() = %q", raw.ComponentType())
	}

	var decoded []map[string]any
	if err := json.Unmarshal(mustMarshal(t, list), &decoded); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	var want []map[string]any
	if err := json.Unmarshal([]byte(`[`+wire+`]`), &want); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("round trip = %v, want %v", decoded, want)
	}

	overridden := RawComponent{
		Type:  "chart",
		ID:    "c2",
		Extra: map[string]json.RawMessage{"type": json.RawMessage(`"series"`), "id": json.RawMessage(`"x"`)},
	}
	object := decodeObject(t, mustMarshal(t, overridden))
	if object["type"] != "chart" || object["id"] != "c2" {
		t.Fatalf("explicit fields did not win: %v", object)
	}
}

// T-13: unset fields are omitted and explicit false/0/"" survive.
func TestComponentOptionalFieldSemantics(t *testing.T) {
	emptyInput := NewInput("inp")
	object := decodeObject(t, mustMarshal(t, emptyInput))
	for _, field := range []string{"value", "placeholder", "max", "lines", "enabled"} {
		if _, ok := object[field]; ok {
			t.Fatalf("unset field %q was serialized: %v", field, object)
		}
	}
	if object["id"] != "inp" || object["type"] != ComponentTypeInput {
		t.Fatalf("required fields = %v", object)
	}

	explicit := NewInput("inp")
	explicit.Enabled = Bool(false)
	explicit.Max = Int(0)
	explicit.Lines = Int(0)
	explicit.Value = ""
	object = decodeObject(t, mustMarshal(t, explicit))
	if enabled, ok := object["enabled"].(bool); !ok || enabled {
		t.Fatalf("enabled = %v, want explicit false", object["enabled"])
	}
	if max, ok := object["max"].(json.Number); !ok || max.String() != "0" {
		t.Fatalf("max = %v, want explicit 0", object["max"])
	}
	if lines, ok := object["lines"].(json.Number); !ok || lines.String() != "0" {
		t.Fatalf("lines = %v, want explicit 0", object["lines"])
	}

	markdown := NewMarkdown("md", "")
	object = decodeObject(t, mustMarshal(t, markdown))
	if value, ok := object["content"].(string); !ok || value != "" {
		t.Fatalf("markdown content = %v, want explicit empty string", object["content"])
	}
}

func TestEmptyContentUsesEmptyChildrenArray(t *testing.T) {
	raw := string(mustMarshal(t, NewContent()))
	if raw != `{"scheme":"1","body":{"children":[]}}` {
		t.Fatalf("empty content = %s", raw)
	}
	if raw := string(mustMarshal(t, DynamicCardContent{})); raw != `{"scheme":"1","body":{"children":[]}}` {
		t.Fatalf("zero-value content = %s (scheme must be injected)", raw)
	}
}

func TestComponentListUnmarshalDispatch(t *testing.T) {
	payload := `[
		{"type":"markdown","id":"m","content":"x"},
		{"type":"text","id":"t","content":"y"},
		{"type":"button","id":"b","text":"go","action":{"method":"query","views":"single"}},
		{"type":"input","id":"i","max":10},
		{"type":"radio_group","id":"r","options":[{"text":"A"}],"value":"A"},
		{"type":"row","id":"rw","children":[{"type":"text","id":"n","content":"z"}]},
		{"type":"column","id":"c","children":[],"wrap":true},
		{"type":"future","id":"f","payload":1}
	]`
	var list ComponentList
	if err := json.Unmarshal([]byte(payload), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []any{
		&MarkdownComponent{}, &TextComponent{}, &ButtonComponent{}, &InputComponent{},
		&RadioGroupComponent{}, &RowComponent{}, &ColumnComponent{}, &RawComponent{},
	}
	for index, expected := range want {
		if reflect.TypeOf(list[index]) != reflect.TypeOf(expected) {
			t.Fatalf("component %d = %T, want %T", index, list[index], expected)
		}
	}
	button := list[2].(*ButtonComponent)
	if button.Action == nil || len(button.Action.Views) != 1 || button.Action.Views[0] != "single" {
		t.Fatalf("single-string views were not normalized: %+v", button.Action)
	}
	row := list[5].(*RowComponent)
	if len(row.Children) != 1 {
		t.Fatalf("row children = %d, want 1", len(row.Children))
	}
	column := list[6].(*ColumnComponent)
	if _, exists := column.Extra["wrap"]; exists {
		t.Fatalf("column wrap stayed in extra: %v", column.Extra)
	}
	if len(column.Children) != 0 {
		t.Fatalf("column children = %d, want 0", len(column.Children))
	}
	if future, ok := list[7].(*RawComponent); !ok || future.Extra["payload"] == nil {
		t.Fatalf("unknown component = %+v", list[7])
	}
}

func TestMarkdownContentMustBeString(t *testing.T) {
	var list ComponentList
	if err := json.Unmarshal([]byte(`[{"type":"markdown","id":"m","content":123}]`), &list); err == nil {
		t.Fatal("numeric markdown content was accepted")
	}
}

func TestComponentUnmarshalKeepsExtra(t *testing.T) {
	var button ButtonComponent
	if err := json.Unmarshal([]byte(`{"type":"button","id":"b","text":"go","trace":"abc"}`), &button); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(button.Extra["trace"]) != `"abc"` {
		t.Fatalf("extra = %v", button.Extra)
	}
	if button.Type != ComponentTypeButton {
		t.Fatalf("type = %q", button.Type)
	}
}

// AB-01 / AB-02: an action_bar keeps its id, the fixed type discriminator and
// the documented fields of each operation key; footer is a sibling of body.
func TestActionBarSerializesInFooter(t *testing.T) {
	bar := NewActionBar("ai_ops",
		ActionBarItem{Key: ActionKeyAITag, Title: map[string]string{"zh_cn": "AI 生成", "en_us": "AI generated"}},
		NewActionBarItem(ActionKeyTTS),
		ActionBarItem{Key: ActionKeyVoteUp, EventEnabled: Bool(true), BizExtra: `{"biz_msg_id":"m1"}`},
		ActionBarItem{Key: ActionKeyVoteDown, EventEnabled: Bool(true), Color: "#999999"},
		ActionBarItem{
			Key:      ActionKeyForward,
			URL:      "https://example.com/a",
			OpenType: ActionOpenTypeWebView,
			Platform: ActionPlatformPC,
		},
	)
	content := NewContent(NewMarkdown("desc", "构建完成"))
	content.Footer = NewFooter(bar)

	decoded := decodeObject(t, mustMarshal(t, content))
	body, ok := decoded["body"].(map[string]any)
	if !ok {
		t.Fatalf("body = %v", decoded["body"])
	}
	if _, exists := body["footer"]; exists {
		t.Fatal("footer must be a sibling of body, not nested inside it")
	}
	footer, ok := decoded["footer"].(map[string]any)
	if !ok {
		t.Fatalf("footer = %v", decoded["footer"])
	}
	children, ok := footer["children"].([]any)
	if !ok || len(children) != 1 {
		t.Fatalf("footer children = %v", footer["children"])
	}
	component := children[0].(map[string]any)
	if component["type"] != ComponentTypeActionBar || component["id"] != "ai_ops" {
		t.Fatalf("action_bar = %v", component)
	}
	actions, ok := component["actions"].([]any)
	if !ok || len(actions) != 5 {
		t.Fatalf("actions = %v", component["actions"])
	}
	wantKeys := []string{ActionKeyAITag, ActionKeyTTS, ActionKeyVoteUp, ActionKeyVoteDown, ActionKeyForward}
	for index, key := range wantKeys {
		if got := actions[index].(map[string]any)["key"]; got != key {
			t.Fatalf("action %d key = %v, want %q", index, got, key)
		}
	}
	aiTag := actions[0].(map[string]any)
	title, ok := aiTag["title"].(map[string]any)
	if !ok || title["zh_cn"] != "AI 生成" || title["en_us"] != "AI generated" {
		t.Fatalf("ai_tag title = %v", aiTag["title"])
	}
	if _, exists := aiTag["event_enabled"]; exists {
		t.Fatalf("unset event_enabled must be omitted: %v", aiTag)
	}
	if _, exists := actions[1].(map[string]any)["title"]; exists {
		t.Fatalf("tts must not synthesize a title: %v", actions[1])
	}
	voteUp := actions[2].(map[string]any)
	if voteUp["event_enabled"] != true || voteUp["biz_extra"] != `{"biz_msg_id":"m1"}` {
		t.Fatalf("vote_up = %v", voteUp)
	}
	voteDown := actions[3].(map[string]any)
	if voteDown["event_enabled"] != true || voteDown["color"] != "#999999" {
		t.Fatalf("vote_down = %v", voteDown)
	}
	forward := actions[4].(map[string]any)
	if forward["url"] != "https://example.com/a" || forward["open_type"] != ActionOpenTypeWebView ||
		forward["platform"] != ActionPlatformPC {
		t.Fatalf("forward = %v", forward)
	}
}

// AB-03: footer and body share the component model, so a decoded footer yields
// the same typed components as a decoded body.
func TestFooterDecodesLikeBody(t *testing.T) {
	payload := `{
	  "scheme": "1",
	  "body": {"children": [{"type": "markdown", "id": "m", "content": "x"}]},
	  "footer": {"children": [
	    {"type": "action_bar", "id": "ops", "actions": [{"key": "vote_up", "event_enabled": true}]},
	    {"type": "text", "id": "note", "content": "y"}
	  ]}
	}`
	var content DynamicCardContent
	if err := json.Unmarshal([]byte(payload), &content); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if content.Footer == nil || len(content.Footer.Children) != 2 {
		t.Fatalf("footer = %+v", content.Footer)
	}
	bar, ok := content.Footer.Children[0].(*ActionBarComponent)
	if !ok {
		t.Fatalf("footer[0] = %T, want *ActionBarComponent", content.Footer.Children[0])
	}
	if bar.ID != "ops" || bar.ComponentType() != ComponentTypeActionBar {
		t.Fatalf("action_bar = %+v", bar)
	}
	if len(bar.Actions) != 1 || bar.Actions[0].Key != ActionKeyVoteUp ||
		bar.Actions[0].EventEnabled == nil || !*bar.Actions[0].EventEnabled {
		t.Fatalf("actions = %+v", bar.Actions)
	}
	if _, ok := content.Footer.Children[1].(*TextComponent); !ok {
		t.Fatalf("footer[1] = %T, want *TextComponent", content.Footer.Children[1])
	}
}

// AB-04: an unknown operation key and unknown fields survive a round trip and
// are never rejected locally.
func TestActionBarPassesUnknownKeysAndFields(t *testing.T) {
	payload := `[{"type":"action_bar","id":"ops","theme":"dark",
	  "actions":[{"key":"future_op","event_enabled":true,"raw_flag":{"nested":1}}]}]`
	var list ComponentList
	if err := json.Unmarshal([]byte(payload), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	bar, ok := list[0].(*ActionBarComponent)
	if !ok {
		t.Fatalf("component = %T", list[0])
	}
	if bar.ID != "ops" || len(bar.Actions) != 1 || bar.Actions[0].Key != "future_op" {
		t.Fatalf("action_bar = %+v", bar)
	}
	if string(bar.Extra["theme"]) != `"dark"` {
		t.Fatalf("component extra = %v", bar.Extra)
	}
	flag, ok := bar.Actions[0].Extra["raw_flag"].(map[string]any)
	if !ok || flag["nested"] == nil {
		t.Fatalf("action extra = %v", bar.Actions[0].Extra)
	}
	if _, exists := bar.Actions[0].Extra["event_enabled"]; exists {
		t.Fatalf("known field leaked into extra: %v", bar.Actions[0].Extra)
	}
	encoded := string(mustMarshal(t, ComponentList{bar}))
	for _, want := range []string{`"theme":"dark"`, `"key":"future_op"`, `"nested":1`, `"event_enabled":true`} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("round trip lost %s: %s", want, encoded)
		}
	}
	if _, err := ValidateContent(&DynamicCardContent{Body: CardBody{Children: list}}); err != nil {
		t.Fatalf("unknown key must not be rejected: %v", err)
	}
}

// AB-01: the structural minimum of action_bar is enforced locally.
func TestActionBarValidation(t *testing.T) {
	cases := []struct {
		name      string
		component *ActionBarComponent
		field     string
	}{
		{"missing id", NewActionBar("", NewActionBarItem(ActionKeyVoteUp)), "component.id"},
		{"nil actions", &ActionBarComponent{Type: ComponentTypeActionBar, ID: "ops"}, "component.actions"},
		{"empty key", NewActionBar("ops", ActionBarItem{}), "component.actions[0].key"},
		{"ai_tag without title", NewActionBar("ops", NewActionBarItem(ActionKeyAITag)), "component.actions[0].title"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			content := NewContent(testCase.component)
			_, err := ValidateContent(content)
			if err == nil {
				t.Fatalf("action_bar was accepted: %+v", testCase.component)
			}
			var cardErr *DynamicCardError
			if !errors.As(err, &cardErr) || cardErr.Category != CategoryValidation {
				t.Fatalf("error = %v, want validation", err)
			}
			if !strings.Contains(err.Error(), testCase.field) {
				t.Fatalf("error = %v, want field %q", err, testCase.field)
			}
		})
	}
	if _, err := ValidateContent(NewContent(NewActionBar("ops",
		ActionBarItem{Key: ActionKeyAITag, Title: map[string]string{"zh_cn": "AI"}},
		NewActionBarItem(ActionKeyVoteUp),
	))); err != nil {
		t.Fatalf("valid action_bar was rejected: %v", err)
	}
}

// IF-05: body config serializes the documented keys and an explicit zero spacing
// survives while a nil max width is omitted.
func TestCardConfigSerializes(t *testing.T) {
	content := NewContent(NewMarkdown("m", "x"))
	content.Body.Config = &CardConfig{MaxWidthDesktop: Int(640), Spacing: Int(0)}
	body := decodeObject(t, mustMarshal(t, content))["body"].(map[string]any)
	config, ok := body["config"].(map[string]any)
	if !ok {
		t.Fatalf("config = %v", body["config"])
	}
	if config["max_width_desktop"] != json.Number("640") || config["spacing"] != json.Number("0") {
		t.Fatalf("config = %v", config)
	}

	content.Body.Config = &CardConfig{}
	encoded := mustMarshal(t, content)
	if strings.Contains(string(encoded), "max_width_desktop") || strings.Contains(string(encoded), "spacing") {
		t.Fatalf("empty config emitted keys: %s", encoded)
	}

	var roundTrip DynamicCardContent
	if err := json.Unmarshal([]byte(`{"scheme":"1","body":{"config":{"max_width_desktop":720,"spacing":12},"children":[]}}`), &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Body.Config == nil || *roundTrip.Body.Config.MaxWidthDesktop != 720 ||
		*roundTrip.Body.Config.Spacing != 12 {
		t.Fatalf("config = %+v", roundTrip.Body.Config)
	}
}
