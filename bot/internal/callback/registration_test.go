package callback

import (
	"errors"
	"testing"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

func TestDecodeRegistrationUsesDedicatedEnvelope(t *testing.T) {
	for _, fixture := range []string{
		`{"code":"ok","data":{}}`,
		`{"code":"ok","data":{"code":0,"msg":"accepted"}}`,
		`{"code":"ok","data":{"code":200,"logId":9007199254740993123}}`,
	} {
		result, err := DecodeRegistration([]byte(fixture))
		if err != nil || len(result.Raw) == 0 {
			t.Errorf("DecodeRegistration(%s) result=%#v error=%v", fixture, result, err)
		}
	}
	for _, fixture := range []string{
		`{"code":"rejected","data":{}}`,
		`{"code":"ok","data":{"code":40044,"msg":"rejected"}}`,
	} {
		_, err := DecodeRegistration([]byte(fixture))
		var target *sdkerrors.BusinessError
		if !errors.As(err, &target) {
			t.Errorf("DecodeRegistration(%s) error=%T %v, want BusinessError", fixture, err, err)
		}
	}
	for _, fixture := range []string{
		`{bad`, `{"data":{"code":200}}`, `{"code":0,"data":{"code":200}}`,
		`{"errcode":0,"data":{"code":200}}`, `{"code":"ok"}`,
	} {
		_, err := DecodeRegistration([]byte(fixture))
		var target *sdkerrors.MalformedResponseError
		if !errors.As(err, &target) {
			t.Errorf("DecodeRegistration(%s) error=%T %v, want MalformedResponseError", fixture, err, err)
		}
	}
}
