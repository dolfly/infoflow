package im

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/envelope"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// GroupService manages documented robot-owned groups.
type GroupService struct {
	transport *transport.Client
	tokens    *auth.TokenManager
}

func NewGroupService(client *transport.Client, tokens *auth.TokenManager) GroupService {
	return GroupService{transport: client, tokens: tokens}
}

type RawResult struct{ raw json.RawMessage }

func (r RawResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type GroupSidebar struct {
	AutoOpen      int    `json:"autoOpen"`
	CustomSidebar int    `json:"customSidebar"`
	Link          string `json:"link"`
}
type CreateGroupRequest struct {
	GroupName     string        `json:"groupName"`
	GroupOwner    string        `json:"groupOwner"`
	MemberList    []string      `json:"memberList,omitempty"`
	RobotList     []int64       `json:"robotList,omitempty"`
	FriendlyLevel int           `json:"friendlyLevel"`
	SearchAbility int           `json:"searchAbility"`
	Managers      []string      `json:"managers,omitempty"`
	RobotManagers []int64       `json:"robotManagers,omitempty"`
	GroupSidebar  *GroupSidebar `json:"groupSidebar,omitempty"`
}
type CreateGroupResult struct {
	GroupID      int64    `json:"groupId"`
	FailMembers  []string `json:"failMembers"`
	FailRobotIDs []int64  `json:"failRobotIds"`
	raw          json.RawMessage
}

func (r CreateGroupResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type GroupIDRequest struct {
	GroupID int64 `json:"groupId"`
}
type DisbandGroupRequest struct {
	GroupID    int64  `json:"groupId"`
	GroupOwner string `json:"groupOwner"`
}
type MemberListRequest struct {
	GroupID    int64 `json:"groupId"`
	RecallType int   `json:"recallType,omitempty"`
}
type MembersRequest struct {
	GroupID     int64  `json:"groupId"`
	MemberList  string `json:"memberList"`
	AgentIDList string `json:"agentIdList"`
}
type UpdateGroupNameRequest struct {
	GroupID int64  `json:"groupId"`
	Name    string `json:"name"`
}
type UpdateBulletinRequest struct {
	GroupID  int64  `json:"groupId"`
	Bulletin string `json:"bulletin"`
}

// Int returns a pointer to v, for optional sidebar flags where nil means
// "do not change".
func Int(v int) *int { return &v }

type SidebarRequest struct {
	GroupID       int64  `json:"groupId"`
	SidebarLink   string `json:"sidebarLink"`
	AutoOpen      *int   `json:"autoOpen,omitempty"`
	CustomSidebar *int   `json:"customSidebar,omitempty"`
}
type AddChatTabRequest struct {
	ChatID  int64  `json:"chatId"`
	TabName string `json:"tabName"`
	TabLink string `json:"tabLink"`
}
type MenuVisibility struct {
	OperationType string   `json:"operationType"`
	MainMenuKeys  []string `json:"mainMenuKeys,omitempty"`
	SubMenuKeys   []string `json:"subMenuKeys,omitempty"`
}

// MarshalJSON keeps menu arrays aligned with the Node SDK, which always sends
// both fields as arrays even when the caller leaves them unset.
func (m MenuVisibility) MarshalJSON() ([]byte, error) {
	mainMenuKeys := m.MainMenuKeys
	if mainMenuKeys == nil {
		mainMenuKeys = []string{}
	}
	subMenuKeys := m.SubMenuKeys
	if subMenuKeys == nil {
		subMenuKeys = []string{}
	}
	return json.Marshal(struct {
		OperationType string   `json:"operationType"`
		MainMenuKeys  []string `json:"mainMenuKeys"`
		SubMenuKeys   []string `json:"subMenuKeys"`
	}{m.OperationType, mainMenuKeys, subMenuKeys})
}

type UpdateMenuVisibilityRequest struct {
	GroupID     int64          `json:"groupId"`
	CustomMenus MenuVisibility `json:"customMenus"`
}
type SetManagersRequest struct {
	GroupID       int64    `json:"groupId"`
	UserEmails    []string `json:"userEmails,omitempty"`
	RobotAgentIDs []int64  `json:"robotAgentIds,omitempty"`
}
type ModifyUserRemarkRequest struct {
	ToUserID string `json:"toUserId"`
	GroupID  string `json:"groupId"`
	Remark   string `json:"remark"`
}

func validGroupID(ctx context.Context, id int64) error {
	if ctx == nil {
		return sdkerrors.NewConfig("context", "must not be nil")
	}
	if id <= 0 {
		return sdkerrors.NewConfig("groupID", "must be positive")
	}
	return nil
}
func (s *GroupService) call(ctx context.Context, route string, body any) (RawResult, error) {
	return s.callWithResponseValidator(ctx, route, body, success)
}

func (s *GroupService) callWithResponseValidator(ctx context.Context, route string, body any, validate func([]byte) error) (RawResult, error) {
	if ctx == nil {
		return RawResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	t, e := s.tokens.Token(ctx)
	if e != nil {
		return RawResult{}, e
	}
	raw, e := s.transport.PostJSON(ctx, route, body, t)
	if e != nil {
		return RawResult{}, e
	}
	if e = validate(raw); e != nil {
		return RawResult{}, e
	}
	return RawResult{append(json.RawMessage(nil), raw...)}, nil
}
func success(raw []byte) error {
	return successWithNestedErrcode(raw, 0)
}

func shortcutCommandSuccess(raw []byte) error {
	return successWithNestedErrcode(raw, 0, 200)
}

func successWithNestedErrcode(raw []byte, allowed ...int) error {
	var x struct {
		Code    any             `json:"code"`
		ErrCode int             `json:"errcode"`
		ErrMsg  string          `json:"errmsg"`
		Data    json.RawMessage `json:"data"`
	}
	if e := json.Unmarshal(raw, &x); e != nil {
		return sdkerrors.NewMalformed("group response is invalid", raw, e)
	}
	if x.ErrCode != 0 {
		return sdkerrors.NewBusiness(strconv.Itoa(x.ErrCode), x.ErrMsg, raw)
	}
	if s, ok := x.Code.(string); ok && s != "ok" {
		return sdkerrors.NewBusiness(s, "", raw)
	}
	if len(x.Data) > 0 {
		var d struct {
			ErrCode int    `json:"errcode"`
			Code    int    `json:"code"`
			ErrMsg  string `json:"errmsg"`
			Message string `json:"message"`
			BizCode int    `json:"bizCode"`
		}
		_ = json.Unmarshal(x.Data, &d)
		if !containsInt(allowed, d.ErrCode) {
			return sdkerrors.NewBusiness(strconv.Itoa(d.ErrCode), d.ErrMsg, raw)
		}
		if d.Code != 0 && d.Code != 200 {
			return sdkerrors.NewBusiness(strconv.Itoa(d.Code), d.Message, raw)
		}
		if d.BizCode != 0 && d.BizCode != 200 {
			return sdkerrors.NewBusiness(strconv.Itoa(d.BizCode), d.Message, raw)
		}
	}
	return nil
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func (s *GroupService) Create(ctx context.Context, r CreateGroupRequest) (CreateGroupResult, error) {
	if ctx == nil || strings.TrimSpace(r.GroupName) == "" || strings.TrimSpace(r.GroupOwner) == "" || r.FriendlyLevel < 1 || r.FriendlyLevel > 3 || r.SearchAbility < 0 || r.SearchAbility > 1 {
		return CreateGroupResult{}, sdkerrors.NewConfig("group", "name, owner, friendly level, and search ability are required")
	}
	token, e := s.tokens.Token(ctx)
	if e != nil {
		return CreateGroupResult{}, e
	}
	raw, e := s.transport.PostJSON(ctx, "/robot/group/create", r, token)
	if e != nil {
		return CreateGroupResult{}, e
	}
	payload, e := envelope.DecodeGroupCreateBFlex(raw)
	if e != nil {
		return CreateGroupResult{}, e
	}
	var result CreateGroupResult
	if e = json.Unmarshal(payload, &result); e != nil || result.GroupID <= 0 {
		return CreateGroupResult{}, sdkerrors.NewMalformed("group create result is invalid", raw, e)
	}
	result.raw = append(json.RawMessage(nil), raw...)
	return result, nil
}
func (s *GroupService) Disband(ctx context.Context, r DisbandGroupRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	if strings.TrimSpace(r.GroupOwner) == "" {
		return RawResult{}, sdkerrors.NewConfig("groupOwner", "must not be blank")
	}
	return s.call(ctx, "/robot/group/disband", r)
}

// Quit removes the robot application from a group. It is deliberately
// separate from Disband: a robot that has quit may no longer be permitted to
// clean up a disposable group.
func (s *GroupService) Quit(ctx context.Context, r GroupIDRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/im/group/quit", r)
}

func (s *GroupService) GetInfo(ctx context.Context, r GroupIDRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/robot/group/getGroupInfo", r)
}
func (s *GroupService) Members(ctx context.Context, r MemberListRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/robot/group/memberList", r)
}
func (s *GroupService) AddMembers(ctx context.Context, r MembersRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	if r.MemberList == "" && r.AgentIDList == "" {
		return RawResult{}, sdkerrors.NewConfig("members", "at least one member is required")
	}
	return s.call(ctx, "/robot/group/addMembers", r)
}
func (s *GroupService) RemoveMembers(ctx context.Context, r MembersRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/robot/group/removeMembers", r)
}
func (s *GroupService) UpdateName(ctx context.Context, r UpdateGroupNameRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	if strings.TrimSpace(r.Name) == "" {
		return RawResult{}, sdkerrors.NewConfig("name", "must not be blank")
	}
	return s.call(ctx, "/robot/group/updateName", r)
}
func (s *GroupService) UpdateBulletin(ctx context.Context, r UpdateBulletinRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/robot/group/updateBulletin", r)
}
func (s *GroupService) SetSidebar(ctx context.Context, r SidebarRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	if r.AutoOpen != nil && (*r.AutoOpen < 0 || *r.AutoOpen > 1) {
		return RawResult{}, sdkerrors.NewConfig("sidebar", "flags must be 0 or 1")
	}
	if r.CustomSidebar != nil && (*r.CustomSidebar < 0 || *r.CustomSidebar > 1) {
		return RawResult{}, sdkerrors.NewConfig("sidebar", "flags must be 0 or 1")
	}
	t, e := s.tokens.Token(ctx)
	if e != nil {
		return RawResult{}, e
	}
	raw, e := s.transport.PutJSON(ctx, "/im/group/sidebar", r, t)
	if e != nil {
		return RawResult{}, e
	}
	if e = success(raw); e != nil {
		return RawResult{}, e
	}
	return RawResult{raw}, nil
}
func (s *GroupService) AddChatTab(ctx context.Context, r AddChatTabRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.ChatID); e != nil {
		return RawResult{}, e
	}
	if strings.TrimSpace(r.TabName) == "" || strings.TrimSpace(r.TabLink) == "" {
		return RawResult{}, sdkerrors.NewConfig("tab", "name and link required")
	}
	return s.call(ctx, "/robot/group/addChatTab", r)
}
func (s *GroupService) UpdateMenuVisible(ctx context.Context, r UpdateMenuVisibilityRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	if r.CustomMenus.OperationType != "show" && r.CustomMenus.OperationType != "hide" {
		return RawResult{}, sdkerrors.NewConfig("operationType", "must be show or hide")
	}
	return s.call(ctx, "/robot/group/updateMenuVisible", r)
}
func (s *GroupService) SupportAuth(ctx context.Context, r GroupIDRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/robot/group/supportAuth", r)
}
func (s *GroupService) CancelAuth(ctx context.Context, r GroupIDRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	return s.call(ctx, "/robot/group/cancelAuth", r)
}
func (s *GroupService) SetManagers(ctx context.Context, r SetManagersRequest) (RawResult, error) {
	if e := validGroupID(ctx, r.GroupID); e != nil {
		return RawResult{}, e
	}
	if len(r.UserEmails)+len(r.RobotAgentIDs) == 0 || len(r.UserEmails)+len(r.RobotAgentIDs) > 19 {
		return RawResult{}, sdkerrors.NewConfig("managers", "must contain 1 to 19 entries")
	}
	// The route contract (and Node/Java SDKs) requires both manager arrays on
	// the wire. In particular, a user-only update must send
	// robotAgentIds: [] rather than silently omitting that field.
	users := append([]string(nil), r.UserEmails...)
	robots := append([]int64(nil), r.RobotAgentIDs...)
	if users == nil {
		users = []string{}
	}
	if robots == nil {
		robots = []int64{}
	}
	wire := struct {
		GroupID       int64    `json:"groupId"`
		UserEmails    []string `json:"userEmails"`
		RobotAgentIDs []int64  `json:"robotAgentIds"`
	}{GroupID: r.GroupID, UserEmails: users, RobotAgentIDs: robots}
	return s.call(ctx, "/im/group/managers/set", wire)
}
func (s *GroupService) ModifyUserRemark(ctx context.Context, r ModifyUserRemarkRequest) (RawResult, error) {
	if ctx == nil || strings.TrimSpace(r.ToUserID) == "" || strings.TrimSpace(r.GroupID) == "" || strings.TrimSpace(r.Remark) == "" {
		return RawResult{}, sdkerrors.NewConfig("remark", "user, group, and remark required")
	}
	// The public input remains an email-or-uuap string, while this route's wire
	// contract requires the UUAP prefix rather than a full email address.
	wire := r
	wire.ToUserID = strings.SplitN(strings.TrimSpace(r.ToUserID), "@", 2)[0]
	if wire.ToUserID == "" {
		return RawResult{}, sdkerrors.NewConfig("toUserID", "must contain a uuap prefix")
	}
	return s.call(ctx, "/im/group/modifyUserRemark", wire)
}
