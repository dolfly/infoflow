package dynamiccard

import (
	"errors"
	"testing"
)

func requireValidation(t *testing.T, err error) {
	t.Helper()
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsValidation() {
		t.Fatalf("error = %v, want validation DynamicCardError", err)
	}
}

func mustMarkdown(t *testing.T) func(string, error) string {
	t.Helper()
	return func(value string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("markdown helper: %v", err)
		}
		return value
	}
}

// MD-01: at / atAll / atAgent exact strings; atAgent accepts numbers or strings.
func TestMarkdownAtHelpers(t *testing.T) {
	if got := mustMarkdown(t)(Markdown.AtUser("zhangsan")); got != "@zhangsan" {
		t.Fatalf("AtUser = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.AtAgent(1282)); got != "@1282" {
		t.Fatalf("AtAgent(1282) = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.AtAgent("1282")); got != "@1282" {
		t.Fatalf("AtAgent(\"1282\") = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.AtAgent(int64(1282))); got != "@1282" {
		t.Fatalf("AtAgent(int64) = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.AtAll()); got != "@all" {
		t.Fatalf("AtAll = %q", got)
	}

	for _, value := range []any{"", "   ", -1, int64(-1), float64(1.5), 1 << 53, map[string]string{}} {
		if _, err := Markdown.AtAgent(value); err == nil {
			t.Fatalf("AtAgent(%v) must fail validation", value)
		} else {
			requireValidation(t, err)
		}
	}
	if _, err := Markdown.AtUser("  "); err == nil {
		t.Fatal("AtUser blank must fail validation")
	} else {
		requireValidation(t, err)
	}
}

// MD-02 / MD-01: colour rendering and the closed colour set.
func TestMarkdownColor(t *testing.T) {
	if got := mustMarkdown(t)(Markdown.Color("188例", "red")); got != `<font color="red">188例</font>` {
		t.Fatalf("Color = %q", got)
	}
	for _, color := range []string{"green", "gray", "blue", "orange"} {
		if _, err := Markdown.Color("x", color); err != nil {
			t.Fatalf("Color(%q): %v", color, err)
		}
	}
	for _, color := range []string{"purple", "RED", ""} {
		if _, err := Markdown.Color("x", color); err == nil {
			t.Fatalf("Color(%q) must fail validation", color)
		} else {
			requireValidation(t, err)
		}
	}
}

// MD-03: table header and column-count validation plus exact rendering.
func TestMarkdownTable(t *testing.T) {
	got := mustMarkdown(t)(Markdown.Table([][]string{
		{"列1", "列2"},
		{"a", "b"},
	}))
	want := "| 列1 | 列2 |\n| --- | --- |\n| a | b |"
	if got != want {
		t.Fatalf("Table:\n got: %q\nwant: %q", got, want)
	}

	for name, rows := range map[string][][]string{
		"nil":           nil,
		"empty":         {},
		"empty header":  {{}},
		"ragged row":    {{"a", "b"}, {"c"}},
		"ragged header": {{"a"}, {"b", "c"}},
	} {
		if _, err := Markdown.Table(rows); err == nil {
			t.Fatalf("Table(%s) must fail validation", name)
		} else {
			requireValidation(t, err)
		}
	}
}

// MD-04: a triple-backtick body raises the fence length.
func TestMarkdownCodeBlock(t *testing.T) {
	if got := mustMarkdown(t)(Markdown.CodeBlock("bash", "curl x")); got != "```bash\ncurl x\n```" {
		t.Fatalf("CodeBlock = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.CodeBlock("", "curl x")); got != "```\ncurl x\n```" {
		t.Fatalf("CodeBlock empty lang = %q", got)
	}
	got := mustMarkdown(t)(Markdown.CodeBlock("md", "a ``` b"))
	want := "````md\na ``` b\n````"
	if got != want {
		t.Fatalf("CodeBlock fence:\n got: %q\nwant: %q", got, want)
	}
}

// MD-05: reply quote validation and a lossless 19-digit base message id.
func TestMarkdownReplyQuote(t *testing.T) {
	got := mustMarkdown(t)(Markdown.ReplyQuote(MarkdownReplyQuoteOptions{
		SenderName: "张三",
		BaseMsgID:  "1866864561573972498",
		Digest:     "被引用摘要",
	}))
	want := "```reply t=\"1\" n=\"张三\" s_basemsgid=\"1866864561573972498\"\n被引用摘要\n```"
	if got != want {
		t.Fatalf("ReplyQuote:\n got: %q\nwant: %q", got, want)
	}

	got = mustMarkdown(t)(Markdown.ReplyQuote(MarkdownReplyQuoteOptions{
		SenderName: "张\"三",
		BaseMsgID:  "1866864561573972498",
		MsgID2:     "300010002",
		SimType:    "1",
		Digest:     "摘要",
	}))
	want = "```reply t=\"1\" n=\"张&quot;三\" s_basemsgid=\"1866864561573972498\" s_msgid2=\"300010002\"\n摘要\n```"
	if got != want {
		t.Fatalf("ReplyQuote attributes:\n got: %q\nwant: %q", got, want)
	}

	for name, options := range map[string]MarkdownReplyQuoteOptions{
		"missing base id": {SenderName: "n", Digest: "d"},
		"blank base id":   {SenderName: "n", BaseMsgID: "  ", Digest: "d"},
		"blank sender":    {BaseMsgID: "1", Digest: "d"},
		"fence in digest": {SenderName: "n", BaseMsgID: "1", Digest: "a ``` b"},
	} {
		if _, err := Markdown.ReplyQuote(options); err == nil {
			t.Fatalf("ReplyQuote(%s) must fail validation", name)
		} else {
			requireValidation(t, err)
		}
	}
}

func toolBlockOptions() AgentFlowOptions {
	return AgentFlowOptions{
		ThinkingExpanded: Bool(true),
		Blocks: []AgentFlowBlock{
			{Kind: AgentFlowBlockKindReasoning, Text: "先读文件"},
			{
				Kind:   AgentFlowBlockKindTool,
				Name:   "read_api",
				Status: AgentFlowToolStatusCompleted,
				Input:  map[string]any{"file": "a.txt"},
				Output: map[string]any{"result": "ok"},
			},
		},
	}
}

// MD-06: invalid status / blank tool name / invalid tool status fail validation.
func TestMarkdownAgentFlowStreamValidation(t *testing.T) {
	cases := map[string]AgentFlowOptions{
		"bad status":      {Status: "failed", Blocks: []AgentFlowBlock{{Kind: AgentFlowBlockKindReasoning, Text: "x"}}},
		"blank tool name": {Blocks: []AgentFlowBlock{{Kind: AgentFlowBlockKindTool, Name: "  "}}},
		"bad tool status": {Blocks: []AgentFlowBlock{{Kind: AgentFlowBlockKindTool, Name: "t", Status: "failed"}}},
		"bad block kind":  {Blocks: []AgentFlowBlock{{Kind: "unknown"}}},
		"bad input":       {Blocks: []AgentFlowBlock{{Kind: AgentFlowBlockKindTool, Name: "t", Input: []string{"x"}}}},
	}
	for name, options := range cases {
		if _, err := Markdown.AgentFlowStream(options); err == nil {
			t.Fatalf("AgentFlowStream(%s) must fail validation", name)
		} else {
			requireValidation(t, err)
		}
	}
}

// MD-07: normal agent-flow-stream structure; the answer stays outside the fence.
func TestMarkdownAgentFlowStream(t *testing.T) {
	got := mustMarkdown(t)(Markdown.AgentFlowStream(toolBlockOptions()))
	want := "```agent-flow-stream status=\"streaming\" thinking_expanded=\"true\"\n" +
		":::reasoning\n先读文件\n:::\n" +
		":::tool name=\"read_api\" status=\"completed\"\n{\"file\":\"a.txt\"}\n\n---\n{\"result\":\"ok\"}\n:::\n" +
		"```"
	if got != want {
		t.Fatalf("AgentFlowStream:\n got: %q\nwant: %q", got, want)
	}
	answer := mustMarkdown(t)(Markdown.Join(got, "最终答案"))
	if answer != want+"\n\n最终答案" {
		t.Fatalf("answer outside fence = %q", answer)
	}

	empty := mustMarkdown(t)(Markdown.AgentFlowStream(AgentFlowOptions{ThinkingExpanded: Bool(false)}))
	if empty != "```agent-flow-stream status=\"streaming\" thinking_expanded=\"false\"\n```" {
		t.Fatalf("empty blocks = %q", empty)
	}

	outputOnly := mustMarkdown(t)(Markdown.AgentFlowStream(AgentFlowOptions{
		Blocks: []AgentFlowBlock{{Kind: AgentFlowBlockKindTool, Name: "t", Output: "out"}},
	}))
	wantOutput := "```agent-flow-stream status=\"streaming\" thinking_expanded=\"true\"\n" +
		":::tool name=\"t\" status=\"running\"\n---\nout\n:::\n```"
	if outputOnly != wantOutput {
		t.Fatalf("output-only tool = %q", outputOnly)
	}
}

// MD-09: join skips empty parts and uses a blank line.
func TestMarkdownJoin(t *testing.T) {
	got := mustMarkdown(t)(Markdown.Join("a", "", "b"))
	if got != "a\n\nb" {
		t.Fatalf("Join = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.Join()); got != "" {
		t.Fatalf("Join() = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.Join("", "")); got != "" {
		t.Fatalf("Join(empty) = %q", got)
	}
}

func TestMarkdownLinkImageDivider(t *testing.T) {
	if got := mustMarkdown(t)(Markdown.Link("百度一下", "https://www.baidu.com")); got != "[百度一下](https://www.baidu.com)" {
		t.Fatalf("Link = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.Image("https://x/y.png", nil)); got != "![](https://x/y.png)" {
		t.Fatalf("Image = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.Image("https://x/y.png", &MarkdownImageOptions{Alt: "截图", Title: "t"})); got != `![截图](https://x/y.png "t")` {
		t.Fatalf("Image with options = %q", got)
	}
	if got := mustMarkdown(t)(Markdown.Divider()); got != "---" {
		t.Fatalf("Divider = %q", got)
	}
	if _, err := Markdown.Image("  ", nil); err == nil {
		t.Fatal("Image blank url must fail validation")
	} else {
		requireValidation(t, err)
	}
	if _, err := Markdown.Link("x", " "); err == nil {
		t.Fatal("Link blank url must fail validation")
	} else {
		requireValidation(t, err)
	}
}

func TestMarkdownButtonGroup(t *testing.T) {
	got := mustMarkdown(t)(Markdown.ButtonGroup([]MarkdownButton{
		{Label: "打开百度", URL: "https://www.baidu.com", OpenType: "N", Style: "primary"},
		{Label: "发送消息", QuerySend: "我是一条发送到当前会话的消息"},
	}, &MarkdownButtonGroupOptions{Layout: "flow", Radius: Float(8)}))
	want := "[::button-group layout=\"flow\" radius=\"8\"]\n" +
		"    [::button label=\"打开百度\" url=\"https://www.baidu.com\" open_type=\"N\" style=\"primary\"]\n" +
		"    [::button label=\"发送消息\" query_send=\"我是一条发送到当前会话的消息\"]\n" +
		"[::button-group /]"
	if got != want {
		t.Fatalf("ButtonGroup:\n got: %q\nwant: %q", got, want)
	}

	if _, err := Markdown.ButtonGroup([]MarkdownButton{{Label: "无事件"}}, nil); err == nil {
		t.Fatal("button without an action must fail validation")
	} else {
		requireValidation(t, err)
	}
	if _, err := Markdown.ButtonGroup([]MarkdownButton{{Label: " ", URL: "u"}}, nil); err == nil {
		t.Fatal("blank label must fail validation")
	} else {
		requireValidation(t, err)
	}
	if _, err := Markdown.ButtonGroup([]MarkdownButton{{Label: "l", URL: "u", OpenType: "X"}}, nil); err == nil {
		t.Fatal("invalid open type must fail validation")
	} else {
		requireValidation(t, err)
	}
	if _, err := Markdown.ButtonGroup(nil, &MarkdownButtonGroupOptions{Layout: "grid"}); err == nil {
		t.Fatal("invalid layout must fail validation")
	} else {
		requireValidation(t, err)
	}
	if _, err := Markdown.ButtonGroup([]MarkdownButton{{Label: "l", URL: "u"}}, &MarkdownButtonGroupOptions{GapH: Float(-1)}); err == nil {
		t.Fatal("negative gap must fail validation")
	} else {
		requireValidation(t, err)
	}
}
