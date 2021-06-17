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
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/uber-go/tally"
	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/cadence/v2/internal/api"
	"go.uber.org/zap"
)

type (
	// activity is an interface of an activity implementation.
	activity interface {
		Execute(ctx context.Context, input []byte) ([]byte, error)
		ActivityType() ActivityType
		GetFunction() interface{}
		GetOptions() RegisterActivityOptions
	}

	activityInfo struct {
		activityID string
	}

	localActivityInfo struct {
		activityID string
	}

	// activityOptions configuration parameters for scheduling an activity
	activityOptions struct {
		ActivityID                    *string // Users can choose IDs but our framework makes it optional to decrease the crust.
		TaskListName                  string
		ScheduleToCloseTimeoutSeconds int32
		ScheduleToStartTimeoutSeconds int32
		StartToCloseTimeoutSeconds    int32
		HeartbeatTimeoutSeconds       int32
		WaitForCancellation           bool
		OriginalTaskListName          string
		RetryPolicy                   *apiv1.RetryPolicy
	}

	localActivityOptions struct {
		ScheduleToCloseTimeoutSeconds int32
		RetryPolicy                   *RetryPolicy
	}

	executeActivityParams struct {
		activityOptions
		ActivityType  ActivityType
		Input         []byte
		DataConverter DataConverter
		Header        *apiv1.Header
	}

	executeLocalActivityParams struct {
		localActivityOptions
		ActivityFn    interface{} // local activity function pointer
		ActivityType  string      // local activity type
		InputArgs     []interface{}
		WorkflowInfo  *WorkflowInfo
		DataConverter DataConverter
		Attempt       int32
		ScheduledTime time.Time
		Header        *apiv1.Header
	}

	// asyncActivityClient for requesting activity execution
	asyncActivityClient interface {
		// The ExecuteActivity schedules an activity with a callback handler.
		// If the activity failed to complete the callback error would indicate the failure
		// and it can be one of ActivityTaskFailedError, ActivityTaskTimeoutError, ActivityTaskCanceledError
		ExecuteActivity(parameters executeActivityParams, callback resultHandler) *activityInfo

		// This only initiates cancel request for activity. if the activity is configured to not waitForCancellation then
		// it would invoke the callback handler immediately with error code ActivityTaskCanceledError.
		// If the activity is not running(either scheduled or started) then it is a no-operation.
		RequestCancelActivity(activityID string)
	}

	// localActivityClient for requesting local activity execution
	localActivityClient interface {
		ExecuteLocalActivity(params executeLocalActivityParams, callback laResultHandler) *localActivityInfo

		RequestCancelLocalActivity(activityID string)
	}

	activityEnvironment struct {
		taskToken          []byte
		workflowExecution  WorkflowExecution
		activityID         string
		activityType       ActivityType
		serviceInvoker     ServiceInvoker
		logger             *zap.Logger
		metricsScope       tally.Scope
		isLocalActivity    bool
		heartbeatTimeout   time.Duration
		deadline           time.Time
		scheduledTimestamp time.Time
		startedTimestamp   time.Time
		taskList           string
		dataConverter      DataConverter
		attempt            int32 // starts from 0.
		heartbeatDetails   []byte
		workflowType       *WorkflowType
		workflowDomain     string
		workerStopChannel  <-chan struct{}
		contextPropagators []ContextPropagator
		tracer             opentracing.Tracer
	}

	// context.WithValue need this type instead of basic type string to avoid lint error
	contextKey string
)

const (
	activityEnvContextKey          contextKey = "activityEnv"
	activityOptionsContextKey      contextKey = "activityOptions"
	localActivityOptionsContextKey contextKey = "localActivityOptions"
)

func getActivityEnv(ctx context.Context) *activityEnvironment {
	env := ctx.Value(activityEnvContextKey)
	if env == nil {
		panic("getActivityEnv: Not an activity context")
	}
	return env.(*activityEnvironment)
}

func getActivityOptions(ctx Context) *activityOptions {
	eap := ctx.Value(activityOptionsContextKey)
	if eap == nil {
		return nil
	}
	return eap.(*activityOptions)
}

func getLocalActivityOptions(ctx Context) *localActivityOptions {
	opts := ctx.Value(localActivityOptionsContextKey)
	if opts == nil {
		return nil
	}
	return opts.(*localActivityOptions)
}

func getValidatedActivityOptions(ctx Context) (*activityOptions, error) {
	p := getActivityOptions(ctx)
	if p == nil {
		// We need task list as a compulsory parameter. This can be removed after registration
		return nil, errActivityParamsBadRequest
	}
	if p.TaskListName == "" {
		// We default to origin task list name.
		p.TaskListName = p.OriginalTaskListName
	}
	if p.ScheduleToStartTimeoutSeconds <= 0 {
		return nil, errors.New("missing or negative ScheduleToStartTimeoutSeconds")
	}
	if p.StartToCloseTimeoutSeconds <= 0 {
		return nil, errors.New("missing or negative StartToCloseTimeoutSeconds")
	}
	if p.ScheduleToCloseTimeoutSeconds < 0 {
		return nil, errors.New("missing or negative ScheduleToCloseTimeoutSeconds")
	}
	if p.ScheduleToCloseTimeoutSeconds == 0 {
		// This is a optional parameter, we default to sum of the other two timeouts.
		p.ScheduleToCloseTimeoutSeconds = p.ScheduleToStartTimeoutSeconds + p.StartToCloseTimeoutSeconds
	}
	if p.HeartbeatTimeoutSeconds < 0 {
		return nil, errors.New("invalid negative HeartbeatTimeoutSeconds")
	}
	if err := validateRetryPolicy(p.RetryPolicy); err != nil {
		return nil, err
	}

	return p, nil
}

func getValidatedLocalActivityOptions(ctx Context) (*localActivityOptions, error) {
	p := getLocalActivityOptions(ctx)
	if p == nil {
		return nil, errLocalActivityParamsBadRequest
	}
	if p.ScheduleToCloseTimeoutSeconds <= 0 {
		return nil, errors.New("missing or negative ScheduleToCloseTimeoutSeconds")
	}

	return p, nil
}

func validateRetryPolicy(p *apiv1.RetryPolicy) error {
	if p == nil {
		return nil
	}

	if api.DurationFromProto(p.InitialInterval) <= 0 {
		return errors.New("missing or negative InitialInterval on retry policy")
	}
	if api.DurationFromProto(p.MaximumInterval) < 0 {
		return errors.New("negative MaximumInterval on retry policy is invalid")
	}
	if api.DurationFromProto(p.MaximumInterval) == 0 {
		// if not set, default to 100x of initial interval
		p.MaximumInterval = api.DurationToProto(100 * api.DurationFromProto(p.InitialInterval))
	}
	if p.GetMaximumAttempts() < 0 {
		return errors.New("negative MaximumAttempts on retry policy is invalid")
	}
	if api.DurationFromProto(p.ExpirationInterval) < 0 {
		return errors.New("ExpirationIntervalInSeconds cannot be less than 0 on retry policy")
	}
	if p.GetBackoffCoefficient() < 1 {
		return errors.New("BackoffCoefficient on retry policy cannot be less than 1.0")
	}
	if p.GetMaximumAttempts() == 0 && api.DurationFromProto(p.ExpirationInterval) == 0 {
		return errors.New("both MaximumAttempts and ExpirationIntervalInSeconds on retry policy are not set, at least one of them must be set")
	}

	return nil
}

func validateFunctionArgs(f interface{}, args []interface{}, isWorkflow bool) error {
	fType := reflect.TypeOf(f)
	if fType == nil || fType.Kind() != reflect.Func {
		return fmt.Errorf("Provided type: %v is not a function type", f)
	}
	fnName := getFunctionName(f)

	fnArgIndex := 0
	// Skip Context function argument.
	if fType.NumIn() > 0 {
		if isWorkflow && isWorkflowContext(fType.In(0)) {
			fnArgIndex++
		}
		if !isWorkflow && isActivityContext(fType.In(0)) {
			fnArgIndex++
		}
	}

	// Validate provided args match with function order match.
	if fType.NumIn()-fnArgIndex != len(args) {
		return fmt.Errorf(
			"expected %d args for function: %v but found %v",
			fType.NumIn()-fnArgIndex, fnName, len(args))
	}

	for i := 0; fnArgIndex < fType.NumIn(); fnArgIndex, i = fnArgIndex+1, i+1 {
		fnArgType := fType.In(fnArgIndex)
		argType := reflect.TypeOf(args[i])
		if argType != nil && !argType.AssignableTo(fnArgType) {
			return fmt.Errorf(
				"cannot assign function argument: %d from type: %s to type: %s",
				fnArgIndex+1, argType, fnArgType,
			)
		}
	}

	return nil
}

func getValidatedActivityFunction(f interface{}, args []interface{}, registry *registry) (*ActivityType, error) {
	fnName := ""
	fType := reflect.TypeOf(f)
	switch getKind(fType) {
	case reflect.String:
		fnName = reflect.ValueOf(f).String()
	case reflect.Func:
		if err := validateFunctionArgs(f, args, false); err != nil {
			return nil, err
		}
		fnName = getFunctionName(f)
		if alias, ok := registry.getActivityAlias(fnName); ok {
			fnName = alias
		}

	default:
		return nil, fmt.Errorf(
			"invalid type 'f' parameter provided, it can be either activity function or name of the activity: %v", f)
	}

	return &ActivityType{Name: fnName}, nil
}

func getKind(fType reflect.Type) reflect.Kind {
	if fType == nil {
		return reflect.Invalid
	}
	return fType.Kind()
}

func isActivityContext(inType reflect.Type) bool {
	contextElem := reflect.TypeOf((*context.Context)(nil)).Elem()
	return inType != nil && inType.Implements(contextElem)
}

func validateFunctionAndGetResults(f interface{}, values []reflect.Value, dataConverter DataConverter) ([]byte, error) {
	resultSize := len(values)

	if resultSize < 1 || resultSize > 2 {
		fnName := getFunctionName(f)
		return nil, fmt.Errorf(
			"the function: %v signature returns %d results, it is expecting to return either error or (result, error)",
			fnName, resultSize)
	}

	var result []byte
	var err error

	// Parse result
	if resultSize > 1 {
		retValue := values[0]
		if retValue.Kind() != reflect.Ptr || !retValue.IsNil() {
			result, err = encodeArg(dataConverter, retValue.Interface())
			if err != nil {
				return nil, err
			}
		}
	}

	// Parse error.
	errValue := values[resultSize-1]
	if errValue.IsNil() {
		return result, nil
	}
	errInterface, ok := errValue.Interface().(error)
	if !ok {
		return nil, fmt.Errorf(
			"Failed to parse error result as it is not of error interface: %v",
			errValue)
	}
	return result, errInterface
}

func serializeResults(f interface{}, results []interface{}, dataConverter DataConverter) (result []byte, err error) {
	// results contain all results including error
	resultSize := len(results)

	if resultSize < 1 || resultSize > 2 {
		fnName := getFunctionName(f)
		err = fmt.Errorf(
			"the function: %v signature returns %d results, it is expecting to return either error or (result, error)",
			fnName, resultSize)
		return
	}
	if resultSize > 1 {
		retValue := results[0]
		if retValue != nil {
			result, err = encodeArg(dataConverter, retValue)
			if err != nil {
				return nil, err
			}
		}
	}
	errResult := results[resultSize-1]
	if errResult != nil {
		var ok bool
		err, ok = errResult.(error)
		if !ok {
			err = fmt.Errorf(
				"failed to serialize error result as it is not of error interface: %v",
				errResult)
		}
	}
	return
}

func deSerializeFnResultFromFnType(fnType reflect.Type, result []byte, to interface{}, dataConverter DataConverter) error {
	if fnType.Kind() != reflect.Func {
		return fmt.Errorf("expecting only function type but got type: %v", fnType)
	}

	// We already validated during registration that it either have (result, error) (or) just error.
	if fnType.NumOut() <= 1 {
		return nil
	} else if fnType.NumOut() == 2 {
		if result == nil {
			return nil
		}
		err := decodeArg(dataConverter, result, to)
		if err != nil {
			return err
		}
	}
	return nil
}

func deSerializeFunctionResult(f interface{}, result []byte, to interface{}, dataConverter DataConverter, registry *registry) error {
	fType := reflect.TypeOf(f)
	if dataConverter == nil {
		dataConverter = getDefaultDataConverter()
	}

	switch getKind(fType) {
	case reflect.Func:
		// We already validated that it either have (result, error) (or) just error.
		return deSerializeFnResultFromFnType(fType, result, to, dataConverter)

	case reflect.String:
		// If we know about this function through registration then we will try to return corresponding result type.
		fnName := reflect.ValueOf(f).String()
		if activity, ok := registry.GetActivity(fnName); ok {
			return deSerializeFnResultFromFnType(reflect.TypeOf(activity.GetFunction()), result, to, dataConverter)
		}
	}

	// For everything we return result.
	return decodeArg(dataConverter, result, to)
}

func setActivityParametersIfNotExist(ctx Context) Context {
	params := getActivityOptions(ctx)
	var newParams activityOptions
	if params != nil {
		newParams = *params
		if params.RetryPolicy != nil {
			var newRetryPolicy apiv1.RetryPolicy
			newRetryPolicy = *newParams.RetryPolicy
			newParams.RetryPolicy = &newRetryPolicy
		}
	}
	return WithValue(ctx, activityOptionsContextKey, &newParams)
}

func setLocalActivityParametersIfNotExist(ctx Context) Context {
	params := getLocalActivityOptions(ctx)
	var newParams localActivityOptions
	if params != nil {
		newParams = *params
	}
	return WithValue(ctx, localActivityOptionsContextKey, &newParams)
}
