// Package robot owns Bot profile, shortcut-command, and follow capabilities.
package robot

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

type Service struct {
	transport *transport.Client
	tokens    *auth.TokenManager
	logIDs    *internalruntime.MessageIDGenerator
}

func NewService(client *transport.Client, tokens *auth.TokenManager, logIDs ...*internalruntime.MessageIDGenerator) Service {
	var generator *internalruntime.MessageIDGenerator
	if len(logIDs) > 0 {
		generator = logIDs[0]
	}
	if generator == nil {
		generator = internalruntime.NewMessageIDGenerator(time.Now)
	}
	return Service{transport: client, tokens: tokens, logIDs: generator}
}

type Result struct{ raw json.RawMessage }

func (r Result) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type ShortcutCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ShortcutCommandGroup struct {
	Type     int               `json:"type"`
	Commands []ShortcutCommand `json:"commands"`
}

type FollowUser struct {
	UserID     string `json:"userId"`
	IsFollow   bool   `json:"isFollow"`
	FollowTime string `json:"followTime"`
}

func (s Service) call(ctx context.Context, route string, body any, shortcut bool) (Result, error) {
	if ctx == nil {
		return Result{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return Result{}, err
	}
	raw, err := s.transport.PostJSONWithHeaders(ctx, route, body, token, http.Header{
		"LOGID": []string{strconv.FormatInt(s.logIDs.Next(), 10)},
	})
	if err != nil {
		return Result{}, err
	}
	if err := decodeResponse(raw, shortcut); err != nil {
		return Result{}, err
	}
	return Result{raw: append(json.RawMessage(nil), raw...)}, nil
}

func decodeResponse(raw []byte, shortcut bool) error {
	var response struct {
		Code    any             `json:"code"`
		ErrCode int             `json:"errcode"`
		ErrMsg  string          `json:"errmsg"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return sdkerrors.NewMalformed("robot response is invalid", raw, err)
	}
	if response.ErrCode != 0 {
		return sdkerrors.NewBusiness(strconv.Itoa(response.ErrCode), response.ErrMsg, raw)
	}
	if code, ok := response.Code.(string); ok && code != "ok" {
		return sdkerrors.NewBusiness(code, "", raw)
	}
	if len(response.Data) == 0 {
		return nil
	}
	var data struct {
		ErrCode int    `json:"errcode"`
		Code    int    `json:"code"`
		ErrMsg  string `json:"errmsg"`
		Message string `json:"message"`
		BizCode int    `json:"bizCode"`
	}
	_ = json.Unmarshal(response.Data, &data)
	if data.ErrCode != 0 && !(shortcut && data.ErrCode == 200) {
		return sdkerrors.NewBusiness(strconv.Itoa(data.ErrCode), data.ErrMsg, raw)
	}
	if data.Code != 0 && data.Code != 200 {
		return sdkerrors.NewBusiness(strconv.Itoa(data.Code), data.Message, raw)
	}
	if data.BizCode != 0 && data.BizCode != 200 {
		return sdkerrors.NewBusiness(strconv.Itoa(data.BizCode), data.Message, raw)
	}
	return nil
}

func (s Service) GetInfo(ctx context.Context) (Result, error) {
	return s.call(ctx, "/imRobot/detail", map[string]any{}, false)
}

func validateCommands(groups []ShortcutCommandGroup) error {
	for _, group := range groups {
		if group.Type < 1 || group.Type > 4 {
			return sdkerrors.NewConfig("commands", "groups must have a supported type")
		}
		for _, command := range group.Commands {
			if strings.TrimSpace(command.Name) == "" || strings.TrimSpace(command.Description) == "" || len([]rune(command.Name)) > 100 || len([]rune(command.Description)) > 100 {
				return sdkerrors.NewConfig("commands", "name and description must be 1..100 characters")
			}
		}
	}
	return nil
}

func (s Service) GetShortcutCommands(ctx context.Context) (Result, error) {
	return s.call(ctx, "/robot/commands/get", map[string]any{}, true)
}

func (s Service) SetShortcutCommands(ctx context.Context, commands []ShortcutCommandGroup) (Result, error) {
	if err := validateCommands(commands); err != nil {
		return Result{}, err
	}
	return s.call(ctx, "/robot/commands/set", map[string]any{"all_commands": commands}, true)
}

func (s Service) AppendShortcutCommands(ctx context.Context, commands []ShortcutCommandGroup) (Result, error) {
	if err := validateCommands(commands); err != nil {
		return Result{}, err
	}
	return s.call(ctx, "/robot/commands/append", map[string]any{"all_commands": commands}, true)
}

func (s Service) GetUserFollowStatus(ctx context.Context, userIDs []string) ([]FollowUser, error) {
	if ctx == nil || len(userIDs) == 0 || len(userIDs) > 100 {
		return nil, sdkerrors.NewConfig("userIDs", "must contain 1 to 100 users")
	}
	for _, userID := range userIDs {
		if strings.TrimSpace(userID) == "" {
			return nil, sdkerrors.NewConfig("userIDs", "must not contain blank user")
		}
	}
	result, err := s.call(ctx, "/app/agent/isFollow", map[string]any{"userids": userIDs}, false)
	if err != nil {
		return nil, err
	}
	var response struct {
		UserList []struct {
			UserID     string `json:"userid"`
			IsFollow   bool   `json:"isFollow"`
			FollowTime string `json:"followTime"`
		} `json:"userlist"`
		Data struct {
			UserList []struct {
				UserID     string `json:"userid"`
				IsFollow   bool   `json:"isFollow"`
				FollowTime string `json:"followTime"`
			} `json:"userlist"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.raw, &response); err != nil {
		return nil, sdkerrors.NewMalformed("follow response invalid", result.raw, err)
	}
	users := response.UserList
	if len(users) == 0 {
		users = response.Data.UserList
	}
	out := make([]FollowUser, len(users))
	for index, user := range users {
		out[index] = FollowUser{UserID: user.UserID, IsFollow: user.IsFollow, FollowTime: user.FollowTime}
	}
	return out, nil
}

func (s Service) IsUserFollowing(ctx context.Context, userID string) (bool, error) {
	users, err := s.GetUserFollowStatus(ctx, []string{userID})
	if err != nil {
		return false, err
	}
	if len(users) != 1 {
		return false, sdkerrors.NewMalformed("follow response incomplete", nil, nil)
	}
	return users[0].IsFollow, nil
}
