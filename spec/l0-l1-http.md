# L0/L1 HTTP Specification

## Client and configuration

`NewClient` requires non-blank app key and original app secret. Empty optional
configuration selects the documented defaults. A zero duration means unset;
negative connect timeout, request timeout, or refresh skew is rejected. A base
URL must be an absolute HTTP(S) URL with a host and without user info, query,
or fragment.

The constructor copies configuration values. Caller mutation after
construction cannot change the active runtime. An injected `*http.Client` is
not mutated; its connection policy remains caller-owned, while the SDK applies
the configured request deadline.

## Authentication

The token request body is exactly `app_key` plus a lower-case MD5 hex digest of
the original `app_secret`. It has no Authorization header. A successful
`code: ok` envelope must contain a non-blank `data.app_access_token` and a
positive `data.expire` in seconds.

The token is reusable only while `now + refreshSkew < expiresAt`. Equality is
the early-refresh boundary. Refresh is serialized, including concurrent first
use.

## Message methods

`SendToUser(ctx, userID, text)` sends exactly:

```json
{"touser":"zhangsan","msgtype":"text","text":{"content":"Hello!"}}
```

`SendToGroup(ctx, groupID, text)` sends a `message` whose header fixes
`totype=GROUP`, `msgtype=TEXT`, and `role=robot`, and whose one body element
fixes `type=TEXT`. Its positive `clientmsgid` comes from the runtime clock.

Both methods acquire tokens internally and send `Authorization:
Bearer-{token}`. They make one attempt only.

Generated group client message IDs are positive and strictly increasing within
one Client. A package-private synchronized generator preserves that property
when the wall clock is fixed, moves backward, or group sends are concurrent.

## Responses and errors

Top-level non-`ok` auth responses become `AuthError`. Top-level non-`ok`
message responses and nonzero nested `errcode` values become `BusinessError`.
Non-2xx responses become `HTTPError`; invalid JSON or structurally incomplete
successful envelopes become `MalformedResponseError`.

Success results expose typed fields and complete raw JSON via a copying
accessor. Error raw accessors also copy. Error strings never render sensitive
configuration or request values.

L1 has no logger or telemetry hook. The created transport preserves Go's
default environment proxy resolution; an injected HTTP client preserves the
caller's proxy, TLS, and connection policy.
