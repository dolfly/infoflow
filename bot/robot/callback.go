package robot

import (
	"context"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

// CallbackUpdateRequest describes the optional fields accepted by the robot
// callback update endpoint. Callers may update webhook or WebSocket mode
// without going through a receiver/session facade.
type CallbackUpdateRequest struct {
	CallbackURL     string `json:"recallUrl,omitempty"`
	Token           string `json:"token,omitempty"`
	EncodingAESKey  string `json:"encodingAesKey,omitempty"`
	ReceiveMsgState int    `json:"receiveMsgState,omitempty"`
	EnableWebSocket int    `json:"enWebSocket,omitempty"`
}

// UpdateCallbackURL updates the robot callback configuration once.
func (s Service) UpdateCallbackURL(ctx context.Context, request CallbackUpdateRequest) (Result, error) {
	if ctx == nil {
		return Result{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if request.CallbackURL == "" && request.Token == "" && request.EncodingAESKey == "" && request.EnableWebSocket == 0 {
		return Result{}, sdkerrors.NewConfig("callback", "at least one callback field is required")
	}
	return s.call(ctx, "/imRobot/updateReCallUrl", request, false)
}
