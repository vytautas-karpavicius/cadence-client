// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package internal

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/zap"
)

const (
	// assume this is some error reason defined by activity implementation.
	customErrReasonA = "CustomReasonA"
)

type testStruct struct {
	Name string
	Age  int
}

type testStruct2 struct {
	Name      string
	Age       int
	Favorites *[]string
}

type testErrorStruct struct {
	message string
}

var (
	testErrorDetails1 = "my details"
	testErrorDetails2 = 123
	testErrorDetails3 = testStruct{"a string", 321}
	testErrorDetails4 = testStruct2{"a string", 321, &[]string{"eat", "code"}}
)

func (tes *testErrorStruct) Error() string {
	return tes.message
}

func Test_GenericError(t *testing.T) {
	// test activity error
	errorActivityFn := func() error {
		return errors.New("error:foo")
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err := env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)
	require.Equal(t, &GenericError{"error:foo"}, err)

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return errors.New("error:foo")
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	require.Equal(t, &GenericError{"error:foo"}, err)
}

func Test_ActivityNotRegistered(t *testing.T) {
	registeredActivityFn, unregisteredActivitFn := "RegisteredActivity", "UnregisteredActivityFn"
	s := &WorkflowTestSuite{}
	s.SetLogger(zap.NewNop())
	env := s.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(func() error { return nil }, RegisterActivityOptions{Name: registeredActivityFn})
	_, err := env.ExecuteActivity(unregisteredActivitFn)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("unable to find activityType=%v", unregisteredActivitFn))
	require.Contains(t, err.Error(), registeredActivityFn)
}

func Test_TimeoutError(t *testing.T) {
	timeoutErr := NewTimeoutError(apiv1.TimeoutType_TIMEOUT_TYPE_SCHEDULE_TO_START)
	require.False(t, timeoutErr.HasDetails())
	var data string
	require.Equal(t, ErrNoData, timeoutErr.Details(&data))

	heartbeatErr := NewHeartbeatTimeoutError(testErrorDetails1)
	require.True(t, heartbeatErr.HasDetails())
	require.NoError(t, heartbeatErr.Details(&data))
	require.Equal(t, testErrorDetails1, data)
}

func Test_TimeoutError_WithDetails(t *testing.T) {
	testTimeoutErrorDetails(t, apiv1.TimeoutType_TIMEOUT_TYPE_HEARTBEAT)
	testTimeoutErrorDetails(t, apiv1.TimeoutType_TIMEOUT_TYPE_SCHEDULE_TO_CLOSE)
	testTimeoutErrorDetails(t, apiv1.TimeoutType_TIMEOUT_TYPE_START_TO_CLOSE)
}

func testTimeoutErrorDetails(t *testing.T, timeoutType apiv1.TimeoutType) {
	context := &workflowEnvironmentImpl{
		decisionsHelper: newDecisionsHelper(),
		dataConverter:   getDefaultDataConverter(),
	}
	h := newDecisionsHelper()
	var actualErr error
	activityID := "activityID"
	context.decisionsHelper.scheduledEventIDToActivityID[5] = activityID
	di := h.newActivityDecisionStateMachine(
		&apiv1.ScheduleActivityTaskDecisionAttributes{ActivityId: activityID})
	di.state = decisionStateInitiated
	di.setData(&scheduledActivity{
		callback: func(r []byte, e error) {
			actualErr = e
		},
	})
	context.decisionsHelper.addDecision(di)
	encodedDetails1, _ := context.dataConverter.ToData(testErrorDetails1)
	event := createTestEventActivityTaskTimedOut(7, &apiv1.ActivityTaskTimedOutEventAttributes{
		Details:          &apiv1.Payload{Data: encodedDetails1},
		ScheduledEventId: 5,
		StartedEventId:   6,
		TimeoutType:      timeoutType,
	})
	weh := &workflowExecutionEventHandlerImpl{context, nil}
	weh.handleActivityTaskTimedOut(event)
	err, ok := actualErr.(*TimeoutError)
	require.True(t, ok)
	require.True(t, err.HasDetails())
	data := ""
	require.NoError(t, err.Details(&data))
	require.Equal(t, testErrorDetails1, data)
}

func Test_CustomError(t *testing.T) {
	// test ErrorDetailValues as Details
	var a1 string
	var a2 int
	var a3 testStruct
	err0 := NewCustomError(customErrReasonA, testErrorDetails1)
	require.True(t, err0.HasDetails())
	err0.Details(&a1)
	require.Equal(t, testErrorDetails1, a1)
	a1 = ""
	err0 = NewCustomError(customErrReasonA, testErrorDetails1, testErrorDetails2, testErrorDetails3)
	require.True(t, err0.HasDetails())
	err0.Details(&a1, &a2, &a3)
	require.Equal(t, testErrorDetails1, a1)
	require.Equal(t, testErrorDetails2, a2)
	require.Equal(t, testErrorDetails3, a3)

	// test EncodedValues as Details
	errorActivityFn := func() error {
		return err0
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err := env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)
	err1, ok := err.(*CustomError)
	require.True(t, ok)
	require.True(t, err1.HasDetails())
	var b1 string
	var b2 int
	var b3 testStruct
	err1.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)

	// test reason and no detail
	require.Panics(t, func() { NewCustomError("cadenceInternal:testReason") })
	newReason := "another reason"
	err2 := NewCustomError(newReason)
	require.True(t, !err2.HasDetails())
	require.Equal(t, ErrNoData, err2.Details())
	require.Equal(t, newReason, err2.Reason())
	err3 := NewCustomError(newReason, nil)
	// TODO: probably we want to handle this case when details are nil, HasDetails return false
	require.True(t, err3.HasDetails())

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return err0
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	err4, ok := err.(*CustomError)
	require.True(t, ok)
	require.True(t, err4.HasDetails())
	err4.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)
}

func Test_CustomError_Pointer(t *testing.T) {
	a1 := testStruct2{}
	err1 := NewCustomError(customErrReasonA, testErrorDetails4)
	require.True(t, err1.HasDetails())
	err := err1.Details(&a1)
	require.NoError(t, err)
	require.Equal(t, testErrorDetails4, a1)

	a2 := &testStruct2{}
	err2 := NewCustomError(customErrReasonA, &testErrorDetails4) // // pointer in details
	require.True(t, err2.HasDetails())
	err = err2.Details(&a2)
	require.NoError(t, err)
	require.Equal(t, &testErrorDetails4, a2)

	// test EncodedValues as Details
	errorActivityFn := func() error {
		return err1
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err = env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)
	err3, ok := err.(*CustomError)
	require.True(t, ok)
	require.True(t, err3.HasDetails())
	b1 := testStruct2{}
	require.NoError(t, err3.Details(&b1))
	require.Equal(t, testErrorDetails4, b1)

	errorActivityFn2 := func() error {
		return err2 // pointer in details
	}
	env.RegisterActivity(errorActivityFn2)
	_, err = env.ExecuteActivity(errorActivityFn2)
	require.Error(t, err)
	err4, ok := err.(*CustomError)
	require.True(t, ok)
	require.True(t, err4.HasDetails())
	b2 := &testStruct2{}
	require.NoError(t, err4.Details(&b2))
	require.Equal(t, &testErrorDetails4, b2)

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return err1
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	err5, ok := err.(*CustomError)
	require.True(t, ok)
	require.True(t, err5.HasDetails())
	err5.Details(&b1)
	require.NoError(t, err5.Details(&b1))
	require.Equal(t, testErrorDetails4, b1)

	errorWorkflowFn2 := func(ctx Context) error {
		return err2 // pointer in details
	}
	wfEnv = s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn2)
	wfEnv.ExecuteWorkflow(errorWorkflowFn2)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	err6, ok := err.(*CustomError)
	require.True(t, ok)
	require.True(t, err6.HasDetails())
	err6.Details(&b2)
	require.NoError(t, err6.Details(&b2))
	require.Equal(t, &testErrorDetails4, b2)
}

func Test_CustomError_WrongDecodedType(t *testing.T) {
	err := NewCustomError("reason", testErrorDetails1, testErrorDetails2)
	var d1 string
	var d2 string // will cause error since it should be of type int
	err1 := err.Details(&d1, &d2)
	require.Error(t, err1)

	err = NewCustomError("reason", testErrorDetails3)
	var d3 testStruct2 // will cause error since it should be of type testStruct
	err2 := err.Details(&d3)
	require.Error(t, err2)
}

func Test_CanceledError(t *testing.T) {
	// test ErrorDetailValues as Details
	var a1 string
	var a2 int
	var a3 testStruct
	err0 := NewCanceledError(testErrorDetails1)
	require.True(t, err0.HasDetails())
	err0.Details(&a1)
	require.Equal(t, testErrorDetails1, a1)
	a1 = ""
	err0 = NewCanceledError(testErrorDetails1, testErrorDetails2, testErrorDetails3)
	require.True(t, err0.HasDetails())
	err0.Details(&a1, &a2, &a3)
	require.Equal(t, testErrorDetails1, a1)
	require.Equal(t, testErrorDetails2, a2)
	require.Equal(t, testErrorDetails3, a3)

	// test EncodedValues as Details
	errorActivityFn := func() error {
		return err0
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err := env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)
	err1, ok := err.(*CanceledError)
	require.True(t, ok)
	require.True(t, err1.HasDetails())
	var b1 string
	var b2 int
	var b3 testStruct
	err1.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)

	err2 := NewCanceledError()
	require.False(t, err2.HasDetails())

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return err0
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	err3, ok := err.(*CanceledError)
	require.True(t, ok)
	require.True(t, err3.HasDetails())
	err3.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)
}

func Test_IsCanceledError(t *testing.T) {

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "empty detail",
			err:      NewCanceledError(),
			expected: true,
		},
		{
			name:     "with detail",
			err:      NewCanceledError("details"),
			expected: true,
		},
		{
			name:     "not canceled error",
			err:      errors.New("details"),
			expected: false,
		},
	}

	for _, test := range tests {
		require.Equal(t, test.expected, IsCanceledError(test.err))
	}
}

func TestErrorDetailsValues(t *testing.T) {
	e := ErrorDetailsValues{}
	require.Equal(t, ErrNoData, e.Get())

	e = ErrorDetailsValues{testErrorDetails1, testErrorDetails2, testErrorDetails3}
	var a1 string
	var a2 int
	var a3 testStruct
	require.True(t, e.HasValues())
	e.Get(&a1)
	require.Equal(t, testErrorDetails1, a1)
	e.Get(&a1, &a2, &a3)
	require.Equal(t, testErrorDetails1, a1)
	require.Equal(t, testErrorDetails2, a2)
	require.Equal(t, testErrorDetails3, a3)

	require.Equal(t, ErrTooManyArg, e.Get(&a1, &a2, &a3, &a3))
}

func TestErrorDetailsValues_WrongDecodedType(t *testing.T) {
	e := ErrorDetailsValues{testErrorDetails1}
	var d1 int // will cause error since it should be of type string
	err := e.Get(&d1)
	require.Error(t, err)
}

func TestErrorDetailsValues_AssignableType(t *testing.T) {
	e := ErrorDetailsValues{&testErrorStruct{message: "my message"}}
	var errorOut error
	err := e.Get(&errorOut)
	require.NoError(t, err)
	require.Equal(t, "my message", errorOut.Error())
}

func Test_SignalExternalWorkflowExecutionFailedError(t *testing.T) {
	context := &workflowEnvironmentImpl{
		decisionsHelper: newDecisionsHelper(),
		dataConverter:   getDefaultDataConverter(),
	}
	h := newDecisionsHelper()
	var actualErr error
	var initiatedEventID int64 = 101
	signalID := "signalID"
	context.decisionsHelper.scheduledEventIDToSignalID[initiatedEventID] = signalID
	di := h.newSignalExternalWorkflowStateMachine(
		&apiv1.SignalExternalWorkflowExecutionDecisionAttributes{},
		signalID,
	)
	di.state = decisionStateInitiated
	di.setData(&scheduledSignal{
		callback: func(r []byte, e error) {
			actualErr = e
		},
	})
	context.decisionsHelper.addDecision(di)
	weh := &workflowExecutionEventHandlerImpl{context, nil}
	event := createTestEventSignalExternalWorkflowExecutionFailed(1, &apiv1.SignalExternalWorkflowExecutionFailedEventAttributes{
		InitiatedEventId: initiatedEventID,
		Cause:            apiv1.SignalExternalWorkflowExecutionFailedCause_SIGNAL_EXTERNAL_WORKFLOW_EXECUTION_FAILED_CAUSE_UNKNOWN_EXTERNAL_WORKFLOW_EXECUTION,
	})
	require.NoError(t, weh.handleSignalExternalWorkflowExecutionFailed(event))
	_, ok := actualErr.(*UnknownExternalWorkflowExecutionError)
	require.True(t, ok)
}

func Test_ContinueAsNewError(t *testing.T) {
	var a1 = 1234
	var a2 = "some random input"

	continueAsNewWfName := "continueAsNewWorkflowFn"
	continueAsNewWorkflowFn := func(ctx Context, testInt int, testString string) error {
		return NewContinueAsNewError(ctx, continueAsNewWfName, a1, a2)
	}

	header := &apiv1.Header{
		Fields: map[string]*apiv1.Payload{"test": {Data: []byte("test-data")}},
	}

	s := &WorkflowTestSuite{
		header:   header,
		ctxProps: []ContextPropagator{NewStringMapPropagator([]string{"test"})},
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflowWithOptions(continueAsNewWorkflowFn, RegisterWorkflowOptions{
		Name: continueAsNewWfName,
	})
	wfEnv.ExecuteWorkflow(continueAsNewWorkflowFn, 101, "another random string")
	err := wfEnv.GetWorkflowError()

	require.Error(t, err)
	continueAsNewErr, ok := err.(*ContinueAsNewError)
	require.True(t, ok)
	require.Equal(t, continueAsNewWfName, continueAsNewErr.WorkflowType().Name)

	args := continueAsNewErr.Args()
	intArg, ok := args[0].(int)
	require.True(t, ok)
	require.Equal(t, a1, intArg)
	stringArg, ok := args[1].(string)
	require.True(t, ok)
	require.Equal(t, a2, stringArg)
	require.Equal(t, header, continueAsNewErr.params.header)
}
