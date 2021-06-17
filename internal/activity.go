// Copyright (c) 2017-2020 Uber Technologies Inc.
// Portions of the Software are attributed to Copyright (c) 2020 Temporal Technologies Inc.
//
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
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/uber-go/tally"
	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/cadence/v2/internal/api"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	// ActivityType identifies a activity type.
	ActivityType struct {
		Name string
	}

	// ActivityInfo contains information about currently executing activity.
	ActivityInfo struct {
		TaskToken          []byte
		WorkflowType       *WorkflowType
		WorkflowDomain     string
		WorkflowExecution  WorkflowExecution
		ActivityID         string
		ActivityType       ActivityType
		TaskList           string
		HeartbeatTimeout   time.Duration // Maximum time between heartbeats. 0 means no heartbeat needed.
		ScheduledTimestamp time.Time     // Time of activity scheduled by a workflow
		StartedTimestamp   time.Time     // Time of activity start
		Deadline           time.Time     // Time of activity timeout
		Attempt            int32         // Attempt starts from 0, and increased by 1 for every retry if retry policy is specified.
	}

	// RegisterActivityOptions consists of options for registering an activity
	RegisterActivityOptions struct {
		// When an activity is a function the name is an actual activity type name.
		// When an activity is part of a structure then each member of the structure becomes an activity with
		// this Name as a prefix + activity function name.
		Name string
		// Activity type name is equal to function name instead of fully qualified
		// name including function package (and struct type if used).
		// This option has no effect when explicit Name is provided.
		EnableShortName               bool
		DisableAlreadyRegisteredCheck bool
		// Automatically send heartbeats for this activity at an interval that is less than the HeartbeatTimeout.
		// This option has no effect if the activity is executed with a HeartbeatTimeout of 0.
		// Default: false
		EnableAutoHeartbeat bool
	}

	// ActivityOptions stores all activity-specific parameters that will be stored inside of a context.
	// The current timeout resolution implementation is in seconds and uses math.Ceil(d.Seconds()) as the duration. But is
	// subjected to change in the future.
	ActivityOptions struct {
		// TaskList that the activity needs to be scheduled on.
		// optional: The default task list with the same name as the workflow task list.
		TaskList string

		// ScheduleToCloseTimeout - The end to end timeout for the activity needed.
		// The zero value of this uses default value.
		// Optional: The default value is the sum of ScheduleToStartTimeout and StartToCloseTimeout
		ScheduleToCloseTimeout time.Duration

		// ScheduleToStartTimeout - The queue timeout before the activity starts executed.
		// Mandatory: No default.
		ScheduleToStartTimeout time.Duration

		// StartToCloseTimeout - The timeout from the start of execution to end of it.
		// Mandatory: No default.
		StartToCloseTimeout time.Duration

		// HeartbeatTimeout - The periodic timeout while the activity is in execution. This is
		// the max interval the server needs to hear at-least one ping from the activity.
		// Optional: Default zero, means no heart beating is needed.
		HeartbeatTimeout time.Duration

		// WaitForCancellation - Whether to wait for cancelled activity to be completed(
		// activity can be failed, completed, cancel accepted)
		// Optional: default false
		WaitForCancellation bool

		// ActivityID - Business level activity ID, this is not needed for most of the cases if you have
		// to specify this then talk to cadence team. This is something will be done in future.
		// Optional: default empty string
		ActivityID string

		// RetryPolicy specify how to retry activity if error happens. When RetryPolicy.ExpirationInterval is specified
		// and it is larger than the activity's ScheduleToStartTimeout, then the ExpirationInterval will override activity's
		// ScheduleToStartTimeout. This is to avoid retrying on ScheduleToStartTimeout error which only happen when worker
		// is not picking up the task within the timeout. Retrying ScheduleToStartTimeout does not make sense as it just
		// mark the task as failed and create a new task and put back in the queue waiting worker to pick again. Cadence
		// server also make sure the ScheduleToStartTimeout will not be larger than the workflow's timeout.
		// Same apply to ScheduleToCloseTimeout. See more details about RetryPolicy on the doc for RetryPolicy.
		// Optional: default is no retry
		RetryPolicy *RetryPolicy
	}

	// LocalActivityOptions stores local activity specific parameters that will be stored inside of a context.
	LocalActivityOptions struct {
		// ScheduleToCloseTimeout - The end to end timeout for the local activity.
		// This field is required.
		ScheduleToCloseTimeout time.Duration

		// RetryPolicy specify how to retry activity if error happens.
		// Optional: default is no retry
		RetryPolicy *RetryPolicy
	}
)

// RegisterActivity - register an activity function or a pointer to a structure with the framework.
// The public form is: activity.Register(...)
// An activity function takes a context and input and returns a (result, error) or just error.
//
// And activity struct is a structure with all its exported methods treated as activities. The default
// name of each activity is the <structure name>_<method name>. Use RegisterActivityWithOptions to override the
// "<structure name>_" prefix.
//
// Examples:
//	func sampleActivity(ctx context.Context, input []byte) (result []byte, err error)
//	func sampleActivity(ctx context.Context, arg1 int, arg2 string) (result *customerStruct, err error)
//	func sampleActivity(ctx context.Context) (err error)
//	func sampleActivity() (result string, err error)
//	func sampleActivity(arg1 bool) (result int, err error)
//	func sampleActivity(arg1 bool) (err error)
//
//  type Activities struct {
//     // fields
//  }
//  func (a *Activities) SampleActivity1(ctx context.Context, arg1 int, arg2 string) (result *customerStruct, err error) {
//    ...
//  }
//
//  func (a *Activities) SampleActivity2(ctx context.Context, arg1 int, arg2 *customerStruct) (result string, err error) {
//    ...
//  }
//
// Serialization of all primitive types, structures is supported ... except channels, functions, variadic, unsafe pointer.
// This method calls panic if activityFunc doesn't comply with the expected format.
// Deprecated: Global activity registration methods are replaced by equivalent Worker instance methods.
// This method is kept to maintain backward compatibility and should not be used.
func RegisterActivity(activityFunc interface{}) {
	RegisterActivityWithOptions(activityFunc, RegisterActivityOptions{})
}

// RegisterActivityWithOptions registers the activity function or struct pointer with options.
// The public form is: activity.RegisterWithOptions(...)
// The user can use options to provide an external name for the activity or leave it empty if no
// external name is required. This can be used as
//  activity.RegisterWithOptions(barActivity, RegisterActivityOptions{})
//  activity.RegisterWithOptions(barActivity, RegisterActivityOptions{Name: "barExternal"})
// When registering the structure that implements activities the name is used as a prefix that is
// prepended to the activity method name.
//  activity.RegisterWithOptions(&Activities{ ... }, RegisterActivityOptions{Name: "MyActivities_"})
// To override each name of activities defined through a structure register the methods one by one:
// activities := &Activities{ ... }
// activity.RegisterWithOptions(activities.SampleActivity1, RegisterActivityOptions{Name: "Sample1"})
// activity.RegisterWithOptions(activities.SampleActivity2, RegisterActivityOptions{Name: "Sample2"})
// See RegisterActivity function for more info.
// The other use of options is to disable duplicated activity registration check
// which might be useful for integration tests.
// activity.RegisterWithOptions(barActivity, RegisterActivityOptions{DisableAlreadyRegisteredCheck: true})
// Deprecated: Global activity registration methods are replaced by equivalent Worker instance methods.
// This method is kept to maintain backward compatibility and should not be used.
func RegisterActivityWithOptions(activityFunc interface{}, opts RegisterActivityOptions) {
	registry := getGlobalRegistry()
	registry.RegisterActivityWithOptions(activityFunc, opts)
}

// GetActivityInfo returns information about currently executing activity.
func GetActivityInfo(ctx context.Context) ActivityInfo {
	env := getActivityEnv(ctx)
	return ActivityInfo{
		ActivityID:         env.activityID,
		ActivityType:       env.activityType,
		TaskToken:          env.taskToken,
		WorkflowExecution:  env.workflowExecution,
		HeartbeatTimeout:   env.heartbeatTimeout,
		Deadline:           env.deadline,
		ScheduledTimestamp: env.scheduledTimestamp,
		StartedTimestamp:   env.startedTimestamp,
		TaskList:           env.taskList,
		Attempt:            env.attempt,
		WorkflowType:       env.workflowType,
		WorkflowDomain:     env.workflowDomain,
	}
}

// HasHeartbeatDetails checks if there is heartbeat details from last attempt.
func HasHeartbeatDetails(ctx context.Context) bool {
	env := getActivityEnv(ctx)
	return len(env.heartbeatDetails) > 0
}

// GetHeartbeatDetails extract heartbeat details from last failed attempt. This is used in combination with retry policy.
// An activity could be scheduled with an optional retry policy on ActivityOptions. If the activity failed then server
// would attempt to dispatch another activity task to retry according to the retry policy. If there was heartbeat
// details reported by activity from the failed attempt, the details would be delivered along with the activity task for
// retry attempt. Activity could extract the details by GetHeartbeatDetails() and resume from the progress.
func GetHeartbeatDetails(ctx context.Context, d ...interface{}) error {
	env := getActivityEnv(ctx)
	if len(env.heartbeatDetails) == 0 {
		return ErrNoData
	}
	encoded := newEncodedValues(env.heartbeatDetails, env.dataConverter)
	return encoded.Get(d...)
}

// GetActivityLogger returns a logger that can be used in activity
func GetActivityLogger(ctx context.Context) *zap.Logger {
	env := getActivityEnv(ctx)
	return env.logger
}

// GetActivityMetricsScope returns a metrics scope that can be used in activity
func GetActivityMetricsScope(ctx context.Context) tally.Scope {
	env := getActivityEnv(ctx)
	return env.metricsScope
}

// GetWorkerStopChannel returns a read-only channel. The closure of this channel indicates the activity worker is stopping.
// When the worker is stopping, it will close this channel and wait until the worker stop timeout finishes. After the timeout
// hit, the worker will cancel the activity context and then exit. The timeout can be defined by worker option: WorkerStopTimeout.
// Use this channel to handle activity graceful exit when the activity worker stops.
func GetWorkerStopChannel(ctx context.Context) <-chan struct{} {
	env := getActivityEnv(ctx)
	return env.workerStopChannel
}

// RecordActivityHeartbeat sends heartbeat for the currently executing activity
// If the activity is either cancelled (or) workflow/activity doesn't exist then we would cancel
// the context with error context.Canceled.
//  TODO: we don't have a way to distinguish between the two cases when context is cancelled because
//  context doesn't support overriding value of ctx.Error.
//  TODO: Implement automatic heartbeating with cancellation through ctx.
// details - the details that you provided here can be seen in the workflow when it receives TimeoutError, you
// can check error TimeoutType()/Details().
func RecordActivityHeartbeat(ctx context.Context, details ...interface{}) {
	env := getActivityEnv(ctx)
	if env.isLocalActivity {
		// no-op for local activity
		return
	}
	var data []byte
	var err error
	// We would like to be a able to pass in "nil" as part of details(that is no progress to report to)
	if len(details) != 1 || details[0] != nil {
		data, err = encodeArgs(getDataConverterFromActivityCtx(ctx), details)
		if err != nil {
			panic(err)
		}
	}
	err = env.serviceInvoker.BatchHeartbeat(data)
	if err != nil {
		log := GetActivityLogger(ctx)
		log.Debug("RecordActivityHeartbeat With Error:", zap.Error(err))
	}
}

// ServiceInvoker abstracts calls to the Cadence service from an activity implementation.
// Implement to unit test activities.
type ServiceInvoker interface {
	// All the heartbeat methods will return ActivityTaskCanceledError if activity is cancelled.
	// Heartbeat sends a record heartbeat request to Cadence server directly without buffering.
	// It should only be used by the sessions framework.
	Heartbeat(details []byte) error
	// BatchHeartbeat sends heartbeat on the first attempt, and batches additional requests
	// to send it later according to heartbeat timeout.
	BatchHeartbeat(details []byte) error
	// BackgroundHeartbeat should only be used by Cadence library internally to heartbeat automatically
	// without detail.
	BackgroundHeartbeat() error
	Close(flushBufferedHeartbeat bool)

	SignalWorkflow(ctx context.Context, domain, workflowID, runID, signalName string, signalInput []byte) error
}

// WithActivityTask adds activity specific information into context.
// Use this method to unit test activity implementations that use context extractor methodshared.
func WithActivityTask(
	ctx context.Context,
	task *apiv1.PollForActivityTaskResponse,
	taskList string,
	invoker ServiceInvoker,
	logger *zap.Logger,
	scope tally.Scope,
	dataConverter DataConverter,
	workerStopChannel <-chan struct{},
	contextPropagators []ContextPropagator,
	tracer opentracing.Tracer,
) context.Context {
	var deadline time.Time
	scheduled := api.TimeFromProto(task.ScheduledTimeOfThisAttempt)
	started := api.TimeFromProto(task.StartedTime)
	scheduleToCloseTimeout := api.DurationFromProto(task.ScheduleToCloseTimeout)
	startToCloseTimeout := api.DurationFromProto(task.StartToCloseTimeout)
	heartbeatTimeout := api.DurationFromProto(task.HeartbeatTimeout)
	scheduleToCloseDeadline := scheduled.Add(scheduleToCloseTimeout)
	startToCloseDeadline := started.Add(startToCloseTimeout)
	// Minimum of the two deadlines.
	if scheduleToCloseDeadline.Before(startToCloseDeadline) {
		deadline = scheduleToCloseDeadline
	} else {
		deadline = startToCloseDeadline
	}

	logger = logger.With(
		zapcore.Field{Key: tagActivityID, Type: zapcore.StringType, String: task.ActivityId},
		zapcore.Field{Key: tagActivityType, Type: zapcore.StringType, String: task.ActivityType.Name},
		zapcore.Field{Key: tagWorkflowType, Type: zapcore.StringType, String: task.WorkflowType.Name},
		zapcore.Field{Key: tagWorkflowID, Type: zapcore.StringType, String: task.WorkflowExecution.WorkflowId},
		zapcore.Field{Key: tagRunID, Type: zapcore.StringType, String: task.WorkflowExecution.RunId},
	)

	return context.WithValue(ctx, activityEnvContextKey, &activityEnvironment{
		taskToken:      task.TaskToken,
		serviceInvoker: invoker,
		activityType:   ActivityType{Name: task.ActivityType.Name},
		activityID:     task.ActivityId,
		workflowExecution: WorkflowExecution{
			RunID: task.WorkflowExecution.RunId,
			ID:    task.WorkflowExecution.WorkflowId},
		logger:             logger,
		metricsScope:       scope,
		deadline:           deadline,
		heartbeatTimeout:   heartbeatTimeout,
		scheduledTimestamp: scheduled,
		startedTimestamp:   started,
		taskList:           taskList,
		dataConverter:      dataConverter,
		attempt:            task.GetAttempt(),
		heartbeatDetails:   task.HeartbeatDetails.GetData(),
		workflowType: &WorkflowType{
			Name: task.WorkflowType.Name,
		},
		workflowDomain:     task.WorkflowDomain,
		workerStopChannel:  workerStopChannel,
		contextPropagators: contextPropagators,
		tracer:             tracer,
	})
}
