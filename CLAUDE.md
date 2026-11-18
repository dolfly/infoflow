# InfoFlow Go SDK

This repository is an independent Go SDK. Read `CONTRACT.md` and the relevant
file under `spec/` before changing public behavior.

Always preserve these rules:

- Keep `Client -> domain service -> transport/auth/envelope/error` boundaries.
- Use only the Go standard library until the contract explicitly changes.
- Never log credentials, tokens, Authorization headers, or message content.
- Do not retry non-idempotent message sends.
- Add or update tests before implementation and run `gofmt`, `go vet`,
  `go test ./...`, and `go test -race ./...` on Go 1.25 and 1.26.
- Do not add a remote, push, tag, or publish without a separate release task.
- Keep worker evidence under ignored `.sdk-factory/`, never in source commits.
- If public API changes (new capabilities, exported methods/types, scope
  wiring), update `CHANGELOG.md`, `README.md` (including stale boundary
  statements), and `docs/API_REFERENCE.md` in the same commit.

Repository design is in `ARCHITECTURE.md`; current delivery state is in
`PLAN.md`, `CHANGELOG.md`, `spec/`, and `story/`.
