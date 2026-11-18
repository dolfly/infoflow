package dynamiccard

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Markdown is the pure-string Markdown helper namespace of an AI dynamic-card
// markdown component. Every method only builds text: it never sends a request
// and never changes the message structure. An invalid argument returns a
// *DynamicCardError with CategoryValidation. Output bytes match the platform's
// robot Markdown syntax (see spec-markdown-syntax.md).
//
// The @ helpers only render the display text. The structured @ declaration
// still belongs to SendRequest.At / StreamOptions.At; the SDK never scans the
// Markdown body and injects at automatically.
var Markdown = MarkdownHelper{}

// MarkdownHelper is the namespace type of Markdown. Its zero value is usable;
// Markdown is the exported instance.
type MarkdownHelper struct{}

// Markdown colour values accepted by Color.
const (
	MarkdownColorRed    = "red"
	MarkdownColorGreen  = "green"
	MarkdownColorGray   = "gray"
	MarkdownColorBlue   = "blue"
	MarkdownColorOrange = "orange"
)

// MarkdownButtonGroupLayout values accepted by MarkdownButtonGroupOptions.
const (
	MarkdownLayoutFlow     = "flow"
	MarkdownLayoutEquant   = "equant"
	MarkdownLayoutVertical = "vertical"
	MarkdownLayoutBanner   = "banner"
)

// MarkdownButtonOpenType values accepted by MarkdownButton.
const (
	MarkdownOpenTypeN = "N"
	MarkdownOpenTypeS = "S"
	MarkdownOpenTypeA = "A"
	MarkdownOpenTypeB = "B"
)

// AgentFlowBlockKind values.
const (
	AgentFlowBlockKindReasoning = "reasoning"
	AgentFlowBlockKindTool      = "tool"
)

// AgentFlowStatus values of an agent-flow-stream fence header.
const (
	AgentFlowStatusStreaming = "streaming"
	AgentFlowStatusCompleted = "completed"
)

// AgentFlowToolStatus values of a tool block.
const (
	AgentFlowToolStatusRunning   = "running"
	AgentFlowToolStatusCompleted = "completed"
	AgentFlowToolStatusError     = "error"
)

// MaxSafeMarkdownID is the largest numeric identifier that stays exact as an
// IEEE-754 double (2^53-1). Larger ids must be passed as strings.
const MaxSafeMarkdownID int64 = 1<<53 - 1

var (
	markdownColors = map[string]struct{}{
		MarkdownColorRed: {}, MarkdownColorGreen: {}, MarkdownColorGray: {},
		MarkdownColorBlue: {}, MarkdownColorOrange: {},
	}
	markdownLayouts = map[string]struct{}{
		MarkdownLayoutFlow: {}, MarkdownLayoutEquant: {},
		MarkdownLayoutVertical: {}, MarkdownLayoutBanner: {},
	}
	markdownOpenTypes = map[string]struct{}{
		MarkdownOpenTypeN: {}, MarkdownOpenTypeS: {}, MarkdownOpenTypeA: {}, MarkdownOpenTypeB: {},
	}
	agentFlowStatuses = map[string]struct{}{
		AgentFlowStatusStreaming: {}, AgentFlowStatusCompleted: {},
	}
	agentFlowToolStatuses = map[string]struct{}{
		AgentFlowToolStatusRunning: {}, AgentFlowToolStatusCompleted: {}, AgentFlowToolStatusError: {},
	}
)

// MarkdownImageOptions carries the optional alt text and title of Image.
type MarkdownImageOptions struct {
	// Alt is the image alt text; empty renders an empty alt.
	Alt string
	// Title is the image title; empty omits the title suffix.
	Title string
}

// MarkdownReplyQuoteOptions configures ReplyQuote.
type MarkdownReplyQuoteOptions struct {
	// SenderName is the quoted sender display name; required.
	SenderName string
	// BaseMsgID is the quoted message id (msg_sbasemsgid); required. Pass the
	// 19-digit decimal string form.
	BaseMsgID string
	// MsgID2 is the optional secondary id (s_msgid2); empty omits it.
	MsgID2 string
	// SimType is the quote type; empty defaults to "1".
	SimType string
	// Digest is the quoted preview text; required and must not contain a
	// triple-backtick fence.
	Digest string
}

// MarkdownButton is one client-local button of ButtonGroup. At least one of
// URL, Interact, Query, QuerySend, QuerySendAt or QueryToInput must be set.
type MarkdownButton struct {
	// Label is the button text; required.
	Label string
	// URL opens a link; pair it with OpenType.
	URL string
	// OpenType is one of N, S, A, B.
	OpenType string
	// Interact is a client callback key; not effective in group chats.
	Interact string
	// Query is a URL query parameter.
	Query string
	// QuerySend sends a message to the current conversation.
	QuerySend string
	// QuerySendAt sends a message and mentions a target.
	QuerySendAt string
	// QueryToInput fills the input box.
	QueryToInput string
	// Style is the button style, for example "primary".
	Style string
}

// MarkdownButtonGroupOptions configures ButtonGroup. Nil pointers omit the
// attribute.
type MarkdownButtonGroupOptions struct {
	// Layout is one of flow (default), equant, vertical, banner.
	Layout string
	// GapH is the horizontal gap; nil omits gap_h.
	GapH *float64
	// GapV is the vertical gap; nil omits gap_v.
	GapV *float64
	// Radius is the corner radius (platform convention 8 or 16); nil omits it.
	Radius *float64
}

// AgentFlowBlock is one reasoning or tool block of AgentFlowStream.
type AgentFlowBlock struct {
	// Kind is AgentFlowBlockKindReasoning or AgentFlowBlockKindTool.
	Kind string
	// Text is the reasoning text; used when Kind is reasoning.
	Text string
	// Name is the tool name; required and non-blank when Kind is tool.
	Name string
	// Status is the tool status: running (default), completed or error.
	Status string
	// Input is the tool input: a string is emitted verbatim, any other value
	// is JSON-serialized and must be an object.
	Input any
	// Output is the tool output, with the same rules as Input.
	Output any
}

// AgentFlowOptions configures AgentFlowStream.
type AgentFlowOptions struct {
	// ThinkingExpanded controls the header thinking_expanded attribute; nil
	// defaults to true.
	ThinkingExpanded *bool
	// Status is the header status: streaming (default) or completed.
	Status string
	// Blocks are the reasoning and tool blocks in chronological order.
	Blocks []AgentFlowBlock
}

// Float returns a pointer to v, for optional numeric Markdown options where an
// explicit 0 must survive serialization.
func Float(v float64) *float64 { return &v }

func markdownError(field, message string) *DynamicCardError {
	return newValidationError(field, message)
}

// AtUser renders "@userId".
func (MarkdownHelper) AtUser(userID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", markdownError("markdown.at.userId", "must not be blank")
	}
	return "@" + userID, nil
}

// AtAgent renders "@agentId". It accepts a string or a safe non-negative
// integer; a 19-digit id must be passed as a string.
func (MarkdownHelper) AtAgent(agentID any) (string, error) {
	id, err := markdownID(agentID, "markdown.atAgent.agentId")
	if err != nil {
		return "", err
	}
	return "@" + id, nil
}

// AtAll renders "@all".
func (MarkdownHelper) AtAll() (string, error) {
	return "@all", nil
}

// Color renders `<font color="color">text</font>`; color is one of red,
// green, gray, blue or orange.
func (MarkdownHelper) Color(text, color string) (string, error) {
	if _, ok := markdownColors[color]; !ok {
		return "", markdownError("markdown.color", "must be one of red, green, gray, blue, orange")
	}
	return `<font color="` + color + `">` + text + "</font>", nil
}

// Link renders `[text](url)`.
func (MarkdownHelper) Link(text, url string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", markdownError("markdown.link.url", "must not be blank")
	}
	return "[" + text + "](" + url + ")", nil
}

// Image renders `![alt](url "title")`. A nil options value renders an empty
// alt and no title.
func (MarkdownHelper) Image(url string, options *MarkdownImageOptions) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", markdownError("markdown.image.url", "must not be blank")
	}
	alt := ""
	titleSuffix := ""
	if options != nil {
		alt = options.Alt
		if options.Title != "" {
			titleSuffix = ` "` + options.Title + `"`
		}
	}
	return "![" + alt + "](" + url + titleSuffix + ")", nil
}

// Divider renders "---". It must stand on its own line; combine it with Join.
func (MarkdownHelper) Divider() (string, error) {
	return "---", nil
}

// CodeBlock renders a fenced code block. When code contains a backtick run the
// fence grows so the body renders verbatim.
func (MarkdownHelper) CodeBlock(lang, code string) (string, error) {
	fence := markdownFence(code)
	return fence + lang + "\n" + code + "\n" + fence, nil
}

// Table renders a Markdown table. The first row is the header; every row must
// have the same number of columns as the header.
func (MarkdownHelper) Table(rows [][]string) (string, error) {
	if len(rows) == 0 {
		return "", markdownError("markdown.table.rows", "must be a non-empty array")
	}
	header := rows[0]
	if len(header) == 0 {
		return "", markdownError("markdown.table.header", "must be a non-empty array")
	}
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, markdownTableRow(header))
	separators := make([]string, len(header))
	for index := range separators {
		separators[index] = "---"
	}
	lines = append(lines, markdownTableRow(separators))
	for index := 1; index < len(rows); index++ {
		if len(rows[index]) != len(header) {
			return "", markdownError("markdown.table.rows", "every row must match the header column count")
		}
		lines = append(lines, markdownTableRow(rows[index]))
	}
	return strings.Join(lines, "\n"), nil
}

// ReplyQuote renders the ```` ```reply ```` quote fence. Attribute values are
// escaped (`"` becomes `&quot;`); digest must not contain a triple-backtick
// fence.
func (MarkdownHelper) ReplyQuote(options MarkdownReplyQuoteOptions) (string, error) {
	if strings.TrimSpace(options.SenderName) == "" {
		return "", markdownError("markdown.replyQuote.senderName", "must not be blank")
	}
	baseMsgID := strings.TrimSpace(options.BaseMsgID)
	if baseMsgID == "" {
		return "", markdownError("markdown.replyQuote.baseMsgId", "must not be blank")
	}
	if markdownLongestBacktickRun(options.Digest) >= 3 {
		return "", markdownError("markdown.replyQuote.digest", "must not contain a triple-backtick fence")
	}
	simType := options.SimType
	if simType == "" {
		simType = "1"
	}
	fence := markdownFence(options.Digest)
	header := fence + `reply t="` + escapeMarkdownAttribute(simType) +
		`" n="` + escapeMarkdownAttribute(options.SenderName) +
		`" s_basemsgid="` + escapeMarkdownAttribute(baseMsgID) + `"`
	if options.MsgID2 != "" {
		header += ` s_msgid2="` + escapeMarkdownAttribute(options.MsgID2) + `"`
	}
	return header + "\n" + options.Digest + "\n" + fence, nil
}

// ButtonGroup renders a `[::button-group]` block of client-local buttons. A
// nil options value omits every group attribute.
func (MarkdownHelper) ButtonGroup(buttons []MarkdownButton, options *MarkdownButtonGroupOptions) (string, error) {
	attributes := make([]string, 0, 4)
	if options != nil {
		if options.Layout != "" {
			if _, ok := markdownLayouts[options.Layout]; !ok {
				return "", markdownError("markdown.buttonGroup.layout", "must be flow, equant, vertical or banner")
			}
			attributes = append(attributes, `layout="`+options.Layout+`"`)
		}
		for _, gap := range []struct {
			name  string
			value *float64
		}{
			{"gap_h", options.GapH},
			{"gap_v", options.GapV},
			{"radius", options.Radius},
		} {
			if gap.value == nil {
				continue
			}
			if *gap.value < 0 || math.IsNaN(*gap.value) || math.IsInf(*gap.value, 0) {
				return "", markdownError("markdown.buttonGroup."+gap.name, "must be a non-negative number")
			}
			attributes = append(attributes, gap.name+`="`+strconv.FormatFloat(*gap.value, 'f', -1, 64)+`"`)
		}
	}
	head := "[::button-group"
	if len(attributes) > 0 {
		head += " " + strings.Join(attributes, " ")
	}
	head += "]"
	lines := make([]string, 0, len(buttons)+2)
	lines = append(lines, head)
	for index, button := range buttons {
		line, err := markdownButtonLine(button, index)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	lines = append(lines, "[::button-group /]")
	return strings.Join(lines, "\n"), nil
}

// AgentFlowStream renders the ```` ```agent-flow-stream ```` fence with
// reasoning and tool blocks. The final answer must be written outside the
// fence; Finish sets the header status to completed.
func (MarkdownHelper) AgentFlowStream(options AgentFlowOptions) (string, error) {
	status := options.Status
	if status == "" {
		status = AgentFlowStatusStreaming
	}
	if _, ok := agentFlowStatuses[status]; !ok {
		return "", markdownError("markdown.agentFlowStream.status", "must be streaming or completed")
	}
	thinkingExpanded := true
	if options.ThinkingExpanded != nil {
		thinkingExpanded = *options.ThinkingExpanded
	}
	const fence = "```"
	header := fence + `agent-flow-stream status="` + status + `" thinking_expanded="` +
		strconv.FormatBool(thinkingExpanded) + `"`
	blocks := make([]string, 0, len(options.Blocks))
	for index, block := range options.Blocks {
		rendered, err := markdownAgentFlowBlock(block, index)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, rendered)
	}
	if len(blocks) == 0 {
		return header + "\n" + fence, nil
	}
	return header + "\n" + strings.Join(blocks, "\n") + "\n" + fence, nil
}

// Join connects parts with a blank line and skips empty parts.
func (MarkdownHelper) Join(parts ...string) (string, error) {
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n\n"), nil
}

func markdownID(value any, field string) (string, error) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", markdownError(field, "must not be blank")
		}
		return typed, nil
	case int:
		return markdownIntID(int64(typed), field)
	case int8:
		return markdownIntID(int64(typed), field)
	case int16:
		return markdownIntID(int64(typed), field)
	case int32:
		return markdownIntID(int64(typed), field)
	case int64:
		return markdownIntID(typed, field)
	case uint:
		return markdownUintID(uint64(typed), field)
	case uint8:
		return markdownUintID(uint64(typed), field)
	case uint16:
		return markdownUintID(uint64(typed), field)
	case uint32:
		return markdownUintID(uint64(typed), field)
	case uint64:
		return markdownUintID(typed, field)
	case float32:
		return markdownFloatID(float64(typed), field)
	case float64:
		return markdownFloatID(typed, field)
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return "", markdownError(field, "number must be a safe non-negative integer")
		}
		return markdownIntID(integer, field)
	default:
		return "", markdownError(field, "must be a string or a safe non-negative integer")
	}
}

func markdownIntID(value int64, field string) (string, error) {
	if value < 0 || value > MaxSafeMarkdownID {
		return "", markdownError(field,
			"number must be a safe non-negative integer; pass a 19-digit id as a string")
	}
	return strconv.FormatInt(value, 10), nil
}

func markdownUintID(value uint64, field string) (string, error) {
	if value > uint64(MaxSafeMarkdownID) {
		return "", markdownError(field,
			"number must be a safe non-negative integer; pass a 19-digit id as a string")
	}
	return strconv.FormatUint(value, 10), nil
}

func markdownFloatID(value float64, field string) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) ||
		value < 0 || value > float64(MaxSafeMarkdownID) {
		return "", markdownError(field,
			"number must be a safe non-negative integer; pass a 19-digit id as a string")
	}
	return strconv.FormatInt(int64(value), 10), nil
}

func markdownTableRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}

func escapeMarkdownAttribute(text string) string {
	return strings.ReplaceAll(text, `"`, "&quot;")
}

func markdownLongestBacktickRun(text string) int {
	longest := 0
	current := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	return longest
}

func markdownFence(content string) string {
	length := markdownLongestBacktickRun(content) + 1
	if length < 3 {
		length = 3
	}
	return strings.Repeat("`", length)
}

func markdownButtonLine(button MarkdownButton, _ int) (string, error) {
	if strings.TrimSpace(button.Label) == "" {
		return "", markdownError("markdown.buttonGroup.buttons.label", "must not be blank")
	}
	attributes := []string{`label="` + escapeMarkdownAttribute(button.Label) + `"`}
	if button.URL != "" {
		if strings.TrimSpace(button.URL) == "" {
			return "", markdownError("markdown.buttonGroup.buttons.url", "must not be blank")
		}
		attributes = append(attributes, `url="`+escapeMarkdownAttribute(button.URL)+`"`)
	}
	if button.OpenType != "" {
		if _, ok := markdownOpenTypes[button.OpenType]; !ok {
			return "", markdownError("markdown.buttonGroup.buttons.openType", "must be N, S, A or B")
		}
		attributes = append(attributes, `open_type="`+button.OpenType+`"`)
	}
	for _, attribute := range []struct {
		wire  string
		value string
	}{
		{"interact", button.Interact},
		{"query", button.Query},
		{"query_send", button.QuerySend},
		{"query_send_at", button.QuerySendAt},
		{"query_to_input", button.QueryToInput},
	} {
		if attribute.value == "" {
			continue
		}
		if strings.TrimSpace(attribute.value) == "" {
			return "", markdownError("markdown.buttonGroup.buttons."+attribute.wire, "must not be blank")
		}
		attributes = append(attributes, attribute.wire+`="`+escapeMarkdownAttribute(attribute.value)+`"`)
	}
	if button.Style != "" {
		if strings.TrimSpace(button.Style) == "" {
			return "", markdownError("markdown.buttonGroup.buttons.style", "must not be blank")
		}
		attributes = append(attributes, `style="`+escapeMarkdownAttribute(button.Style)+`"`)
	}
	hasEvent := button.URL != "" || button.Interact != "" || button.Query != "" ||
		button.QuerySend != "" || button.QuerySendAt != "" || button.QueryToInput != ""
	if !hasEvent {
		return "", markdownError("markdown.buttonGroup.buttons",
			"must declare at least one action attribute")
	}
	return "    [::button " + strings.Join(attributes, " ") + "]", nil
}

func markdownAgentFlowBlock(block AgentFlowBlock, index int) (string, error) {
	switch block.Kind {
	case AgentFlowBlockKindReasoning:
		return ":::reasoning\n" + block.Text + "\n:::", nil
	case AgentFlowBlockKindTool:
		if strings.TrimSpace(block.Name) == "" {
			return "", markdownError("markdown.agentFlowStream.blocks.name", "must not be blank")
		}
		status := block.Status
		if status == "" {
			status = AgentFlowToolStatusRunning
		}
		if _, ok := agentFlowToolStatuses[status]; !ok {
			return "", markdownError("markdown.agentFlowStream.blocks.status",
				"must be running, completed or error")
		}
		input, err := markdownToolPayload(block.Input, "markdown.agentFlowStream.blocks.input")
		if err != nil {
			return "", err
		}
		output, err := markdownToolPayload(block.Output, "markdown.agentFlowStream.blocks.output")
		if err != nil {
			return "", err
		}
		body := ""
		switch {
		case input != nil && output != nil:
			body = "\n" + *input + "\n\n---\n" + *output
		case input != nil:
			body = "\n" + *input
		case output != nil:
			body = "\n---\n" + *output
		}
		return `:::tool name="` + escapeMarkdownAttribute(block.Name) + `" status="` + status + `"` +
			body + "\n:::", nil
	default:
		return "", markdownError("markdown.agentFlowStream.blocks.kind",
			`must be "reasoning" or "tool"`)
	}
}

// markdownToolPayload serializes one tool input/output. nil means absent; a
// string is emitted verbatim; any other value must encode to a JSON object.
func markdownToolPayload(value any, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		return &text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, markdownError(field, "must be a string or an object")
	}
	trimmed := strings.TrimSpace(string(encoded))
	if !strings.HasPrefix(trimmed, "{") {
		return nil, markdownError(field, "must be a string or an object")
	}
	return &trimmed, nil
}
