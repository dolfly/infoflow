package dynamiccard

import "encoding/json"

// SchemeV1 is the only supported dynamic-card content scheme.
const SchemeV1 = "1"

// DynamicCardContent is the scheme=1 card content: a config and an ordered list
// of components. Footer is an optional top-level region that is a sibling of
// body on the wire (contents.footer) and uses the same component model; the
// action_bar component is documented to live there.
type DynamicCardContent struct {
	Scheme string   `json:"scheme"`
	Body   CardBody `json:"body"`
	Footer *Footer  `json:"footer,omitempty"`
}

// CardBody holds the optional card config and the top-level components.
type CardBody struct {
	Config   *CardConfig   `json:"config,omitempty"`
	Children ComponentList `json:"children"`
}

// Footer is the optional top-level contents.footer region. It is structurally
// identical to CardBody (children plus an optional config) and accepts the same
// components, including action_bar. An empty footer is omitted when nil.
type Footer struct {
	Config   *CardConfig   `json:"config,omitempty"`
	Children ComponentList `json:"children"`
}

// NewFooter builds a top-level footer from the given components.
func NewFooter(children ...Component) *Footer {
	if children == nil {
		children = ComponentList{}
	}
	return &Footer{Children: ComponentList(children)}
}

// CardConfig holds the card-level layout config. The desktop max width applies
// only when positive; mobile clients ignore it.
type CardConfig struct {
	MaxWidthDesktop *int `json:"max_width_desktop,omitempty"`
	Spacing         *int `json:"spacing,omitempty"`
}

// NewContent builds a scheme=1 content value from the given components.
func NewContent(children ...Component) *DynamicCardContent {
	if children == nil {
		children = ComponentList{}
	}
	return &DynamicCardContent{
		Scheme: SchemeV1,
		Body:   CardBody{Children: ComponentList(children)},
	}
}

// MarshalJSON injects the protocol-fixed scheme "1" when the caller left it
// empty; any other explicit value is preserved so validation can report it.
func (c DynamicCardContent) MarshalJSON() ([]byte, error) {
	type alias DynamicCardContent
	value := alias(c)
	if value.Scheme == "" {
		value.Scheme = SchemeV1
	}
	return json.Marshal(value)
}
