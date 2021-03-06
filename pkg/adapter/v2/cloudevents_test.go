/*
Copyright 2020 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package adapter

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cloudevents/sdk-go/v2/protocol"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/protocol/http"
	"knative.dev/eventing/pkg/adapter/v2/test"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/source"
)

type mockReporter struct {
	eventCount      int
	retryEventCount int
}

var (
	fakeURL = "test-source"
)

func (r *mockReporter) ReportEventCount(args *source.ReportArgs, responseCode int) error {
	r.eventCount++
	return nil
}

func (r *mockReporter) ReportRetryEventCount(args *source.ReportArgs, responseCode int) error {
	r.retryEventCount++
	return nil
}

func TestNewCloudEventsClient_send(t *testing.T) {
	demoEvent := func() *cloudevents.Event {
		event := cloudevents.NewEvent()
		event.SetID("abc-123")
		event.SetSource("unit/test")
		event.SetType("unit.type")
		return &event
	}

	testCases := map[string]struct {
		ceOverrides *duckv1.CloudEventOverrides
		event       *cloudevents.Event
		timeout     int
		result      protocol.Result
		wantErr     bool
	}{
		"timeout": {timeout: 13},
		"none":    {},
		"send": {
			event: demoEvent(),
		},
		"send fails": {
			event:   demoEvent(),
			result:  http.NewResult(400, "%w", protocol.ResultNACK),
			wantErr: true,
		},
		"send after retries": {
			event: demoEvent(),
			result: &http.RetriesResult{
				Result:   http.NewResult(200, "%w", protocol.ResultACK),
				Retries:  10,
				Duration: time.Microsecond * 42,
				Attempts: nil,
			},
		},
		"send with ceOverrides": {
			event: demoEvent(),
			ceOverrides: &duckv1.CloudEventOverrides{Extensions: map[string]string{
				"foo": "bar",
			}},
		},
		"not a http result": {
			event:   demoEvent(),
			result:  errors.New("totally not an http result"),
			wantErr: true,
		},
	}
	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			restoreHTTP := cloudevents.NewHTTP
			restoreEnv, setEnv := os.LookupEnv("K_SINK_TIMEOUT")
			if tc.timeout != 0 {
				if err := os.Setenv("K_SINK_TIMEOUT", strconv.Itoa(tc.timeout)); err != nil {
					t.Error(err)
				}
			}

			defer func(restoreHTTP func(opts ...http.Option) (*http.Protocol, error), restoreEnv string, setEnv bool) {
				cloudevents.NewHTTP = restoreHTTP
				if setEnv {
					if err := os.Setenv("K_SINK_TIMEOUT", restoreEnv); err != nil {
						t.Error(err)
					}
				} else {
					if err := os.Unsetenv("K_SINK_TIMEOUT"); err != nil {
						t.Error(err)
					}
				}

			}(restoreHTTP, restoreEnv, setEnv)

			sendOptions := []http.Option{}
			cloudevents.NewHTTP = func(opts ...http.Option) (*http.Protocol, error) {
				sendOptions = append(sendOptions, opts...)
				return nil, nil
			}
			envConfigAccessor := ConstructEnvOrDie(func() EnvConfigAccessor {
				return &EnvConfig{}

			})

			ceClient, err := NewCloudEventsClientCRStatus(envConfigAccessor, &mockReporter{}, nil)
			if err != nil {
				t.Fail()
			}
			timeoutSet := false
			if tc.timeout != 0 {
				for _, opt := range sendOptions {
					p := http.Protocol{}
					if err := opt(&p); err != nil {
						t.Error(err)
					}
					if p.Client != nil {
						if p.Client.Timeout == time.Duration(tc.timeout)*time.Second {
							timeoutSet = true
						}
					}

				}
				if !timeoutSet {
					t.Error("Expected timeout to be set")
				}

			}
			got, ok := ceClient.(*client)
			if !ok {
				t.Errorf("expected NewCloudEventsClient to return a *client, but did not")
			}
			innerClient := &test.TestCloudEventsClient{}
			if tc.result != nil {
				innerClient.Send_AppendResult(tc.result)
			}
			got.ceClient = innerClient

			if tc.event != nil {
				err := got.Send(context.TODO(), *tc.event)
				if !tc.wantErr && cloudevents.IsUndelivered(err) {
					t.Fatal(err)
				} else if tc.wantErr && cloudevents.IsACK(err) {
					t.Fatal(err)
				}
				validateSent(t, innerClient, tc.event.Type())
				validateMetric(t, got.reporter, 1)
			} else {
				validateNotSent(t, innerClient)
			}
		})
	}
}

func TestNewCloudEventsClient_request(t *testing.T) {
	testCases := map[string]struct {
		ceOverrides *duckv1.CloudEventOverrides
		event       *cloudevents.Event
	}{
		"none": {},
		"send": {
			event: func() *cloudevents.Event {
				event := cloudevents.NewEvent()
				event.SetID("abc-123")
				event.SetSource("unit/test")
				event.SetType("unit.type")
				return &event
			}(),
		},
		"send with ceOverrides": {
			event: func() *cloudevents.Event {
				event := cloudevents.NewEvent()
				event.SetID("abc-123")
				event.SetSource("unit/test")
				event.SetType("unit.type")
				return &event
			}(),
			ceOverrides: &duckv1.CloudEventOverrides{Extensions: map[string]string{
				"foo": "bar",
			}},
		},
	}
	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			ceClient, err := NewCloudEventsClient(fakeURL, tc.ceOverrides, &mockReporter{})
			if err != nil {
				t.Fail()
			}
			got, ok := ceClient.(*client)
			if !ok {
				t.Errorf("expected NewCloudEventsClient to return a *client, but did not")
			}
			innerClient := &test.TestCloudEventsClient{}
			got.ceClient = innerClient

			if tc.event != nil {
				_, err := got.Request(context.TODO(), *tc.event)
				if !cloudevents.IsACK(err) {
					t.Fatal(err)
				}
				validateSent(t, innerClient, tc.event.Type())
				validateMetric(t, got.reporter, 1)
			} else {
				validateNotSent(t, innerClient)
			}
		})
	}
}

func validateSent(t *testing.T, ce *test.TestCloudEventsClient, want string) {
	if got := len(ce.Sent()); got != 1 {
		t.Error("Expected 1 event to be sent, got", got)
	}

	if got := ce.Sent()[0].Type(); got != want {
		t.Errorf("Expected %q event to be sent, got %q", want, got)
	}
}

func validateNotSent(t *testing.T, ce *test.TestCloudEventsClient) {
	if got := len(ce.Sent()); got != 0 {
		t.Error("Expected no events to be sent, got", got)
	}
}

func validateMetric(t *testing.T, reporter source.StatsReporter, want int) {
	if mockReporter, ok := reporter.(*mockReporter); !ok {
		t.Errorf("Reporter is not a mockReporter")
	} else if mockReporter.eventCount != want {
		t.Errorf("Expected %d for metric, got %d", want, mockReporter.eventCount)
	}
}
