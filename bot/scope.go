// Package bot owns capabilities invoked with the Bot/App identity.
package bot

import (
	"github.com/dolfly/infoflow/bot/calendar"
	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/im"
	"github.com/dolfly/infoflow/bot/meeting"
	"github.com/dolfly/infoflow/bot/robot"
	"github.com/dolfly/infoflow/bot/webhook"
	"github.com/dolfly/infoflow/bot/websocket"
)

type Scope struct {
	Calendar       calendar.Service
	IM             im.Service
	Meeting        meeting.Service
	MessageSummary im.MessageSummaryService
	Robot          robot.Service
	Events         *events.Dispatcher
	Webhook        webhook.Service
	WebSocket      websocket.Service
}
