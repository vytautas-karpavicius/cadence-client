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

// All code in this file is private to the package.

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gogo/protobuf/types"
	"github.com/opentracing/opentracing-go"
	"github.com/uber-go/tally"
	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/cadence/v2/internal/api"
	"go.uber.org/cadence/v2/internal/common/metrics"
	"go.uber.org/cadence/v2/internal/common/util"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	queryResultSizeLimit = 2000000 // 2MB
)

// Make sure that interfaces are implemented
var _ workflowEnvironment = (*workflowEnvironmentImpl)(nil)
var _ workflowExecutionEventHandler = (*workflowExecutionEventHandlerImpl)(nil)

type (
	// completionHandler Handler to indicate completion result
	completionHandler func(result []byte, err error)

	// workflowExecutionEventHandlerImpl handler to handle workflowExecutionEventHandler
	workflowExecutionEventHandlerImpl struct {
		*workflowEnvironmentImpl
		workflowDefinition workflowDefinition
	}

	scheduledTimer struct {
		callback resultHandler
		handled  bool
	}

	scheduledActivity struct {
		callback             resultHandler
		waitForCancelRequest bool
		handled              bool
	}

	scheduledChildWorkflow struct {
		resultCallback      resultHandler
		startedCallback     func(r WorkflowExecution, e error)
		waitForCancellation bool
		handled             bool
	}

	scheduledCancellation struct {
		callback resultHandler
		handled  bool
	}

	scheduledSignal struct {
		callback resultHandler
		handled  bool
	}

	// workflowEnvironmentImpl an implementation of workflowEnvironment represents a environment for workflow execution.
	workflowEnvironmentImpl struct {
		workflowInfo *WorkflowInfo

		decisionsHelper   *decisionsHelper
		sideEffectResult  map[int32][]byte
		changeVersions    map[string]Version
		pendingLaTasks    map[string]*localActivityTask
		mutableSideEffect map[string][]byte
		unstartedLaTasks  map[string]struct{}
		openSessions      map[string]*SessionInfo

		counterID         int32     // To generate sequence IDs for activity/timer etc.
		currentReplayTime time.Time // Indicates current replay time of the decision.
		currentLocalTime  time.Time // Local time when currentReplayTime was updated.

		completeHandler completionHandler               // events completion handler
		cancelHandler   func()                          // A cancel handler to be invoked on a cancel notification
		signalHandler   func(name string, input []byte) // A signal handler to be invoked on a signal event
		queryHandler    func(queryType string, queryArgs []byte) ([]byte, error)

		logger                *zap.Logger
		isReplay              bool // flag to indicate if workflow is in replay mode
		enableLoggingInReplay bool // flag to indicate if workflow should enable logging in replay mode

		metricsScope         tally.Scope
		registry             *registry
		dataConverter        DataConverter
		contextPropagators   []ContextPropagator
		tracer               opentracing.Tracer
		workflowInterceptors []WorkflowInterceptorFactory
	}

	localActivityTask struct {
		sync.Mutex
		workflowTask *workflowTask
		activityID   string
		params       *executeLocalActivityParams
		callback     laResultHandler
		wc           *workflowExecutionContextImpl
		canceled     bool
		cancelFunc   func()
		attempt      int32 // attempt starting from 0
		retryPolicy  *RetryPolicy
		expireTime   time.Time
		header       *apiv1.Header
	}

	locallyDispatchedActivityTask struct {
		// used to notify the poller the response from server is completed and the task is ready
		readyCh                    chan bool
		TaskToken                  []byte
		WorkflowExecution          *apiv1.WorkflowExecution
		ActivityId                 string
		ActivityType               *apiv1.ActivityType
		Input                      *apiv1.Payload
		ScheduledTime              *types.Timestamp
		ScheduleToCloseTimeout     *types.Duration
		StartedTime                *types.Timestamp
		StartToCloseTimeout        *types.Duration
		HeartbeatTimeout           *types.Duration
		ScheduledTimeOfThisAttempt *types.Timestamp
		WorkflowType               *apiv1.WorkflowType
		WorkflowDomain             string
		Header                     *apiv1.Header
	}

	localActivityMarkerData struct {
		ActivityID   string        `json:"activityId,omitempty"`
		ActivityType string        `json:"activityType,omitempty"`
		ErrReason    string        `json:"errReason,omitempty"`
		ErrJSON      string        `json:"errJson,omitempty"` // string instead of []byte so the encoded blob is human readable
		ResultJSON   string        `json:"resultJson,omitempty"`
		ReplayTime   time.Time     `json:"replayTime,omitempty"`
		Attempt      int32         `json:"attempt,omitempty"` // record attempt, starting from 0.
		Backoff      time.Duration `json:"backoff,omitempty"` // retry backoff duration
	}

	// wrapper around zapcore.Core that will be aware of replay
	replayAwareZapCore struct {
		zapcore.Core
		isReplay              *bool // pointer to bool that indicate if it is in replay mode
		enableLoggingInReplay *bool // pointer to bool that indicate if logging is enabled in replay mode
	}
)

func wrapLogger(isReplay *bool, enableLoggingInReplay *bool) func(zapcore.Core) zapcore.Core {
	return func(c zapcore.Core) zapcore.Core {
		return &replayAwareZapCore{c, isReplay, enableLoggingInReplay}
	}
}

func (c *replayAwareZapCore) Check(entry zapcore.Entry, checkedEntry *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if *c.isReplay && !*c.enableLoggingInReplay {
		return checkedEntry
	}
	return c.Core.Check(entry, checkedEntry)
}

func (c *replayAwareZapCore) With(fields []zapcore.Field) zapcore.Core {
	coreWithFields := c.Core.With(fields)
	return &replayAwareZapCore{coreWithFields, c.isReplay, c.enableLoggingInReplay}
}

func newWorkflowExecutionEventHandler(
	workflowInfo *WorkflowInfo,
	completeHandler completionHandler,
	logger *zap.Logger,
	enableLoggingInReplay bool,
	scope tally.Scope,
	registry *registry,
	dataConverter DataConverter,
	contextPropagators []ContextPropagator,
	tracer opentracing.Tracer,
	workflowInterceptors []WorkflowInterceptorFactory,
) workflowExecutionEventHandler {
	context := &workflowEnvironmentImpl{
		workflowInfo:          workflowInfo,
		decisionsHelper:       newDecisionsHelper(),
		sideEffectResult:      make(map[int32][]byte),
		mutableSideEffect:     make(map[string][]byte),
		changeVersions:        make(map[string]Version),
		pendingLaTasks:        make(map[string]*localActivityTask),
		unstartedLaTasks:      make(map[string]struct{}),
		openSessions:          make(map[string]*SessionInfo),
		completeHandler:       completeHandler,
		enableLoggingInReplay: enableLoggingInReplay,
		registry:              registry,
		dataConverter:         dataConverter,
		contextPropagators:    contextPropagators,
		tracer:                tracer,
		workflowInterceptors:  workflowInterceptors,
	}
	context.logger = logger.With(
		zapcore.Field{Key: tagWorkflowType, Type: zapcore.StringType, String: workflowInfo.WorkflowType.Name},
		zapcore.Field{Key: tagWorkflowID, Type: zapcore.StringType, String: workflowInfo.WorkflowExecution.ID},
		zapcore.Field{Key: tagRunID, Type: zapcore.StringType, String: workflowInfo.WorkflowExecution.RunID},
	).WithOptions(zap.WrapCore(wrapLogger(&context.isReplay, &context.enableLoggingInReplay)))

	if scope != nil {
		context.metricsScope = tagScope(metrics.WrapScope(&context.isReplay, scope, context),
			tagWorkflowType, workflowInfo.WorkflowType.Name)
	}

	return &workflowExecutionEventHandlerImpl{context, nil}
}

func (s *scheduledTimer) handle(result []byte, err error) {
	if s.handled {
		panic(fmt.Sprintf("timer already handled %v", s))
	}
	s.handled = true
	s.callback(result, err)
}

func (s *scheduledActivity) handle(result []byte, err error) {
	if s.handled {
		panic(fmt.Sprintf("activity already handled %v", s))
	}
	s.handled = true
	s.callback(result, err)
}

func (s *scheduledChildWorkflow) handle(result []byte, err error) {
	if s.handled {
		panic(fmt.Sprintf("child workflow already handled %v", s))
	}
	s.handled = true
	s.resultCallback(result, err)
}

func (t *localActivityTask) cancel() {
	t.Lock()
	t.canceled = true
	if t.cancelFunc != nil {
		t.cancelFunc()
	}
	t.Unlock()
}

func (s *scheduledCancellation) handle(result []byte, err error) {
	if s.handled {
		panic(fmt.Sprintf("cancellation already handled %v", s))
	}
	s.handled = true
	s.callback(result, err)
}

func (s *scheduledSignal) handle(result []byte, err error) {
	if s.handled {
		panic(fmt.Sprintf("signal already handled %v", s))
	}
	s.handled = true
	s.callback(result, err)
}

func (wc *workflowEnvironmentImpl) WorkflowInfo() *WorkflowInfo {
	return wc.workflowInfo
}

func (wc *workflowEnvironmentImpl) Complete(result []byte, err error) {
	wc.completeHandler(result, err)
}

func (wc *workflowEnvironmentImpl) RequestCancelChildWorkflow(domainName string, workflowID string) {
	// For cancellation of child workflow only, we do not use cancellation ID and run ID
	isChildWorkflowOnly := true
	cancellationID := ""
	runID := ""
	wc.decisionsHelper.requestCancelExternalWorkflowExecution(domainName, workflowID, runID, cancellationID, isChildWorkflowOnly)
}

func (wc *workflowEnvironmentImpl) RequestCancelExternalWorkflow(domainName, workflowID, runID string, callback resultHandler) {
	// for cancellation of external workflow, we have to use cancellation ID and set isChildWorkflowOnly to false
	isChildWorkflowOnly := false
	cancellationID := wc.GenerateSequenceID()
	decision := wc.decisionsHelper.requestCancelExternalWorkflowExecution(domainName, workflowID, runID, cancellationID, isChildWorkflowOnly)
	decision.setData(&scheduledCancellation{callback: callback})
}

func (wc *workflowEnvironmentImpl) SignalExternalWorkflow(domainName, workflowID, runID, signalName string,
	input []byte, _ /* THIS IS FOR TEST FRAMEWORK. DO NOT USE HERE. */ interface{}, childWorkflowOnly bool, callback resultHandler) {

	signalID := wc.GenerateSequenceID()
	decision := wc.decisionsHelper.signalExternalWorkflowExecution(domainName, workflowID, runID, signalName, input, signalID, childWorkflowOnly)
	decision.setData(&scheduledSignal{callback: callback})
}

func (wc *workflowEnvironmentImpl) UpsertSearchAttributes(attributes map[string]interface{}) error {
	// This has to be used in workflowEnvironment implementations instead of in Workflow for testsuite mock purpose.
	attr, err := validateAndSerializeSearchAttributes(attributes)
	if err != nil {
		return err
	}

	var upsertID string
	if changeVersion, ok := attributes[CadenceChangeVersion]; ok {
		// to ensure backward compatibility on searchable GetVersion, use latest changeVersion as upsertID
		upsertID = changeVersion.([]string)[0]
	} else {
		upsertID = wc.GenerateSequenceID()
	}

	wc.decisionsHelper.upsertSearchAttributes(upsertID, attr)
	wc.updateWorkflowInfoWithSearchAttributes(attr) // this is for getInfo correctness
	return nil
}

func (wc *workflowEnvironmentImpl) updateWorkflowInfoWithSearchAttributes(attributes *apiv1.SearchAttributes) {
	wc.workflowInfo.SearchAttributes = mergeSearchAttributes(wc.workflowInfo.SearchAttributes, attributes)
}

func mergeSearchAttributes(current, upsert *apiv1.SearchAttributes) *apiv1.SearchAttributes {
	if current == nil || len(current.IndexedFields) == 0 {
		if upsert == nil || len(upsert.IndexedFields) == 0 {
			return nil
		}
		current = &apiv1.SearchAttributes{
			IndexedFields: make(map[string]*apiv1.Payload),
		}
	}

	fields := current.IndexedFields
	for k, v := range upsert.IndexedFields {
		fields[k] = v
	}
	return current
}

func validateAndSerializeSearchAttributes(attributes map[string]interface{}) (*apiv1.SearchAttributes, error) {
	if len(attributes) == 0 {
		return nil, errSearchAttributesNotSet
	}
	attr, err := serializeSearchAttributes(attributes)
	if err != nil {
		return nil, err
	}
	return attr, nil
}

func (wc *workflowEnvironmentImpl) RegisterCancelHandler(handler func()) {
	wc.cancelHandler = handler
}

func (wc *workflowEnvironmentImpl) ExecuteChildWorkflow(
	params executeWorkflowParams, callback resultHandler, startedHandler func(r WorkflowExecution, e error)) error {
	if params.workflowID == "" {
		params.workflowID = wc.workflowInfo.WorkflowExecution.RunID + "_" + wc.GenerateSequenceID()
	}
	memo, err := getWorkflowMemo(params.memo, wc.dataConverter)
	if err != nil {
		return err
	}
	searchAttr, err := serializeSearchAttributes(params.searchAttributes)
	if err != nil {
		return err
	}

	attributes := &apiv1.StartChildWorkflowExecutionDecisionAttributes{
		Domain:                       *params.domain,
		TaskList:                     &apiv1.TaskList{Name: *params.taskListName},
		WorkflowId:                   params.workflowID,
		ExecutionStartToCloseTimeout: api.SecondsToProto(*params.executionStartToCloseTimeoutSeconds),
		TaskStartToCloseTimeout:      api.SecondsToProto(*params.taskStartToCloseTimeoutSeconds),
		Input:                        &apiv1.Payload{Data: params.input},
		WorkflowType:                 &apiv1.WorkflowType{Name: params.workflowType.Name},
		WorkflowIdReusePolicy:        params.workflowIDReusePolicy.toProto(),
		ParentClosePolicy:            params.parentClosePolicy.toProto(),
		RetryPolicy:                  params.retryPolicy,
		Header:                       params.header,
		Memo:                         memo,
		SearchAttributes:             searchAttr,
		CronSchedule:                 params.cronSchedule,
	}

	decision := wc.decisionsHelper.startChildWorkflowExecution(attributes)
	decision.setData(&scheduledChildWorkflow{
		resultCallback:      callback,
		startedCallback:     startedHandler,
		waitForCancellation: params.waitForCancellation,
	})

	wc.logger.Debug("ExecuteChildWorkflow",
		zap.String(tagChildWorkflowID, params.workflowID),
		zap.String(tagWorkflowType, params.workflowType.Name))

	return nil
}

func (wc *workflowEnvironmentImpl) RegisterSignalHandler(handler func(name string, input []byte)) {
	wc.signalHandler = handler
}

func (wc *workflowEnvironmentImpl) RegisterQueryHandler(handler func(string, []byte) ([]byte, error)) {
	wc.queryHandler = handler
}

func (wc *workflowEnvironmentImpl) GetLogger() *zap.Logger {
	return wc.logger
}

func (wc *workflowEnvironmentImpl) GetMetricsScope() tally.Scope {
	return wc.metricsScope
}

func (wc *workflowEnvironmentImpl) GetDataConverter() DataConverter {
	return wc.dataConverter
}

func (wc *workflowEnvironmentImpl) GetContextPropagators() []ContextPropagator {
	return wc.contextPropagators
}

func (wc *workflowEnvironmentImpl) IsReplaying() bool {
	return wc.isReplay
}

func (wc *workflowEnvironmentImpl) GenerateSequenceID() string {
	return fmt.Sprintf("%d", wc.GenerateSequence())
}

func (wc *workflowEnvironmentImpl) GenerateSequence() int32 {
	result := wc.counterID
	wc.counterID++
	return result
}

func (wc *workflowEnvironmentImpl) ExecuteActivity(parameters executeActivityParams, callback resultHandler) *activityInfo {
	scheduleTaskAttr := &apiv1.ScheduleActivityTaskDecisionAttributes{}
	if parameters.ActivityID == nil || *parameters.ActivityID == "" {
		scheduleTaskAttr.ActivityId = wc.GenerateSequenceID()
	} else {
		scheduleTaskAttr.ActivityId = *parameters.ActivityID
	}
	activityID := scheduleTaskAttr.GetActivityId()
	scheduleTaskAttr.ActivityType = &apiv1.ActivityType{Name: parameters.ActivityType.Name}
	scheduleTaskAttr.TaskList = &apiv1.TaskList{Name: parameters.TaskListName}
	scheduleTaskAttr.Input = &apiv1.Payload{Data: parameters.Input}
	scheduleTaskAttr.ScheduleToCloseTimeout = api.SecondsToProto(parameters.ScheduleToCloseTimeoutSeconds)
	scheduleTaskAttr.StartToCloseTimeout = api.SecondsToProto(parameters.StartToCloseTimeoutSeconds)
	scheduleTaskAttr.ScheduleToStartTimeout = api.SecondsToProto(parameters.ScheduleToStartTimeoutSeconds)
	scheduleTaskAttr.HeartbeatTimeout = api.SecondsToProto(parameters.HeartbeatTimeoutSeconds)
	scheduleTaskAttr.RetryPolicy = parameters.RetryPolicy
	scheduleTaskAttr.Header = parameters.Header

	decision := wc.decisionsHelper.scheduleActivityTask(scheduleTaskAttr)
	decision.setData(&scheduledActivity{
		callback:             callback,
		waitForCancelRequest: parameters.WaitForCancellation,
	})

	wc.logger.Debug("ExecuteActivity",
		zap.String(tagActivityID, activityID),
		zap.String(tagActivityType, scheduleTaskAttr.ActivityType.GetName()))

	return &activityInfo{activityID: activityID}
}

func (wc *workflowEnvironmentImpl) RequestCancelActivity(activityID string) {
	decision := wc.decisionsHelper.requestCancelActivityTask(activityID)
	activity := decision.getData().(*scheduledActivity)
	if activity.handled {
		return
	}

	if decision.isDone() || !activity.waitForCancelRequest {
		activity.handle(nil, ErrCanceled)
	}

	wc.logger.Debug("RequestCancelActivity", zap.String(tagActivityID, activityID))
}

func (wc *workflowEnvironmentImpl) ExecuteLocalActivity(params executeLocalActivityParams, callback laResultHandler) *localActivityInfo {
	activityID := wc.GenerateSequenceID()
	task := newLocalActivityTask(params, callback, activityID)

	wc.pendingLaTasks[activityID] = task
	wc.unstartedLaTasks[activityID] = struct{}{}
	return &localActivityInfo{activityID: activityID}
}

func newLocalActivityTask(params executeLocalActivityParams, callback laResultHandler, activityID string) *localActivityTask {
	task := &localActivityTask{
		activityID:  activityID,
		params:      &params,
		callback:    callback,
		retryPolicy: params.RetryPolicy,
		attempt:     params.Attempt,
		header:      params.Header,
	}

	if params.RetryPolicy != nil && params.RetryPolicy.ExpirationInterval > 0 {
		task.expireTime = params.ScheduledTime.Add(params.RetryPolicy.ExpirationInterval)
	}
	return task
}

func (wc *workflowEnvironmentImpl) RequestCancelLocalActivity(activityID string) {
	if task, ok := wc.pendingLaTasks[activityID]; ok {
		task.cancel()
	}
}

func (wc *workflowEnvironmentImpl) SetCurrentReplayTime(replayTime time.Time) {
	if replayTime.Before(wc.currentReplayTime) {
		return
	}
	wc.currentReplayTime = replayTime
	wc.currentLocalTime = time.Now()
}

func (wc *workflowEnvironmentImpl) Now() time.Time {
	return wc.currentReplayTime
}

func (wc *workflowEnvironmentImpl) NewTimer(d time.Duration, callback resultHandler) *timerInfo {
	if d < 0 {
		callback(nil, fmt.Errorf("negative duration provided %v", d))
		return nil
	}
	if d == 0 {
		callback(nil, nil)
		return nil
	}

	timerID := wc.GenerateSequenceID()
	startTimerAttr := &apiv1.StartTimerDecisionAttributes{
		TimerId: timerID,
		StartToFireTimeout: api.DurationToProto(d),
	}

	decision := wc.decisionsHelper.startTimer(startTimerAttr)
	decision.setData(&scheduledTimer{callback: callback})

	wc.logger.Debug("NewTimer",
		zap.String(tagTimerID, startTimerAttr.GetTimerId()),
		zap.Duration("Duration", d))

	return &timerInfo{timerID: timerID}
}

func (wc *workflowEnvironmentImpl) RequestCancelTimer(timerID string) {
	decision := wc.decisionsHelper.cancelTimer(timerID)
	timer := decision.getData().(*scheduledTimer)
	if timer.handled {
		return
	}
	timer.handle(nil, ErrCanceled)
	wc.logger.Debug("RequestCancelTimer", zap.String(tagTimerID, timerID))
}

func validateVersion(changeID string, version, minSupported, maxSupported Version) {
	if version < minSupported {
		panic(fmt.Sprintf("Workflow code removed support of version %v. "+
			"for \"%v\" changeID. The oldest supported version is %v",
			version, changeID, minSupported))
	}
	if version > maxSupported {
		panic(fmt.Sprintf("Workflow code is too old to support version %v "+
			"for \"%v\" changeID. The maximum supported version is %v",
			version, changeID, maxSupported))
	}
}

func (wc *workflowEnvironmentImpl) GetVersion(changeID string, minSupported, maxSupported Version) Version {
	if version, ok := wc.changeVersions[changeID]; ok {
		validateVersion(changeID, version, minSupported, maxSupported)
		return version
	}

	var version Version
	if wc.isReplay {
		// GetVersion for changeID is called first time in replay mode, use DefaultVersion
		version = DefaultVersion
	} else {
		// GetVersion for changeID is called first time (non-replay mode), generate a marker decision for it.
		// Also upsert search attributes to enable ability to search by changeVersion.
		version = maxSupported
		wc.decisionsHelper.recordVersionMarker(changeID, version, wc.GetDataConverter())
		wc.UpsertSearchAttributes(createSearchAttributesForChangeVersion(changeID, version, wc.changeVersions))
	}

	validateVersion(changeID, version, minSupported, maxSupported)
	wc.changeVersions[changeID] = version
	return version
}

func createSearchAttributesForChangeVersion(changeID string, version Version, existingChangeVersions map[string]Version) map[string]interface{} {
	return map[string]interface{}{
		CadenceChangeVersion: getChangeVersions(changeID, version, existingChangeVersions),
	}
}

func getChangeVersions(changeID string, version Version, existingChangeVersions map[string]Version) []string {
	res := []string{getChangeVersion(changeID, version)}
	for k, v := range existingChangeVersions {
		res = append(res, getChangeVersion(k, v))
	}
	return res
}

func getChangeVersion(changeID string, version Version) string {
	return fmt.Sprintf("%s-%v", changeID, version)
}

func (wc *workflowEnvironmentImpl) SideEffect(f func() ([]byte, error), callback resultHandler) {
	sideEffectID := wc.GenerateSequence()
	var details []byte
	var result []byte
	if wc.isReplay {
		var ok bool
		result, ok = wc.sideEffectResult[sideEffectID]
		if !ok {
			keys := make([]int32, 0, len(wc.sideEffectResult))
			for k := range wc.sideEffectResult {
				keys = append(keys, k)
			}
			panic(fmt.Sprintf("No cached result found for side effectID=%v. KnownSideEffects=%v",
				sideEffectID, keys))
		}
		wc.logger.Debug("SideEffect returning already calculated result.",
			zap.Int32(tagSideEffectID, sideEffectID))
		details = result
	} else {
		var err error
		result, err = f()
		if err != nil {
			callback(result, err)
			return
		}
		details, err = encodeArgs(wc.GetDataConverter(), []interface{}{sideEffectID, result})
		if err != nil {
			callback(nil, fmt.Errorf("failure encoding sideEffectID: %v", err))
			return
		}
	}

	wc.decisionsHelper.recordSideEffectMarker(sideEffectID, details)

	callback(result, nil)
	wc.logger.Debug("SideEffect Marker added", zap.Int32(tagSideEffectID, sideEffectID))
}

func (wc *workflowEnvironmentImpl) MutableSideEffect(id string, f func() interface{}, equals func(a, b interface{}) bool) Value {
	if result, ok := wc.mutableSideEffect[id]; ok {
		encodedResult := newEncodedValue(result, wc.GetDataConverter())
		if wc.isReplay {
			return encodedResult
		}

		newValue := f()
		if wc.isEqualValue(newValue, result, equals) {
			return encodedResult
		}

		return wc.recordMutableSideEffect(id, wc.encodeValue(newValue))
	}

	if wc.isReplay {
		// This should not happen
		panic(fmt.Sprintf("Non deterministic workflow code change detected. MutableSideEffect API call doesn't have a correspondent event in the workflow history. MutableSideEffect ID: %s", id))
	}

	return wc.recordMutableSideEffect(id, wc.encodeValue(f()))
}

func (wc *workflowEnvironmentImpl) isEqualValue(newValue interface{}, encodedOldValue []byte, equals func(a, b interface{}) bool) bool {
	if newValue == nil {
		// new value is nil
		newEncodedValue := wc.encodeValue(nil)
		return bytes.Equal(newEncodedValue, encodedOldValue)
	}

	oldValue := decodeValue(newEncodedValue(encodedOldValue, wc.GetDataConverter()), newValue)
	return equals(newValue, oldValue)
}

func decodeValue(encodedValue Value, value interface{}) interface{} {
	// We need to decode oldValue out of encodedValue, first we need to prepare valuePtr as the same type as value
	valuePtr := reflect.New(reflect.TypeOf(value)).Interface()
	if err := encodedValue.Get(valuePtr); err != nil {
		panic(err)
	}
	decodedValue := reflect.ValueOf(valuePtr).Elem().Interface()
	return decodedValue
}

func (wc *workflowEnvironmentImpl) encodeValue(value interface{}) []byte {
	blob, err := wc.encodeArg(value)
	if err != nil {
		panic(err)
	}
	return blob
}

func (wc *workflowEnvironmentImpl) encodeArg(arg interface{}) ([]byte, error) {
	return wc.GetDataConverter().ToData(arg)
}

func (wc *workflowEnvironmentImpl) recordMutableSideEffect(id string, data []byte) Value {
	details, err := encodeArgs(wc.GetDataConverter(), []interface{}{id, string(data)})
	if err != nil {
		panic(err)
	}
	wc.decisionsHelper.recordMutableSideEffectMarker(id, details)
	wc.mutableSideEffect[id] = data
	return newEncodedValue(data, wc.GetDataConverter())
}

func (wc *workflowEnvironmentImpl) AddSession(sessionInfo *SessionInfo) {
	wc.openSessions[sessionInfo.SessionID] = sessionInfo
}

func (wc *workflowEnvironmentImpl) RemoveSession(sessionID string) {
	delete(wc.openSessions, sessionID)
}

func (wc *workflowEnvironmentImpl) getOpenSessions() []*SessionInfo {
	openSessions := make([]*SessionInfo, 0, len(wc.openSessions))
	for _, info := range wc.openSessions {
		openSessions = append(openSessions, info)
	}
	return openSessions
}

func (wc *workflowEnvironmentImpl) GetRegistry() *registry {
	return wc.registry
}

func (wc *workflowEnvironmentImpl) GetWorkflowInterceptors() []WorkflowInterceptorFactory {
	return wc.workflowInterceptors
}

func (weh *workflowExecutionEventHandlerImpl) ProcessEvent(
	event *apiv1.HistoryEvent,
	isReplay bool,
	isLast bool,
) (err error) {
	if event == nil {
		return errors.New("nil event provided")
	}
	defer func() {
		if p := recover(); p != nil {
			weh.metricsScope.Counter(metrics.DecisionTaskPanicCounter).Inc(1)
			topLine := fmt.Sprintf("process event for %s [panic]:", weh.workflowInfo.TaskListName)
			st := getStackTraceRaw(topLine, 7, 0)
			weh.logger.Error("ProcessEvent panic.",
				zap.String(tagPanicError, fmt.Sprintf("%v", p)),
				zap.String(tagPanicStack, st))

			weh.Complete(nil, newWorkflowPanicError(p, st))
		}
	}()

	weh.isReplay = isReplay
	traceLog(func() {
		weh.logger.Debug("ProcessEvent",
			zap.Int64(tagEventID, event.GetEventId()),
			zap.String(tagEventType, util.GetHistoryEventType(event)))
	})

	switch attr := event.Attributes.(type) {
	case *apiv1.HistoryEvent_WorkflowExecutionStartedEventAttributes:
		err = weh.handleWorkflowExecutionStarted(attr.WorkflowExecutionStartedEventAttributes)

	case *apiv1.HistoryEvent_WorkflowExecutionCompletedEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_WorkflowExecutionFailedEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_WorkflowExecutionTimedOutEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_DecisionTaskScheduledEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_DecisionTaskStartedEventAttributes:
		// Set replay clock.
		weh.SetCurrentReplayTime(api.TimeFromProto(event.GetEventTime()))
		weh.workflowDefinition.OnDecisionTaskStarted()

	case *apiv1.HistoryEvent_DecisionTaskTimedOutEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_DecisionTaskFailedEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_DecisionTaskCompletedEventAttributes:
		// No Operation
	case *apiv1.HistoryEvent_ActivityTaskScheduledEventAttributes:
		weh.decisionsHelper.handleActivityTaskScheduled(
			event.GetEventId(), attr.ActivityTaskScheduledEventAttributes.GetActivityId())

	case *apiv1.HistoryEvent_ActivityTaskStartedEventAttributes:
		// No Operation

	case *apiv1.HistoryEvent_ActivityTaskCompletedEventAttributes:
		err = weh.handleActivityTaskCompleted(event)

	case *apiv1.HistoryEvent_ActivityTaskFailedEventAttributes:
		err = weh.handleActivityTaskFailed(event)

	case *apiv1.HistoryEvent_ActivityTaskTimedOutEventAttributes:
		err = weh.handleActivityTaskTimedOut(event)

	case *apiv1.HistoryEvent_ActivityTaskCancelRequestedEventAttributes:
		weh.decisionsHelper.handleActivityTaskCancelRequested(
			attr.ActivityTaskCancelRequestedEventAttributes.GetActivityId())

	case *apiv1.HistoryEvent_RequestCancelActivityTaskFailedEventAttributes:
		weh.decisionsHelper.handleRequestCancelActivityTaskFailed(
			attr.RequestCancelActivityTaskFailedEventAttributes.GetActivityId())

	case *apiv1.HistoryEvent_ActivityTaskCanceledEventAttributes:
		err = weh.handleActivityTaskCanceled(event)

	case *apiv1.HistoryEvent_TimerStartedEventAttributes:
		weh.decisionsHelper.handleTimerStarted(attr.TimerStartedEventAttributes.GetTimerId())

	case *apiv1.HistoryEvent_TimerFiredEventAttributes:
		weh.handleTimerFired(event)

	case *apiv1.HistoryEvent_TimerCanceledEventAttributes:
		weh.decisionsHelper.handleTimerCanceled(attr.TimerCanceledEventAttributes.GetTimerId())

	case *apiv1.HistoryEvent_CancelTimerFailedEventAttributes:
		weh.decisionsHelper.handleCancelTimerFailed(attr.CancelTimerFailedEventAttributes.GetTimerId())

	case *apiv1.HistoryEvent_WorkflowExecutionCancelRequestedEventAttributes:
		weh.handleWorkflowExecutionCancelRequested()

	case *apiv1.HistoryEvent_WorkflowExecutionCanceledEventAttributes:
		// No Operation.

	case *apiv1.HistoryEvent_RequestCancelExternalWorkflowExecutionInitiatedEventAttributes:
		weh.handleRequestCancelExternalWorkflowExecutionInitiated(event)

	case *apiv1.HistoryEvent_RequestCancelExternalWorkflowExecutionFailedEventAttributes:
		weh.handleRequestCancelExternalWorkflowExecutionFailed(event)

	case *apiv1.HistoryEvent_ExternalWorkflowExecutionCancelRequestedEventAttributes:
		weh.handleExternalWorkflowExecutionCancelRequested(event)

	case *apiv1.HistoryEvent_WorkflowExecutionContinuedAsNewEventAttributes:
		// No Operation.

	case *apiv1.HistoryEvent_WorkflowExecutionSignaledEventAttributes:
		weh.handleWorkflowExecutionSignaled(attr.WorkflowExecutionSignaledEventAttributes)

	case *apiv1.HistoryEvent_SignalExternalWorkflowExecutionInitiatedEventAttributes:
		signalID := string(attr.SignalExternalWorkflowExecutionInitiatedEventAttributes.Control)
		weh.decisionsHelper.handleSignalExternalWorkflowExecutionInitiated(event.GetEventId(), signalID)

	case *apiv1.HistoryEvent_SignalExternalWorkflowExecutionFailedEventAttributes:
		weh.handleSignalExternalWorkflowExecutionFailed(event)

	case *apiv1.HistoryEvent_ExternalWorkflowExecutionSignaledEventAttributes:
		weh.handleSignalExternalWorkflowExecutionCompleted(event)

	case *apiv1.HistoryEvent_MarkerRecordedEventAttributes:
		err = weh.handleMarkerRecorded(event.GetEventId(), attr.MarkerRecordedEventAttributes)

	case *apiv1.HistoryEvent_StartChildWorkflowExecutionInitiatedEventAttributes:
		weh.decisionsHelper.handleStartChildWorkflowExecutionInitiated(
			attr.StartChildWorkflowExecutionInitiatedEventAttributes.GetWorkflowId())

	case *apiv1.HistoryEvent_StartChildWorkflowExecutionFailedEventAttributes:
		err = weh.handleStartChildWorkflowExecutionFailed(event)

	case *apiv1.HistoryEvent_ChildWorkflowExecutionStartedEventAttributes:
		err = weh.handleChildWorkflowExecutionStarted(event)

	case *apiv1.HistoryEvent_ChildWorkflowExecutionCompletedEventAttributes:
		err = weh.handleChildWorkflowExecutionCompleted(event)

	case *apiv1.HistoryEvent_ChildWorkflowExecutionFailedEventAttributes:
		err = weh.handleChildWorkflowExecutionFailed(event)

	case *apiv1.HistoryEvent_ChildWorkflowExecutionCanceledEventAttributes:
		err = weh.handleChildWorkflowExecutionCanceled(event)

	case *apiv1.HistoryEvent_ChildWorkflowExecutionTimedOutEventAttributes:
		err = weh.handleChildWorkflowExecutionTimedOut(event)

	case *apiv1.HistoryEvent_ChildWorkflowExecutionTerminatedEventAttributes:
		err = weh.handleChildWorkflowExecutionTerminated(event)

	case *apiv1.HistoryEvent_UpsertWorkflowSearchAttributesEventAttributes:
		weh.handleUpsertWorkflowSearchAttributes(event)

	default:
		weh.logger.Error("unknown event type",
			zap.Int64(tagEventID, event.GetEventId()),
			zap.String(tagEventType, util.GetHistoryEventType(event)))
		// Do not fail to be forward compatible with new events
	}

	if err != nil {
		return err
	}

	// When replaying histories to get stack trace or current state the last event might be not
	// decision started. So always call OnDecisionTaskStarted on the last event.
	// Don't call for EventType_DecisionTaskStarted as it was already called when handling it.
	if isLast && event.GetDecisionTaskStartedEventAttributes() == nil {
		weh.workflowDefinition.OnDecisionTaskStarted()
	}

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) ProcessQuery(queryType string, queryArgs []byte) ([]byte, error) {
	switch queryType {
	case QueryTypeStackTrace:
		return weh.encodeArg(weh.StackTrace())
	case QueryTypeOpenSessions:
		return weh.encodeArg(weh.getOpenSessions())
	default:
		result, err := weh.queryHandler(queryType, queryArgs)
		if err != nil {
			return nil, err
		}

		resultSize := len(result)
		if resultSize > queryResultSizeLimit {
			weh.logger.Error("Query result size exceeds limit.",
				zap.String(tagQueryType, queryType),
				zap.String(tagWorkflowID, weh.workflowInfo.WorkflowExecution.ID),
				zap.String(tagRunID, weh.workflowInfo.WorkflowExecution.RunID))
			return nil, fmt.Errorf("query result size (%v) exceeds limit (%v)", resultSize, queryResultSizeLimit)
		}

		return result, nil
	}
}

func (weh *workflowExecutionEventHandlerImpl) StackTrace() string {
	return weh.workflowDefinition.StackTrace()
}

func (weh *workflowExecutionEventHandlerImpl) Close() {
	if weh.workflowDefinition != nil {
		weh.workflowDefinition.Close()
	}
}

func (weh *workflowExecutionEventHandlerImpl) handleWorkflowExecutionStarted(
	attributes *apiv1.WorkflowExecutionStartedEventAttributes) (err error) {
	weh.workflowDefinition, err = weh.registry.getWorkflowDefinition(
		weh.workflowInfo.WorkflowType,
	)
	if err != nil {
		return err
	}

	// Invoke the workflow.
	weh.workflowDefinition.Execute(weh, attributes.Header, attributes.Input.GetData())
	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleActivityTaskCompleted(event *apiv1.HistoryEvent) error {
	activityID := weh.decisionsHelper.getActivityID(event)
	decision := weh.decisionsHelper.handleActivityTaskClosed(activityID)
	activity := decision.getData().(*scheduledActivity)
	if activity.handled {
		return nil
	}
	activity.handle(event.GetActivityTaskCompletedEventAttributes().Result.GetData(), nil)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleActivityTaskFailed(event *apiv1.HistoryEvent) error {
	activityID := weh.decisionsHelper.getActivityID(event)
	decision := weh.decisionsHelper.handleActivityTaskClosed(activityID)
	activity := decision.getData().(*scheduledActivity)
	if activity.handled {
		return nil
	}

	attributes := event.GetActivityTaskFailedEventAttributes()
	err := constructError(attributes.Failure.GetReason(), attributes.Failure.GetDetails(), weh.GetDataConverter())
	activity.handle(nil, err)
	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleActivityTaskTimedOut(event *apiv1.HistoryEvent) error {
	activityID := weh.decisionsHelper.getActivityID(event)
	decision := weh.decisionsHelper.handleActivityTaskClosed(activityID)
	activity := decision.getData().(*scheduledActivity)
	if activity.handled {
		return nil
	}

	var err error
	attributes := event.GetActivityTaskTimedOutEventAttributes()
	if len(attributes.LastFailure.GetReason()) > 0 && attributes.GetTimeoutType() == apiv1.TimeoutType_TIMEOUT_TYPE_START_TO_CLOSE {
		// When retry activity timeout, it is possible that previous attempts got other customer timeout errors.
		// To stabilize the error type, we always return the customer error.
		// See more details of background: https://github.com/uber/cadence/issues/2627
		err = constructError(attributes.LastFailure.GetReason(), attributes.LastFailure.GetDetails(), weh.GetDataConverter())
	} else {
		details := newEncodedValues(attributes.Details.GetData(), weh.GetDataConverter())
		err = NewTimeoutError(attributes.GetTimeoutType(), details)
	}
	activity.handle(nil, err)
	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleActivityTaskCanceled(event *apiv1.HistoryEvent) error {
	activityID := weh.decisionsHelper.getActivityID(event)
	decision := weh.decisionsHelper.handleActivityTaskCanceled(activityID)
	activity := decision.getData().(*scheduledActivity)
	if activity.handled {
		return nil
	}

	if decision.isDone() || !activity.waitForCancelRequest {
		// Clear this so we don't have a recursive call that while executing might call the cancel one.
		details := newEncodedValues(event.GetActivityTaskCanceledEventAttributes().Details.GetData(), weh.GetDataConverter())
		err := NewCanceledError(details)
		activity.handle(nil, err)
	}

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleTimerFired(event *apiv1.HistoryEvent) {
	timerID := event.GetTimerFiredEventAttributes().GetTimerId()
	decision := weh.decisionsHelper.handleTimerClosed(timerID)
	timer := decision.getData().(*scheduledTimer)
	if timer.handled {
		return
	}

	timer.handle(nil, nil)
}

func (weh *workflowExecutionEventHandlerImpl) handleWorkflowExecutionCancelRequested() {
	weh.cancelHandler()
}

func (weh *workflowExecutionEventHandlerImpl) handleMarkerRecorded(
	eventID int64,
	attributes *apiv1.MarkerRecordedEventAttributes,
) error {
	encodedValues := newEncodedValues(attributes.Details.GetData(), weh.dataConverter)
	switch attributes.GetMarkerName() {
	case sideEffectMarkerName:
		var sideEffectID int32
		var result []byte
		encodedValues.Get(&sideEffectID, &result)
		weh.sideEffectResult[sideEffectID] = result
		return nil
	case versionMarkerName:
		var changeID string
		var version Version
		encodedValues.Get(&changeID, &version)
		weh.changeVersions[changeID] = version
		return nil
	case localActivityMarkerName:
		return weh.handleLocalActivityMarker(attributes.Details.GetData())
	case mutableSideEffectMarkerName:
		var fixedID string
		var result string
		encodedValues.Get(&fixedID, &result)
		weh.mutableSideEffect[fixedID] = []byte(result)
		return nil
	default:
		return fmt.Errorf("unknown marker name \"%v\" for eventID \"%v\"",
			attributes.GetMarkerName(), eventID)
	}
}

func (weh *workflowExecutionEventHandlerImpl) handleLocalActivityMarker(markerData []byte) error {
	var lamd localActivityMarkerData
	if err := newEncodedValue(markerData, weh.dataConverter).Get(&lamd); err != nil {
		return err
	}

	if la, ok := weh.pendingLaTasks[lamd.ActivityID]; ok {
		if len(lamd.ActivityType) > 0 && lastPartOfName(lamd.ActivityType) != lastPartOfName(la.params.ActivityType) {
			// history marker mismatch to the current code.
			panicMsg := fmt.Sprintf("code execute local activity %v, but history event found %v, markerData: %v", la.params.ActivityType, lamd.ActivityType, string(markerData))
			panicIllegalState(panicMsg)
		}
		weh.decisionsHelper.recordLocalActivityMarker(lamd.ActivityID, markerData)
		delete(weh.pendingLaTasks, lamd.ActivityID)
		delete(weh.unstartedLaTasks, lamd.ActivityID)
		lar := &localActivityResultWrapper{}
		if len(lamd.ErrReason) > 0 {
			lar.attempt = lamd.Attempt
			lar.backoff = lamd.Backoff
			lar.err = constructError(lamd.ErrReason, []byte(lamd.ErrJSON), weh.GetDataConverter())
		} else if len(lamd.ResultJSON) > 0 {
			lar.result = []byte(lamd.ResultJSON)
		}
		la.callback(lar)

		// update time
		weh.SetCurrentReplayTime(lamd.ReplayTime)

		// resume workflow execution after apply local activity result
		weh.workflowDefinition.OnDecisionTaskStarted()
	}

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) ProcessLocalActivityResult(lar *localActivityResult) error {
	// convert local activity result and error to marker data
	lamd := localActivityMarkerData{
		ActivityID:   lar.task.activityID,
		ActivityType: lar.task.params.ActivityType,
		ReplayTime:   weh.currentReplayTime.Add(time.Now().Sub(weh.currentLocalTime)),
		Attempt:      lar.task.attempt,
	}
	if lar.err != nil {
		errReason, errDetails := getErrorDetails(lar.err, weh.GetDataConverter())
		lamd.ErrReason = errReason
		lamd.ErrJSON = string(errDetails)
		lamd.Backoff = lar.backoff
	} else {
		lamd.ResultJSON = string(lar.result)
	}

	// encode marker data
	markerData, err := weh.encodeArg(lamd)
	if err != nil {
		return err
	}

	// create marker event for local activity result
	markerEvent := &apiv1.HistoryEvent{
		Attributes: &apiv1.HistoryEvent_MarkerRecordedEventAttributes{
			MarkerRecordedEventAttributes: &apiv1.MarkerRecordedEventAttributes{
				MarkerName: localActivityMarkerName,
				Details:    &apiv1.Payload{Data: markerData},
			},
		},
	}

	// apply the local activity result to workflow
	return weh.ProcessEvent(markerEvent, false, false)
}

func (weh *workflowExecutionEventHandlerImpl) handleWorkflowExecutionSignaled(
	attributes *apiv1.WorkflowExecutionSignaledEventAttributes) {
	weh.signalHandler(attributes.GetSignalName(), attributes.Input.GetData())
}

func (weh *workflowExecutionEventHandlerImpl) handleStartChildWorkflowExecutionFailed(event *apiv1.HistoryEvent) error {
	attributes := event.GetStartChildWorkflowExecutionFailedEventAttributes()
	childWorkflowID := attributes.GetWorkflowId()
	decision := weh.decisionsHelper.handleStartChildWorkflowExecutionFailed(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}

	err := &api.WorkflowExecutionAlreadyStartedError{
		Message: "Workflow execution already started",
	}
	childWorkflow.startedCallback(WorkflowExecution{}, err)
	childWorkflow.handle(nil, err)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleChildWorkflowExecutionStarted(event *apiv1.HistoryEvent) error {
	attributes := event.GetChildWorkflowExecutionStartedEventAttributes()
	childWorkflowID := attributes.WorkflowExecution.GetWorkflowId()
	childRunID := attributes.WorkflowExecution.GetRunId()
	decision := weh.decisionsHelper.handleChildWorkflowExecutionStarted(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}

	childWorkflowExecution := WorkflowExecution{
		ID:    childWorkflowID,
		RunID: childRunID,
	}
	childWorkflow.startedCallback(childWorkflowExecution, nil)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleChildWorkflowExecutionCompleted(event *apiv1.HistoryEvent) error {
	attributes := event.GetChildWorkflowExecutionCompletedEventAttributes()
	childWorkflowID := attributes.WorkflowExecution.GetWorkflowId()
	decision := weh.decisionsHelper.handleChildWorkflowExecutionClosed(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}
	childWorkflow.handle(attributes.Result.GetData(), nil)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleChildWorkflowExecutionFailed(event *apiv1.HistoryEvent) error {
	attributes := event.GetChildWorkflowExecutionFailedEventAttributes()
	childWorkflowID := attributes.WorkflowExecution.GetWorkflowId()
	decision := weh.decisionsHelper.handleChildWorkflowExecutionClosed(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}

	err := constructError(attributes.Failure.GetReason(), attributes.Failure.GetDetails(), weh.GetDataConverter())
	childWorkflow.handle(nil, err)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleChildWorkflowExecutionCanceled(event *apiv1.HistoryEvent) error {
	attributes := event.GetChildWorkflowExecutionCanceledEventAttributes()
	childWorkflowID := attributes.WorkflowExecution.GetWorkflowId()
	decision := weh.decisionsHelper.handleChildWorkflowExecutionCanceled(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}
	details := newEncodedValues(attributes.Details.GetData(), weh.GetDataConverter())
	err := NewCanceledError(details)
	childWorkflow.handle(nil, err)
	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleChildWorkflowExecutionTimedOut(event *apiv1.HistoryEvent) error {
	attributes := event.GetChildWorkflowExecutionTimedOutEventAttributes()
	childWorkflowID := attributes.WorkflowExecution.GetWorkflowId()
	decision := weh.decisionsHelper.handleChildWorkflowExecutionClosed(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}
	err := NewTimeoutError(attributes.GetTimeoutType())
	childWorkflow.handle(nil, err)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleChildWorkflowExecutionTerminated(event *apiv1.HistoryEvent) error {
	attributes := event.GetChildWorkflowExecutionTerminatedEventAttributes()
	childWorkflowID := attributes.WorkflowExecution.GetWorkflowId()
	decision := weh.decisionsHelper.handleChildWorkflowExecutionClosed(childWorkflowID)
	childWorkflow := decision.getData().(*scheduledChildWorkflow)
	if childWorkflow.handled {
		return nil
	}
	err := newTerminatedError()
	childWorkflow.handle(nil, err)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleUpsertWorkflowSearchAttributes(event *apiv1.HistoryEvent) {
	weh.updateWorkflowInfoWithSearchAttributes(event.GetUpsertWorkflowSearchAttributesEventAttributes().SearchAttributes)
}

func (weh *workflowExecutionEventHandlerImpl) handleRequestCancelExternalWorkflowExecutionInitiated(event *apiv1.HistoryEvent) error {
	// For cancellation of child workflow only, we do not use cancellation ID
	// for cancellation of external workflow, we have to use cancellation ID
	attribute := event.GetRequestCancelExternalWorkflowExecutionInitiatedEventAttributes()
	workflowID := attribute.WorkflowExecution.GetWorkflowId()
	cancellationID := string(attribute.Control)
	weh.decisionsHelper.handleRequestCancelExternalWorkflowExecutionInitiated(event.GetEventId(), workflowID, cancellationID)
	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleExternalWorkflowExecutionCancelRequested(event *apiv1.HistoryEvent) error {
	// For cancellation of child workflow only, we do not use cancellation ID
	// for cancellation of external workflow, we have to use cancellation ID
	attributes := event.GetExternalWorkflowExecutionCancelRequestedEventAttributes()
	workflowID := attributes.WorkflowExecution.GetWorkflowId()
	isExternal, decision := weh.decisionsHelper.handleExternalWorkflowExecutionCancelRequested(attributes.GetInitiatedEventId(), workflowID)
	if isExternal {
		// for cancel external workflow, we need to set the future
		cancellation := decision.getData().(*scheduledCancellation)
		if cancellation.handled {
			return nil
		}
		cancellation.handle(nil, nil)
	}

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleRequestCancelExternalWorkflowExecutionFailed(event *apiv1.HistoryEvent) error {
	// For cancellation of child workflow only, we do not use cancellation ID
	// for cancellation of external workflow, we have to use cancellation ID
	attributes := event.GetRequestCancelExternalWorkflowExecutionFailedEventAttributes()
	workflowID := attributes.WorkflowExecution.GetWorkflowId()
	isExternal, decision := weh.decisionsHelper.handleRequestCancelExternalWorkflowExecutionFailed(attributes.GetInitiatedEventId(), workflowID)
	if isExternal {
		// for cancel external workflow, we need to set the future
		cancellation := decision.getData().(*scheduledCancellation)
		if cancellation.handled {
			return nil
		}
		err := fmt.Errorf("cancel external workflow failed, %v", attributes.GetCause())
		cancellation.handle(nil, err)
	}

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleSignalExternalWorkflowExecutionCompleted(event *apiv1.HistoryEvent) error {
	attributes := event.GetExternalWorkflowExecutionSignaledEventAttributes()
	decision := weh.decisionsHelper.handleSignalExternalWorkflowExecutionCompleted(attributes.GetInitiatedEventId())
	signal := decision.getData().(*scheduledSignal)
	if signal.handled {
		return nil
	}
	signal.handle(nil, nil)

	return nil
}

func (weh *workflowExecutionEventHandlerImpl) handleSignalExternalWorkflowExecutionFailed(event *apiv1.HistoryEvent) error {
	attributes := event.GetSignalExternalWorkflowExecutionFailedEventAttributes()
	decision := weh.decisionsHelper.handleSignalExternalWorkflowExecutionFailed(attributes.GetInitiatedEventId())
	signal := decision.getData().(*scheduledSignal)
	if signal.handled {
		return nil
	}

	var err error
	switch attributes.GetCause() {
	case apiv1.SignalExternalWorkflowExecutionFailedCause_SIGNAL_EXTERNAL_WORKFLOW_EXECUTION_FAILED_CAUSE_UNKNOWN_EXTERNAL_WORKFLOW_EXECUTION:
		err = newUnknownExternalWorkflowExecutionError()
	default:
		err = fmt.Errorf("signal external workflow failed, %v", attributes.GetCause())
	}

	signal.handle(nil, err)

	return nil
}
