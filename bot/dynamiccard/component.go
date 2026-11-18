package dynamiccard

import (
	"bytes"
	"encoding/json"
)

// Component type discriminators used on the wire.
const (
	ComponentTypeMarkdown   = "markdown"
	ComponentTypeText       = "text"
	ComponentTypeButton     = "button"
	ComponentTypeInput      = "input"
	ComponentTypeRadioGroup = "radio_group"
	ComponentTypeRow        = "row"
	ComponentTypeColumn     = "column"
	ComponentTypeActionBar  = "action_bar"
)

// Action bar item keys and their platform/title enums.
const (
	ActionKeyAITag    = "ai_tag"
	ActionKeyTTS      = "tts"
	ActionKeyVoteUp   = "vote_up"
	ActionKeyVoteDown = "vote_down"
	ActionKeyForward  = "forward"

	ActionPlatformAll    = "all"
	ActionPlatformPC     = "pc"
	ActionPlatformMobile = "mobile"

	ActionOpenTypeNavigation = "N"
	ActionOpenTypeWebView    = "A"
	ActionOpenTypeSidebar    = "S"
)

// Button action methods and enum values.
const (
	ButtonMethodDataSubmit = "data_submit"
	ButtonMethodQuery      = "query"
	ButtonMethodJSSDK      = "jssdk"

	ButtonStylePrimary = "primary"
	ButtonStyleSuccess = "success"
	ButtonStyleDanger  = "danger"

	ButtonWidthWrap = "wrap"

	DirectionRow    = "row"
	DirectionColumn = "column"
)

// Component is one element of a dynamic-card body or footer. Implementations
// are the eight scheme=1 components plus RawComponent, which carries unknown or
// id-less components verbatim.
type Component interface {
	json.Marshaler
	// ComponentType returns the wire "type" discriminator.
	ComponentType() string
}

// ComponentList is an ordered list of components. It decodes each element by
// its "type" discriminator and falls back to RawComponent for unknown types.
type ComponentList []Component

// MarshalJSON encodes the list, representing an empty list as [] rather than
// null.
func (l ComponentList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	entries := make([]json.RawMessage, 0, len(l))
	for _, component := range l {
		if component == nil {
			entries = append(entries, json.RawMessage("null"))
			continue
		}
		raw, err := component.MarshalJSON()
		if err != nil {
			return nil, err
		}
		entries = append(entries, json.RawMessage(raw))
	}
	return json.Marshal(entries)
}

// UnmarshalJSON decodes each element through decodeComponent.
func (l *ComponentList) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*l = nil
		return nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return err
	}
	components := make(ComponentList, 0, len(entries))
	for _, entry := range entries {
		component, err := decodeComponent(entry)
		if err != nil {
			return err
		}
		components = append(components, component)
	}
	*l = components
	return nil
}

func decodeComponent(raw json.RawMessage) (Component, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	var component Component
	switch head.Type {
	case ComponentTypeMarkdown:
		component = &MarkdownComponent{}
	case ComponentTypeText:
		component = &TextComponent{}
	case ComponentTypeButton:
		component = &ButtonComponent{}
	case ComponentTypeInput:
		component = &InputComponent{}
	case ComponentTypeRadioGroup:
		component = &RadioGroupComponent{}
	case ComponentTypeRow:
		component = &RowComponent{}
	case ComponentTypeColumn:
		component = &ColumnComponent{}
	case ComponentTypeActionBar:
		component = &ActionBarComponent{}
	default:
		component = &RawComponent{}
	}
	if err := json.Unmarshal(raw, component); err != nil {
		return nil, err
	}
	return component, nil
}

// MarkdownComponent renders Markdown. Its id is required for streaming and
// partial updates.
type MarkdownComponent struct {
	Type    string                     `json:"type"`
	ID      string                     `json:"id"`
	Content string                     `json:"content"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// NewMarkdown builds a markdown component.
func NewMarkdown(id, content string) *MarkdownComponent {
	return &MarkdownComponent{Type: ComponentTypeMarkdown, ID: id, Content: content}
}

// ComponentType implements Component.
func (*MarkdownComponent) ComponentType() string { return ComponentTypeMarkdown }

func (c *MarkdownComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c MarkdownComponent) MarshalJSON() ([]byte, error) {
	type alias MarkdownComponent
	value := alias(c)
	value.Type = ComponentTypeMarkdown
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *MarkdownComponent) UnmarshalJSON(raw []byte) error {
	type alias MarkdownComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra, "type", "id", "content"); err != nil {
		return err
	}
	*c = MarkdownComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeMarkdown
	return nil
}

// TextComponent renders plain text without Markdown parsing.
type TextComponent struct {
	Type    string                     `json:"type"`
	ID      string                     `json:"id"`
	Content string                     `json:"content"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// NewText builds a text component.
func NewText(id, content string) *TextComponent {
	return &TextComponent{Type: ComponentTypeText, ID: id, Content: content}
}

// ComponentType implements Component.
func (*TextComponent) ComponentType() string { return ComponentTypeText }

func (c *TextComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c TextComponent) MarshalJSON() ([]byte, error) {
	type alias TextComponent
	value := alias(c)
	value.Type = ComponentTypeText
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *TextComponent) UnmarshalJSON(raw []byte) error {
	type alias TextComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra, "type", "id", "content"); err != nil {
		return err
	}
	*c = TextComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeText
	return nil
}

// ButtonComponent is an interactive button; its id is reported back as
// element_id in the card event.
type ButtonComponent struct {
	Type    string                     `json:"type"`
	ID      string                     `json:"id"`
	Text    string                     `json:"text"`
	Style   string                     `json:"style,omitempty"`
	Width   string                     `json:"width,omitempty"`
	Enabled *bool                      `json:"enabled,omitempty"`
	Value   string                     `json:"value,omitempty"`
	Action  *ButtonAction              `json:"action,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// NewButton builds a button component.
func NewButton(id, text string) *ButtonComponent {
	return &ButtonComponent{Type: ComponentTypeButton, ID: id, Text: text}
}

// ComponentType implements Component.
func (*ButtonComponent) ComponentType() string { return ComponentTypeButton }

func (c *ButtonComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c ButtonComponent) MarshalJSON() ([]byte, error) {
	type alias ButtonComponent
	value := alias(c)
	value.Type = ComponentTypeButton
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *ButtonComponent) UnmarshalJSON(raw []byte) error {
	type alias ButtonComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra,
		"type", "id", "text", "style", "width", "enabled", "value", "action"); err != nil {
		return err
	}
	*c = ButtonComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeButton
	return nil
}

// ButtonAction is the click action of a button. A missing action means the
// click is swallowed by the platform.
type ButtonAction struct {
	Method string   `json:"method"`
	Views  []string `json:"views,omitempty"`
	Value  string   `json:"value,omitempty"`
	Silent *bool    `json:"silent,omitempty"`
}

// UnmarshalJSON accepts both the documented string array and the single string
// compatibility form for views.
func (a *ButtonAction) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Method string          `json:"method"`
		Views  json.RawMessage `json:"views"`
		Value  string          `json:"value"`
		Silent *bool           `json:"silent"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	views, err := decodeViews(wire.Views)
	if err != nil {
		return err
	}
	*a = ButtonAction{Method: wire.Method, Views: views, Value: wire.Value, Silent: wire.Silent}
	return nil
}

func decodeViews(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var views []string
		if err := json.Unmarshal(trimmed, &views); err != nil {
			return nil, err
		}
		return views, nil
	}
	var view string
	if err := json.Unmarshal(trimmed, &view); err != nil {
		return nil, err
	}
	return []string{view}, nil
}

// InputComponent is a text input; its id is the submitted field name.
type InputComponent struct {
	Type        string                     `json:"type"`
	ID          string                     `json:"id"`
	Value       string                     `json:"value,omitempty"`
	Placeholder string                     `json:"placeholder,omitempty"`
	Max         *int                       `json:"max,omitempty"`
	Lines       *int                       `json:"lines,omitempty"`
	Enabled     *bool                      `json:"enabled,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

// NewInput builds an input component.
func NewInput(id string) *InputComponent {
	return &InputComponent{Type: ComponentTypeInput, ID: id}
}

// ComponentType implements Component.
func (*InputComponent) ComponentType() string { return ComponentTypeInput }

func (c *InputComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c InputComponent) MarshalJSON() ([]byte, error) {
	type alias InputComponent
	value := alias(c)
	value.Type = ComponentTypeInput
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *InputComponent) UnmarshalJSON(raw []byte) error {
	type alias InputComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra,
		"type", "id", "value", "placeholder", "max", "lines", "enabled"); err != nil {
		return err
	}
	*c = InputComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeInput
	return nil
}

// RadioValue is the radio_group selection: Single for a single-select group,
// Multiple for a multi-select group. Multiple wins when both are set.
type RadioValue struct {
	Single   string
	Multiple []string
}

// MarshalJSON encodes the selected form only.
func (v RadioValue) MarshalJSON() ([]byte, error) {
	if v.Multiple != nil {
		return json.Marshal(v.Multiple)
	}
	return json.Marshal(v.Single)
}

// UnmarshalJSON accepts either a JSON string or a JSON string array.
func (v *RadioValue) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return json.Unmarshal(raw, &v.Multiple)
	}
	return json.Unmarshal(raw, &v.Single)
}

// RadioOption is one selectable option of a radio group.
type RadioOption struct {
	Text    string `json:"text"`
	Value   string `json:"value,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// RadioGroupComponent is a single- or multi-select group; its id is the
// submitted field name.
type RadioGroupComponent struct {
	Type      string                     `json:"type"`
	ID        string                     `json:"id"`
	Multiple  *bool                      `json:"multiple,omitempty"`
	Value     *RadioValue                `json:"value,omitempty"`
	Direction string                     `json:"direction,omitempty"`
	Enabled   *bool                      `json:"enabled,omitempty"`
	Options   []RadioOption              `json:"options"`
	Extra     map[string]json.RawMessage `json:"-"`
}

// NewRadioGroup builds a radio group component.
func NewRadioGroup(id string, options ...RadioOption) *RadioGroupComponent {
	if options == nil {
		options = []RadioOption{}
	}
	return &RadioGroupComponent{Type: ComponentTypeRadioGroup, ID: id, Options: options}
}

// ComponentType implements Component.
func (*RadioGroupComponent) ComponentType() string { return ComponentTypeRadioGroup }

func (c *RadioGroupComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c RadioGroupComponent) MarshalJSON() ([]byte, error) {
	type alias RadioGroupComponent
	value := alias(c)
	value.Type = ComponentTypeRadioGroup
	if value.Options == nil {
		value.Options = []RadioOption{}
	}
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *RadioGroupComponent) UnmarshalJSON(raw []byte) error {
	type alias RadioGroupComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra,
		"type", "id", "multiple", "value", "direction", "enabled", "options"); err != nil {
		return err
	}
	*c = RadioGroupComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeRadioGroup
	return nil
}

// RowComponent lays its children out horizontally; children may nest.
type RowComponent struct {
	Type      string                     `json:"type"`
	ID        string                     `json:"id"`
	Children  ComponentList              `json:"children"`
	MainAxis  string                     `json:"main_axis,omitempty"`
	CrossAxis string                     `json:"cross_axis,omitempty"`
	Spacing   *int                       `json:"spacing,omitempty"`
	Wrap      *bool                      `json:"wrap,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

// NewRow builds a row component.
func NewRow(id string, children ...Component) *RowComponent {
	if children == nil {
		children = ComponentList{}
	}
	return &RowComponent{Type: ComponentTypeRow, ID: id, Children: ComponentList(children)}
}

// ComponentType implements Component.
func (*RowComponent) ComponentType() string { return ComponentTypeRow }

func (c *RowComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c RowComponent) MarshalJSON() ([]byte, error) {
	type alias RowComponent
	value := alias(c)
	value.Type = ComponentTypeRow
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *RowComponent) UnmarshalJSON(raw []byte) error {
	type alias RowComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra,
		"type", "id", "children", "main_axis", "cross_axis", "spacing", "wrap"); err != nil {
		return err
	}
	*c = RowComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeRow
	return nil
}

// ColumnComponent lays its children out vertically; children may nest. The
// platform does not support wrap on a column, so a decoded wrap field is
// dropped.
type ColumnComponent struct {
	Type      string                     `json:"type"`
	ID        string                     `json:"id"`
	Children  ComponentList              `json:"children"`
	MainAxis  string                     `json:"main_axis,omitempty"`
	CrossAxis string                     `json:"cross_axis,omitempty"`
	Spacing   *int                       `json:"spacing,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

// NewColumn builds a column component.
func NewColumn(id string, children ...Component) *ColumnComponent {
	if children == nil {
		children = ComponentList{}
	}
	return &ColumnComponent{Type: ComponentTypeColumn, ID: id, Children: ComponentList(children)}
}

// ComponentType implements Component.
func (*ColumnComponent) ComponentType() string { return ComponentTypeColumn }

func (c *ColumnComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its protocol type and flattened extra.
func (c ColumnComponent) MarshalJSON() ([]byte, error) {
	type alias ColumnComponent
	value := alias(c)
	value.Type = ComponentTypeColumn
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *ColumnComponent) UnmarshalJSON(raw []byte) error {
	type alias ColumnComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra,
		"type", "id", "children", "main_axis", "cross_axis", "spacing", "wrap"); err != nil {
		return err
	}
	*c = ColumnComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeColumn
	return nil
}

// ActionBarComponent is the client-rendered operation bar introduced by the
// platform's AI dynamic message guide v37. It is usually mounted under
// contents.footer and is a pure declaration: the client owns icons, layout,
// display order (ai_tag -> tts -> vote_up -> vote_down -> forward) and the vote
// state. The SDK validates only the structural minimum (non-empty id, non-nil
// actions, ai_tag title) and passes unknown keys and fields through.
type ActionBarComponent struct {
	Type    string                     `json:"type"`
	ID      string                     `json:"id"`
	Actions []ActionBarItem            `json:"actions"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// NewActionBar builds an action_bar component; id must be non-empty and is
// reported back as element_id by footer_operation events.
func NewActionBar(id string, actions ...ActionBarItem) *ActionBarComponent {
	if actions == nil {
		actions = []ActionBarItem{}
	}
	return &ActionBarComponent{Type: ComponentTypeActionBar, ID: id, Actions: actions}
}

// ComponentType implements Component.
func (*ActionBarComponent) ComponentType() string { return ComponentTypeActionBar }

func (c *ActionBarComponent) componentID() string { return c.ID }

// MarshalJSON encodes the component with its fixed protocol type and flattened
// extra.
func (c ActionBarComponent) MarshalJSON() ([]byte, error) {
	type alias ActionBarComponent
	value := alias(c)
	value.Type = ComponentTypeActionBar
	return mergeExtra(value, c.Extra)
}

// UnmarshalJSON decodes the component and keeps unknown fields in Extra.
func (c *ActionBarComponent) UnmarshalJSON(raw []byte) error {
	type alias ActionBarComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra, "type", "id", "actions"); err != nil {
		return err
	}
	*c = ActionBarComponent(value)
	c.Extra = extra
	c.Type = ComponentTypeActionBar
	return nil
}

// ActionBarItem is one operation of an ActionBarComponent. Key selects the
// operation (ai_tag / tts / vote_up / vote_down / forward); an unknown key is
// passed through unchanged so a newer platform key keeps working. Title is a
// language map such as {"zh_cn":"有用","en_us":"Helpful"}. EventEnabled only
// controls whether a vote click produces a footer_operation event; it does not
// change clickability. Extra preserves unknown fields verbatim.
type ActionBarItem struct {
	Key          string            `json:"key"`
	Platform     string            `json:"platform,omitempty"`
	EventEnabled *bool             `json:"event_enabled,omitempty"`
	BizExtra     string            `json:"biz_extra,omitempty"`
	Title        map[string]string `json:"title,omitempty"`
	Color        string            `json:"color,omitempty"`
	URL          string            `json:"url,omitempty"`
	OpenType     string            `json:"open_type,omitempty"`
	Extra        map[string]any    `json:"-"`
}

// NewActionBarItem builds an action item with the given key and no other field.
func NewActionBarItem(key string) ActionBarItem {
	return ActionBarItem{Key: key}
}

// MarshalJSON encodes the known fields and flattens unknown extra fields.
func (i ActionBarItem) MarshalJSON() ([]byte, error) {
	type alias ActionBarItem
	return mergeAnyExtra(alias(i), i.Extra)
}

// UnmarshalJSON decodes the known fields and keeps unknown fields in Extra.
func (i *ActionBarItem) UnmarshalJSON(raw []byte) error {
	type alias ActionBarItem
	var value alias
	var extra map[string]any
	if err := splitAnyExtra(raw, &value, &extra,
		"key", "platform", "event_enabled", "biz_extra", "title", "color", "url", "open_type"); err != nil {
		return err
	}
	*i = ActionBarItem(value)
	i.Extra = extra
	return nil
}

// RawComponent carries an unknown component, an id-less component, or a
// component the SDK does not model yet. Type and ID win over same-named extra
// fields, and every other field is passed through verbatim.
type RawComponent struct {
	Type  string                     `json:"type"`
	ID    string                     `json:"id,omitempty"`
	Extra map[string]json.RawMessage `json:"-"`
}

// NewRaw builds a passthrough component.
func NewRaw(componentType, id string, extra map[string]json.RawMessage) *RawComponent {
	return &RawComponent{Type: componentType, ID: id, Extra: extra}
}

// ComponentType implements Component.
func (c *RawComponent) ComponentType() string { return c.Type }

func (c *RawComponent) componentID() string { return c.ID }

// MarshalJSON flattens extra around the explicit type and id.
func (c RawComponent) MarshalJSON() ([]byte, error) {
	type alias RawComponent
	return mergeExtra(alias(c), c.Extra)
}

// UnmarshalJSON keeps every field except type and id in Extra.
func (c *RawComponent) UnmarshalJSON(raw []byte) error {
	type alias RawComponent
	var value alias
	var extra map[string]json.RawMessage
	if err := splitExtra(raw, &value, &extra, "type", "id"); err != nil {
		return err
	}
	*c = RawComponent(value)
	c.Extra = extra
	return nil
}

// Bool returns a pointer to v, for optional boolean component fields where an
// explicit false must survive serialization.
func Bool(v bool) *bool { return &v }

// Int returns a pointer to v, for optional integer component fields where an
// explicit 0 must survive serialization.
func Int(v int) *int { return &v }
