package im

import (
	"github.com/dolfly/infoflow/bot/dynamiccard"
	"github.com/dolfly/infoflow/bot/robot"
)

// Service groups the Bot instant-messaging capabilities.
type Service struct {
	Message     MessageService
	Group       GroupService
	File        FileService
	Menu        robot.MenuService
	DynamicCard dynamiccard.Service
}
