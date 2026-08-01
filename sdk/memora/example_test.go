package memora_test

import (
	"fmt"

	"github.com/HW-Yue/Memora/sdk/memora"
)

func ExampleRequest() {
	request := memora.Request{
		Version: memora.RequestVersion,
		Source:  "SHOW DATABASES LIMIT 8",
		Statements: []memora.StatementInput{{
			Authorization: memora.Authorization{
				Version: memora.AuthorizationVersion,
				Actor:   "my-agent",
				AuthorizedDatabases: []string{
					"work",
				},
				DefaultLevel: memora.LevelRead,
			},
		}},
	}
	fmt.Println(request.Version)
	// Output: memora.go-sdk-request/v1
}
