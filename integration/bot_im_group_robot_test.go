package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	infoflow "github.com/dolfly/infoflow"
	botim "github.com/dolfly/infoflow/bot/im"
	"github.com/dolfly/infoflow/bot/robot"
)

func TestCreateGroupPreservesPlatformCodeAndMessage(t *testing.T) {
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":321,"errmsg":"owner is not eligible"}}`)
	})
	defer s.Close()
	_, err := newFixtureClient(t, s, time.Now).Bot.IM.Group.Create(context.Background(), botim.CreateGroupRequest{
		GroupName: "safe", GroupOwner: "owner@baidu.com", FriendlyLevel: 3, SearchAbility: 0,
	})
	var business *infoflow.BusinessError
	if !errors.As(err, &business) {
		t.Fatalf("error = %T %v, want infoflow.BusinessError", err, err)
	}
	if business.Code != "321" || business.Message != "owner is not eligible" {
		t.Fatalf("business = %#v", business)
	}
}

func TestCreateGroupBFlexConformance(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		groupID   int64
		business  string
		requestID string
		malformed bool
	}{
		{"root-groupid", `{"code":"ok","groupid":101}`, 101, "", "", false},
		{"root-groupId", `{"code":"ok","groupId":102}`, 102, "", "", false},
		{"data-groupid-with-request-id", `{"code":"ok","data":{"errcode":0,"groupid":103,"requestId":"r-103"}}`, 103, "", "", false},
		{"nested-group-id", `{"code":"ok","data":{"errcode":0,"data":{"groupId":104}}}`, 104, "", "", false},
		{"declared-business-error", `{"code":"ok","data":{"errcode":321,"errmsg":"owner is not eligible","request_id":"r-321"}}`, 0, "321", "r-321", false},
		{"missing-group-id", `{"code":"ok","data":{"errcode":0}}`, 0, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, tt.body)
			})
			defer s.Close()
			result, err := newFixtureClient(t, s, time.Now).Bot.IM.Group.Create(context.Background(), botim.CreateGroupRequest{
				GroupName: "safe", GroupOwner: "owner@baidu.com", FriendlyLevel: 3, SearchAbility: 0,
			})
			if tt.business != "" {
				var business *infoflow.BusinessError
				if !errors.As(err, &business) {
					t.Fatalf("error = %T %v, want infoflow.BusinessError", err, err)
				}
				if business.Code != tt.business || business.Code == "ok" || business.RequestID != tt.requestID {
					t.Fatalf("business = %#v", business)
				}
				return
			}
			if tt.malformed {
				var malformed *infoflow.MalformedResponseError
				if !errors.As(err, &malformed) {
					t.Fatalf("error = %T %v, want infoflow.MalformedResponseError", err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.GroupID != tt.groupID {
				t.Fatalf("group ID = %d, want %d", result.GroupID, tt.groupID)
			}
		})
	}
}

func TestCreateGroupBFlexMalformedRetainsRequestID(t *testing.T) {
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"requestId":"r-incomplete"}}`)
	})
	defer s.Close()
	_, err := newFixtureClient(t, s, time.Now).Bot.IM.Group.Create(context.Background(), botim.CreateGroupRequest{
		GroupName: "safe", GroupOwner: "owner@baidu.com", FriendlyLevel: 3, SearchAbility: 0,
	})
	var malformed *infoflow.MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("error = %T %v, want infoflow.MalformedResponseError", err, err)
	}
	if malformed.RequestID != "r-incomplete" {
		t.Fatalf("request ID = %q", malformed.RequestID)
	}
}

func TestCreateGroupBFlexDoesNotRecursivelySearchPayload(t *testing.T) {
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"other":{"groupid":106}}}`)
	})
	defer s.Close()
	_, err := newFixtureClient(t, s, time.Now).Bot.IM.Group.Create(context.Background(), botim.CreateGroupRequest{
		GroupName: "safe", GroupOwner: "owner@baidu.com", FriendlyLevel: 3, SearchAbility: 0,
	})
	var malformed *infoflow.MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("error = %T %v, want infoflow.MalformedResponseError", err, err)
	}
}

func TestCreateGroupBFlexRetainsExistingNestedCreateShape(t *testing.T) {
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"groupid":105,"failMembers":["a@baidu.com"]}}}`)
	})
	defer s.Close()
	result, err := newFixtureClient(t, s, time.Now).Bot.IM.Group.Create(context.Background(), botim.CreateGroupRequest{
		GroupName: "safe", GroupOwner: "owner@baidu.com", FriendlyLevel: 3, SearchAbility: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.GroupID != 105 || len(result.FailMembers) != 1 || result.FailMembers[0] != "a@baidu.com" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGroupWorkbenchRequestShapes(t *testing.T) {
	tests := []struct {
		path, method string
		response     string
		call         func(*infoflow.Client) error
		check        func(map[string]any) bool
	}{
		{"/api/v1/robot/group/updateName", http.MethodPost, `{"code":"ok","data":{"errcode":0,"data":{}}}`, func(c *infoflow.Client) error {
			_, e := c.Bot.IM.Group.UpdateName(context.Background(), botim.UpdateGroupNameRequest{GroupID: 7, Name: "n"})
			return e
		}, func(b map[string]any) bool { return b["groupId"] == float64(7) && b["name"] == "n" }},
		{"/api/v1/im/group/quit", http.MethodPost, `{"code":"ok","data":{"errcode":0,"data":{}}}`, func(c *infoflow.Client) error {
			_, e := c.Bot.IM.Group.Quit(context.Background(), botim.GroupIDRequest{GroupID: 7})
			return e
		}, func(b map[string]any) bool { return b["groupId"] == float64(7) }},
		{"/api/v1/im/group/sidebar", http.MethodPut, `{"code":"ok","data":{"errcode":0,"data":{}}}`, func(c *infoflow.Client) error {
			_, e := c.Bot.IM.Group.SetSidebar(context.Background(), botim.SidebarRequest{GroupID: 7, SidebarLink: "https://x", AutoOpen: botim.Int(1), CustomSidebar: botim.Int(0)})
			return e
		}, func(b map[string]any) bool {
			return b["groupId"] == float64(7) && b["sidebarLink"] == "https://x" && b["autoOpen"] == float64(1) && b["customSidebar"] == float64(0)
		}},
		{"/api/v1/im/group/sidebar", http.MethodPut, `{"code":"ok","data":{"errcode":0,"data":{}}}`, func(c *infoflow.Client) error {
			_, e := c.Bot.IM.Group.SetSidebar(context.Background(), botim.SidebarRequest{GroupID: 7, SidebarLink: "https://x"})
			return e
		}, func(b map[string]any) bool {
			_, autoOpenOK := b["autoOpen"]
			_, customSidebarOK := b["customSidebar"]
			return b["groupId"] == float64(7) && b["sidebarLink"] == "https://x" && !autoOpenOK && !customSidebarOK
		}},
		{"/api/v1/im/group/managers/set", http.MethodPost, `{"code":"ok","data":{"errcode":0,"data":{}}}`, func(c *infoflow.Client) error {
			_, e := c.Bot.IM.Group.SetManagers(context.Background(), botim.SetManagersRequest{GroupID: 7, UserEmails: []string{"member@example.com"}, RobotAgentIDs: []int64{}})
			return e
		}, func(b map[string]any) bool {
			users, usersOK := b["userEmails"].([]any)
			robots, robotsOK := b["robotAgentIds"].([]any)
			return b["groupId"] == float64(7) && usersOK && len(users) == 1 && users[0] == "member@example.com" && robotsOK && len(robots) == 0
		}},
		{"/api/v1/robot/commands/set", http.MethodPost, `{"code":"ok","data":{"errcode":200,"errmsg":"success","data":{}}}`, func(c *infoflow.Client) error {
			_, e := c.Bot.Robot.SetShortcutCommands(context.Background(), []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: "help", Description: "show"}}}})
			return e
		}, func(b map[string]any) bool { return len(b["all_commands"].([]any)) == 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path || r.Method != tt.method {
					t.Fatalf("%s %s", r.Method, r.URL.Path)
				}
				var b map[string]any
				_ = json.NewDecoder(r.Body).Decode(&b)
				if !tt.check(b) {
					t.Fatalf("body=%#v", b)
				}
				writeJSON(w, 200, tt.response)
			})
			defer s.Close()
			if e := tt.call(newFixtureClient(t, s, time.Now)); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestShortcutCommandRoutesAcceptNestedErrcode200(t *testing.T) {
	commands := []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: "help", Description: "show"}}}}
	tests := []struct {
		name string
		path string
		call func(*infoflow.Client) error
	}{
		{"get", "/api/v1/robot/commands/get", func(c *infoflow.Client) error {
			_, err := c.Bot.Robot.GetShortcutCommands(context.Background())
			return err
		}},
		{"set", "/api/v1/robot/commands/set", func(c *infoflow.Client) error {
			_, err := c.Bot.Robot.SetShortcutCommands(context.Background(), commands)
			return err
		}},
		{"append", "/api/v1/robot/commands/append", func(c *infoflow.Client) error {
			_, err := c.Bot.Robot.AppendShortcutCommands(context.Background(), commands)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path || r.Method != http.MethodPost {
					t.Fatalf("%s %s", r.Method, r.URL.Path)
				}
				writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":200,"errmsg":"success","data":{"all_commands":[]}}}`)
			})
			defer s.Close()
			if err := tt.call(newFixtureClient(t, s, time.Now)); err != nil {
				t.Fatalf("shortcut %s returned error for errcode 200: %v", tt.name, err)
			}
		})
	}
}

func TestShortcutCommandRoutesRejectOtherNestedErrcodes(t *testing.T) {
	commands := []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: "help", Description: "show"}}}}
	tests := []struct {
		name string
		path string
		call func(*infoflow.Client) error
	}{
		{"get", "/api/v1/robot/commands/get", func(c *infoflow.Client) error {
			_, err := c.Bot.Robot.GetShortcutCommands(context.Background())
			return err
		}},
		{"set", "/api/v1/robot/commands/set", func(c *infoflow.Client) error {
			_, err := c.Bot.Robot.SetShortcutCommands(context.Background(), commands)
			return err
		}},
		{"append", "/api/v1/robot/commands/append", func(c *infoflow.Client) error {
			_, err := c.Bot.Robot.AppendShortcutCommands(context.Background(), commands)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path || r.Method != http.MethodPost {
					t.Fatalf("%s %s", r.Method, r.URL.Path)
				}
				writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":201,"errmsg":"rejected","data":{}}}`)
			})
			defer s.Close()
			err := tt.call(newFixtureClient(t, s, time.Now))
			var business *infoflow.BusinessError
			if !errors.As(err, &business) || business.Code != "201" || business.Message != "rejected" {
				t.Fatalf("shortcut %s error = %#v, want infoflow.BusinessError code 201", tt.name, err)
			}
		})
	}
}

func TestUnrelatedRobotRouteStillRejectsNestedErrcode200(t *testing.T) {
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/imRobot/detail" || r.Method != http.MethodPost {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":200,"errmsg":"not a success policy for this route","data":{}}}`)
	})
	defer s.Close()
	_, err := newFixtureClient(t, s, time.Now).Bot.Robot.GetInfo(context.Background())
	var business *infoflow.BusinessError
	if !errors.As(err, &business) || business.Code != "200" {
		t.Fatalf("error = %#v, want infoflow.BusinessError code 200", err)
	}
}

func TestModifyUserRemarkSerializesEmailAsUUAPPrefix(t *testing.T) {
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/im/group/modifyUserRemark" || r.Method != http.MethodPost {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["toUserId"] != "fixture-owner" || body["groupId"] != "7" || body["remark"] != "safe" {
			t.Fatalf("body=%#v", body)
		}
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0}}`)
	})
	defer s.Close()
	_, err := newFixtureClient(t, s, time.Now).Bot.IM.Group.ModifyUserRemark(context.Background(), botim.ModifyUserRemarkRequest{ToUserID: "fixture-owner@baidu.com", GroupID: "7", Remark: "safe"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGroupWorkbenchLocalValidation(t *testing.T) {
	c := &botim.GroupService{}
	if _, e := c.UpdateMenuVisible(context.Background(), botim.UpdateMenuVisibilityRequest{GroupID: 1, CustomMenus: botim.MenuVisibility{OperationType: "bad"}}); e == nil {
		t.Fatal("expected menu validation")
	}
	r := &robot.Service{}
	if _, e := r.SetShortcutCommands(context.Background(), []robot.ShortcutCommandGroup{{Type: 5}}); e == nil {
		t.Fatal("expected command validation")
	}
}

func TestRobotMenuFacadeUsesGetAndUpdateRoutes(t *testing.T) {
	server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/robot/menu/get":
			if r.Method != http.MethodGet {
				t.Fatalf("method = %s", r.Method)
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"menus":[{"name":"Help","action":{"dataType":"push","eventkey":"help"}}]}}}`)
		case "/api/v1/robot/menu/update":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			var body struct {
				Menus []robot.MenuItem `json:"menus"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Menus) != 1 || body.Menus[0].Name != "Help" {
				t.Fatalf("body = %#v", body)
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{}}}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer server.Close()
	client := newFixtureClient(t, server, time.Now)
	got, err := client.Bot.IM.Menu.Get(context.Background())
	if err != nil || len(got.Menus) != 1 || got.Menus[0].Action == nil || got.Menus[0].Action.EventKey != "help" {
		t.Fatalf("get result=%#v err=%v", got, err)
	}
	if _, err := client.Bot.IM.Menu.Update(context.Background(), got.Menus); err != nil {
		t.Fatalf("update error = %v", err)
	}
}

func TestShortcutCommandRoutesAcceptEmptyCommandsForSupportedType(t *testing.T) {
	var routes []string
	s := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.URL.Path)
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		var body struct {
			AllCommands []robot.ShortcutCommandGroup `json:"all_commands"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.AllCommands) != 1 || body.AllCommands[0].Type != 1 || body.AllCommands[0].Commands == nil || len(body.AllCommands[0].Commands) != 0 {
			t.Fatalf("body = %#v, want one supported group with an empty commands array", body)
		}
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":200,"errmsg":"success","data":{}}}`)
	})
	defer s.Close()
	client := newFixtureClient(t, s, time.Now)
	empty := []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{}}}
	if _, err := client.Bot.Robot.SetShortcutCommands(context.Background(), empty); err != nil {
		t.Fatalf("set empty commands: %v", err)
	}
	if _, err := client.Bot.Robot.AppendShortcutCommands(context.Background(), empty); err != nil {
		t.Fatalf("append empty commands: %v", err)
	}
	if got := strings.Join(routes, ","); got != "/api/v1/robot/commands/set,/api/v1/robot/commands/append" {
		t.Fatalf("routes = %s", got)
	}
}

func TestShortcutCommandValidationStillRejectsInvalidGroupsAndEntries(t *testing.T) {
	tests := []struct {
		name     string
		commands []robot.ShortcutCommandGroup
	}{
		{"unsupported type", []robot.ShortcutCommandGroup{{Type: 5, Commands: []robot.ShortcutCommand{}}}},
		{"blank name", []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: "", Description: "show"}}}}},
		{"blank description", []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: "help", Description: ""}}}}},
		{"name too long", []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: strings.Repeat("n", 101), Description: "show"}}}}},
		{"description too long", []robot.ShortcutCommandGroup{{Type: 1, Commands: []robot.ShortcutCommand{{Name: "help", Description: strings.Repeat("d", 101)}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := infoflow.NewClient(infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: &http.Client{Transport: &countingRoundTripper{}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Bot.Robot.SetShortcutCommands(context.Background(), tt.commands); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
