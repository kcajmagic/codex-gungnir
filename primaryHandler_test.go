/**
 * Copyright 2019 Comcast Cable Communications Management, LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Comcast/webpa-common/logging"
	"github.com/Comcast/webpa-common/xmetrics/xmetricstest"
	kithttp "github.com/go-kit/kit/transport/http"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	"github.com/Comcast/codex/db"
)

var (
	goodEvent = db.Event{
		ID:          1234,
		Time:        567890974,
		Source:      "test source",
		Destination: "/test/online",
		PartnerIDs:  []string{"test1", "test2"},
		Payload: []byte(`{
			"id": "mac:48f7c0d79024",
			"ts": "2019-02-14T21:19:02.614191735Z",
			"bytes-sent": 0,
			"messages-sent": 1,
			"bytes-received": 0,
			"messages-received": 0,
			"connected-at": "2018-11-22T21:19:02.614191735Z",
			"up-time": "16m46.6s",
			"reason-for-close": "ping miss"
		}`),
	}
)

const (
	key = iota
)

func TestGetDeviceInfo(t *testing.T) {
	getRecordsErr := errors.New("get records test error")

	testassert := assert.New(t)
	futureTime := time.Now().Add(time.Duration(50000) * time.Minute).Unix()
	prevTime, err := time.Parse(time.RFC3339Nano, "2019-02-13T21:19:02.614191735Z")
	testassert.Nil(err)
	previousTime := prevTime.Unix()

	goodData, err := json.Marshal(&goodEvent)
	testassert.Nil(err)
	badData, err := json.Marshal("")
	testassert.Nil(err)

	tests := []struct {
		description           string
		recordsToReturn       []db.Record
		getRecordsErr         error
		expectedFailureMetric float64
		expectedEvents        []db.Event
		expectedErr           error
		expectedStatus        int
	}{
		{
			description:    "Get Records Error",
			getRecordsErr:  getRecordsErr,
			expectedEvents: []db.Event{},
			expectedErr:    getRecordsErr,
			expectedStatus: http.StatusInternalServerError,
		},
		{
			description:    "Empty Records Error",
			expectedEvents: []db.Event{},
			expectedErr:    errors.New("No events found"),
			expectedStatus: http.StatusNotFound,
		},
		{
			description: "Expired Records Error",
			recordsToReturn: []db.Record{
				db.Record{
					DeathDate: previousTime,
				},
			},
			expectedEvents: []db.Event{},
			expectedErr:    errors.New("No events found"),
			expectedStatus: http.StatusNotFound,
		},
		{
			description: "Unmarshal Event Error",
			recordsToReturn: []db.Record{
				{
					DeathDate: futureTime,
					Data:      badData,
				},
			},
			expectedFailureMetric: 1.0,
			expectedEvents:        []db.Event{},
			expectedErr:           errors.New("No events found"),
			expectedStatus:        http.StatusNotFound,
		},
		{
			description: "Success",
			recordsToReturn: []db.Record{
				{
					ID:        1234,
					DeathDate: futureTime,
					Data:      goodData,
				},
			},
			expectedEvents: []db.Event{
				goodEvent,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			assert := assert.New(t)
			mockGetter := new(mockRecordGetter)
			mockGetter.On("GetRecords", "test", 5).Return(tc.recordsToReturn, tc.getRecordsErr).Once()
			p := xmetricstest.NewProvider(nil, Metrics)
			m := NewMeasures(p)
			app := App{
				eventGetter: mockGetter,
				logger:      logging.DefaultLogger(),
				measures:    m,
				getLimit:    5,
			}
			p.Assert(t, UnmarshalFailureCounter)(xmetricstest.Value(0.0))
			events, err := app.getDeviceInfo("test")
			p.Assert(t, UnmarshalFailureCounter)(xmetricstest.Value(tc.expectedFailureMetric))
			assert.Equal(tc.expectedEvents, events)

			if tc.expectedErr == nil || err == nil {
				assert.Equal(tc.expectedErr, err)
			} else {
				assert.Contains(err.Error(), tc.expectedErr.Error())
			}
			if tc.expectedStatus > 0 {
				statusCodeErr, ok := err.(kithttp.StatusCoder)
				assert.True(ok, "expected error to have a status code")
				assert.Equal(tc.expectedStatus, statusCodeErr.StatusCode())
			}
		})
	}
}

func TestHandleGetEvents(t *testing.T) {
	testassert := assert.New(t)
	futureTime := time.Now().Add(time.Duration(50000) * time.Minute).Unix()
	goodData, err := json.Marshal(&goodEvent)
	testassert.Nil(err)

	tests := []struct {
		description        string
		deviceID           string
		recordsToReturn    []db.Record
		expectedStatusCode int
		expectedBody       []byte
	}{
		{
			description:        "Empty Device ID Error",
			deviceID:           "",
			expectedStatusCode: http.StatusNotFound,
		},
		{
			description:        "Get Device Info Error",
			deviceID:           "1234",
			expectedStatusCode: http.StatusNotFound,
		},
		{
			description: "Success",
			deviceID:    "1234",
			recordsToReturn: []db.Record{
				{
					ID:        1234,
					DeathDate: futureTime,
					Data:      goodData,
				},
			},
			expectedStatusCode: http.StatusOK,
			expectedBody:       goodData,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			assert := assert.New(t)
			mockGetter := new(mockRecordGetter)
			mockGetter.On("GetRecords", tc.deviceID, 5).Return(tc.recordsToReturn, nil).Once()
			app := App{
				eventGetter: mockGetter,
				getLimit:    5,
				logger:      logging.DefaultLogger(),
			}
			rr := httptest.NewRecorder()
			request := mux.SetURLVars(
				httptest.NewRequest("GET", "/1234/status", nil),
				map[string]string{"deviceID": tc.deviceID},
			)
			app.handleGetEvents(rr, request)
			assert.Equal(tc.expectedStatusCode, rr.Code)
		})
	}
}
