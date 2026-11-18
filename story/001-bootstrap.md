# Story 001: Go L0/L1 bootstrap

As an InfoFlow developer, I can construct one Go client with original app
credentials and send text to a user or group without managing tokens or raw
HTTP requests.

Acceptance is executable in repository tests: exact wire shapes, versioned URL
joining, typed results/errors, one-attempt sends, synchronized token caching,
early refresh, timeout defaults/overrides, local input rejection, raw defensive
copies, and both supported Go toolchains.

The story ends at local validation. Real-service smoke is blocked until an
owner supplies credentials and approved safe user/group targets.
