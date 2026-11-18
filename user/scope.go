// Package user owns capabilities invoked with User/Robot2C/UGate identity.
package user

import (
	"context"
	"net/http"
	"time"

	"github.com/dolfly/infoflow/user/calendar"
	"github.com/dolfly/infoflow/user/im"
	"github.com/dolfly/infoflow/user/internal/core"
	"github.com/dolfly/infoflow/user/meeting"
	"github.com/dolfly/infoflow/userauthorization"
)

const (
	DefaultRobot2CBaseURL      = core.DefaultRobot2CBaseURL
	DefaultUserCalendarBaseURL = core.DefaultUserCalendarBaseURL
	DefaultUserMeetingBaseURL  = core.DefaultUserMeetingBaseURL
)

type Config struct {
	Robot2CToken    string
	UGateToken      string
	Robot2CBaseURL  string
	CalendarBaseURL string
	MeetingBaseURL  string
}

type RuntimeOptions struct {
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	HTTPClient     *http.Client
	AccessChannel  string
	Tokens         TokenProvider
}

type TokenProvider interface {
	Token(context.Context) (string, error)
}

type Scope struct {
	IM       im.Scope
	Calendar calendar.Service
	Meeting  meeting.Service
}

func NewRuntime(config Config, options RuntimeOptions) (Scope, userauthorization.Service) {
	internalConfig := core.UserConfig{
		Robot2CToken: config.Robot2CToken, UGateToken: config.UGateToken,
		Robot2CBaseURL: config.Robot2CBaseURL, CalendarBaseURL: config.CalendarBaseURL,
		MeetingBaseURL: config.MeetingBaseURL,
	}
	runtime := core.NewRuntime(internalConfig, core.RuntimeOptions{
		ConnectTimeout: options.ConnectTimeout,
		RequestTimeout: options.RequestTimeout,
		HTTPClient:     options.HTTPClient,
		AccessChannel:  options.AccessChannel,
	}, options.Tokens)
	convert := func(result core.UserRawResult, err error) (userauthorization.Result, error) {
		if err != nil {
			return userauthorization.Result{}, err
		}
		return userauthorization.NewResult(result.Data, result.RawJSON()), nil
	}
	authorization := userauthorization.NewService(
		func(ctx context.Context, request userauthorization.ListRequest) (userauthorization.Result, error) {
			return convert(runtime.Authorization.List(ctx, request))
		},
		func(ctx context.Context, request userauthorization.GrantRequest) (userauthorization.Result, error) {
			return convert(runtime.Authorization.Grant(ctx, request))
		},
		func(ctx context.Context, request userauthorization.RevokeRequest) (userauthorization.Result, error) {
			return convert(runtime.Authorization.Revoke(ctx, request))
		},
	)
	return Scope{
		IM:       im.Scope{Message: im.NewService(runtime.IM.Message)},
		Calendar: calendar.NewService(runtime.Calendar),
		Meeting:  meeting.NewService(runtime.Meeting),
	}, authorization
}
