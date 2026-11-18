package im

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

const (
	// BOSGatewayBaseURL is the fixed InfoFlow BOS upload and temporary-URL
	// gateway. File uploads and get-URL lookups always target this host.
	BOSGatewayBaseURL = "https://infoflow-open-gateway.baidu.com"

	fileBosUploadPath          = "/im/bos/upload"
	fileBosGetURLPath          = "/im/bos/getUrl"
	fileDownloadURLPrivatePath = "/open-file-service/file/get/download/url/robot-chat/byFid"
	fileDownloadURLGroupPath   = "/open-file-service/file/get/download/url/byFid"

	fileDefaultExpirationSeconds = 3600
	fileMaxExpirationSeconds     = 86400

	// fileObjectKeyPrefix is the fixed ASCII directory SDK-generated object
	// keys live under.
	fileObjectKeyPrefix = "sdk-uploads"
	// fileObjectKeyMaxLength bounds caller-supplied object keys.
	fileObjectKeyMaxLength = 1024
)

var (
	// fileObjectKeyPattern is the strict ASCII character set the BOS get-URL
	// route accepts.
	fileObjectKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	// fileObjectKeyExtPattern extracts a safe, short extension from a file
	// name; anything else is dropped.
	fileObjectKeyExtPattern = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)
)

// FileChatType selects the private or group chat file download route.
type FileChatType int

const (
	FileChatPrivate FileChatType = 1
	FileChatGroup   FileChatType = 2
)

// UploadFileOptions describes an in-memory file to upload to BOS.
type UploadFileOptions struct {
	// File is the binary file content.
	File []byte
	// FileName is the multipart file name; it must not be blank and must not
	// contain a carriage return, line feed, or "/".
	FileName string
	// ObjectKey optionally overrides the BOS object key written to the
	// multipart fields. When blank, the SDK generates a pure-ASCII
	// sdk-uploads/{yyyyMMdd}/{uuid}{ext} key from FileName; a supplied key
	// must use only [A-Za-z0-9._/-] with non-empty, non-"." path segments.
	ObjectKey string
	// ExpirationSeconds is the temporary URL lifetime from 1 to 86400;
	// it defaults to 3600.
	ExpirationSeconds int
}

// UploadFileResult is the normalized BOS upload result.
type UploadFileResult struct {
	ObjectKey         string
	ETag              string
	URL               string
	ExpirationSeconds int
	raw               json.RawMessage
}

// RawJSON returns a defensive copy of the raw upload response.
func (r UploadFileResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// BosFileURLOptions requests a temporary presigned URL for a BOS object key.
type BosFileURLOptions struct {
	ObjectKey                  string
	ExpirationSeconds          int
	ResponseContentDisposition string
}

// BosFileURLResult is a short-lived presigned URL for a BOS object.
type BosFileURLResult struct {
	ObjectKey         string
	URL               string
	ExpirationSeconds int
	raw               json.RawMessage
}

// RawJSON returns a defensive copy of the raw URL response with any URL field
// redacted.
func (r BosFileURLResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// FileDownloadURLOptions requests a temporary download URL for a chat file.
type FileDownloadURLOptions struct {
	FID        string
	ChatID     string
	FileMsgID  string
	ExpSeconds int
}

// FileDownloadURLResult is a temporary download URL for a chat file.
type FileDownloadURLResult struct {
	URL      string
	FileName string
	FileSize int64
	raw      json.RawMessage
}

// RawJSON returns a defensive copy of the raw download URL response.
func (r FileDownloadURLResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// DownloadFileOptions describes a chat file to fetch as bytes.
type DownloadFileOptions struct {
	FID        string
	ChatType   FileChatType
	ChatID     string
	FileMsgID  string
	ExpSeconds int
	MaxBytes   int64
}

// DownloadFileResult is the downloaded file bytes plus its metadata.
type DownloadFileResult struct {
	URL         string
	FileName    string
	FileSize    int64
	Content     []byte
	ContentType string
	raw         json.RawMessage
}

// UploadURLRecoveryError reports a BOS upload that succeeded but whose
// follow-up temporary-URL lookup failed. The uploaded object key is carried so
// the caller can recover without re-uploading.
type UploadURLRecoveryError struct {
	ObjectKey string
	Cause     error
}

func (e *UploadURLRecoveryError) Error() string {
	return "file uploaded but BOS URL lookup failed"
}

func (e *UploadURLRecoveryError) Unwrap() error {
	return e.Cause
}

// FileService uploads binary files to BOS, resolves temporary URLs, and
// downloads chat file bytes. Upload and get-URL always target the fixed BOS
// gateway; chat-file download URLs go through the shared open API transport.
type FileService struct {
	transport    *transport.Client
	bosTransport *transport.Client
	tokens       *auth.TokenManager
}

func NewFileService(client, bosClient *transport.Client, tokens *auth.TokenManager) FileService {
	return FileService{transport: client, bosTransport: bosClient, tokens: tokens}
}

// Upload uploads an in-memory file once to BOS and returns its object key plus
// a temporary presigned URL. A failed URL lookup never re-uploads the file; it
// returns an UploadURLRecoveryError carrying the uploaded object key.
func (f *FileService) Upload(ctx context.Context, options UploadFileOptions) (UploadFileResult, error) {
	if ctx == nil {
		return UploadFileResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if len(options.File) == 0 {
		return UploadFileResult{}, sdkerrors.NewConfig("file", "must not be empty")
	}
	if err := validateSafeFileName(options.FileName, "fileName"); err != nil {
		return UploadFileResult{}, err
	}
	objectKey := options.ObjectKey
	if objectKey == "" {
		objectKey = defaultObjectKey(options.FileName)
	} else if err := validateObjectKey(objectKey); err != nil {
		return UploadFileResult{}, err
	}
	expiration, err := normalizeExpirationSeconds(options.ExpirationSeconds)
	if err != nil {
		return UploadFileResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return UploadFileResult{}, err
	}
	body, contentType, err := buildUploadMultipart(options.File, options.FileName, objectKey)
	if err != nil {
		return UploadFileResult{}, err
	}
	raw, err := f.bosTransport.PostMultipart(ctx, fileBosUploadPath, contentType, body, token)
	if err != nil {
		return UploadFileResult{}, err
	}
	objectKey, eTag, err := parseUploadResponse(raw)
	if err != nil {
		return UploadFileResult{}, err
	}
	urlResult, err := f.GetURL(ctx, BosFileURLOptions{
		ObjectKey:         objectKey,
		ExpirationSeconds: expiration,
	})
	if err != nil {
		return UploadFileResult{}, &UploadURLRecoveryError{ObjectKey: objectKey, Cause: err}
	}
	return UploadFileResult{
		ObjectKey:         objectKey,
		ETag:              eTag,
		URL:               urlResult.URL,
		ExpirationSeconds: urlResult.ExpirationSeconds,
		raw:               append(json.RawMessage(nil), raw...),
	}, nil
}

// GetURL resolves or refreshes a temporary presigned URL for an uploaded BOS
// object key.
func (f *FileService) GetURL(ctx context.Context, options BosFileURLOptions) (BosFileURLResult, error) {
	if ctx == nil {
		return BosFileURLResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if strings.TrimSpace(options.ObjectKey) == "" {
		return BosFileURLResult{}, sdkerrors.NewConfig("objectKey", "must not be blank")
	}
	expiration, err := normalizeExpirationSeconds(options.ExpirationSeconds)
	if err != nil {
		return BosFileURLResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return BosFileURLResult{}, err
	}
	query := url.Values{}
	query.Set("objectKey", options.ObjectKey)
	query.Set("expirationSeconds", strconv.Itoa(expiration))
	if strings.TrimSpace(options.ResponseContentDisposition) != "" {
		query.Set("responseContentDisposition", options.ResponseContentDisposition)
	}
	raw, err := f.bosTransport.Get(ctx, fileBosGetURLPath, query, token)
	if err != nil {
		return BosFileURLResult{}, err
	}
	u, effective, err := parseBosURLResponse(raw, expiration)
	if err != nil {
		return BosFileURLResult{}, err
	}
	return BosFileURLResult{
		ObjectKey:         options.ObjectKey,
		URL:               u,
		ExpirationSeconds: effective,
		raw:               append(json.RawMessage(nil), raw...),
	}, nil
}

// GetDownloadURLPrivate resolves a temporary download URL for a private chat
// file identified by its fid.
func (f *FileService) GetDownloadURLPrivate(ctx context.Context, options FileDownloadURLOptions) (FileDownloadURLResult, error) {
	if ctx == nil {
		return FileDownloadURLResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	fid, err := normalizeFid(options.FID)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	raw, err := f.transport.PostJSON(ctx, fileDownloadURLPrivatePath, map[string]any{
		"fid":      fid,
		"chatType": FileChatPrivate,
	}, token)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	u, fileName, fileSize, err := parseDownloadURLResponse(raw)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	return FileDownloadURLResult{
		URL:      u,
		FileName: fileName,
		FileSize: fileSize,
		raw:      append(json.RawMessage(nil), raw...),
	}, nil
}

// GetDownloadURLGroup resolves a temporary download URL for a group chat file;
// the file's chat ID and file message ID are required.
func (f *FileService) GetDownloadURLGroup(ctx context.Context, options FileDownloadURLOptions) (FileDownloadURLResult, error) {
	if ctx == nil {
		return FileDownloadURLResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	fid, err := normalizeFid(options.FID)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	if strings.TrimSpace(options.ChatID) == "" {
		return FileDownloadURLResult{}, sdkerrors.NewConfig("chatId", "is required for group file download")
	}
	if strings.TrimSpace(options.FileMsgID) == "" {
		return FileDownloadURLResult{}, sdkerrors.NewConfig("fileMsgId", "is required for group file download")
	}
	body := map[string]any{
		"fid":       fid,
		"chatType":  FileChatGroup,
		"chatId":    options.ChatID,
		"fileMsgId": options.FileMsgID,
	}
	if options.ExpSeconds > 0 {
		body["expSeconds"] = options.ExpSeconds
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	raw, err := f.transport.PostJSON(ctx, fileDownloadURLGroupPath, body, token)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	u, fileName, fileSize, err := parseDownloadURLResponse(raw)
	if err != nil {
		return FileDownloadURLResult{}, err
	}
	return FileDownloadURLResult{
		URL:      u,
		FileName: fileName,
		FileSize: fileSize,
		raw:      append(json.RawMessage(nil), raw...),
	}, nil
}

// Download resolves a chat file to a temporary download URL and fetches its
// bytes using the current app access token. It enforces maxBytes when set.
func (f *FileService) Download(ctx context.Context, options DownloadFileOptions) (DownloadFileResult, error) {
	if ctx == nil {
		return DownloadFileResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if options.MaxBytes < 0 {
		return DownloadFileResult{}, sdkerrors.NewConfig("maxBytes", "must not be negative")
	}
	var urlResult FileDownloadURLResult
	var err error
	switch options.ChatType {
	case FileChatGroup:
		urlResult, err = f.GetDownloadURLGroup(ctx, FileDownloadURLOptions{
			FID: options.FID, ChatID: options.ChatID, FileMsgID: options.FileMsgID, ExpSeconds: options.ExpSeconds,
		})
	default:
		urlResult, err = f.GetDownloadURLPrivate(ctx, FileDownloadURLOptions{FID: options.FID, ExpSeconds: options.ExpSeconds})
	}
	if err != nil {
		return DownloadFileResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return DownloadFileResult{}, err
	}
	content, headers, err := f.transport.GetAbsoluteURLWithIdentity(ctx, urlResult.URL, "Bearer-"+token, options.MaxBytes)
	if err != nil {
		return DownloadFileResult{}, err
	}
	return DownloadFileResult{
		URL:         urlResult.URL,
		FileName:    urlResult.FileName,
		FileSize:    urlResult.FileSize,
		Content:     content,
		ContentType: headers.Get("Content-Type"),
		raw:         append(json.RawMessage(nil), urlResult.raw...),
	}, nil
}

func validateSafeFileName(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return sdkerrors.NewConfig(field, "must not be blank")
	}
	if strings.Contains(value, "\r") || strings.Contains(value, "\n") {
		return sdkerrors.NewConfig(field, "must not contain carriage returns or line feeds")
	}
	if strings.Contains(value, "/") {
		return sdkerrors.NewConfig(field, "must not contain \"/\"; pass a bare file name")
	}
	return nil
}

// defaultObjectKey builds the SDK-generated BOS object key used when the caller
// does not provide one. The BOS get-URL route only accepts strict ASCII keys,
// so a file name with non-ASCII characters (or other reserved characters) must
// never become the object key. The generated key is
// sdk-uploads/{yyyyMMdd}/{uuid}{ext}, where ext is only kept when the file name
// ends with a short ASCII extension.
func defaultObjectKey(fileName string) string {
	ext := ""
	if match := fileObjectKeyExtPattern.FindString(fileName); match != "" {
		ext = strings.ToLower(match)
	}
	return fmt.Sprintf("%s/%s/%s%s", fileObjectKeyPrefix, time.Now().UTC().Format("20060102"), newObjectKeyToken(), ext)
}

// newObjectKeyToken returns a 128-bit random hex token used to keep generated
// object keys unique without relying on a third-party UUID dependency.
func newObjectKeyToken() string {
	var buf [16]byte
	// crypto/rand.Read never returns an error and always fills the buffer on
	// supported platforms (Go 1.24+); ignore the always-nil error explicitly.
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// validateObjectKey rejects caller-supplied object keys that the BOS get-URL
// route cannot serve. Validating before upload avoids leaving an unreachable
// object behind (upload succeeds, URL lookup fails, re-upload returns 40900).
func validateObjectKey(key string) error {
	const guidance = "allowed characters are [A-Za-z0-9._/-]; each path segment must be non-empty and not \".\" or \"..\"; length must be at most 1024; omit objectKey to let the SDK generate a safe key"
	if !fileObjectKeyPattern.MatchString(key) {
		return sdkerrors.NewConfig("objectKey", "must use only [A-Za-z0-9._/-]; "+guidance)
	}
	if len(key) > fileObjectKeyMaxLength {
		return sdkerrors.NewConfig("objectKey", "must be at most 1024 characters; "+guidance)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return sdkerrors.NewConfig("objectKey", "must not contain empty, \".\" or \"..\" path segments; "+guidance)
		}
	}
	return nil
}

func normalizeExpirationSeconds(value int) (int, error) {
	if value == 0 {
		return fileDefaultExpirationSeconds, nil
	}
	if value < 1 || value > fileMaxExpirationSeconds {
		return 0, sdkerrors.NewConfig("expirationSeconds", "must be an integer from 1 to 86400")
	}
	return value, nil
}

func normalizeFid(fid string) (string, error) {
	normalized := strings.TrimSpace(fid)
	if normalized == "" {
		return "", sdkerrors.NewConfig("fid", "must not be blank")
	}
	return normalized, nil
}

func buildUploadMultipart(file []byte, fileName, objectKey string) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, "", sdkerrors.NewConfig("file upload", "could not create multipart file part")
	}
	if _, err := part.Write(file); err != nil {
		return nil, "", sdkerrors.NewConfig("file upload", "could not write multipart file part")
	}
	if err := writer.WriteField("objectKey", objectKey); err != nil {
		return nil, "", sdkerrors.NewConfig("file upload", "could not write multipart object key")
	}
	if err := writer.Close(); err != nil {
		return nil, "", sdkerrors.NewConfig("file upload", "could not finalize multipart body")
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func parseUploadResponse(raw []byte) (string, string, error) {
	var response struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", "", sdkerrors.NewMalformed("file upload response is invalid", raw, err)
	}
	code, codeErr := intCode(response.Code)
	if codeErr != nil || code != 200 {
		return "", "", sdkerrors.NewBusiness(uploadCodeText(code, codeErr), uploadMessage(response.Data), raw)
	}
	var data struct {
		ObjectKey string `json:"object_key"`
		ETag      string `json:"e_tag"`
	}
	if len(response.Data) == 0 {
		return "", "", sdkerrors.NewMalformed("file upload response is missing data.object_key", raw, nil)
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return "", "", sdkerrors.NewMalformed("file upload response is missing data.object_key", raw, err)
	}
	if strings.TrimSpace(data.ObjectKey) == "" {
		return "", "", sdkerrors.NewMalformed("file upload response is missing data.object_key", raw, nil)
	}
	return data.ObjectKey, data.ETag, nil
}

func parseBosURLResponse(raw []byte, requestedExpiration int) (string, int, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", 0, sdkerrors.NewMalformed("BOS URL response is invalid", nil, err)
	}
	code, codeErr := intCode(root["code"])
	if codeErr != nil || code != 200 {
		return "", 0, sdkerrors.NewBusiness(uploadCodeText(code, codeErr), downloadURLMessage(root), raw)
	}
	var data struct {
		URL               string          `json:"url"`
		ExpirationSeconds json.RawMessage `json:"expiration_seconds"`
	}
	rawData := root["data"]
	if len(rawData) == 0 {
		return "", 0, sdkerrors.NewMalformed("BOS URL response is missing data.url", nil, nil)
	}
	if err := json.Unmarshal(rawData, &data); err != nil {
		return "", 0, sdkerrors.NewMalformed("BOS URL response is missing data.url", nil, err)
	}
	if strings.TrimSpace(data.URL) == "" {
		return "", 0, sdkerrors.NewMalformed("BOS URL response is missing data.url", nil, nil)
	}
	effective := requestedExpiration
	if len(data.ExpirationSeconds) > 0 {
		parsed, err := intCode(data.ExpirationSeconds)
		if err != nil || parsed <= 0 {
			return "", 0, sdkerrors.NewMalformed("BOS URL response has an invalid data.expiration_seconds", nil, nil)
		}
		effective = parsed
	}
	return data.URL, effective, nil
}

func parseDownloadURLResponse(raw []byte) (string, string, int64, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", "", 0, sdkerrors.NewMalformed("file download URL response is invalid", raw, err)
	}
	if topCode, ok := objectStringField(root, "code"); ok && topCode != "ok" {
		return "", "", 0, sdkerrors.NewBusiness(topCode, downloadURLMessage(root), raw)
	}
	topData := root
	if nested, ok := objectField(root, "data"); ok {
		topData = nested
	}
	if dataCode, ok := intField(topData, "code"); ok && dataCode != 0 && dataCode != 200 {
		return "", "", 0, sdkerrors.NewBusiness(strconv.Itoa(dataCode), downloadURLMessage(topData), raw)
	}
	fileData := topData
	if nested, ok := objectField(topData, "data"); ok {
		if nestedURL, present := objectStringField(nested, "url"); present && strings.TrimSpace(nestedURL) != "" {
			fileData = nested
		}
	}
	u, present := objectStringField(fileData, "url")
	if !present || strings.TrimSpace(u) == "" {
		return "", "", 0, sdkerrors.NewMalformed("file download URL response is missing data.url", nil, nil)
	}
	fileName, _ := objectStringField(fileData, "fileName")
	fileSize, _ := intField(fileData, "fileSize")
	return u, fileName, int64(fileSize), nil
}

func intCode(raw json.RawMessage) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func uploadCodeText(code int, err error) string {
	if err != nil {
		return "invalid"
	}
	return strconv.Itoa(code)
}

func uploadMessage(data json.RawMessage) string {
	var value map[string]json.RawMessage
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	return downloadURLMessage(value)
}

func downloadURLMessage(value map[string]json.RawMessage) string {
	for _, name := range []string{"message", "msg", "errmsg"} {
		if text, ok := objectStringField(value, name); ok {
			return text
		}
	}
	return ""
}

func objectField(value map[string]json.RawMessage, name string) (map[string]json.RawMessage, bool) {
	raw, ok := value[name]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	var child map[string]json.RawMessage
	if err := json.Unmarshal(raw, &child); err != nil || child == nil {
		return nil, false
	}
	return child, true
}

func objectStringField(value map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := value[name]
	if !ok || len(raw) == 0 {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}

func intField(value map[string]json.RawMessage, name string) (int, bool) {
	raw, ok := value[name]
	if !ok || len(raw) == 0 {
		return 0, false
	}
	var parsed int
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}
