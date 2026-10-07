package api

import (
	"errors"
	"testing"
)

func TestRequestMutationDistinguishesProviderAndTransportOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		exit         string
		code         ErrorCode
		rejected     bool
	}{
		{name: "success", output: "HTTP/2.0 201 Created\r\nContent-Type: application/json\r\n\r\n{\"id\":123}"},
		{name: "malformed success", output: "HTTP/2.0 201 Created\r\n\r\nbad", code: ErrorCodeMutationAccepted},
		{name: "rejected", output: "HTTP/2.0 422 Unprocessable Entity\r\n\r\n{\"message\":\"Validation Failed\",\"errors\":[{\"field\":\"body\"}]}", exit: "1", rejected: true},
		{name: "connection lost", exit: "1", code: ErrorCodeMutationUncertain},
		{name: "server failure", output: "HTTP/2.0 502 Bad Gateway\r\n\r\n{}", exit: "1", code: ErrorCodeMutationUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMMENT_RESPONSE", tc.output)
			exit := tc.exit
			if exit == "" {
				exit = "0"
			}
			t.Setenv("COMMENT_EXIT", exit)
			command := fakeGH(t, "#!/bin/sh\ncat >/dev/null\nprintf '%s' \"$COMMENT_RESPONSE\"\nexit \"$COMMENT_EXIT\"\n")
			var out struct {
				ID int `json:"id"`
			}
			err := (&CLIClient{Command: command}).RequestMutation(t.Context(), "POST", "repos/o/r/issues/1/comments", map[string]string{"body": "body"}, &out)
			if tc.rejected {
				var e *RejectionError
				if !errors.As(err, &e) || e.Status != 422 {
					t.Fatalf("rejection: %v", err)
				}
				return
			}
			if tc.code != "" {
				var e *Error
				if !errors.As(err, &e) || e.Code != tc.code {
					t.Fatalf("error: %+v expected %s", err, tc.code)
				}
				return
			}
			if err != nil || out.ID != 123 {
				t.Fatalf("success: %v %+v", err, out)
			}
		})
	}
}
