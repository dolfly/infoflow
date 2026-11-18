package infoflow_test

import (
	"errors"
	"fmt"

	infoflow "github.com/dolfly/infoflow"
)

func ExampleNewClient() {
	client, err := infoflow.NewClient(infoflow.Config{
		AppKey:    "example-app",
		AppSecret: "example-original-secret",
	})
	fmt.Println(client != nil, err == nil)
	// Output: true true
}

func ExampleConfigError() {
	_, err := infoflow.NewClient(infoflow.Config{})
	var configErr *infoflow.ConfigError
	fmt.Println(errors.As(err, &configErr))
	// Output: true
}
