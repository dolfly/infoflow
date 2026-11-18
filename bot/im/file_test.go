package im

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/testkit"
	"github.com/dolfly/infoflow/internal/transport"
)

func newFileTestService(t *testing.T, bosHandler, mainHandler http.HandlerFunc) FileService {
	t.Helper()
	mainServer := testkit.ExtendedServer(mainHandler)
	t.Cleanup(mainServer.Close)
	bosServer := httptest.NewServer(http.HandlerFunc(bosHandler))
	t.Cleanup(bosServer.Close)
	now := func() time.Time { return time.UnixMilli(1700000000000) }
	mainClient := transport.New(mainServer.URL+"/api/v1", time.Second, time.Second, mainServer.Client(), "sdk")
	tokens := auth.NewTokenManager(mainClient, testkit.AppKey, testkit.Secret, 0, now)
	bosClient := transport.New(bosServer.URL, time.Second, time.Second, bosServer.Client(), "sdk")
	return NewFileService(mainClient, bosClient, tokens)
}

func readMultipartBody(t *testing.T, body []byte, contentType string) map[string]string {
	t.Helper()
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Fatalf("content type = %q, want multipart/form-data", contentType)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	reader := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	parts := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		content, _ := io.ReadAll(part)
		parts[part.FormName()] = string(content)
	}
	return parts
}

func TestFileUploadMultipartAndURLRecovery(t *testing.T) {
	var contentType string
	var uploadBody []byte
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/im/bos/getUrl" {
				if r.URL.Query().Get("objectKey") != "obj-1" {
					t.Fatalf("getUrl objectKey = %q", r.URL.Query().Get("objectKey"))
				}
				testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"url":"https://presigned.example/obj-1"}}`)
				return
			}
			if r.URL.Path != "/im/bos/upload" || r.Method != http.MethodPost {
				t.Fatalf("route = %s %s", r.Method, r.URL.Path)
			}
			contentType = r.Header.Get("Content-Type")
			uploadBody = testkit.ReadRequestBody(t, r)
			if got := r.Header.Get("Authorization"); got != "Bearer-"+testkit.Token {
				t.Fatalf("Authorization = %q", got)
			}
			testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"object_key":"obj-1","e_tag":"etag-1"}}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main route %s %s", r.Method, r.URL.Path)
		},
	)

	result, err := service.Upload(context.Background(), UploadFileOptions{
		File:              []byte("hello world"),
		FileName:          "hello.txt",
		ObjectKey:         "safe-disposable/hello.txt",
		ExpirationSeconds: 900,
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if result.ObjectKey != "obj-1" || result.ETag != "etag-1" || result.URL == "" || result.ExpirationSeconds != 900 {
		t.Fatalf("result = %#v", result)
	}
	parts := readMultipartBody(t, uploadBody, contentType)
	if parts["file"] != "hello world" {
		t.Fatalf("file part = %q", parts["file"])
	}
	if parts["objectKey"] != "safe-disposable/hello.txt" {
		t.Fatalf("objectKey part = %q", parts["objectKey"])
	}
}

func TestFileUploadRecoveryErrorCarriesObjectKey(t *testing.T) {
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/im/bos/upload" {
				testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"object_key":"obj-1"}}`)
				return
			}
			if r.URL.Path == "/im/bos/getUrl" {
				testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"url":""}}`)
				return
			}
			t.Fatalf("unexpected bos route %s %s", r.Method, r.URL.Path)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main route %s %s", r.Method, r.URL.Path)
		},
	)
	_, err := service.Upload(context.Background(), UploadFileOptions{
		File: []byte("x"), FileName: "x.txt", ExpirationSeconds: 900,
	})
	var recovery *UploadURLRecoveryError
	if err == nil || !errors.As(err, &recovery) {
		t.Fatalf("Upload() error = %T %v, want *UploadURLRecoveryError", err, err)
	}
	if recovery.ObjectKey != "obj-1" {
		t.Fatalf("recovery.ObjectKey = %q", recovery.ObjectKey)
	}
}

func TestFileUploadGeneratesASCIIDefaultObjectKey(t *testing.T) {
	var contentType string
	var uploadBody []byte
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/im/bos/upload":
				contentType = r.Header.Get("Content-Type")
				uploadBody = testkit.ReadRequestBody(t, r)
				testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"object_key":"obj-1","e_tag":"etag-1"}}`)
			case "/im/bos/getUrl":
				if got := r.URL.Query().Get("objectKey"); got != "obj-1" {
					t.Fatalf("getUrl objectKey = %q", got)
				}
				testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"url":"https://presigned.example/obj-1"}}`)
			default:
				t.Fatalf("unexpected bos route %s %s", r.Method, r.URL.Path)
			}
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main route %s %s", r.Method, r.URL.Path)
		},
	)

	_, err := service.Upload(context.Background(), UploadFileOptions{
		File:     []byte("hi"),
		FileName: "中文文件名.pdf",
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	parts := readMultipartBody(t, uploadBody, contentType)
	key := parts["objectKey"]
	if key == "" {
		t.Fatal("multipart objectKey field is missing")
	}
	pattern := regexp.MustCompile(`^sdk-uploads/\d{8}/[0-9a-f]{32}(\.[A-Za-z0-9]{1,8})?$`)
	if !pattern.MatchString(key) {
		t.Fatalf("generated objectKey = %q, want sdk-uploads/{yyyyMMdd}/{uuid}{ext}", key)
	}
	for i := 0; i < len(key); i++ {
		if key[i] > 127 {
			t.Fatalf("generated objectKey %q contains a non-ASCII byte", key)
		}
	}
	if !strings.HasSuffix(key, ".pdf") {
		t.Fatalf("generated objectKey = %q, want a .pdf extension", key)
	}
}

func TestFileUploadRejectsInvalidObjectKey(t *testing.T) {
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected bos request %s %s", r.Method, r.URL.Path)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main request %s %s", r.Method, r.URL.Path)
		},
	)
	cases := map[string]string{
		"chinese":        "文件.pdf",
		"space":          "dir/my file.txt",
		"leading-slash":  "/obj-1",
		"double-slash":   "dir//obj-1",
		"parent-segment": "dir/../obj-1",
		"trailing-slash": "dir/",
		"too-long":       strings.Repeat("a", 1025),
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := service.Upload(context.Background(), UploadFileOptions{
				File: []byte("x"), FileName: "x.txt", ObjectKey: key,
			})
			if err == nil {
				t.Fatalf("Upload() objectKey %q error = nil, want parameter error", key)
			}
			var config *sdkerrors.ConfigError
			if !errors.As(err, &config) {
				t.Fatalf("Upload() objectKey %q error = %T %v, want *ConfigError", key, err, err)
			}
		})
	}
}

func TestFileUploadRejectsFileNameWithSlash(t *testing.T) {
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected bos request %s %s", r.Method, r.URL.Path)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main request %s %s", r.Method, r.URL.Path)
		},
	)
	_, err := service.Upload(context.Background(), UploadFileOptions{
		File: []byte("x"), FileName: "dir/x.txt",
	})
	if err == nil {
		t.Fatal("Upload() fileName with slash error = nil, want parameter error")
	}
	var config *sdkerrors.ConfigError
	if !errors.As(err, &config) {
		t.Fatalf("Upload() error = %T %v, want *ConfigError", err, err)
	}
}

func TestFileGetURLQueryShapeAndExpiration(t *testing.T) {
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/im/bos/getUrl" || r.Method != http.MethodGet {
				t.Fatalf("route = %s %s", r.Method, r.URL.Path)
			}
			query := r.URL.Query()
			if query.Get("objectKey") != "obj-1" || query.Get("expirationSeconds") != "1800" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			if query.Get("responseContentDisposition") != "attachment; filename*=UTF-8''hello.txt" {
				t.Fatalf("responseContentDisposition = %q", query.Get("responseContentDisposition"))
			}
			testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"url":"https://presigned.example/obj-1"}}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main route %s %s", r.Method, r.URL.Path)
		},
	)
	result, err := service.GetURL(context.Background(), BosFileURLOptions{
		ObjectKey:                  "obj-1",
		ExpirationSeconds:          1800,
		ResponseContentDisposition: "attachment; filename*=UTF-8''hello.txt",
	})
	if err != nil {
		t.Fatalf("GetURL() error = %v", err)
	}
	if result.URL != "https://presigned.example/obj-1" || result.ExpirationSeconds != 1800 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFileGetURLRedactsSensitiveResponse(t *testing.T) {
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `{"code":200,"data":{"url":"https://secret.example/private-link","expiration_seconds":"bad"}}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected main route %s %s", r.Method, r.URL.Path)
		},
	)
	_, err := service.GetURL(context.Background(), BosFileURLOptions{ObjectKey: "o", ExpirationSeconds: 60})
	if err == nil {
		t.Fatal("GetURL() error = nil, want malformed error")
	}
	if strings.Contains(err.Error(), "secret.example") {
		t.Fatalf("error leaks sensitive URL: %v", err)
	}
	var target *sdkerrors.MalformedResponseError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
	}
	if len(target.RawResponse()) != 0 {
		t.Fatal("MalformedResponseError.RawResponse() leaks sensitive URL")
	}
}

func TestFileGetURLBusinessErrorExposesPlatformMessage(t *testing.T) {
	for _, field := range []string{"message", "msg", "errmsg"} {
		t.Run(field, func(t *testing.T) {
			service := newFileTestService(t,
				func(w http.ResponseWriter, r *http.Request) {
					testkit.WriteJSON(w, http.StatusOK, `{"code":40001,"`+field+`":"非法objectKey"}`)
				},
				func(w http.ResponseWriter, r *http.Request) {
					t.Fatalf("unexpected main route %s %s", r.Method, r.URL.Path)
				},
			)
			_, err := service.GetURL(context.Background(), BosFileURLOptions{ObjectKey: "obj-1", ExpirationSeconds: 60})
			if err == nil {
				t.Fatal("GetURL() error = nil, want business error")
			}
			var business *sdkerrors.BusinessError
			if !errors.As(err, &business) {
				t.Fatalf("GetURL() error = %T %v, want *BusinessError", err, err)
			}
			if business.Code != "40001" {
				t.Fatalf("business.Code = %q, want 40001", business.Code)
			}
			if business.Message != "非法objectKey" {
				t.Fatalf("business.Message = %q, want 非法objectKey", business.Message)
			}
		})
	}
}

func TestFileGetDownloadURLPrivate(t *testing.T) {
	var body []byte
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected bos route %s %s", r.Method, r.URL.Path)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/open-file-service/file/get/download/url/robot-chat/byFid" {
				t.Fatalf("route = %s", r.URL.Path)
			}
			body = testkit.ReadRequestBody(t, r)
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"code":0,"data":{"url":"https://dl.example/private","fileName":"a.txt","fileSize":123}}}`)
		},
	)
	result, err := service.GetDownloadURLPrivate(context.Background(), FileDownloadURLOptions{FID: "FID1"})
	if err != nil {
		t.Fatalf("GetDownloadURLPrivate() error = %v", err)
	}
	testkit.AssertJSONEqual(t, body, `{"fid":"FID1","chatType":1}`)
	if result.URL != "https://dl.example/private" || result.FileName != "a.txt" || result.FileSize != 123 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFileGetDownloadURLGroup(t *testing.T) {
	var body []byte
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected bos route %s %s", r.Method, r.URL.Path)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/open-file-service/file/get/download/url/byFid" {
				t.Fatalf("route = %s", r.URL.Path)
			}
			body = testkit.ReadRequestBody(t, r)
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"data":{"url":"https://dl.example/group"}}}`)
		},
	)
	result, err := service.GetDownloadURLGroup(context.Background(), FileDownloadURLOptions{
		FID: "F", ChatID: "123", FileMsgID: "M", ExpSeconds: 600,
	})
	if err != nil {
		t.Fatalf("GetDownloadURLGroup() error = %v", err)
	}
	testkit.AssertJSONEqual(t, body, `{"fid":"F","chatType":2,"chatId":"123","fileMsgId":"M","expSeconds":600}`)
	if result.URL != "https://dl.example/group" {
		t.Fatalf("result.URL = %q", result.URL)
	}
}

func TestFileDownloadBytesWithIdentityHeader(t *testing.T) {
	var mainServer *httptest.Server
	mainServer = testkit.ExtendedServer(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/open-file-service/file/get/download/url/robot-chat/byFid":
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"data":{"url":"`+mainServer.URL+`/raw","fileName":"a.txt","fileSize":5}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/raw":
			if got := r.Header.Get("x-openapi-gateway-identity"); got != "Bearer-"+testkit.Token {
				t.Fatalf("identity header = %q", got)
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "hello")
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.Path)
		}
	})
	t.Cleanup(mainServer.Close)
	now := func() time.Time { return time.UnixMilli(1700000000000) }
	mainClient := transport.New(mainServer.URL+"/api/v1", time.Second, time.Second, mainServer.Client(), "sdk")
	tokens := auth.NewTokenManager(mainClient, testkit.AppKey, testkit.Secret, 0, now)
	bosClient := transport.New("http://127.0.0.1:1", time.Second, time.Second, mainServer.Client(), "sdk")
	service := NewFileService(mainClient, bosClient, tokens)

	result, err := service.Download(context.Background(), DownloadFileOptions{
		FID: "F", ChatType: FileChatPrivate, MaxBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(result.Content) != "hello" || result.ContentType != "text/plain" || result.FileName != "a.txt" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFileDownloadMaxBytes(t *testing.T) {
	var mainServer *httptest.Server
	mainServer = testkit.ExtendedServer(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/open-file-service/file/get/download/url/robot-chat/byFid":
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"data":{"url":"`+mainServer.URL+`/big"}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/big":
			_, _ = io.WriteString(w, strings.Repeat("x", 64))
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.Path)
		}
	})
	t.Cleanup(mainServer.Close)
	now := func() time.Time { return time.UnixMilli(1700000000000) }
	mainClient := transport.New(mainServer.URL+"/api/v1", time.Second, time.Second, mainServer.Client(), "sdk")
	tokens := auth.NewTokenManager(mainClient, testkit.AppKey, testkit.Secret, 0, now)
	service := NewFileService(mainClient, transport.New("http://127.0.0.1:1", time.Second, time.Second, mainServer.Client(), "sdk"), tokens)

	_, err := service.Download(context.Background(), DownloadFileOptions{
		FID: "F", ChatType: FileChatPrivate, MaxBytes: 16,
	})
	if err == nil || !strings.Contains(err.Error(), "maxBytes") {
		t.Fatalf("Download() error = %v, want maxBytes error", err)
	}
}

func TestFileValidation(t *testing.T) {
	service := newFileTestService(t,
		func(w http.ResponseWriter, r *http.Request) { t.Fatalf("unexpected bos route") },
		func(w http.ResponseWriter, r *http.Request) { t.Fatalf("unexpected main route") },
	)
	base := UploadFileOptions{File: []byte("x"), FileName: "x.txt"}
	if _, err := service.Upload(context.Background(), UploadFileOptions{}); err == nil {
		t.Fatal("Upload() empty file error = nil")
	}
	if _, err := service.Upload(context.Background(), func() UploadFileOptions {
		o := base
		o.FileName = ""
		return o
	}()); err == nil {
		t.Fatal("Upload() blank fileName error = nil")
	}
	if _, err := service.Upload(context.Background(), func() UploadFileOptions {
		o := base
		o.FileName = "bad\nname"
		return o
	}()); err == nil {
		t.Fatal("Upload() newline fileName error = nil")
	}
	if _, err := service.Upload(context.Background(), func() UploadFileOptions {
		o := base
		o.ExpirationSeconds = 99999
		return o
	}()); err == nil {
		t.Fatal("Upload() out-of-range expiration error = nil")
	}
	if _, err := service.GetURL(context.Background(), BosFileURLOptions{ObjectKey: "  "}); err == nil {
		t.Fatal("GetURL() blank objectKey error = nil")
	}
	if _, err := service.GetDownloadURLPrivate(context.Background(), FileDownloadURLOptions{FID: ""}); err == nil {
		t.Fatal("GetDownloadURLPrivate() blank fid error = nil")
	}
	if _, err := service.GetDownloadURLGroup(context.Background(), FileDownloadURLOptions{FID: "F", ChatID: "1"}); err == nil {
		t.Fatal("GetDownloadURLGroup() missing fileMsgId error = nil")
	}
	if _, err := service.Download(context.Background(), DownloadFileOptions{FID: "F", ChatType: FileChatPrivate, MaxBytes: -1}); err == nil {
		t.Fatal("Download() negative maxBytes error = nil")
	}
}
