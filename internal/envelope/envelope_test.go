package envelope

import (
	"bytes"
	"errors"
	"testing"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

func TestDecodeEnvelopeCNumericAndStringBizCode(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantCode string
		wantMsg  string
		wantData string
	}{
		{
			name:     "numeric bizCode",
			response: `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{"taskId":"abc"}}}`,
			wantCode: "200",
			wantMsg:  "accepted",
			wantData: `{"taskId":"abc"}`,
		},
		{
			name:     "string bizCode",
			response: `{"code":"ok","data":{"bizCode":"200","bizMsg":"accepted","bizData":null}}`,
			wantCode: "200",
			wantMsg:  "accepted",
			wantData: "null",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelopeC([]byte(tt.response))
			if err != nil {
				t.Fatalf("DecodeEnvelopeC() error = %v", err)
			}
			if env.BizCode != tt.wantCode || env.BizMsg != tt.wantMsg {
				t.Fatalf("env = %#v", env)
			}
			if string(env.BizData) != tt.wantData {
				t.Fatalf("BizData = %s, want %s", env.BizData, tt.wantData)
			}
			if !bytes.Equal(env.RawJSON(), []byte(tt.response)) {
				t.Fatalf("RawJSON() = %s", env.RawJSON())
			}
		})
	}
}

func TestDecodeEnvelopeCTopLevelErrorBecomesBusinessError(t *testing.T) {
	_, err := DecodeEnvelopeC([]byte(`{"code":"fail","message":"refused","data":{}}`))
	var target *sdkerrors.BusinessError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *BusinessError", err, err)
	}
	if target.Code != "fail" {
		t.Fatalf("BusinessError.Code = %q", target.Code)
	}
}

func TestDecodeEnvelopeCMalformed(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "not json", response: `nope`},
		{name: "missing top code", response: `{"data":{}}`},
		{name: "data not object", response: `{"code":"ok","data":null}`},
		{name: "missing bizCode", response: `{"code":"ok","data":{"bizMsg":"x"}}`},
		{name: "invalid bizCode", response: `{"code":"ok","data":{"bizCode":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeEnvelopeC([]byte(tt.response))
			var target *sdkerrors.MalformedResponseError
			if !errors.As(err, &target) {
				t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
			}
		})
	}
}
