package im

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dolfly/infoflow/internal/envelope"
	"github.com/dolfly/infoflow/internal/sdkerrors"
)

const bubbleWorkflowCardRenderPath = "/robot/card/bubble-workflow-card/render"

const (
	bubbleWorkflowCardMaxComponents = 8
	bubbleWorkflowCardMaxButtons    = 6
)

// BubbleWorkflowCardReceiverType is the target conversation type for a bubble
// workflow card.
type BubbleWorkflowCardReceiverType string

const (
	BubbleWorkflowCardReceiverGroup BubbleWorkflowCardReceiverType = "group"
	BubbleWorkflowCardReceiverUser  BubbleWorkflowCardReceiverType = "user"
)

// BubbleWorkflowCardComponentType is the type of an embedded workflow card
// component.
type BubbleWorkflowCardComponentType string

const (
	BubbleWorkflowCardComponentText           BubbleWorkflowCardComponentType = "text"
	BubbleWorkflowCardComponentInput          BubbleWorkflowCardComponentType = "input"
	BubbleWorkflowCardComponentSingleSelect   BubbleWorkflowCardComponentType = "single-select"
	BubbleWorkflowCardComponentMultiSelect    BubbleWorkflowCardComponentType = "multi-select"
	BubbleWorkflowCardComponentDropdownSingle BubbleWorkflowCardComponentType = "dropdown-single"
	BubbleWorkflowCardComponentDropdownMulti  BubbleWorkflowCardComponentType = "dropdown-multi"
)

// BubbleWorkflowCardOption is one selectable option. The submit event maps a
// selection back through the option value.
type BubbleWorkflowCardOption struct {
	Value string
}

// BubbleWorkflowCardComponent is one embedded workflow card component. The
// Type field selects which fields apply.
type BubbleWorkflowCardComponent struct {
	ComponentID     string
	Type            BubbleWorkflowCardComponentType
	Text            string
	Description     string
	PlaceholderText string
	Options         []BubbleWorkflowCardOption
}

// BubbleWorkflowCardButton is a bottom action button; the submit event echoes
// EventKey back.
type BubbleWorkflowCardButton struct {
	EventKey string
	Text     string
}

// BubbleWorkflowCardCaller identifies the caller and is echoed back by the
// submit event.
type BubbleWorkflowCardCaller struct {
	CallerName string
	RequestID  string
}

// BubbleWorkflowCardReceiver targets the user or group that receives the card.
type BubbleWorkflowCardReceiver struct {
	Type BubbleWorkflowCardReceiverType
	ID   string
}

// BubbleWorkflowCardDefinition is the embedded workflow card business shape.
type BubbleWorkflowCardDefinition struct {
	Title      string
	Components []BubbleWorkflowCardComponent
	Buttons    []BubbleWorkflowCardButton
}

// BubbleWorkflowCardRenderOptions is the input to RenderBubbleWorkflowCard.
type BubbleWorkflowCardRenderOptions struct {
	CardID                          string
	CardInstanceID                  string
	Caller                          BubbleWorkflowCardCaller
	Question                        string
	Receiver                        BubbleWorkflowCardReceiver
	Card                            BubbleWorkflowCardDefinition
	RefreshDefaultIntermediateState *bool
}

// BubbleWorkflowCardCardInfo is the latest stable card identity returned by
// the rendering service.
type BubbleWorkflowCardCardInfo struct {
	CardID   string
	CardType string
	Revision *int
}

// BubbleWorkflowCardRenderResult is the normalized render result.
type BubbleWorkflowCardRenderResult struct {
	OK         bool
	Created    bool
	Caller     *BubbleWorkflowCardCaller
	Receiver   *BubbleWorkflowCardReceiver
	CardInfo   *BubbleWorkflowCardCardInfo
	Recognized bool
	Updated    bool
	raw        json.RawMessage
}

// RawJSON returns a defensive copy of the complete raw HTTP response.
func (r BubbleWorkflowCardRenderResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// RenderBubbleWorkflowCard sends or refreshes the bubble workflow card for one
// caller, receiver, and card ID through the xplatform render route. It
// validates the business shape locally, maps fields to the wire format, and
// normalizes the service response.
func (s *MessageService) RenderBubbleWorkflowCard(ctx context.Context, options BubbleWorkflowCardRenderOptions) (BubbleWorkflowCardRenderResult, error) {
	if ctx == nil {
		return BubbleWorkflowCardRenderResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if err := validateBubbleWorkflowCard(options); err != nil {
		return BubbleWorkflowCardRenderResult{}, err
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return BubbleWorkflowCardRenderResult{}, err
	}
	raw, err := s.transport.PostJSON(ctx, bubbleWorkflowCardRenderPath, buildBubbleWorkflowCardRequest(options), token)
	if err != nil {
		return BubbleWorkflowCardRenderResult{}, err
	}
	return decodeBubbleWorkflowCardResult(raw)
}

func validateBubbleWorkflowCard(options BubbleWorkflowCardRenderOptions) error {
	if strings.TrimSpace(options.CardID) == "" {
		return sdkerrors.NewConfig("cardId", "must not be blank")
	}
	if strings.TrimSpace(options.Caller.CallerName) == "" {
		return sdkerrors.NewConfig("caller.callerName", "must not be blank")
	}
	if options.CardInstanceID != "" && strings.TrimSpace(options.CardInstanceID) == "" {
		return sdkerrors.NewConfig("cardInstanceId", "must not be blank")
	}
	if strings.TrimSpace(options.Receiver.ID) == "" {
		return sdkerrors.NewConfig("receiver.id", "must not be blank")
	}
	if options.Receiver.Type != BubbleWorkflowCardReceiverGroup && options.Receiver.Type != BubbleWorkflowCardReceiverUser {
		return sdkerrors.NewConfig("receiver.type", "must be group or user")
	}
	if len(options.Card.Components) > bubbleWorkflowCardMaxComponents {
		return sdkerrors.NewConfig("card.components", "supports at most 8 items")
	}
	if len(options.Card.Buttons) > bubbleWorkflowCardMaxButtons {
		return sdkerrors.NewConfig("card.buttons", "supports at most 6 items")
	}
	componentIDs := make(map[string]struct{}, len(options.Card.Components))
	for _, component := range options.Card.Components {
		if strings.TrimSpace(component.ComponentID) == "" {
			return sdkerrors.NewConfig("card.components[].componentId", "must not be blank")
		}
		if _, duplicate := componentIDs[component.ComponentID]; duplicate {
			return sdkerrors.NewConfig("card.components[].componentId", "must be unique")
		}
		componentIDs[component.ComponentID] = struct{}{}
		switch component.Type {
		case BubbleWorkflowCardComponentText:
			if strings.TrimSpace(component.Text) == "" {
				return sdkerrors.NewConfig("card.components[].text", "must not be blank")
			}
		case BubbleWorkflowCardComponentInput:
			// No required option or text fields.
		default:
			if len(component.Options) == 0 {
				return sdkerrors.NewConfig("card.components[].options", "must not be empty")
			}
			for _, option := range component.Options {
				if strings.TrimSpace(option.Value) == "" {
					return sdkerrors.NewConfig("card.components[].options[].value", "must not be blank")
				}
			}
		}
	}
	for _, button := range options.Card.Buttons {
		if strings.TrimSpace(button.EventKey) == "" {
			return sdkerrors.NewConfig("card.buttons[].eventKey", "must not be blank")
		}
		if strings.TrimSpace(button.Text) == "" {
			return sdkerrors.NewConfig("card.buttons[].text", "must not be blank")
		}
	}
	return nil
}

func buildBubbleWorkflowCardRequest(options BubbleWorkflowCardRenderOptions) map[string]any {
	caller := map[string]any{"caller_name": options.Caller.CallerName}
	if options.Caller.RequestID != "" {
		caller["request_id"] = options.Caller.RequestID
	}
	card := map[string]any{}
	if options.Card.Title != "" {
		card["title"] = options.Card.Title
	}
	if len(options.Card.Components) > 0 {
		components := make([]map[string]any, 0, len(options.Card.Components))
		for _, component := range options.Card.Components {
			components = append(components, buildBubbleWorkflowCardComponent(component))
		}
		card["components"] = components
	}
	if len(options.Card.Buttons) > 0 {
		buttons := make([]map[string]any, 0, len(options.Card.Buttons))
		for _, button := range options.Card.Buttons {
			buttons = append(buttons, map[string]any{"event_key": button.EventKey, "text": button.Text})
		}
		card["buttons"] = buttons
	}
	request := map[string]any{
		"card_id":   options.CardID,
		"card_type": "general_form",
		"caller":    caller,
		"receiver":  map[string]any{"type": string(options.Receiver.Type), "id": options.Receiver.ID},
		"card":      card,
	}
	if options.CardInstanceID != "" {
		request["card_instance_id"] = options.CardInstanceID
	}
	if options.Question != "" {
		request["question"] = options.Question
	}
	if options.RefreshDefaultIntermediateState != nil {
		request["refresh_default_intermediate_state"] = *options.RefreshDefaultIntermediateState
	}
	return request
}

func buildBubbleWorkflowCardComponent(component BubbleWorkflowCardComponent) map[string]any {
	mapped := map[string]any{"component_id": component.ComponentID, "type": string(component.Type)}
	if component.Description != "" {
		mapped["description"] = component.Description
	}
	switch component.Type {
	case BubbleWorkflowCardComponentText:
		mapped["text"] = component.Text
	case BubbleWorkflowCardComponentInput, BubbleWorkflowCardComponentDropdownSingle, BubbleWorkflowCardComponentDropdownMulti:
		if component.PlaceholderText != "" {
			mapped["placeholder_text"] = component.PlaceholderText
		}
	}
	if component.Type != BubbleWorkflowCardComponentText && component.Type != BubbleWorkflowCardComponentInput {
		options := make([]map[string]any, 0, len(component.Options))
		for _, option := range component.Options {
			options = append(options, map[string]any{"value": option.Value})
		}
		mapped["options"] = options
	}
	return mapped
}

type bubbleWorkflowCardRenderResponse struct {
	OK         *bool                       `json:"ok"`
	Created    *bool                       `json:"created"`
	Caller     *bubbleWorkflowCardCaller   `json:"caller"`
	Receiver   *bubbleWorkflowCardReceiver `json:"receiver"`
	CardInfo   *bubbleWorkflowCardCardInfo `json:"card_info"`
	Recognized *bool                       `json:"recognized"`
	Updated    *bool                       `json:"updated"`
}

type bubbleWorkflowCardCaller struct {
	CallerName string `json:"caller_name"`
	RequestID  string `json:"request_id"`
}

type bubbleWorkflowCardReceiver struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type bubbleWorkflowCardCardInfo struct {
	CardID   string `json:"card_id"`
	CardType string `json:"card_type"`
	Revision *int   `json:"revision"`
}

func decodeBubbleWorkflowCardResult(raw []byte) (BubbleWorkflowCardRenderResult, error) {
	env, err := envelope.DecodeBusiness(raw)
	if err != nil {
		return BubbleWorkflowCardRenderResult{}, err
	}
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		return BubbleWorkflowCardRenderResult{}, sdkerrors.NewMalformed("bubble workflow card result is invalid", raw, err)
	}
	var data bubbleWorkflowCardRenderResponse
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return BubbleWorkflowCardRenderResult{}, sdkerrors.NewMalformed("bubble workflow card result is invalid", raw, err)
	}
	if data.OK == nil || !*data.OK || data.Created == nil || data.Recognized == nil || !*data.Recognized || data.Updated == nil || !*data.Updated {
		return BubbleWorkflowCardRenderResult{}, sdkerrors.NewMalformed("bubble workflow card did not confirm success", raw, nil)
	}
	result := BubbleWorkflowCardRenderResult{
		OK:         *data.OK,
		Created:    *data.Created,
		Recognized: *data.Recognized,
		Updated:    *data.Updated,
		raw:        append(json.RawMessage(nil), raw...),
	}
	if data.Caller != nil {
		result.Caller = &BubbleWorkflowCardCaller{CallerName: data.Caller.CallerName, RequestID: data.Caller.RequestID}
	}
	if data.Receiver != nil {
		result.Receiver = &BubbleWorkflowCardReceiver{Type: BubbleWorkflowCardReceiverType(data.Receiver.Type), ID: data.Receiver.ID}
	}
	if data.CardInfo != nil {
		result.CardInfo = &BubbleWorkflowCardCardInfo{CardID: data.CardInfo.CardID, CardType: data.CardInfo.CardType, Revision: data.CardInfo.Revision}
	}
	return result, nil
}
