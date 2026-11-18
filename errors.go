package infoflow

import "github.com/dolfly/infoflow/internal/sdkerrors"

type ConfigError = sdkerrors.ConfigError
type AuthError = sdkerrors.AuthError
type HTTPError = sdkerrors.HTTPError
type BusinessError = sdkerrors.BusinessError
type MalformedResponseError = sdkerrors.MalformedResponseError
