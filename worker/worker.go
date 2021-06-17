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

// Package worker contains functions to manage lifecycle of a Cadence client side worker.
package worker

import (
	"context"

	"go.uber.org/cadence/v2"
	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/cadence/v2/activity"
	"go.uber.org/cadence/v2/internal"
	"go.uber.org/cadence/v2/internal/api"
	"go.uber.org/cadence/v2/workflow"
	"go.uber.org/zap"
)

type (
	// Worker hosts workflow and activity implementations.
	// Use worker.New(...) to create an instance.
	Worker interface {
		Registry

		// Start starts the worker in a non-blocking fashion
		Start() error
		// Run is a blocking start and cleans up resources when killed
		// returns error only if it fails to start the worker
		Run() error
		// Stop cleans up any resources opened by worker
		Stop()
	}

	// Registry exposes registration functions to consumers.
	Registry interface {
		WorkflowRegistry
		ActivityRegistry
	}

	// WorkflowRegistry exposes workflow registration functions to consumers.
	WorkflowRegistry interface {
		// RegisterWorkflow - registers a workflow function with the worker.
		// A workflow takes a workflow.Context and input and returns a (result, error) or just error.
		// Examples:
		//	func sampleWorkflow(ctx workflow.Context, input []byte) (result []byte, err error)
		//	func sampleWorkflow(ctx workflow.Context, arg1 int, arg2 string) (result []byte, err error)
		//	func sampleWorkflow(ctx workflow.Context) (result []byte, err error)
		//	func sampleWorkflow(ctx workflow.Context, arg1 int) (result string, err error)
		// Serialization of all primitive types, structures is supported ... except channels, functions, variadic, unsafe pointer.
		// For global registration consider workflow.Register
		// This method panics if workflowFunc doesn't comply with the expected format or tries to register the same workflow
		RegisterWorkflow(w interface{})

		// RegisterWorkflowWithOptions registers the workflow function with options.
		// The user can use options to provide an external name for the workflow or leave it empty if no
		// external name is required. This can be used as
		//  worker.RegisterWorkflowWithOptions(sampleWorkflow, RegisterWorkflowOptions{})
		//  worker.RegisterWorkflowWithOptions(sampleWorkflow, RegisterWorkflowOptions{Name: "foo"})
		// This method panics if workflowFunc doesn't comply with the expected format or tries to register the same workflow
		// type name twice. Use workflow.RegisterOptions.DisableAlreadyRegisteredCheck to allow multiple registrations.
		RegisterWorkflowWithOptions(w interface{}, options workflow.RegisterOptions)
	}

	// ActivityRegistry exposes activity registration functions to consumers.
	ActivityRegistry interface {
		// RegisterActivity - register an activity function or a pointer to a structure with the worker.
		// An activity function takes a context and input and returns a (result, error) or just error.
		//
		// And activity struct is a structure with all its exported methods treated as activities. The default
		// name of each activity is the method name.
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
		// This method panics if activityFunc doesn't comply with the expected format or an activity with the same
		// type name is registered more than once.
		// For global registration consider activity.Register
		RegisterActivity(a interface{})

		// RegisterActivityWithOptions registers the activity function or struct pointer with options.
		// The user can use options to provide an external name for the activity or leave it empty if no
		// external name is required. This can be used as
		//  worker.RegisterActivityWithOptions(barActivity, RegisterActivityOptions{})
		//  worker.RegisterActivityWithOptions(barActivity, RegisterActivityOptions{Name: "barExternal"})
		// When registering the structure that implements activities the name is used as a prefix that is
		// prepended to the activity method name.
		//  worker.RegisterActivityWithOptions(&Activities{ ... }, RegisterActivityOptions{Name: "MyActivities_"})
		// To override each name of activities defined through a structure register the methods one by one:
		// activities := &Activities{ ... }
		// worker.RegisterActivityWithOptions(activities.SampleActivity1, RegisterActivityOptions{Name: "Sample1"})
		// worker.RegisterActivityWithOptions(activities.SampleActivity2, RegisterActivityOptions{Name: "Sample2"})
		// See RegisterActivity function for more info.
		// The other use of options is to disable duplicated activity registration check
		// which might be useful for integration tests.
		// worker.RegisterActivityWithOptions(barActivity, RegisterActivityOptions{DisableAlreadyRegisteredCheck: true})
		RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions)
	}

	// WorkflowReplayer supports replaying a workflow from its event history.
	// Use for troubleshooting and backwards compatibility unit tests.
	// For example if a workflow failed in production then its history can be downloaded through UI or CLI
	// and replayed in a debugger as many times as necessary.
	// Use this class to create unit tests that check if workflow changes are backwards compatible.
	// It is important to maintain backwards compatibility through use of workflow.GetVersion
	// to ensure that new deployments are not going to break open workflows.
	WorkflowReplayer interface {
		WorkflowRegistry

		// ReplayWorkflowHistory executes a single decision task for the given json history file.
		// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
		// The logger is an optional parameter. Defaults to the noop logger.
		ReplayWorkflowHistory(logger *zap.Logger, history *apiv1.History) error

		// ReplayWorkflowHistoryFromJSONFile executes a single decision task for the json history file downloaded from the cli.
		// To download the history file: cadence workflow showid <workflow_id> -of <output_filename>
		// See https://github.com/uber/cadence/blob/master/tools/cli/README.md for full documentation
		// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
		// The logger is an optional parameter. Defaults to the noop logger.
		ReplayWorkflowHistoryFromJSONFile(logger *zap.Logger, jsonfileName string) error

		// ReplayPartialWorkflowHistoryFromJSONFile executes a single decision task for the json history file upto provided
		// lastEventID(inclusive), downloaded from the cli.
		// To download the history file: cadence workflow showid <workflow_id> -of <output_filename>
		// See https://github.com/uber/cadence/blob/master/tools/cli/README.md for full documentation
		// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
		// The logger is an optional parameter. Defaults to the noop logger.
		ReplayPartialWorkflowHistoryFromJSONFile(logger *zap.Logger, jsonfileName string, lastEventID int64) error

		// ReplayWorkflowExecution loads a workflow execution history from the Cadence service and executes a single decision task for it.
		// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
		// The logger is the only optional parameter. Defaults to the noop logger.
		ReplayWorkflowExecution(ctx context.Context, service api.Interface, logger *zap.Logger, domain string, execution workflow.Execution) error
	}

	// WorkflowShadower retrieves and replays workflow history from Cadence service to determine if there's any nondeterministic changes in the workflow definition
	WorkflowShadower interface {
		WorkflowRegistry

		Run() error
	}

	// Options is used to configure a worker instance.
	Options = internal.WorkerOptions

	// ShadowOptions is used to configure a WorkflowShadower.
	ShadowOptions = internal.ShadowOptions
	// ShadowMode is an enum for configuring if shadowing should continue after all workflows matches the WorkflowQuery have been replayed.
	ShadowMode = internal.ShadowMode
	// TimeFilter represents a time range through the min and max timestamp
	TimeFilter = internal.TimeFilter
	// ShadowExitCondition configures when the workflow shadower should exit.
	// If not specified shadower will exit after replaying all workflows satisfying the visibility query.
	ShadowExitCondition = internal.ShadowExitCondition

	// ReplayOptions is used to configure the replay decision task worker.
	ReplayOptions = internal.ReplayOptions

	// NonDeterministicWorkflowPolicy is an enum for configuring how client's decision task handler deals with
	// mismatched history events (presumably arising from non-deterministic workflow definitions).
	NonDeterministicWorkflowPolicy = internal.NonDeterministicWorkflowPolicy
)

const (
	// NonDeterministicWorkflowPolicyBlockWorkflow is the default policy for handling detected non-determinism.
	// This option simply logs to console with an error message that non-determinism is detected, but
	// does *NOT* reply anything back to the server.
	// It is chosen as default for backward compatibility reasons because it preserves the old behavior
	// for handling non-determinism that we had before NonDeterministicWorkflowPolicy type was added to
	// allow more configurability.
	NonDeterministicWorkflowPolicyBlockWorkflow = internal.NonDeterministicWorkflowPolicyBlockWorkflow
	// NonDeterministicWorkflowPolicyFailWorkflow behaves exactly the same as Ignore, up until the very
	// end of processing a decision task.
	// Whereas default does *NOT* reply anything back to the server, fail workflow replies back with a request
	// to fail the workflow execution.
	NonDeterministicWorkflowPolicyFailWorkflow = internal.NonDeterministicWorkflowPolicyFailWorkflow
)

const (
	// ShadowModeNormal is the default mode for workflow shadowing.
	// Shadowing will complete after all workflows matches WorkflowQuery have been replayed.
	ShadowModeNormal = internal.ShadowModeNormal
	// ShadowModeContinuous mode will start a new round of shadowing
	// after all workflows matches WorkflowQuery have been replayed.
	// There will be a 5 min wait period between each round,
	// currently this wait period is not configurable.
	// Shadowing will complete only when ExitCondition is met.
	// ExitCondition must be specified when using this mode
	ShadowModeContinuous = internal.ShadowModeContinuous
)

// New creates an instance of worker for managing workflow and activity executions.
//    service  - API interface to the cadence server
//    domain   - the name of the cadence domain
//    taskList - is the task list name you use to identify your client worker, also
//               identifies group of workflow and activity implementations that are
//               hosted by a single worker process
//    options  - configure any worker specific options like logger, metrics, identity
func New(
	service cadence.Interface,
	domain string,
	taskList string,
	options Options,
) Worker {
	return internal.NewWorker(service, domain, taskList, options)
}

// NewWorkflowReplayer creates a WorkflowReplayer instance.
func NewWorkflowReplayer() WorkflowReplayer {
	return internal.NewWorkflowReplayer()
}

// NewWorkflowReplayerWithOptions creates an instance of the WorkflowReplayer
// with provided replay worker options
func NewWorkflowReplayerWithOptions(
	options ReplayOptions,
) WorkflowReplayer {
	return internal.NewWorkflowReplayerWithOptions(options)
}

// NewWorkflowShadower creates a WorkflowShadower instance.
func NewWorkflowShadower(
	service api.Interface,
	domain string,
	shadowOptions ShadowOptions,
	replayOptions ReplayOptions,
	logger *zap.Logger,
) (WorkflowShadower, error) {
	return internal.NewWorkflowShadower(service, domain, shadowOptions, replayOptions, logger)
}

// EnableVerboseLogging enable or disable verbose logging of internal Cadence library components.
// Most customers don't need this feature, unless advised by the Cadence team member.
// Also there is no guarantee that this API is not going to change.
func EnableVerboseLogging(enable bool) {
	internal.EnableVerboseLogging(enable)
}

// ReplayWorkflowHistory executes a single decision task for the given json history file.
// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
// The logger is an optional parameter. Defaults to the noop logger.
func ReplayWorkflowHistory(logger *zap.Logger, history *apiv1.History) error {
	return internal.ReplayWorkflowHistory(logger, history)
}

// ReplayWorkflowHistoryFromJSONFile executes a single decision task for the json history file downloaded from the cli.
// To download the history file: cadence workflow showid <workflow_id> -of <output_filename>
// See https://github.com/uber/cadence/blob/master/tools/cli/README.md for full documentation
// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
// The logger is an optional parameter. Defaults to the noop logger.
func ReplayWorkflowHistoryFromJSONFile(logger *zap.Logger, jsonfileName string) error {
	return internal.ReplayWorkflowHistoryFromJSONFile(logger, jsonfileName)
}

// ReplayPartialWorkflowHistoryFromJSONFile executes a single decision task for the json history file upto provided
//// lastEventID(inclusive), downloaded from the cli.
// To download the history file: cadence workflow showid <workflow_id> -of <output_filename>
// See https://github.com/uber/cadence/blob/master/tools/cli/README.md for full documentation
// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
// The logger is an optional parameter. Defaults to the noop logger.
func ReplayPartialWorkflowHistoryFromJSONFile(logger *zap.Logger, jsonfileName string, lastEventID int64) error {
	return internal.ReplayPartialWorkflowHistoryFromJSONFile(logger, jsonfileName, lastEventID)
}

// ReplayWorkflowExecution loads a workflow execution history from the Cadence service and executes a single decision task for it.
// Use for testing the backwards compatibility of code changes and troubleshooting workflows in a debugger.
// The logger is the only optional parameter. Defaults to the noop logger.
func ReplayWorkflowExecution(ctx context.Context, service api.Interface, logger *zap.Logger, domain string, execution workflow.Execution) error {
	return internal.ReplayWorkflowExecution(ctx, service, logger, domain, execution)
}

// SetStickyWorkflowCacheSize sets the cache size for sticky workflow cache. Sticky workflow execution is the affinity
// between decision tasks of a specific workflow execution to a specific worker. The affinity is set if sticky execution
// is enabled via Worker.Options (It is enabled by default unless disabled explicitly). The benefit of sticky execution
// is that workflow does not have to reconstruct the state by replaying from beginning of history events. But the cost
// is it consumes more memory as it rely on caching workflow execution's running state on the worker. The cache is shared
// between workers running within same process. This must be called before any worker is started. If not called, the
// default size of 10K (might change in future) will be used.
func SetStickyWorkflowCacheSize(cacheSize int) {
	internal.SetStickyWorkflowCacheSize(cacheSize)
}

// SetBinaryChecksum sets the identifier of the binary(aka BinaryChecksum).
// The identifier is mainly used in recording reset points when respondDecisionTaskCompleted. For each workflow, the very first
// decision completed by a binary will be associated as a auto-reset point for the binary. So that when a customer wants to
// mark the binary as bad, the workflow will be reset to that point -- which means workflow will forget all progress generated
// by the binary.
// On another hand, once the binary is marked as bad, the bad binary cannot poll decision and make any progress any more.
func SetBinaryChecksum(checksum string) {
	internal.SetBinaryChecksum(checksum)
}
