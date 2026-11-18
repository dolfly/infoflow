// Package userauthorization contains the public contracts for managing the
// cross-identity authorization relationship used by User scope calls.
package userauthorization

import (
	"context"
	"encoding/json"
)

type ListRequest struct {
	InstanceID, Status, PageToken string
	ST, PageSize                  int
}

type GrantRequest struct {
	InstanceID  string   `json:"instanceId"`
	User        string   `json:"user"`
	Permissions []string `json:"permissions"`
	ExpiredDays int      `json:"expiredDays"`
}

type RevokeRequest struct {
	InstanceID string `json:"instanceId"`
	User       string `json:"user"`
}

type Result struct {
	Data json.RawMessage
	raw  json.RawMessage
}

func NewResult(data, raw json.RawMessage) Result {
	return Result{Data: append(json.RawMessage(nil), data...), raw: append(json.RawMessage(nil), raw...)}
}

func (r Result) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type Service struct {
	list   func(context.Context, ListRequest) (Result, error)
	grant  func(context.Context, GrantRequest) (Result, error)
	revoke func(context.Context, RevokeRequest) (Result, error)
}

func NewService(
	list func(context.Context, ListRequest) (Result, error),
	grant func(context.Context, GrantRequest) (Result, error),
	revoke func(context.Context, RevokeRequest) (Result, error),
) Service {
	return Service{list: list, grant: grant, revoke: revoke}
}

func (s Service) List(ctx context.Context, request ListRequest) (Result, error) {
	return s.list(ctx, request)
}

func (s Service) Grant(ctx context.Context, request GrantRequest) (Result, error) {
	return s.grant(ctx, request)
}

func (s Service) Revoke(ctx context.Context, request RevokeRequest) (Result, error) {
	return s.revoke(ctx, request)
}
