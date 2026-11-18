package robot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// MenuService manages the robot's single-chat menu definition.
type MenuService struct {
	transport *transport.Client
	tokens    *auth.TokenManager
}

func NewMenuService(client *transport.Client, tokens *auth.TokenManager) MenuService {
	return MenuService{transport: client, tokens: tokens}
}

type MenuLinkConfig struct {
	PCURL          string `json:"pcUrl"`
	PCBrowser      int    `json:"pcBrowser"`
	MobileURL      string `json:"mobileUrl"`
	MobileOpenType int    `json:"mobileOpenType"`
	Width          *int   `json:"width,omitempty"`
	Height         *int   `json:"height,omitempty"`
}

type MenuAction struct {
	DataType   string          `json:"dataType"`
	LinkConfig *MenuLinkConfig `json:"linkConfig,omitempty"`
	EventKey   string          `json:"eventkey,omitempty"`
}

type MenuItem struct {
	Name     string      `json:"name"`
	SubMenus []MenuItem  `json:"subMenus,omitempty"`
	Action   *MenuAction `json:"action,omitempty"`
}

type MenuDefinitionResult struct {
	ErrCode int        `json:"errcode"`
	ErrMsg  string     `json:"errmsg"`
	Menus   []MenuItem `json:"menus"`
	raw     json.RawMessage
}

func (r MenuDefinitionResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

type MenuUpdateResult struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	raw     json.RawMessage
}

func (r MenuUpdateResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// Get returns the current robot menu definition.
func (s MenuService) Get(ctx context.Context) (MenuDefinitionResult, error) {
	if ctx == nil {
		return MenuDefinitionResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return MenuDefinitionResult{}, err
	}
	raw, err := s.transport.Get(ctx, "/robot/menu/get", nil, token)
	if err != nil {
		return MenuDefinitionResult{}, err
	}
	if err := decodeResponse(raw, false); err != nil {
		return MenuDefinitionResult{}, err
	}
	menus, err := decodeMenus(raw)
	if err != nil {
		return MenuDefinitionResult{}, err
	}
	return MenuDefinitionResult{Menus: menus, raw: append(json.RawMessage(nil), raw...)}, nil
}

// Update replaces the complete robot menu definition.
func (s MenuService) Update(ctx context.Context, menus []MenuItem) (MenuUpdateResult, error) {
	if err := validateMenus(menus, 0); err != nil {
		return MenuUpdateResult{}, err
	}
	if ctx == nil {
		return MenuUpdateResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return MenuUpdateResult{}, err
	}
	raw, err := s.transport.PostJSON(ctx, "/robot/menu/update", map[string]any{"menus": menus}, token)
	if err != nil {
		return MenuUpdateResult{}, err
	}
	if err := decodeResponse(raw, false); err != nil {
		return MenuUpdateResult{}, err
	}
	return MenuUpdateResult{raw: append(json.RawMessage(nil), raw...)}, nil
}

func validateMenus(menus []MenuItem, depth int) error {
	if depth == 0 && len(menus) == 0 {
		return sdkerrors.NewConfig("menus", "must contain at least one menu item")
	}
	limit := 10
	if depth == 0 {
		limit = 20
	}
	if len(menus) > limit {
		return sdkerrors.NewConfig("menus", "exceeds the supported menu item limit")
	}
	for index, menu := range menus {
		path := "menus"
		if depth > 0 {
			path = "menus.subMenus"
		}
		if strings.TrimSpace(menu.Name) == "" {
			return sdkerrors.NewConfig(path, "name must not be blank")
		}
		hasSubMenus := len(menu.SubMenus) > 0
		if hasSubMenus && depth >= 1 {
			return sdkerrors.NewConfig(path, "supports at most two menu levels")
		}
		if hasSubMenus && menu.Action != nil {
			return sdkerrors.NewConfig(path, "parent menu must not define an action")
		}
		if !hasSubMenus && menu.Action == nil {
			return sdkerrors.NewConfig(path, "leaf menu must define an action")
		}
		if menu.Action != nil {
			if err := validateAction(*menu.Action, path); err != nil {
				return err
			}
		}
		if hasSubMenus {
			if err := validateMenus(menu.SubMenus, depth+1); err != nil {
				return sdkerrors.NewConfig(path+"["+strconv.Itoa(index)+"].subMenus", err.Error())
			}
		}
	}
	return nil
}

func validateAction(action MenuAction, path string) error {
	switch action.DataType {
	case "push":
		if action.LinkConfig != nil {
			return sdkerrors.NewConfig(path, "push action must not define linkConfig")
		}
	case "url":
		if action.LinkConfig == nil {
			return sdkerrors.NewConfig(path, "url action requires linkConfig")
		}
		link := action.LinkConfig
		if strings.TrimSpace(link.PCURL) == "" || strings.TrimSpace(link.MobileURL) == "" {
			return sdkerrors.NewConfig(path, "linkConfig URLs must not be blank")
		}
		if link.PCBrowser < 0 || link.PCBrowser > 2 || link.MobileOpenType < 0 || link.MobileOpenType > 1 {
			return sdkerrors.NewConfig(path, "linkConfig open modes are invalid")
		}
	default:
		return sdkerrors.NewConfig(path, "dataType must be push or url")
	}
	return nil
}

func decodeMenus(raw []byte) ([]MenuItem, error) {
	var current map[string]json.RawMessage
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, sdkerrors.NewMalformed("robot menu response is invalid", raw, err)
	}
	for depth := 0; depth < 3 && current != nil; depth++ {
		if encoded, ok := current["menus"]; ok {
			var menus []MenuItem
			if err := json.Unmarshal(encoded, &menus); err != nil {
				return nil, sdkerrors.NewMalformed("robot menu definition is invalid", raw, err)
			}
			return menus, nil
		}
		var next map[string]json.RawMessage
		encoded, ok := current["data"]
		if !ok || json.Unmarshal(encoded, &next) != nil {
			break
		}
		current = next
	}
	return []MenuItem{}, nil
}
