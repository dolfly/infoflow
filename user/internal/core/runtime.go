package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
	"github.com/dolfly/infoflow/userauthorization"
)

const (
	DefaultRobot2CBaseURL      = "https://open-ruliu.baidu-int.com/open-plat/api/v1"
	DefaultUserCalendarBaseURL = "https://apigo.baidu-int.com/meeting-agent/ai-meeting/openClaw"
	DefaultUserMeetingBaseURL  = "https://apigo.baidu-int.com/calendar/openclaw"
)

// UserConfig selects identity tokens and optionally overrides documented
// profile bases. Explicit Robot2C wins over explicit UGate for Robot2C calls.
type UserConfig struct {
	Robot2CToken    string
	UGateToken      string
	Robot2CBaseURL  string
	CalendarBaseURL string
	MeetingBaseURL  string
}

type UserIMService struct{ Message UserMessageService }
type userProfile struct {
	client         *transport.Client
	tokens         TokenProvider
	robot2c, ugate string
}
type UserAuthService struct{ profile userProfile }
type UserMessageService struct{ profile userProfile }
type UserCalendarService struct {
	profile    userProfile
	hubProfile userProfile
}
type UserMeetingService struct{ profile userProfile }

type UserRawResult struct {
	Data json.RawMessage
	raw  json.RawMessage
}

func NewRawResult(data, raw json.RawMessage) UserRawResult {
	return UserRawResult{Data: append(json.RawMessage(nil), data...), raw: append(json.RawMessage(nil), raw...)}
}

func (r UserRawResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type LatestContactsRequest struct {
	Limit, MaxPages int
	IncludeRaw      bool
}
type UserContact struct {
	ID, Type, Name string
	Raw            json.RawMessage
}
type LatestContactsResult struct {
	Contacts []UserContact
	Text     string
	RawPages []json.RawMessage
}
type CreateSummaryRequest struct {
	ChatID             string `json:"chatId"`
	ChatType           string `json:"chatType"`
	StartTime, EndTime int64
}
type ImportSummaryRequest struct {
	StartQueryTime, EndQueryTime int64
	TopicType                    string
	Intent                       string
	UnreadType                   string
}
type SummaryResult struct {
	RequestID, TaskID string
	raw               json.RawMessage
}

func (r SummaryResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type CalendarRequest struct {
	EventID string
	Body    map[string]any
	Query   url.Values
}
type FreeBusyRequest struct {
	StartTime int64    `json:"startTime"`
	EndTime   int64    `json:"endTime"`
	Emails    []string `json:"emails"`
}
type MeetingListRequest struct {
	BeginTime, EndTime       int64
	TimeZone                 string
	PageSize, PageNumber     int
	ShowEnd                  bool
	ShowCalendar             *bool
	FilterTypes, FilterItems []string
}
type CallMeetingRequest struct {
	Subject     string   `json:"subject"`
	InviteUsers []string `json:"inviteUsers"`
	StartRecord bool     `json:"startRecord"`
	StartAIGC   bool     `json:"startAigc"`
}

func userBase(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

type RuntimeOptions struct {
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	HTTPClient     *http.Client
	AccessChannel  string
}

type TokenProvider interface {
	Token(context.Context) (string, error)
}

type Runtime struct {
	IM            UserIMService
	Calendar      UserCalendarService
	Meeting       UserMeetingService
	Authorization UserAuthService
}

func NewRuntime(cfg UserConfig, options RuntimeOptions, tokens TokenProvider) Runtime {
	profile := func(base string) userProfile {
		return userProfile{
			client:  transport.New(base, options.ConnectTimeout, options.RequestTimeout, options.HTTPClient, options.AccessChannel),
			tokens:  tokens,
			robot2c: strings.TrimSpace(cfg.Robot2CToken),
			ugate:   strings.TrimSpace(cfg.UGateToken),
		}
	}
	robot2c := profile(userBase(cfg.Robot2CBaseURL, DefaultRobot2CBaseURL))
	calendar := profile(userBase(cfg.CalendarBaseURL, DefaultUserCalendarBaseURL))
	meeting := profile(userBase(cfg.MeetingBaseURL, DefaultUserMeetingBaseURL))
	return Runtime{
		IM:            UserIMService{Message: UserMessageService{profile: robot2c}},
		Calendar:      UserCalendarService{profile: calendar, hubProfile: meeting},
		Meeting:       UserMeetingService{profile: meeting},
		Authorization: UserAuthService{profile: robot2c},
	}
}
func (p userProfile) headers(robot2c bool) (http.Header, error) {
	h := make(http.Header)
	if robot2c {
		if p.robot2c != "" {
			h.Set("Robot2c-Token", p.robot2c)
			return h, nil
		}
		token, err := p.resolvedUGateToken()
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, sdkerrors.NewConfig("user identity", "Robot2C or UGate token is required")
		}
		h.Set("Ugate-Token", token)
		return h, nil
	}
	token, err := p.resolvedUGateToken()
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, sdkerrors.NewConfig("UGate token", "is required")
	}
	h.Set("Ugate-Token", token)
	return h, nil
}

func (p userProfile) resolvedUGateToken() (string, error) {
	if strings.TrimSpace(p.ugate) != "" {
		return normalizeUGateToken(p.ugate)
	}
	return discoverUGateToken(), nil
}

// normalizeUGateToken accepts either the token itself or the local cache JSON
// value. In particular, it never forwards a JSON blob as a Ugate-Token header.
func normalizeUGateToken(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "{") {
		return value, nil
	}
	var cache struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(value), &cache); err != nil || strings.TrimSpace(cache.Token) == "" {
		return "", sdkerrors.NewConfig("UGateToken", "JSON input must contain a non-blank token field")
	}
	return strings.TrimSpace(cache.Token), nil
}
func discoverUGateToken() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(u.HomeDir, ".config", "uuap", ".eac_ugate_token_"+u.Username))
	if err != nil {
		return ""
	}
	var v struct {
		Token     string          `json:"token"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if json.Unmarshal(b, &v) != nil || len(v.ExpiresAt) != 0 {
		return ""
	}
	return strings.TrimSpace(v.Token)
}
func (p userProfile) get(ctx context.Context, route string, query url.Values, robot bool) (UserRawResult, error) {
	h, err := p.headers(robot)
	if err != nil {
		return UserRawResult{}, err
	}
	token := ""
	if robot {
		token, err = p.tokens.Token(ctx)
		if err != nil {
			return UserRawResult{}, err
		}
	}
	raw, err := p.client.GetWithHeaders(ctx, route, query, token, h)
	if err != nil {
		return UserRawResult{}, err
	}
	return decodeUserRoute(raw, route)
}
func (p userProfile) post(ctx context.Context, route string, body any, robot bool) (UserRawResult, error) {
	h, err := p.headers(robot)
	if err != nil {
		return UserRawResult{}, err
	}
	token := ""
	if robot {
		token, err = p.tokens.Token(ctx)
		if err != nil {
			return UserRawResult{}, err
		}
	}
	raw, err := p.client.PostJSONWithHeaders(ctx, route, body, token, h)
	if err != nil {
		return UserRawResult{}, err
	}
	return decodeUserRoute(raw, route)
}

func decodeUserRoute(raw []byte, route string) (UserRawResult, error) {
	result, err := decodeUser(raw)
	if err != nil {
		return UserRawResult{}, err
	}
	if err := routeBusinessError(raw, route); err != nil {
		return UserRawResult{}, err
	}
	return result, nil
}

// routeBusinessError follows only the envelope paths declared for the
// asynchronous user-message routes. The outer `code=ok` acknowledgement
// is not sufficient when the route's nested business code rejects the input.
func routeBusinessError(raw []byte, route string) error {
	paths, ok := map[string][][]string{
		"/im/robot/summary":    {{"data", "code"}, {"data", "data", "code"}},
		"/im/importMsgSummary": {{"data", "code"}, {"data", "data", "code"}},
	}[route]
	if !ok {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	for _, path := range paths {
		object, ok := userEnvelopeObjectAt(root, path[:len(path)-1])
		if !ok {
			continue
		}
		code, ok := userBusinessCode(object[path[len(path)-1]])
		if !ok || userBusinessCodeSuccess(code) {
			continue
		}
		return sdkerrors.NewBusiness(code, userBusinessMessage(object), raw, userRequestID(root))
	}
	return nil
}

func userEnvelopeObjectAt(root map[string]json.RawMessage, path []string) (map[string]json.RawMessage, bool) {
	current := root
	for _, key := range path {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(current[key], &nested); err != nil || nested == nil {
			return nil, false
		}
		current = nested
	}
	return current, true
}

func userBusinessCode(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String(), true
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value), true
	}
	return "", false
}

func userBusinessCodeSuccess(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "0", "200", "ok":
		return true
	default:
		return false
	}
}

func userBusinessMessage(object map[string]json.RawMessage) string {
	for _, key := range []string{"errmsg", "message", "msg", "returnMessage"} {
		var value string
		if json.Unmarshal(object[key], &value) == nil && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func userRequestID(root map[string]json.RawMessage) string {
	objects := []map[string]json.RawMessage{root}
	if data, ok := userEnvelopeObjectAt(root, []string{"data"}); ok {
		objects = append(objects, data)
		if nested, ok := userEnvelopeObjectAt(root, []string{"data", "data"}); ok {
			objects = append(objects, nested)
		}
	}
	for _, object := range objects {
		for _, key := range []string{"requestId", "request_id"} {
			var value string
			if json.Unmarshal(object[key], &value) == nil && value != "" {
				return value
			}
		}
	}
	return ""
}

func decodeUser(raw []byte) (UserRawResult, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return UserRawResult{}, sdkerrors.NewMalformed("user response is invalid", raw, err)
	}
	for _, key := range []string{"errcode", "code"} {
		if code, ok := root[key]; ok {
			var n json.Number
			if json.Unmarshal(code, &n) == nil && n.String() != "0" && n.String() != "200" {
				return UserRawResult{}, sdkerrors.NewBusiness(n.String(), string(root["errmsg"]), raw)
			}
			var s string
			if json.Unmarshal(code, &s) == nil && s != "ok" && s != "0" && s != "200" {
				return UserRawResult{}, sdkerrors.NewBusiness(s, string(root["message"]), raw)
			}
		}
	}
	data := root["data"]
	if len(data) == 0 {
		data = append(json.RawMessage(nil), raw...)
	}
	return UserRawResult{Data: append(json.RawMessage(nil), data...), raw: append(json.RawMessage(nil), raw...)}, nil
}
func (s UserAuthService) List(ctx context.Context, r userauthorization.ListRequest) (UserRawResult, error) {
	q := url.Values{}
	if r.InstanceID != "" {
		q.Set("instanceId", r.InstanceID)
	}
	if r.Status != "" {
		q.Set("status", r.Status)
	}
	if r.ST != 0 {
		q.Set("st", strconv.Itoa(r.ST))
	}
	if r.PageSize != 0 {
		q.Set("pageSize", strconv.Itoa(r.PageSize))
	}
	if r.PageToken != "" {
		q.Set("pageToken", r.PageToken)
	}
	return s.profile.get(ctx, "/robot2c/auth/list", q, true)
}
func (s UserAuthService) Grant(ctx context.Context, r userauthorization.GrantRequest) (UserRawResult, error) {
	if strings.TrimSpace(r.InstanceID) == "" || strings.TrimSpace(r.User) == "" || len(r.Permissions) == 0 || r.ExpiredDays <= 0 {
		return UserRawResult{}, sdkerrors.NewConfig("grant", "user, instanceId, permissions, and expiredDays are required")
	}
	return s.profile.post(ctx, "/robot2c/auth/grant", r, true)
}
func (s UserAuthService) Revoke(ctx context.Context, r userauthorization.RevokeRequest) (UserRawResult, error) {
	if strings.TrimSpace(r.InstanceID) == "" || strings.TrimSpace(r.User) == "" {
		return UserRawResult{}, sdkerrors.NewConfig("revoke", "user and instanceId are required")
	}
	return s.profile.post(ctx, "/robot2c/auth/revoke", r, true)
}
func (s UserMessageService) GetLatestContacts(ctx context.Context, r LatestContactsRequest) (LatestContactsResult, error) {
	if r.MaxPages <= 0 {
		r.MaxPages = 10
	}
	if r.Limit < 0 {
		return LatestContactsResult{}, sdkerrors.NewConfig("limit", "must not be negative")
	}
	seen := map[string]bool{}
	out := LatestContactsResult{}
	timestamp := int64(0)
	for page := 0; page < r.MaxPages; page++ {
		raw, err := s.profile.post(ctx, "/im/get_latest_contacts", map[string]any{"timestamp": timestamp, "page_start": 0}, true)
		if err != nil {
			return LatestContactsResult{}, err
		}
		if r.IncludeRaw {
			out.RawPages = append(out.RawPages, raw.RawJSON())
		}
		var root struct {
			Code      int                        `json:"code"`
			Msg       string                     `json:"msg"`
			Data      map[string]json.RawMessage `json:"data"`
			Timestamp json.RawMessage            `json:"timestamp"`
		}
		_ = json.Unmarshal(raw.RawJSON(), &root)
		for _, payload := range latestContactsPayloadMaps(root.Data) {
			for _, name := range []string{"top_list", "change_set", "delete_set", "unknown_set", "topInfo", "info", "deleteSet", "unknownSet"} {
				for _, v := range latestContactValues(payload, name) {
					id := latestContactString(v["id"])
					typ := latestContactString(v["type"])
					key := typ + ":" + id
					if !seen[key] {
						seen[key] = true
						contact := UserContact{ID: id, Type: typ, Name: latestContactString(v["name"])}
						if r.IncludeRaw {
							contact.Raw = append(json.RawMessage(nil), mustJSON(v)...)
						}
						out.Contacts = append(out.Contacts, contact)
					}
				}
			}
		}
		if r.Limit > 0 && len(out.Contacts) >= r.Limit {
			out.Contacts = out.Contacts[:r.Limit]
			break
		}
		businessCode := root.Code
		if dataCode := latestContactsCode(root.Data); dataCode != 0 {
			businessCode = dataCode
		}
		pageTimestamp := parseLatestContactsTimestamp(root.Timestamp)
		if pageTimestamp == 0 {
			pageTimestamp = latestContactsTimestamp(root.Data)
		}
		if businessCode != 210 || (root.Msg != "" && root.Msg != "success") || pageTimestamp == 0 || pageTimestamp == timestamp {
			break
		}
		timestamp = pageTimestamp
	}
	out.Text = strconv.Itoa(len(out.Contacts)) + " latest contacts"
	return out, nil
}

func latestContactsPayloadMaps(data map[string]json.RawMessage) []map[string]json.RawMessage {
	if data == nil {
		return nil
	}
	maps := make([]map[string]json.RawMessage, 0, 4)
	var walk func(map[string]json.RawMessage, int)
	walk = func(current map[string]json.RawMessage, depth int) {
		if current == nil || depth > 3 {
			return
		}
		maps = append(maps, current)
		for _, key := range []string{"data", "result_set", "resultSet"} {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(current[key], &nested); err == nil && nested != nil {
				walk(nested, depth+1)
			}
		}
	}
	walk(data, 0)
	return maps
}

func latestContactValues(data map[string]json.RawMessage, name string) []map[string]json.RawMessage {
	raw, ok := data[name]
	if !ok {
		return nil
	}
	if values, ok := latestContactArray(raw); ok {
		return values
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
		return nil
	}
	for _, key := range []string{"result_set." + name, name} {
		if values, ok := latestContactArray(nested[key]); ok {
			return values
		}
	}
	return nil
}

func latestContactArray(raw json.RawMessage) ([]map[string]json.RawMessage, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var values []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

func latestContactString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}

func latestContactsTimestamp(data map[string]json.RawMessage) int64 {
	for _, payload := range latestContactsPayloadMaps(data) {
		if value := parseLatestContactsTimestamp(payload["timestamp"]); value != 0 {
			return value
		}
	}
	return 0
}

func latestContactsCode(data map[string]json.RawMessage) int {
	for _, payload := range latestContactsPayloadMaps(data) {
		if value := parseLatestContactsTimestamp(payload["code"]); value != 0 {
			return int(value)
		}
	}
	return 0
}

func parseLatestContactsTimestamp(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var value int64
	if json.Unmarshal(raw, &value) == nil && value != 0 {
		return value
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		value, _ = strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	}
	return value
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (s UserMessageService) CreateSummary(ctx context.Context, r CreateSummaryRequest) (SummaryResult, error) {
	chatType, err := summaryChatType(r.ChatType)
	if strings.TrimSpace(r.ChatID) == "" || err != nil || r.StartTime <= 0 || r.EndTime <= r.StartTime {
		if err != nil {
			return SummaryResult{}, err
		}
		return SummaryResult{}, sdkerrors.NewConfig("summary", "chat and valid millisecond window are required")
	}
	x, e := s.profile.post(ctx, "/im/robot/summary", map[string]any{"chatId": r.ChatID, "chatType": chatType, "startTime": r.StartTime, "endTime": r.EndTime}, true)
	return summary(x, e)
}
func (s UserMessageService) ImportSummary(ctx context.Context, r ImportSummaryRequest) (SummaryResult, error) {
	if r.StartQueryTime <= 0 || r.EndQueryTime <= r.StartQueryTime {
		return SummaryResult{}, sdkerrors.NewConfig("summary", "valid millisecond window is required")
	}
	if r.TopicType != "" && r.TopicType != "1" && r.TopicType != "2" {
		return SummaryResult{}, sdkerrors.NewConfig("topicType", "must be \"1\" or \"2\"")
	}
	if r.UnreadType != "" && r.UnreadType != "1" {
		return SummaryResult{}, sdkerrors.NewConfig("unreadType", "must be \"1\"")
	}
	body := map[string]any{"startQueryTime": strconv.FormatInt(r.StartQueryTime, 10), "endQueryTime": strconv.FormatInt(r.EndQueryTime, 10)}
	if intent := strings.TrimSpace(r.Intent); intent != "" {
		if runes := []rune(intent); len(runes) > 50 {
			intent = string(runes[:50])
		}
		body["intent"] = intent
	}
	if r.TopicType != "" {
		body["topicType"] = r.TopicType
	}
	if r.UnreadType != "" {
		body["unreadType"] = r.UnreadType
	}
	x, e := s.profile.post(ctx, "/im/importMsgSummary", body, true)
	return summary(x, e)
}

func summaryChatType(value string) (int, error) {
	switch strings.TrimSpace(value) {
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	case "7":
		return 7, nil
	default:
		return 0, sdkerrors.NewConfig("chatType", "must be 1, 2, or 7")
	}
}
func summary(x UserRawResult, e error) (SummaryResult, error) {
	if e != nil {
		return SummaryResult{}, e
	}
	var v map[string]json.RawMessage
	_ = json.Unmarshal(x.Data, &v)
	id, task := summaryCorrelation(v)
	var root map[string]json.RawMessage
	_ = json.Unmarshal(x.RawJSON(), &root)
	if rawID, rawTask := summaryCorrelation(root); id == "" || task == "" {
		if id == "" {
			id = rawID
		}
		if task == "" {
			task = rawTask
		}
	}
	return SummaryResult{RequestID: id, TaskID: task, raw: x.RawJSON()}, nil
}

func summaryCorrelation(root map[string]json.RawMessage) (string, string) {
	var id, task string
	levels := []map[string]json.RawMessage{root}
	current := root
	for i := 0; i < 3; i++ {
		var child map[string]json.RawMessage
		if json.Unmarshal(current["data"], &child) != nil || len(child) == 0 {
			break
		}
		levels = append(levels, child)
		current = child
	}
	for _, level := range levels {
		if id == "" {
			for _, key := range []string{"requestId", "request_id"} {
				_ = json.Unmarshal(level[key], &id)
				if id != "" {
					break
				}
			}
		}
		if task == "" {
			for _, key := range []string{"taskId", "task_id"} {
				_ = json.Unmarshal(level[key], &task)
				if task != "" {
					break
				}
			}
		}
	}
	return id, task
}
func calendarValid(r CalendarRequest) error {
	if strings.TrimSpace(r.EventID) == "" && r.Body == nil {
		return sdkerrors.NewConfig("calendar", "eventId or body is required")
	}
	return nil
}
func (s UserCalendarService) CreateEvent(ctx context.Context, r CalendarRequest) (UserRawResult, error) {
	if r.Body == nil {
		return UserRawResult{}, sdkerrors.NewConfig("event", "body is required")
	}
	return s.profile.post(ctx, "/v1/calendar/events/v4", r.Body, false)
}
func (s UserCalendarService) UpdateEvent(ctx context.Context, r CalendarRequest) (UserRawResult, error) {
	if strings.TrimSpace(r.EventID) == "" {
		return UserRawResult{}, sdkerrors.NewConfig("eventId", "is required")
	}
	b := r.Body
	if b == nil {
		b = map[string]any{}
	}
	b["eventId"] = r.EventID
	return s.profile.post(ctx, "/v1/calendar/events/v4/update", b, false)
}
func (s UserCalendarService) DeleteEvent(ctx context.Context, r CalendarRequest) (UserRawResult, error) {
	if strings.TrimSpace(r.EventID) == "" {
		return UserRawResult{}, sdkerrors.NewConfig("eventId", "is required")
	}
	q := url.Values{"eventId": {r.EventID}}
	return s.profile.get(ctx, "/v1/calendar/events/v4/delete", q, false)
}
func (s UserCalendarService) GetEvent(ctx context.Context, r CalendarRequest) (UserRawResult, error) {
	if strings.TrimSpace(r.EventID) == "" {
		return UserRawResult{}, sdkerrors.NewConfig("eventId", "is required")
	}
	q := url.Values{"eventId": {r.EventID}}
	return s.hubProfile.get(ctx, "/calendar/openUGate/api/v1/events", q, false)
}
func (s UserCalendarService) QueryMyMeetingDetail(ctx context.Context, r MeetingListRequest) (UserRawResult, error) {
	return s.hubProfile.post(ctx, "/workflow/api/meeting/inner/listall", meetingBody(r), false)
}
func (s UserCalendarService) GetFreeBusy(ctx context.Context, r FreeBusyRequest) (UserRawResult, error) {
	if r.StartTime <= 0 || r.EndTime <= r.StartTime || len(r.Emails) == 0 {
		return UserRawResult{}, sdkerrors.NewConfig("free busy", "valid time window and emails are required")
	}
	return s.profile.post(ctx, "/v1/calendar/freebusy", r, false)
}
func (s UserCalendarService) QuerySchedule(ctx context.Context, r FreeBusyRequest) (UserRawResult, error) {
	return s.GetFreeBusy(ctx, r)
}
func meetingBody(r MeetingListRequest) map[string]any {
	// The user meeting service contract uses a numeric UTC offset (8 for
	// Asia/Shanghai). Preserve the public string field for compatibility while
	// emitting a number when callers provide the documented numeric form.
	var timeZone any = r.TimeZone
	if n, err := strconv.Atoi(strings.TrimSpace(r.TimeZone)); err == nil {
		timeZone = n
	}
	return map[string]any{"beginTime": r.BeginTime, "endTime": r.EndTime, "timeZone": timeZone, "pageSize": r.PageSize, "showEnd": r.ShowEnd, "showCalendar": r.ShowCalendar, "pageNumber": r.PageNumber, "filterTypes": r.FilterTypes, "filterItems": r.FilterItems}
}
func (s UserMeetingService) ListMeetings(ctx context.Context, r MeetingListRequest) (UserRawResult, error) {
	if r.BeginTime <= 0 || strings.TrimSpace(r.TimeZone) == "" || r.PageSize <= 0 || r.PageNumber <= 0 {
		return UserRawResult{}, sdkerrors.NewConfig("meeting list", "beginTime, timeZone, pageSize, and pageNumber are required")
	}
	return s.profile.post(ctx, "/workflow/api/meeting/inner/listall", meetingBody(r), false)
}
func (s UserMeetingService) FetchASR(ctx context.Context, eventID string) (UserRawResult, error) {
	return s.fetch(ctx, "/calendar/openUGate/api/v1/fetchasr", eventID)
}
func (s UserMeetingService) FetchAIGC(ctx context.Context, eventID string) (UserRawResult, error) {
	return s.fetch(ctx, "/calendar/openUGate/api/v1/fetchaigc", eventID)
}
func (s UserMeetingService) FetchLinkInfo(ctx context.Context, eventID string) (UserRawResult, error) {
	return s.fetch(ctx, "/calendar/openUGate/api/v1/fetchLinkInfo", eventID)
}
func (s UserMeetingService) fetch(ctx context.Context, path, eventID string) (UserRawResult, error) {
	if strings.TrimSpace(eventID) == "" {
		return UserRawResult{}, sdkerrors.NewConfig("eventId", "is required")
	}
	return s.profile.post(ctx, path, map[string]string{"eventId": eventID}, false)
}
func (s UserMeetingService) CallMeeting(ctx context.Context, r CallMeetingRequest) (UserRawResult, error) {
	if strings.TrimSpace(r.Subject) == "" || len(r.InviteUsers) == 0 {
		return UserRawResult{}, sdkerrors.NewConfig("call meeting", "subject and inviteUsers are required")
	}
	return s.profile.post(ctx, "/calendar/openUGate/api/v1/callMeeting", r, false)
}

var _ = time.Second
