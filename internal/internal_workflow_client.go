// Copyright (c) 2017-2020 Uber Technologies Inc.
// Portions of the Software are attributed to Copyright (c) 2020 Temporal Technologies Inc.
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/pborman/uuid"
	"github.com/uber-go/tally"

	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/cadence/v2/internal/api"
	"go.uber.org/cadence/v2/internal/common"
	"go.uber.org/cadence/v2/internal/common/backoff"
	"go.uber.org/cadence/v2/internal/common/metrics"
	"go.uber.org/cadence/v2/internal/common/serializer"
	"go.uber.org/cadence/v2/internal/common/util"
)

// Assert that structs do indeed implement the interfaces
var _ Client = (*workflowClient)(nil)
var _ DomainClient = (*domainClient)(nil)

const (
	defaultDecisionTaskTimeout = 10 * time.Second
	defaultGetHistoryTimeoutInSecs   = 25
)

var (
	maxListArchivedWorkflowTimeout = time.Minute * 3
)

type (
	// workflowClient is the client for starting a workflow execution.
	workflowClient struct {
		workflowService    api.Interface
		domain             string
		registry           *registry
		metricsScope       *metrics.TaggedScope
		identity           string
		dataConverter      DataConverter
		contextPropagators []ContextPropagator
		tracer             opentracing.Tracer
	}

	// domainClient is the client for managing domains.
	domainClient struct {
		workflowService api.Interface
		metricsScope    tally.Scope
		identity        string
	}

	// WorkflowRun represents a started non child workflow
	WorkflowRun interface {
		// GetID return workflow ID, which will be same as StartWorkflowOptions.ID if provided.
		GetID() string

		// GetRunID return the first started workflow run ID (please see below)
		GetRunID() string

		// Get will fill the workflow execution result to valuePtr,
		// if workflow execution is a success, or return corresponding,
		// error. This is a blocking API.
		Get(ctx context.Context, valuePtr interface{}) error

		// NOTE: if the started workflow return ContinueAsNewError during the workflow execution, the
		// return result of GetRunID() will be the started workflow run ID, not the new run ID caused by ContinueAsNewError,
		// however, Get(ctx context.Context, valuePtr interface{}) will return result from the run which did not return ContinueAsNewError.
		// Say ExecuteWorkflow started a workflow, in its first run, has run ID "run ID 1", and returned ContinueAsNewError,
		// the second run has run ID "run ID 2" and return some result other than ContinueAsNewError:
		// GetRunID() will always return "run ID 1" and  Get(ctx context.Context, valuePtr interface{}) will return the result of second run.
		// NOTE: DO NOT USE client.ExecuteWorkflow API INSIDE A WORKFLOW, USE workflow.ExecuteChildWorkflow instead
	}

	// workflowRunImpl is an implementation of WorkflowRun
	workflowRunImpl struct {
		workflowFn    interface{}
		workflowID    string
		firstRunID    string
		currentRunID  string
		iterFn        func(ctx context.Context, runID string) HistoryEventIterator
		dataConverter DataConverter
		registry      *registry
	}

	// HistoryEventIterator represents the interface for
	// history event iterator
	HistoryEventIterator interface {
		// HasNext return whether this iterator has next value
		HasNext() bool
		// Next returns the next history events and error
		// The errors it can return:
		//	- EntityNotExistsError
		//	- BadRequestError
		//	- InternalServiceError
		Next() (*apiv1.HistoryEvent, error)
	}

	// historyEventIteratorImpl is the implementation of HistoryEventIterator
	historyEventIteratorImpl struct {
		// whether this iterator is initialized
		initialized bool
		// local cached history events and corresponding consuming index
		nextEventIndex int
		events         []*apiv1.HistoryEvent
		// token to get next page of history events
		nexttoken []byte
		// err when getting next page of history events
		err error
		// func which use a next token to get next page of history events
		paginate func(nexttoken []byte) (*apiv1.GetWorkflowExecutionHistoryResponse, error)
	}
)

// StartWorkflow starts a workflow execution
// The user can use this to start using a functor like.
// Either by
//     StartWorkflow(options, "workflowTypeName", arg1, arg2, arg3)
//     or
//     StartWorkflow(options, workflowExecuteFn, arg1, arg2, arg3)
// The current timeout resolution implementation is in seconds and uses math.Ceil(d.Seconds()) as the duration. But is
// subjected to change in the future.
func (wc *workflowClient) StartWorkflow(
	ctx context.Context,
	options StartWorkflowOptions,
	workflowFunc interface{},
	args ...interface{},
) (*WorkflowExecution, error) {
	workflowID := options.ID
	if len(workflowID) == 0 {
		workflowID = uuid.NewRandom().String()
	}

	if options.TaskList == "" {
		return nil, errors.New("missing TaskList")
	}

	executionTimeout := options.ExecutionStartToCloseTimeout
	if executionTimeout <= 0 {
		return nil, errors.New("missing or invalid ExecutionStartToCloseTimeout")
	}

	decisionTaskTimeout := options.DecisionTaskStartToCloseTimeout
	if decisionTaskTimeout < 0 {
		return nil, errors.New("negative DecisionTaskStartToCloseTimeout provided")
	}
	if decisionTaskTimeout == 0 {
		decisionTaskTimeout = defaultDecisionTaskTimeout
	}

	// Validate type and its arguments.
	workflowType, input, err := getValidatedWorkflowFunction(workflowFunc, args, wc.dataConverter, wc.registry)
	if err != nil {
		return nil, err
	}

	memo, err := getWorkflowMemo(options.Memo, wc.dataConverter)
	if err != nil {
		return nil, err
	}

	searchAttr, err := serializeSearchAttributes(options.SearchAttributes)
	if err != nil {
		return nil, err
	}

	delayStart := options.DelayStart
	if delayStart < 0 {
		return nil, errors.New("Invalid DelayStart option")
	}

	// create a workflow start span and attach it to the context object.
	// N.B. we need to finish this immediately as jaeger does not give us a way
	// to recreate a span given a span context - which means we will run into
	// issues during replay. we work around this by creating and ending the
	// workflow start span and passing in that context to the workflow. So
	// everything beginning with the StartWorkflowExecutionRequest will be
	// parented by the created start workflow span.
	ctx, span := createOpenTracingWorkflowSpan(ctx, wc.tracer, time.Now(), fmt.Sprintf("StartWorkflow-%s", workflowType.Name), workflowID)
	span.Finish()

	// get workflow headers from the context
	header := wc.getWorkflowHeader(ctx)

	// run propagators to extract information about tracing and other stuff, store in headers field
	startRequest := &apiv1.StartWorkflowExecutionRequest{
		Domain:                       wc.domain,
		RequestId:                    uuid.New(),
		WorkflowId:                   workflowID,
		WorkflowType:                 &apiv1.WorkflowType{Name: workflowType.Name},
		TaskList:                     &apiv1.TaskList{Name: options.TaskList},
		Input:                        &apiv1.Payload{Data: input},
		ExecutionStartToCloseTimeout: api.DurationToProto(executionTimeout),
		TaskStartToCloseTimeout:      api.DurationToProto(decisionTaskTimeout),
		Identity:                     wc.identity,
		WorkflowIdReusePolicy:        options.WorkflowIDReusePolicy.toProto(),
		RetryPolicy:                  convertRetryPolicy(options.RetryPolicy),
		CronSchedule:                 options.CronSchedule,
		Memo:                         memo,
		SearchAttributes:             searchAttr,
		Header:                       header,
		DelayStart:                   api.DurationToProto(delayStart),
	}

	var response *apiv1.StartWorkflowExecutionResponse

	// Start creating workflow request.
	err = backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()

			var err1 error
			response, err1 = wc.workflowService.StartWorkflowExecution(tchCtx, startRequest, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)

	if err != nil {
		return nil, err
	}

	if wc.metricsScope != nil {
		scope := wc.metricsScope.GetTaggedScope(tagTaskList, options.TaskList, tagWorkflowType, workflowType.Name)
		scope.Counter(metrics.WorkflowStartCounter).Inc(1)
	}

	executionInfo := &WorkflowExecution{
		ID:    workflowID,
		RunID: response.GetRunId()}
	return executionInfo, nil
}

// ExecuteWorkflow starts a workflow execution and returns a WorkflowRun that will allow you to wait until this workflow
// reaches the end state, such as workflow finished successfully or timeout.
// The user can use this to start using a functor like below and get the workflow execution result, as Value
// Either by
//     ExecuteWorkflow(options, "workflowTypeName", arg1, arg2, arg3)
//     or
//     ExecuteWorkflow(options, workflowExecuteFn, arg1, arg2, arg3)
// The current timeout resolution implementation is in seconds and uses math.Ceil(d.Seconds()) as the duration. But is
// subjected to change in the future.
// NOTE: the context.Context should have a fairly large timeout, since workflow execution may take a while to be finished
func (wc *workflowClient) ExecuteWorkflow(ctx context.Context, options StartWorkflowOptions, workflow interface{}, args ...interface{}) (WorkflowRun, error) {

	// start the workflow execution
	var runID string
	var workflowID string
	executionInfo, err := wc.StartWorkflow(ctx, options, workflow, args...)
	if err != nil {
		if alreadyStartedErr, ok := err.(*api.WorkflowExecutionAlreadyStartedError); ok {
			runID = alreadyStartedErr.RunID
			// Assumption is that AlreadyStarted is never returned when options.ID is empty as UUID generated by
			// StartWorkflow is not going to collide ever.
			workflowID = options.ID
		} else {
			return nil, err
		}
	} else {
		runID = executionInfo.RunID
		workflowID = executionInfo.ID
	}

	iterFn := func(fnCtx context.Context, fnRunID string) HistoryEventIterator {
		return wc.GetWorkflowHistory(fnCtx, workflowID, fnRunID, true, apiv1.EventFilterType_EVENT_FILTER_TYPE_CLOSE_EVENT)
	}

	return &workflowRunImpl{
		workflowFn:    workflow,
		workflowID:    workflowID,
		firstRunID:    runID,
		currentRunID:  runID,
		iterFn:        iterFn,
		dataConverter: wc.dataConverter,
		registry:      wc.registry,
	}, nil
}

// GetWorkflow gets a workflow execution and returns a WorkflowRun that will allow you to wait until this workflow
// reaches the end state, such as workflow finished successfully or timeout.
// The current timeout resolution implementation is in seconds and uses math.Ceil(d.Seconds()) as the duration. But is
// subjected to change in the future.
func (wc *workflowClient) GetWorkflow(ctx context.Context, workflowID string, runID string) WorkflowRun {

	iterFn := func(fnCtx context.Context, fnRunID string) HistoryEventIterator {
		return wc.GetWorkflowHistory(fnCtx, workflowID, fnRunID, true, apiv1.EventFilterType_EVENT_FILTER_TYPE_CLOSE_EVENT)
	}

	return &workflowRunImpl{
		workflowID:    workflowID,
		firstRunID:    runID,
		currentRunID:  runID,
		iterFn:        iterFn,
		dataConverter: wc.dataConverter,
		registry:      wc.registry,
	}
}

// SignalWorkflow signals a workflow in execution.
func (wc *workflowClient) SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg interface{}) error {
	input, err := encodeArg(wc.dataConverter, arg)
	if err != nil {
		return err
	}
	return signalWorkflow(ctx, wc.workflowService, wc.identity, wc.domain, workflowID, runID, signalName, input)
}

// SignalWithStartWorkflow sends a signal to a running workflow.
// If the workflow is not running or not found, it starts the workflow and then sends the signal in transaction.
func (wc *workflowClient) SignalWithStartWorkflow(ctx context.Context, workflowID string, signalName string, signalArg interface{},
	options StartWorkflowOptions, workflowFunc interface{}, workflowArgs ...interface{}) (*WorkflowExecution, error) {

	signalInput, err := encodeArg(wc.dataConverter, signalArg)
	if err != nil {
		return nil, err
	}

	if workflowID == "" {
		workflowID = uuid.NewRandom().String()
	}

	if options.TaskList == "" {
		return nil, errors.New("missing TaskList")
	}

	executionTimeout := options.ExecutionStartToCloseTimeout
	if executionTimeout <= 0 {
		return nil, errors.New("missing or invalid ExecutionStartToCloseTimeout")
	}

	decisionTaskTimeout := options.DecisionTaskStartToCloseTimeout
	if decisionTaskTimeout < 0 {
		return nil, errors.New("negative DecisionTaskStartToCloseTimeout provided")
	}
	if decisionTaskTimeout == 0 {
		decisionTaskTimeout = defaultDecisionTaskTimeout
	}

	// Validate type and its arguments.
	workflowType, input, err := getValidatedWorkflowFunction(workflowFunc, workflowArgs, wc.dataConverter, wc.registry)
	if err != nil {
		return nil, err
	}

	memo, err := getWorkflowMemo(options.Memo, wc.dataConverter)
	if err != nil {
		return nil, err
	}

	searchAttr, err := serializeSearchAttributes(options.SearchAttributes)
	if err != nil {
		return nil, err
	}

	delayStart := options.DelayStart
	if delayStart < 0 {
		return nil, errors.New("Invalid DelayStart option")
	}

	// create a workflow start span and attach it to the context object. finish it immediately
	ctx, span := createOpenTracingWorkflowSpan(ctx, wc.tracer, time.Now(), fmt.Sprintf("SignalWithStartWorkflow-%s", workflowType.Name), workflowID)
	span.Finish()

	// get workflow headers from the context
	header := wc.getWorkflowHeader(ctx)

	signalWithStartRequest := &apiv1.SignalWithStartWorkflowExecutionRequest{
		SignalName:   signalName,
		SignalInput:  &apiv1.Payload{Data: signalInput},
		StartRequest: &apiv1.StartWorkflowExecutionRequest{
			Domain:                       wc.domain,
			RequestId:                    uuid.New(),
			WorkflowId:                   workflowID,
			WorkflowType:                 &apiv1.WorkflowType{Name: workflowType.Name},
			TaskList:                     &apiv1.TaskList{Name: options.TaskList},
			Input:                        &apiv1.Payload{Data: input},
			ExecutionStartToCloseTimeout: api.DurationToProto(executionTimeout),
			TaskStartToCloseTimeout:      api.DurationToProto(decisionTaskTimeout),
			Identity:                     wc.identity,
			RetryPolicy:                  convertRetryPolicy(options.RetryPolicy),
			CronSchedule:                 options.CronSchedule,
			Memo:                         memo,
			SearchAttributes:             searchAttr,
			WorkflowIdReusePolicy:        options.WorkflowIDReusePolicy.toProto(),
			Header:                       header,
			DelayStart:                   api.DurationToProto(delayStart),
		},
	}

	var response *apiv1.SignalWithStartWorkflowExecutionResponse

	// Start creating workflow request.
	err = backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()

			var err1 error
			response, err1 = wc.workflowService.SignalWithStartWorkflowExecution(tchCtx, signalWithStartRequest, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)

	if err != nil {
		return nil, err
	}

	if wc.metricsScope != nil {
		scope := wc.metricsScope.GetTaggedScope(tagTaskList, options.TaskList, tagWorkflowType, workflowType.Name)
		scope.Counter(metrics.WorkflowSignalWithStartCounter).Inc(1)
	}

	executionInfo := &WorkflowExecution{
		ID:    options.ID,
		RunID: response.GetRunId()}
	return executionInfo, nil
}

// CancelWorkflow cancels a workflow in execution.  It allows workflow to properly clean up and gracefully close.
// workflowID is required, other parameters are optional.
// If runID is omit, it will terminate currently running workflow (if there is one) based on the workflowID.
func (wc *workflowClient) CancelWorkflow(ctx context.Context, workflowID string, runID string) error {
	request := &apiv1.RequestCancelWorkflowExecutionRequest{
		Domain: wc.domain,
		WorkflowExecution: &apiv1.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
		Identity: wc.identity,
	}

	return backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			_, err := wc.workflowService.RequestCancelWorkflowExecution(tchCtx, request, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
}

// TerminateWorkflow terminates a workflow execution.
// workflowID is required, other parameters are optional.
// If runID is omit, it will terminate currently running workflow (if there is one) based on the workflowID.
func (wc *workflowClient) TerminateWorkflow(ctx context.Context, workflowID string, runID string, reason string, details []byte) error {
	request := &apiv1.TerminateWorkflowExecutionRequest{
		Domain: wc.domain,
		WorkflowExecution: &apiv1.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
		Reason:   reason,
		Details:  &apiv1.Payload{Data: details},
		Identity: wc.identity,
	}

	err := backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			_, err := wc.workflowService.TerminateWorkflowExecution(tchCtx, request, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)

	return err
}

// GetWorkflowHistory return a channel which contains the history events of a given workflow
func (wc *workflowClient) GetWorkflowHistory(
	ctx context.Context,
	workflowID string,
	runID string,
	isLongPoll bool,
	filterType apiv1.EventFilterType,
) HistoryEventIterator {

	domain := wc.domain
	paginate := func(nextToken []byte) (*apiv1.GetWorkflowExecutionHistoryResponse, error) {
		request := &apiv1.GetWorkflowExecutionHistoryRequest{
			Domain: domain,
			WorkflowExecution: &apiv1.WorkflowExecution{
				WorkflowId: workflowID,
				RunId:      runID,
			},
			WaitForNewEvent:        isLongPoll,
			HistoryEventFilterType: filterType,
			NextPageToken:          nextToken,
			SkipArchival:           isLongPoll,
		}

		var response *apiv1.GetWorkflowExecutionHistoryResponse
		var err error
	Loop:
		for {
			err = backoff.Retry(ctx,
				func() error {
					var err1 error
					tchCtx, cancel, opt := newChannelContext(ctx, func(builder *contextBuilder) {
						if isLongPoll {
							builder.Timeout = defaultGetHistoryTimeoutInSecs * time.Second
						}
					})
					defer cancel()
					response, err1 = wc.workflowService.GetWorkflowExecutionHistory(tchCtx, request, opt...)
					err1 = api.ConvertError(err1)

					if err1 != nil {
						return err1
					}

					if response.RawHistory != nil {
						history, err := serializer.DeserializeBlobDataToHistoryEvents(response.RawHistory, filterType)
						if err != nil {
							return err
						}
						response.History = history
					}
					return err1
				},
				createDynamicServiceRetryPolicy(ctx),
				func(err error) bool {
					return isServiceTransientError(err) || isEntityNonExistFromPassive(err)
				},
			)

			if err != nil {
				return nil, err
			}
			if isLongPoll && len(response.History.Events) == 0 && len(response.NextPageToken) != 0 {
				request.NextPageToken = response.NextPageToken
				continue Loop
			}
			break Loop
		}
		return response, nil
	}

	return &historyEventIteratorImpl{
		paginate: paginate,
	}
}

func isEntityNonExistFromPassive(err error) bool {
	if nonExistError, ok := err.(*api.EntityNotExistsError); ok {
		return nonExistError.ActiveCluster != "" &&
			nonExistError.CurrentCluster != "" &&
			nonExistError.ActiveCluster != nonExistError.CurrentCluster
	}

	return false
}

// CompleteActivity reports activity completed. activity Execute method can return activity.ErrResultPending to
// indicate the activity is not completed when it's Execute method returns. In that case, this CompleteActivity() method
// should be called when that activity is completed with the actual result and error. If err is nil, activity task
// completed event will be reported; if err is CanceledError, activity task cancelled event will be reported; otherwise,
// activity task failed event will be reported.
func (wc *workflowClient) CompleteActivity(ctx context.Context, taskToken []byte, result interface{}, err error) error {
	if taskToken == nil {
		return errors.New("invalid task token provided")
	}

	var data []byte
	if result != nil {
		var err0 error
		data, err0 = encodeArg(wc.dataConverter, result)
		if err0 != nil {
			return err0
		}
	}
	request := convertActivityResultToRespondRequest(wc.identity, taskToken, data, err, wc.dataConverter)
	return reportActivityComplete(ctx, wc.workflowService, request, wc.metricsScope)
}

// CompleteActivityById reports activity completed. Similar to CompleteActivity
// It takes domain name, workflowID, runID, activityID as arguments.
func (wc *workflowClient) CompleteActivityByID(ctx context.Context, domain, workflowID, runID, activityID string,
	result interface{}, err error) error {

	if activityID == "" || workflowID == "" || domain == "" {
		return errors.New("empty activity or workflow id or domainName")
	}

	var data []byte
	if result != nil {
		var err0 error
		data, err0 = encodeArg(wc.dataConverter, result)
		if err0 != nil {
			return err0
		}
	}

	request := convertActivityResultToRespondRequestByID(wc.identity, domain, workflowID, runID, activityID, data, err, wc.dataConverter)
	return reportActivityCompleteByID(ctx, wc.workflowService, request, wc.metricsScope)
}

// RecordActivityHeartbeat records heartbeat for an activity.
func (wc *workflowClient) RecordActivityHeartbeat(ctx context.Context, taskToken []byte, details ...interface{}) error {
	data, err := encodeArgs(wc.dataConverter, details)
	if err != nil {
		return err
	}
	return recordActivityHeartbeat(ctx, wc.workflowService, wc.identity, taskToken, data)
}

// RecordActivityHeartbeatByID records heartbeat for an activity.
func (wc *workflowClient) RecordActivityHeartbeatByID(ctx context.Context,
	domain, workflowID, runID, activityID string, details ...interface{}) error {
	data, err := encodeArgs(wc.dataConverter, details)
	if err != nil {
		return err
	}
	return recordActivityHeartbeatByID(ctx, wc.workflowService, wc.identity, domain, workflowID, runID, activityID, data)
}

// ListClosedWorkflow gets closed workflow executions based on request filters
// The errors it can throw:
//  - BadRequestError
//  - InternalServiceError
//  - EntityNotExistError
func (wc *workflowClient) ListClosedWorkflow(ctx context.Context, request *apiv1.ListClosedWorkflowExecutionsRequest) (*apiv1.ListClosedWorkflowExecutionsResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.ListClosedWorkflowExecutionsResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.ListClosedWorkflowExecutions(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// ListOpenWorkflow gets open workflow executions based on request filters
// The errors it can throw:
//  - BadRequestError
//  - InternalServiceError
//  - EntityNotExistError
func (wc *workflowClient) ListOpenWorkflow(ctx context.Context, request *apiv1.ListOpenWorkflowExecutionsRequest) (*apiv1.ListOpenWorkflowExecutionsResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.ListOpenWorkflowExecutionsResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.ListOpenWorkflowExecutions(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// ListWorkflow implementation
func (wc *workflowClient) ListWorkflow(ctx context.Context, request *apiv1.ListWorkflowExecutionsRequest) (*apiv1.ListWorkflowExecutionsResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.ListWorkflowExecutionsResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.ListWorkflowExecutions(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// ListArchivedWorkflow implementation
func (wc *workflowClient) ListArchivedWorkflow(ctx context.Context, request *apiv1.ListArchivedWorkflowExecutionsRequest) (*apiv1.ListArchivedWorkflowExecutionsResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.ListArchivedWorkflowExecutionsResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			timeout := maxListArchivedWorkflowTimeout
			now := time.Now()
			if ctx != nil {
				if expiration, ok := ctx.Deadline(); ok && expiration.After(now) {
					timeout = expiration.Sub(now)
					if timeout > maxListArchivedWorkflowTimeout {
						timeout = maxListArchivedWorkflowTimeout
					} else if timeout < minRPCTimeout {
						timeout = minRPCTimeout
					}
				}
			}
			tchCtx, cancel, opt := newChannelContext(ctx, chanTimeout(timeout))
			defer cancel()
			response, err1 = wc.workflowService.ListArchivedWorkflowExecutions(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// ScanWorkflow implementation
func (wc *workflowClient) ScanWorkflow(ctx context.Context, request *apiv1.ScanWorkflowExecutionsRequest) (*apiv1.ScanWorkflowExecutionsResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.ScanWorkflowExecutionsResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.ScanWorkflowExecutions(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// CountWorkflow implementation
func (wc *workflowClient) CountWorkflow(ctx context.Context, request *apiv1.CountWorkflowExecutionsRequest) (*apiv1.CountWorkflowExecutionsResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.CountWorkflowExecutionsResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.CountWorkflowExecutions(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// ResetWorkflow implementation
func (wc *workflowClient) ResetWorkflow(ctx context.Context, request *apiv1.ResetWorkflowExecutionRequest) (*apiv1.ResetWorkflowExecutionResponse, error) {
	if len(request.GetDomain()) == 0 {
		request.Domain = wc.domain
	}
	var response *apiv1.ResetWorkflowExecutionResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.ResetWorkflowExecution(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// GetSearchAttributes implementation
func (wc *workflowClient) GetSearchAttributes(ctx context.Context) (*apiv1.GetSearchAttributesResponse, error) {
	var response *apiv1.GetSearchAttributesResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.GetSearchAttributes(tchCtx, &apiv1.GetSearchAttributesRequest{}, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// DescribeWorkflowExecution returns information about the specified workflow execution.
// The errors it can return:
//  - BadRequestError
//  - InternalServiceError
//  - EntityNotExistError
func (wc *workflowClient) DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*apiv1.DescribeWorkflowExecutionResponse, error) {
	request := &apiv1.DescribeWorkflowExecutionRequest{
		Domain: wc.domain,
		WorkflowExecution: &apiv1.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
	}
	var response *apiv1.DescribeWorkflowExecutionResponse
	err := backoff.Retry(ctx,
		func() error {
			var err1 error
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			response, err1 = wc.workflowService.DescribeWorkflowExecution(tchCtx, request, opt...)
			return api.ConvertError(err1)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// QueryWorkflow queries a given workflow execution
// workflowID and queryType are required, other parameters are optional.
// - workflow ID of the workflow.
// - runID can be default(empty string). if empty string then it will pick the running execution of that workflow ID.
// - taskList can be default(empty string). If empty string then it will pick the taskList of the running execution of that workflow ID.
// - queryType is the type of the query.
// - args... are the optional query parameters.
// The errors it can return:
//  - BadRequestError
//  - InternalServiceError
//  - EntityNotExistError
//  - QueryFailError
func (wc *workflowClient) QueryWorkflow(ctx context.Context, workflowID string, runID string, queryType string, args ...interface{}) (Value, error) {
	queryWorkflowWithOptionsRequest := &QueryWorkflowWithOptionsRequest{
		WorkflowID: workflowID,
		RunID:      runID,
		QueryType:  queryType,
		Args:       args,
	}
	result, err := wc.QueryWorkflowWithOptions(ctx, queryWorkflowWithOptionsRequest)
	if err != nil {
		return nil, err
	}
	return result.QueryResult, nil
}

// QueryWorkflowWithOptionsRequest is the request to QueryWorkflowWithOptions
type QueryWorkflowWithOptionsRequest struct {
	// WorkflowID is a required field indicating the workflow which should be queried.
	WorkflowID string

	// RunID is an optional field used to identify a specific run of the queried workflow.
	// If RunID is not provided the latest run will be used.
	RunID string

	// QueryType is a required field which specifies the query you want to run.
	// By default, cadence supports "__stack_trace" as a standard query type, which will return string value
	// representing the call stack of the target workflow. The target workflow could also setup different query handler to handle custom query types.
	// See comments at workflow.SetQueryHandler(ctx Context, queryType string, handler interface{}) for more details on how to setup query handler within the target workflow.
	QueryType string

	// Args is an optional field used to identify the arguments passed to the query.
	Args []interface{}

	// QueryRejectCondition is an optional field used to reject queries based on workflow state.
	// QueryRejectConditionNotOpen will reject queries to workflows which are not open
	// QueryRejectConditionNotCompletedCleanly will reject queries to workflows which completed in any state other than completed (e.g. terminated, canceled timeout etc...)
	QueryRejectCondition apiv1.QueryRejectCondition

	// QueryConsistencyLevel is an optional field used to control the consistency level.
	// QueryConsistencyLevelEventual means that query will eventually reflect up to date state of a workflow.
	// QueryConsistencyLevelStrong means that query will reflect a workflow state of having applied all events which came before the query.
	QueryConsistencyLevel apiv1.QueryConsistencyLevel
}

// QueryWorkflowWithOptionsResponse is the response to QueryWorkflowWithOptions
type QueryWorkflowWithOptionsResponse struct {
	// QueryResult contains the result of executing the query.
	// This will only be set if the query was completed successfully and not rejected.
	QueryResult Value

	// QueryRejected contains information about the query rejection.
	QueryRejected *apiv1.QueryRejected
}

// QueryWorkflowWithOptions queries a given workflow execution and returns the query result synchronously.
// See QueryWorkflowWithOptionsRequest and QueryWorkflowWithOptionsResult for more information.
// The errors it can return:
//  - BadRequestError
//  - InternalServiceError
//  - EntityNotExistError
//  - QueryFailError
func (wc *workflowClient) QueryWorkflowWithOptions(ctx context.Context, request *QueryWorkflowWithOptionsRequest) (*QueryWorkflowWithOptionsResponse, error) {
	var input []byte
	if len(request.Args) > 0 {
		var err error
		if input, err = encodeArgs(wc.dataConverter, request.Args); err != nil {
			return nil, err
		}
	}
	req := &apiv1.QueryWorkflowRequest{
		Domain: wc.domain,
		WorkflowExecution: &apiv1.WorkflowExecution{
			WorkflowId: request.WorkflowID,
			RunId:      request.RunID,
		},
		Query: &apiv1.WorkflowQuery{
			QueryType: request.QueryType,
			QueryArgs: &apiv1.Payload{Data: input},
		},
		QueryRejectCondition:  request.QueryRejectCondition,
		QueryConsistencyLevel: request.QueryConsistencyLevel,
	}

	var resp *apiv1.QueryWorkflowResponse
	err := backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContextForQuery(ctx)
			defer cancel()
			var err error
			resp, err = wc.workflowService.QueryWorkflow(tchCtx, req, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}

	if resp.QueryRejected != nil {
		return &QueryWorkflowWithOptionsResponse{
			QueryRejected: resp.QueryRejected,
			QueryResult:   nil,
		}, nil
	}
	return &QueryWorkflowWithOptionsResponse{
		QueryRejected: nil,
		QueryResult:   newEncodedValue(resp.QueryResult.GetData(), wc.dataConverter),
	}, nil
}

// DescribeTaskList returns information about the target tasklist, right now this API returns the
// pollers which polled this tasklist in last few minutes.
// - tasklist name of tasklist
// - tasklistType type of tasklist, can be decision or activity
// The errors it can return:
//  - BadRequestError
//  - InternalServiceError
//  - EntityNotExistError
func (wc *workflowClient) DescribeTaskList(ctx context.Context, tasklist string, tasklistType apiv1.TaskListType) (*apiv1.DescribeTaskListResponse, error) {
	request := &apiv1.DescribeTaskListRequest{
		Domain:       wc.domain,
		TaskList:     &apiv1.TaskList{Name: tasklist},
		TaskListType: tasklistType,
	}

	var resp *apiv1.DescribeTaskListResponse
	err := backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			var err error
			resp, err = wc.workflowService.DescribeTaskList(tchCtx, request, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (wc *workflowClient) getWorkflowHeader(ctx context.Context) *apiv1.Header {
	header := &apiv1.Header{
		Fields: make(map[string]*apiv1.Payload),
	}
	writer := NewHeaderWriter(header)
	for _, ctxProp := range wc.contextPropagators {
		ctxProp.Inject(ctx, writer)
	}
	return header
}

// Register a domain with cadence server
// The errors it can throw:
//	- DomainAlreadyExistsError
//	- BadRequestError
//	- InternalServiceError
func (dc *domainClient) Register(ctx context.Context, request *apiv1.RegisterDomainRequest) error {
	return backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			_, err := dc.workflowService.RegisterDomain(tchCtx, request, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
}

// Describe a domain. The domain has 3 part of information
// DomainInfo - Which has Name, Status, Description, Owner Email
// DomainConfiguration - Configuration like Workflow Execution Retention Period In Days, Whether to emit metrics.
// ReplicationConfiguration - replication config like clusters and active cluster name
// The errors it can throw:
//	- EntityNotExistsError
//	- BadRequestError
//	- InternalServiceError
func (dc *domainClient) Describe(ctx context.Context, name string) (*apiv1.DescribeDomainResponse, error) {
	request := &apiv1.DescribeDomainRequest{
		DescribeBy: &apiv1.DescribeDomainRequest_Name{Name: name},
	}

	var response *apiv1.DescribeDomainResponse
	err := backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			var err error
			response, err = dc.workflowService.DescribeDomain(tchCtx, request, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// Update a domain.
// The errors it can throw:
//	- EntityNotExistsError
//	- BadRequestError
//	- InternalServiceError
func (dc *domainClient) Update(ctx context.Context, request *apiv1.UpdateDomainRequest) error {
	return backoff.Retry(ctx,
		func() error {
			tchCtx, cancel, opt := newChannelContext(ctx)
			defer cancel()
			_, err := dc.workflowService.UpdateDomain(tchCtx, request, opt...)
			return api.ConvertError(err)
		}, createDynamicServiceRetryPolicy(ctx), isServiceTransientError)
}

func getRunID(runID string) *string {
	if runID == "" {
		// Cadence Server will pick current runID if provided empty.
		return nil
	}
	return common.StringPtr(runID)
}

func (iter *historyEventIteratorImpl) HasNext() bool {
	if iter.nextEventIndex < len(iter.events) || iter.err != nil {
		return true
	} else if !iter.initialized || len(iter.nexttoken) != 0 {
		iter.initialized = true
		response, err := iter.paginate(iter.nexttoken)
		iter.nextEventIndex = 0
		if err == nil {
			iter.events = response.History.Events
			iter.nexttoken = response.NextPageToken
			iter.err = nil
		} else {
			iter.events = nil
			iter.nexttoken = nil
			iter.err = err
		}

		if iter.nextEventIndex < len(iter.events) || iter.err != nil {
			return true
		}
		return false
	}

	return false
}

func (iter *historyEventIteratorImpl) Next() (*apiv1.HistoryEvent, error) {
	// if caller call the Next() when iteration is over, just return nil, nil
	if !iter.HasNext() {
		panic("HistoryEventIterator Next() called without checking HasNext()")
	}

	// we have cached events
	if iter.nextEventIndex < len(iter.events) {
		index := iter.nextEventIndex
		iter.nextEventIndex++
		return iter.events[index], nil
	} else if iter.err != nil {
		// we have err, clear that iter.err and return err
		err := iter.err
		iter.err = nil
		return nil, err
	}

	panic("HistoryEventIterator Next() should return either a history event or a err")
}

func (workflowRun *workflowRunImpl) GetRunID() string {
	return workflowRun.firstRunID
}

func (workflowRun *workflowRunImpl) GetID() string {
	return workflowRun.workflowID
}

func (workflowRun *workflowRunImpl) Get(ctx context.Context, valuePtr interface{}) error {

	iter := workflowRun.iterFn(ctx, workflowRun.currentRunID)
	if !iter.HasNext() {
		panic("could not get last history event for workflow")
	}
	closeEvent, err := iter.Next()
	if err != nil {
		return err
	}

	switch attr := closeEvent.Attributes.(type){
	case *apiv1.HistoryEvent_WorkflowExecutionCompletedEventAttributes:
		attributes := attr.WorkflowExecutionCompletedEventAttributes
		if valuePtr == nil || attributes.Result == nil {
			return nil
		}
		rf := reflect.ValueOf(valuePtr)
		if rf.Type().Kind() != reflect.Ptr {
			return errors.New("value parameter is not a pointer")
		}
		err = deSerializeFunctionResult(workflowRun.workflowFn, attributes.Result.GetData(), valuePtr, workflowRun.dataConverter, workflowRun.registry)
	case *apiv1.HistoryEvent_WorkflowExecutionFailedEventAttributes:
		attributes := attr.WorkflowExecutionFailedEventAttributes
		err = constructError(attributes.Failure.GetReason(), attributes.Failure.GetDetails(), workflowRun.dataConverter)
	case *apiv1.HistoryEvent_WorkflowExecutionCanceledEventAttributes:
		attributes := attr.WorkflowExecutionCanceledEventAttributes
		details := newEncodedValues(attributes.Details.GetData(), workflowRun.dataConverter)
		err = NewCanceledError(details)
	case *apiv1.HistoryEvent_WorkflowExecutionTerminatedEventAttributes:
		err = newTerminatedError()
	case *apiv1.HistoryEvent_WorkflowExecutionTimedOutEventAttributes:
		attributes := attr.WorkflowExecutionTimedOutEventAttributes
		err = NewTimeoutError(attributes.GetTimeoutType())
	case *apiv1.HistoryEvent_WorkflowExecutionContinuedAsNewEventAttributes:
		attributes := attr.WorkflowExecutionContinuedAsNewEventAttributes
		workflowRun.currentRunID = attributes.GetNewExecutionRunId()
		return workflowRun.Get(ctx, valuePtr)
	default:
		err = fmt.Errorf("Unexpected event type %s when handling workflow execution result", util.GetHistoryEventType(closeEvent))
	}
	return err
}

func getWorkflowMemo(input map[string]interface{}, dc DataConverter) (*apiv1.Memo, error) {
	if input == nil {
		return nil, nil
	}

	memo := make(map[string]*apiv1.Payload)
	for k, v := range input {
		memoBytes, err := encodeArg(dc, v)
		if err != nil {
			return nil, fmt.Errorf("encode workflow memo error: %v", err.Error())
		}
		memo[k] = &apiv1.Payload{Data: memoBytes}
	}
	return &apiv1.Memo{Fields: memo}, nil
}

func serializeSearchAttributes(input map[string]interface{}) (*apiv1.SearchAttributes, error) {
	if input == nil {
		return nil, nil
	}

	attr := make(map[string]*apiv1.Payload)
	for k, v := range input {
		attrBytes, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encode search attribute [%s] error: %v", k, err)
		}
		attr[k] = &apiv1.Payload{Data: attrBytes}
	}
	return &apiv1.SearchAttributes{IndexedFields: attr}, nil
}
