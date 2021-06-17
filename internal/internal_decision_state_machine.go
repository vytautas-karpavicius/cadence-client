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
	"container/list"
	"fmt"

	apiv1 "go.uber.org/cadence/v2/.gen/proto/api/v1"
	"go.uber.org/cadence/v2/internal/common/util"
)

type (
	decisionState int32
	decisionType  int32

	decisionID struct {
		decisionType decisionType
		id           string
	}

	decisionStateMachine interface {
		getState() decisionState
		getID() decisionID
		isDone() bool
		getDecision() *apiv1.Decision // return nil if there is no decision in current state
		cancel()

		handleStartedEvent()
		handleCancelInitiatedEvent()
		handleCanceledEvent()
		handleCancelFailedEvent()
		handleCompletionEvent()
		handleInitiationFailedEvent()
		handleInitiatedEvent()

		handleDecisionSent()

		setData(data interface{})
		getData() interface{}
	}

	decisionStateMachineBase struct {
		id      decisionID
		state   decisionState
		history []string
		data    interface{}
		helper  *decisionsHelper
	}

	activityDecisionStateMachine struct {
		*decisionStateMachineBase
		attributes *apiv1.ScheduleActivityTaskDecisionAttributes
	}

	timerDecisionStateMachine struct {
		*decisionStateMachineBase
		attributes *apiv1.StartTimerDecisionAttributes
		canceled   bool
	}

	childWorkflowDecisionStateMachine struct {
		*decisionStateMachineBase
		attributes *apiv1.StartChildWorkflowExecutionDecisionAttributes
	}

	naiveDecisionStateMachine struct {
		*decisionStateMachineBase
		decision *apiv1.Decision
	}

	// only possible state transition is: CREATED->SENT->INITIATED->COMPLETED
	cancelExternalWorkflowDecisionStateMachine struct {
		*naiveDecisionStateMachine
	}

	signalExternalWorkflowDecisionStateMachine struct {
		*naiveDecisionStateMachine
	}

	// only possible state transition is: CREATED->SENT->COMPLETED
	markerDecisionStateMachine struct {
		*naiveDecisionStateMachine
	}

	upsertSearchAttributesDecisionStateMachine struct {
		*naiveDecisionStateMachine
	}

	decisionsHelper struct {
		orderedDecisions *list.List
		decisions        map[decisionID]*list.Element

		scheduledEventIDToActivityID     map[int64]string
		scheduledEventIDToCancellationID map[int64]string
		scheduledEventIDToSignalID       map[int64]string
	}

	// panic when decision state machine is in illegal state
	stateMachineIllegalStatePanic struct {
		message string
	}
)

const (
	decisionStateCreated                                decisionState = 0
	decisionStateDecisionSent                           decisionState = 1
	decisionStateCanceledBeforeInitiated                decisionState = 2
	decisionStateInitiated                              decisionState = 3
	decisionStateStarted                                decisionState = 4
	decisionStateCanceledAfterInitiated                 decisionState = 5
	decisionStateCanceledAfterStarted                   decisionState = 6
	decisionStateCancellationDecisionSent               decisionState = 7
	decisionStateCompletedAfterCancellationDecisionSent decisionState = 8
	decisionStateCompleted                              decisionState = 9
)

const (
	decisionTypeActivity               decisionType = 0
	decisionTypeChildWorkflow          decisionType = 1
	decisionTypeCancellation           decisionType = 2
	decisionTypeMarker                 decisionType = 3
	decisionTypeTimer                  decisionType = 4
	decisionTypeSignal                 decisionType = 5
	decisionTypeUpsertSearchAttributes decisionType = 6
)

const (
	eventCancel           = "cancel"
	eventDecisionSent     = "handleDecisionSent"
	eventInitiated        = "handleInitiatedEvent"
	eventInitiationFailed = "handleInitiationFailedEvent"
	eventStarted          = "handleStartedEvent"
	eventCompletion       = "handleCompletionEvent"
	eventCancelInitiated  = "handleCancelInitiatedEvent"
	eventCancelFailed     = "handleCancelFailedEvent"
	eventCanceled         = "handleCanceledEvent"
)

const (
	sideEffectMarkerName        = "SideEffect"
	versionMarkerName           = "Version"
	localActivityMarkerName     = "LocalActivity"
	mutableSideEffectMarkerName = "MutableSideEffect"
)

func (d decisionState) String() string {
	switch d {
	case decisionStateCreated:
		return "Created"
	case decisionStateDecisionSent:
		return "DecisionSent"
	case decisionStateCanceledBeforeInitiated:
		return "CanceledBeforeInitiated"
	case decisionStateInitiated:
		return "Initiated"
	case decisionStateStarted:
		return "Started"
	case decisionStateCanceledAfterInitiated:
		return "CanceledAfterInitiated"
	case decisionStateCanceledAfterStarted:
		return "CanceledAfterStarted"
	case decisionStateCancellationDecisionSent:
		return "CancellationDecisionSent"
	case decisionStateCompletedAfterCancellationDecisionSent:
		return "CompletedAfterCancellationDecisionSent"
	case decisionStateCompleted:
		return "Completed"
	default:
		return "Unknown"
	}
}

func (d decisionType) String() string {
	switch d {
	case decisionTypeActivity:
		return "Activity"
	case decisionTypeChildWorkflow:
		return "ChildWorkflow"
	case decisionTypeCancellation:
		return "Cancellation"
	case decisionTypeMarker:
		return "Marker"
	case decisionTypeTimer:
		return "Timer"
	case decisionTypeSignal:
		return "Signal"
	default:
		return "Unknown"
	}
}

func (d decisionID) String() string {
	return fmt.Sprintf("DecisionType: %v, ID: %v", d.decisionType, d.id)
}

func makeDecisionID(decisionType decisionType, id string) decisionID {
	return decisionID{decisionType: decisionType, id: id}
}

func (h *decisionsHelper) newDecisionStateMachineBase(decisionType decisionType, id string) *decisionStateMachineBase {
	return &decisionStateMachineBase{
		id:      makeDecisionID(decisionType, id),
		state:   decisionStateCreated,
		history: []string{decisionStateCreated.String()},
		helper:  h,
	}
}

func (h *decisionsHelper) newActivityDecisionStateMachine(attributes *apiv1.ScheduleActivityTaskDecisionAttributes) *activityDecisionStateMachine {
	base := h.newDecisionStateMachineBase(decisionTypeActivity, attributes.GetActivityId())
	return &activityDecisionStateMachine{
		decisionStateMachineBase: base,
		attributes:               attributes,
	}
}

func (h *decisionsHelper) newTimerDecisionStateMachine(attributes *apiv1.StartTimerDecisionAttributes) *timerDecisionStateMachine {
	base := h.newDecisionStateMachineBase(decisionTypeTimer, attributes.GetTimerId())
	return &timerDecisionStateMachine{
		decisionStateMachineBase: base,
		attributes:               attributes,
	}
}

func (h *decisionsHelper) newChildWorkflowDecisionStateMachine(attributes *apiv1.StartChildWorkflowExecutionDecisionAttributes) *childWorkflowDecisionStateMachine {
	base := h.newDecisionStateMachineBase(decisionTypeChildWorkflow, attributes.GetWorkflowId())
	return &childWorkflowDecisionStateMachine{
		decisionStateMachineBase: base,
		attributes:               attributes,
	}
}

func (h *decisionsHelper) newNaiveDecisionStateMachine(decisionType decisionType, id string, decision *apiv1.Decision) *naiveDecisionStateMachine {
	base := h.newDecisionStateMachineBase(decisionType, id)
	return &naiveDecisionStateMachine{
		decisionStateMachineBase: base,
		decision:                 decision,
	}
}

func (h *decisionsHelper) newMarkerDecisionStateMachine(id string, attributes *apiv1.RecordMarkerDecisionAttributes) *markerDecisionStateMachine {
	d := &apiv1.Decision{
		Attributes: &apiv1.Decision_RecordMarkerDecisionAttributes{
			RecordMarkerDecisionAttributes: attributes,
		},
	}
	return &markerDecisionStateMachine{
		naiveDecisionStateMachine: h.newNaiveDecisionStateMachine(decisionTypeMarker, id, d),
	}
}

func (h *decisionsHelper) newCancelExternalWorkflowStateMachine(attributes *apiv1.RequestCancelExternalWorkflowExecutionDecisionAttributes, cancellationID string) *cancelExternalWorkflowDecisionStateMachine {
	d := &apiv1.Decision{
		Attributes: &apiv1.Decision_RequestCancelExternalWorkflowExecutionDecisionAttributes{
			RequestCancelExternalWorkflowExecutionDecisionAttributes: attributes,
		},
	}
	return &cancelExternalWorkflowDecisionStateMachine{
		naiveDecisionStateMachine: h.newNaiveDecisionStateMachine(decisionTypeCancellation, cancellationID, d),
	}
}

func (h *decisionsHelper) newSignalExternalWorkflowStateMachine(attributes *apiv1.SignalExternalWorkflowExecutionDecisionAttributes, signalID string) *signalExternalWorkflowDecisionStateMachine {
	d := &apiv1.Decision{
		Attributes: &apiv1.Decision_SignalExternalWorkflowExecutionDecisionAttributes{
			SignalExternalWorkflowExecutionDecisionAttributes: attributes,
		},
	}
	return &signalExternalWorkflowDecisionStateMachine{
		naiveDecisionStateMachine: h.newNaiveDecisionStateMachine(decisionTypeSignal, signalID, d),
	}
}

func (h *decisionsHelper) newUpsertSearchAttributesStateMachine(attributes *apiv1.UpsertWorkflowSearchAttributesDecisionAttributes, upsertID string) *upsertSearchAttributesDecisionStateMachine {
	d := &apiv1.Decision{
		Attributes: &apiv1.Decision_UpsertWorkflowSearchAttributesDecisionAttributes{
			UpsertWorkflowSearchAttributesDecisionAttributes: attributes,
		},
	}
	return &upsertSearchAttributesDecisionStateMachine{
		naiveDecisionStateMachine: h.newNaiveDecisionStateMachine(decisionTypeUpsertSearchAttributes, upsertID, d),
	}
}

func (d *decisionStateMachineBase) getState() decisionState {
	return d.state
}

func (d *decisionStateMachineBase) getID() decisionID {
	return d.id
}

func (d *decisionStateMachineBase) isDone() bool {
	return d.state == decisionStateCompleted || d.state == decisionStateCompletedAfterCancellationDecisionSent
}

func (d *decisionStateMachineBase) setData(data interface{}) {
	d.data = data
}

func (d *decisionStateMachineBase) getData() interface{} {
	return d.data
}

func (d *decisionStateMachineBase) moveState(newState decisionState, event string) {
	d.history = append(d.history, event)
	d.state = newState
	d.history = append(d.history, newState.String())

	if newState == decisionStateCompleted {
		if elem, ok := d.helper.decisions[d.getID()]; ok {
			d.helper.orderedDecisions.Remove(elem)
			delete(d.helper.decisions, d.getID())
		}
	}
}

func (d stateMachineIllegalStatePanic) String() string {
	return d.message
}

func panicIllegalState(message string) {
	panic(stateMachineIllegalStatePanic{message: message})
}

func (d *decisionStateMachineBase) failStateTransition(event string) {
	// this is when we detect illegal state transition, likely due to ill history sequence or nondeterministic decider code
	panicIllegalState(fmt.Sprintf("invalid state transition: attempt to %v, %v", event, d))
}

func (d *decisionStateMachineBase) handleDecisionSent() {
	switch d.state {
	case decisionStateCreated:
		d.moveState(decisionStateDecisionSent, eventDecisionSent)
	}
}

func (d *decisionStateMachineBase) cancel() {
	switch d.state {
	case decisionStateCompleted, decisionStateCompletedAfterCancellationDecisionSent:
		// No op. This is legit. People could cancel context after timer/activity is done.
	case decisionStateCreated:
		d.moveState(decisionStateCompleted, eventCancel)
	case decisionStateDecisionSent:
		d.moveState(decisionStateCanceledBeforeInitiated, eventCancel)
	case decisionStateInitiated:
		d.moveState(decisionStateCanceledAfterInitiated, eventCancel)
	default:
		d.failStateTransition(eventCancel)
	}
}

func (d *decisionStateMachineBase) handleInitiatedEvent() {
	switch d.state {
	case decisionStateDecisionSent:
		d.moveState(decisionStateInitiated, eventInitiated)
	case decisionStateCanceledBeforeInitiated:
		d.moveState(decisionStateCanceledAfterInitiated, eventInitiated)
	default:
		d.failStateTransition(eventInitiated)
	}
}

func (d *decisionStateMachineBase) handleInitiationFailedEvent() {
	switch d.state {
	case decisionStateInitiated, decisionStateDecisionSent, decisionStateCanceledBeforeInitiated:
		d.moveState(decisionStateCompleted, eventInitiationFailed)
	default:
		d.failStateTransition(eventInitiationFailed)
	}
}

func (d *decisionStateMachineBase) handleStartedEvent() {
	d.history = append(d.history, eventStarted)
}

func (d *decisionStateMachineBase) handleCompletionEvent() {
	switch d.state {
	case decisionStateCanceledAfterInitiated, decisionStateInitiated:
		d.moveState(decisionStateCompleted, eventCompletion)
	case decisionStateCancellationDecisionSent:
		d.moveState(decisionStateCompletedAfterCancellationDecisionSent, eventCompletion)
	default:
		d.failStateTransition(eventCompletion)
	}
}

func (d *decisionStateMachineBase) handleCancelInitiatedEvent() {
	d.history = append(d.history, eventCancelInitiated)
	switch d.state {
	case decisionStateCancellationDecisionSent:
	// No state change
	default:
		d.failStateTransition(eventCancelInitiated)
	}
}

func (d *decisionStateMachineBase) handleCancelFailedEvent() {
	switch d.state {
	case decisionStateCompletedAfterCancellationDecisionSent:
		d.moveState(decisionStateCompleted, eventCancelFailed)
	default:
		d.failStateTransition(eventCancelFailed)
	}
}

func (d *decisionStateMachineBase) handleCanceledEvent() {
	switch d.state {
	case decisionStateCancellationDecisionSent:
		d.moveState(decisionStateCompleted, eventCanceled)
	default:
		d.failStateTransition(eventCanceled)
	}
}

func (d *decisionStateMachineBase) String() string {
	return fmt.Sprintf("%v, state=%v, isDone()=%v, history=%v",
		d.id, d.state, d.isDone(), d.history)
}

func (d *activityDecisionStateMachine) getDecision() *apiv1.Decision {
	switch d.state {
	case decisionStateCreated:
		return &apiv1.Decision{
			Attributes: &apiv1.Decision_ScheduleActivityTaskDecisionAttributes{
				ScheduleActivityTaskDecisionAttributes: d.attributes,
			},
		}
	case decisionStateCanceledAfterInitiated:
		return &apiv1.Decision{
			Attributes: &apiv1.Decision_RequestCancelActivityTaskDecisionAttributes{
				RequestCancelActivityTaskDecisionAttributes: &apiv1.RequestCancelActivityTaskDecisionAttributes{
					ActivityId: d.attributes.ActivityId,
				},
			},
		}
	default:
		return nil
	}
}

func (d *activityDecisionStateMachine) handleDecisionSent() {
	switch d.state {
	case decisionStateCanceledAfterInitiated:
		d.moveState(decisionStateCancellationDecisionSent, eventDecisionSent)
	default:
		d.decisionStateMachineBase.handleDecisionSent()
	}
}

func (d *activityDecisionStateMachine) handleCancelFailedEvent() {
	switch d.state {
	case decisionStateCancellationDecisionSent:
		d.moveState(decisionStateInitiated, eventCancelFailed)
	default:
		d.decisionStateMachineBase.handleCancelFailedEvent()
	}
}

func (d *timerDecisionStateMachine) cancel() {
	d.canceled = true
	d.decisionStateMachineBase.cancel()
}

func (d *timerDecisionStateMachine) isDone() bool {
	return d.state == decisionStateCompleted || d.canceled
}

func (d *timerDecisionStateMachine) handleDecisionSent() {
	switch d.state {
	case decisionStateCanceledAfterInitiated:
		d.moveState(decisionStateCancellationDecisionSent, eventDecisionSent)
	default:
		d.decisionStateMachineBase.handleDecisionSent()
	}
}

func (d *timerDecisionStateMachine) handleCancelFailedEvent() {
	switch d.state {
	case decisionStateCancellationDecisionSent:
		d.moveState(decisionStateInitiated, eventCancelFailed)
	default:
		d.decisionStateMachineBase.handleCancelFailedEvent()
	}
}

func (d *timerDecisionStateMachine) getDecision() *apiv1.Decision {
	switch d.state {
	case decisionStateCreated:
		return &apiv1.Decision{
			Attributes: &apiv1.Decision_StartTimerDecisionAttributes{
				StartTimerDecisionAttributes: d.attributes,
			},
		}
	case decisionStateCanceledAfterInitiated:
		return &apiv1.Decision{
			Attributes: &apiv1.Decision_CancelTimerDecisionAttributes{
				CancelTimerDecisionAttributes: &apiv1.CancelTimerDecisionAttributes{
					TimerId: d.attributes.TimerId,
				},
			},
		}
	default:
		return nil
	}
}

func (d *childWorkflowDecisionStateMachine) getDecision() *apiv1.Decision {
	switch d.state {
	case decisionStateCreated:
		return &apiv1.Decision{
			Attributes: &apiv1.Decision_StartChildWorkflowExecutionDecisionAttributes{
				StartChildWorkflowExecutionDecisionAttributes: d.attributes,
			},
		}
	case decisionStateCanceledAfterStarted:
		return &apiv1.Decision{
			Attributes: &apiv1.Decision_RequestCancelExternalWorkflowExecutionDecisionAttributes{
				RequestCancelExternalWorkflowExecutionDecisionAttributes: &apiv1.RequestCancelExternalWorkflowExecutionDecisionAttributes{
					Domain:            d.attributes.Domain,
					WorkflowExecution: &apiv1.WorkflowExecution{
						WorkflowId: d.attributes.WorkflowId,
					},
					ChildWorkflowOnly: true,
				},
			},
		}
	default:
		return nil
	}
}

func (d *childWorkflowDecisionStateMachine) handleDecisionSent() {
	switch d.state {
	case decisionStateCanceledAfterStarted:
		d.moveState(decisionStateCancellationDecisionSent, eventDecisionSent)
	default:
		d.decisionStateMachineBase.handleDecisionSent()
	}
}

func (d *childWorkflowDecisionStateMachine) handleStartedEvent() {
	switch d.state {
	case decisionStateInitiated:
		d.moveState(decisionStateStarted, eventStarted)
	case decisionStateCanceledAfterInitiated:
		d.moveState(decisionStateCanceledAfterStarted, eventStarted)
	default:
		d.decisionStateMachineBase.handleStartedEvent()
	}
}

func (d *childWorkflowDecisionStateMachine) handleCancelFailedEvent() {
	switch d.state {
	case decisionStateCancellationDecisionSent:
		d.moveState(decisionStateStarted, eventCancelFailed)
	default:
		d.decisionStateMachineBase.handleCancelFailedEvent()
	}
}

func (d *childWorkflowDecisionStateMachine) cancel() {
	switch d.state {
	case decisionStateStarted:
		d.moveState(decisionStateCanceledAfterStarted, eventCancel)
	default:
		d.decisionStateMachineBase.cancel()
	}
}

func (d *childWorkflowDecisionStateMachine) handleCanceledEvent() {
	switch d.state {
	case decisionStateStarted:
		d.moveState(decisionStateCompleted, eventCanceled)
	default:
		d.decisionStateMachineBase.handleCanceledEvent()
	}
}

func (d *childWorkflowDecisionStateMachine) handleCompletionEvent() {
	switch d.state {
	case decisionStateStarted, decisionStateCanceledAfterStarted:
		d.moveState(decisionStateCompleted, eventCompletion)
	default:
		d.decisionStateMachineBase.handleCompletionEvent()
	}
}

func (d *naiveDecisionStateMachine) getDecision() *apiv1.Decision {
	switch d.state {
	case decisionStateCreated:
		return d.decision
	default:
		return nil
	}
}

func (d *naiveDecisionStateMachine) cancel() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleCompletionEvent() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleInitiatedEvent() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleInitiationFailedEvent() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleStartedEvent() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleCanceledEvent() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleCancelFailedEvent() {
	panic("unsupported operation")
}

func (d *naiveDecisionStateMachine) handleCancelInitiatedEvent() {
	panic("unsupported operation")
}

func (d *cancelExternalWorkflowDecisionStateMachine) handleInitiatedEvent() {
	switch d.state {
	case decisionStateDecisionSent:
		d.moveState(decisionStateInitiated, eventInitiated)
	default:
		d.failStateTransition(eventInitiated)
	}
}

func (d *cancelExternalWorkflowDecisionStateMachine) handleCompletionEvent() {
	switch d.state {
	case decisionStateInitiated:
		d.moveState(decisionStateCompleted, eventCompletion)
	default:
		d.failStateTransition(eventCompletion)
	}
}

func (d *signalExternalWorkflowDecisionStateMachine) handleInitiatedEvent() {
	switch d.state {
	case decisionStateDecisionSent:
		d.moveState(decisionStateInitiated, eventInitiated)
	default:
		d.failStateTransition(eventInitiated)
	}
}

func (d *signalExternalWorkflowDecisionStateMachine) handleCompletionEvent() {
	switch d.state {
	case decisionStateInitiated:
		d.moveState(decisionStateCompleted, eventCompletion)
	default:
		d.failStateTransition(eventCompletion)
	}
}

func (d *markerDecisionStateMachine) handleDecisionSent() {
	// Marker decision state machine is considered as completed once decision is sent.
	// For SideEffect/Version markers, when the history event is applied, there is no marker decision state machine yet
	// because we preload those marker events.
	// For local activity, when we apply the history event, we use it to create the marker state machine, there is no
	// other event to drive it to completed state.
	switch d.state {
	case decisionStateCreated:
		d.moveState(decisionStateCompleted, eventDecisionSent)
	}
}

func (d *upsertSearchAttributesDecisionStateMachine) handleDecisionSent() {
	// This decision is considered as completed once decision is sent.
	switch d.state {
	case decisionStateCreated:
		d.moveState(decisionStateCompleted, eventDecisionSent)
	}
}

func newDecisionsHelper() *decisionsHelper {
	return &decisionsHelper{
		orderedDecisions: list.New(),
		decisions:        make(map[decisionID]*list.Element),

		scheduledEventIDToActivityID:     make(map[int64]string),
		scheduledEventIDToCancellationID: make(map[int64]string),
		scheduledEventIDToSignalID:       make(map[int64]string),
	}
}

func (h *decisionsHelper) getDecision(id decisionID) decisionStateMachine {
	decision, ok := h.decisions[id]
	if !ok {
		panicMsg := fmt.Sprintf("unknown decision %v, possible causes are nondeterministic workflow definition code"+
			" or incompatible change in the workflow definition", id)
		panicIllegalState(panicMsg)
	}
	// Move the last update decision state machine to the back of the list.
	// Otherwise decisions (like timer cancellations) can end up out of order.
	h.orderedDecisions.MoveToBack(decision)
	return decision.Value.(decisionStateMachine)
}

func (h *decisionsHelper) addDecision(decision decisionStateMachine) {
	if _, ok := h.decisions[decision.getID()]; ok {
		panicMsg := fmt.Sprintf("adding duplicate decision %v", decision)
		panicIllegalState(panicMsg)
	}
	element := h.orderedDecisions.PushBack(decision)
	h.decisions[decision.getID()] = element
}

func (h *decisionsHelper) scheduleActivityTask(attributes *apiv1.ScheduleActivityTaskDecisionAttributes) decisionStateMachine {
	decision := h.newActivityDecisionStateMachine(attributes)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) requestCancelActivityTask(activityID string) decisionStateMachine {
	id := makeDecisionID(decisionTypeActivity, activityID)
	decision := h.getDecision(id)
	decision.cancel()
	return decision
}

func (h *decisionsHelper) handleActivityTaskClosed(activityID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeActivity, activityID))
	decision.handleCompletionEvent()
	return decision
}

func (h *decisionsHelper) handleActivityTaskScheduled(scheduledEventID int64, activityID string) {
	h.scheduledEventIDToActivityID[scheduledEventID] = activityID
	decision := h.getDecision(makeDecisionID(decisionTypeActivity, activityID))
	decision.handleInitiatedEvent()
}

func (h *decisionsHelper) handleActivityTaskCancelRequested(activityID string) {
	decision := h.getDecision(makeDecisionID(decisionTypeActivity, activityID))
	decision.handleCancelInitiatedEvent()
}

func (h *decisionsHelper) handleActivityTaskCanceled(activityID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeActivity, activityID))
	decision.handleCanceledEvent()
	return decision
}

func (h *decisionsHelper) handleRequestCancelActivityTaskFailed(activityID string) {
	decision := h.getDecision(makeDecisionID(decisionTypeActivity, activityID))
	decision.handleCancelFailedEvent()
}

func (h *decisionsHelper) getActivityID(event *apiv1.HistoryEvent) string {
	var scheduledEventID int64 = -1
	switch attr := event.Attributes.(type) {
	case *apiv1.HistoryEvent_ActivityTaskCanceledEventAttributes:
		scheduledEventID = attr.ActivityTaskCanceledEventAttributes.GetScheduledEventId()
	case *apiv1.HistoryEvent_ActivityTaskCompletedEventAttributes:
		scheduledEventID = attr.ActivityTaskCompletedEventAttributes.GetScheduledEventId()
	case *apiv1.HistoryEvent_ActivityTaskFailedEventAttributes:
		scheduledEventID = attr.ActivityTaskFailedEventAttributes.GetScheduledEventId()
	case *apiv1.HistoryEvent_ActivityTaskTimedOutEventAttributes:
		scheduledEventID = attr.ActivityTaskTimedOutEventAttributes.GetScheduledEventId()
	default:
		panicIllegalState(fmt.Sprintf("unexpected event type %v", util.GetHistoryEventType(event)))
	}

	activityID, ok := h.scheduledEventIDToActivityID[scheduledEventID]
	if !ok {
		panicIllegalState(fmt.Sprintf("unable to find activity ID for the event %v", util.HistoryEventToString(event)))
	}
	return activityID
}

func (h *decisionsHelper) recordVersionMarker(changeID string, version Version, dataConverter DataConverter) decisionStateMachine {
	markerID := fmt.Sprintf("%v_%v", versionMarkerName, changeID)
	details, err := encodeArgs(dataConverter, []interface{}{changeID, version})
	if err != nil {
		panic(err)
	}

	recordMarker := &apiv1.RecordMarkerDecisionAttributes{
		MarkerName: versionMarkerName,
		Details:    &apiv1.Payload{Data: details}, // Keep
	}

	decision := h.newMarkerDecisionStateMachine(markerID, recordMarker)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) recordSideEffectMarker(sideEffectID int32, data []byte) decisionStateMachine {
	markerID := fmt.Sprintf("%v_%v", sideEffectMarkerName, sideEffectID)
	attributes := &apiv1.RecordMarkerDecisionAttributes{
		MarkerName: sideEffectMarkerName,
		Details:    &apiv1.Payload{Data: data},
	}
	decision := h.newMarkerDecisionStateMachine(markerID, attributes)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) recordLocalActivityMarker(activityID string, result []byte) decisionStateMachine {
	markerID := fmt.Sprintf("%v_%v", localActivityMarkerName, activityID)
	attributes := &apiv1.RecordMarkerDecisionAttributes{
		MarkerName: localActivityMarkerName,
		Details:    &apiv1.Payload{Data: result},
	}
	decision := h.newMarkerDecisionStateMachine(markerID, attributes)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) recordMutableSideEffectMarker(mutableSideEffectID string, data []byte) decisionStateMachine {
	markerID := fmt.Sprintf("%v_%v", mutableSideEffectMarkerName, mutableSideEffectID)
	attributes := &apiv1.RecordMarkerDecisionAttributes{
		MarkerName: mutableSideEffectMarkerName,
		Details:    &apiv1.Payload{Data: data},
	}
	decision := h.newMarkerDecisionStateMachine(markerID, attributes)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) startChildWorkflowExecution(attributes *apiv1.StartChildWorkflowExecutionDecisionAttributes) decisionStateMachine {
	decision := h.newChildWorkflowDecisionStateMachine(attributes)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) handleStartChildWorkflowExecutionInitiated(workflowID string) {
	decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
	decision.handleInitiatedEvent()
}

func (h *decisionsHelper) handleStartChildWorkflowExecutionFailed(workflowID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
	decision.handleInitiationFailedEvent()
	return decision
}

func (h *decisionsHelper) requestCancelExternalWorkflowExecution(domain, workflowID, runID string, cancellationID string, childWorkflowOnly bool) decisionStateMachine {
	if childWorkflowOnly {
		// For cancellation of child workflow only, we do not use cancellation ID
		// since the child workflow cancellation go through the existing child workflow
		// state machine, and we use workflow ID as identifier
		// we also do not use run ID, since child workflow can do continue-as-new
		// which will have different run ID
		// there will be server side validation that target workflow is child workflow

		// sanity check that cancellation ID is not set
		if len(cancellationID) != 0 {
			panic("cancellation on child workflow should not use cancellation ID")
		}
		// sanity check that run ID is not set
		if len(runID) != 0 {
			panic("cancellation on child workflow should not use run ID")
		}
		// targeting child workflow
		decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
		decision.cancel()
		return decision
	}

	// For cancellation of external workflow, we have to use cancellation ID
	// to identify different cancellation request (decision) / response (history event)
	// client can also use this code path to cancel its own child workflow, however, there will
	// be no server side validation that target workflow is the child

	// sanity check that cancellation ID is set
	if len(cancellationID) == 0 {
		panic("cancellation on external workflow should use cancellation ID")
	}
	attributes := &apiv1.RequestCancelExternalWorkflowExecutionDecisionAttributes{
		Domain:            domain,
		WorkflowExecution: &apiv1.WorkflowExecution{
			WorkflowId:        workflowID,
			RunId:             runID,
		},
		Control:           []byte(cancellationID),
		ChildWorkflowOnly: false,
	}
	decision := h.newCancelExternalWorkflowStateMachine(attributes, cancellationID)
	h.addDecision(decision)

	return decision
}

func (h *decisionsHelper) handleRequestCancelExternalWorkflowExecutionInitiated(initiatedeventID int64, workflowID, cancellationID string) {
	if h.isCancelExternalWorkflowEventForChildWorkflow(cancellationID) {
		// this is cancellation for child workflow only
		decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
		decision.handleCancelInitiatedEvent()
	} else {
		// this is cancellation for external workflow
		h.scheduledEventIDToCancellationID[initiatedeventID] = cancellationID
		decision := h.getDecision(makeDecisionID(decisionTypeCancellation, cancellationID))
		decision.handleInitiatedEvent()
	}
}

func (h *decisionsHelper) handleExternalWorkflowExecutionCancelRequested(initiatedeventID int64, workflowID string) (bool, decisionStateMachine) {
	var decision decisionStateMachine
	cancellationID, isExternal := h.scheduledEventIDToCancellationID[initiatedeventID]
	if !isExternal {
		decision = h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
		// no state change for child workflow, it is still in CancellationDecisionSent
	} else {
		// this is cancellation for external workflow
		decision = h.getDecision(makeDecisionID(decisionTypeCancellation, cancellationID))
		decision.handleCompletionEvent()
	}
	return isExternal, decision
}

func (h *decisionsHelper) handleRequestCancelExternalWorkflowExecutionFailed(initiatedeventID int64, workflowID string) (bool, decisionStateMachine) {
	var decision decisionStateMachine
	cancellationID, isExternal := h.scheduledEventIDToCancellationID[initiatedeventID]
	if !isExternal {
		// this is cancellation for child workflow only
		decision = h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
		decision.handleCancelFailedEvent()
	} else {
		// this is cancellation for external workflow
		decision = h.getDecision(makeDecisionID(decisionTypeCancellation, cancellationID))
		decision.handleCompletionEvent()
	}
	return isExternal, decision
}

func (h *decisionsHelper) signalExternalWorkflowExecution(domain, workflowID, runID, signalName string, input []byte, signalID string, childWorkflowOnly bool) decisionStateMachine {
	attributes := &apiv1.SignalExternalWorkflowExecutionDecisionAttributes{
		Domain: domain,
		WorkflowExecution: &apiv1.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
		SignalName:        signalName,
		Input:             &apiv1.Payload{Data: input},
		Control:           []byte(signalID),
		ChildWorkflowOnly: childWorkflowOnly,
	}
	decision := h.newSignalExternalWorkflowStateMachine(attributes, signalID)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) upsertSearchAttributes(upsertID string, searchAttr *apiv1.SearchAttributes) decisionStateMachine {
	attributes := &apiv1.UpsertWorkflowSearchAttributesDecisionAttributes{
		SearchAttributes: searchAttr,
	}
	decision := h.newUpsertSearchAttributesStateMachine(attributes, upsertID)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) handleSignalExternalWorkflowExecutionInitiated(initiatedEventID int64, signalID string) {
	h.scheduledEventIDToSignalID[initiatedEventID] = signalID
	decision := h.getDecision(makeDecisionID(decisionTypeSignal, signalID))
	decision.handleInitiatedEvent()
}

func (h *decisionsHelper) handleSignalExternalWorkflowExecutionCompleted(initiatedEventID int64) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeSignal, h.getSignalID(initiatedEventID)))
	decision.handleCompletionEvent()
	return decision
}

func (h *decisionsHelper) handleSignalExternalWorkflowExecutionFailed(initiatedEventID int64) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeSignal, h.getSignalID(initiatedEventID)))
	decision.handleCompletionEvent()
	return decision
}

func (h *decisionsHelper) getSignalID(initiatedEventID int64) string {
	signalID, ok := h.scheduledEventIDToSignalID[initiatedEventID]
	if !ok {
		panic(fmt.Sprintf("unable to find signal ID: %v", initiatedEventID))
	}
	return signalID
}

func (h *decisionsHelper) startTimer(attributes *apiv1.StartTimerDecisionAttributes) decisionStateMachine {
	decision := h.newTimerDecisionStateMachine(attributes)
	h.addDecision(decision)
	return decision
}

func (h *decisionsHelper) cancelTimer(timerID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeTimer, timerID))
	decision.cancel()
	return decision
}

func (h *decisionsHelper) handleTimerClosed(timerID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeTimer, timerID))
	decision.handleCompletionEvent()
	return decision
}

func (h *decisionsHelper) handleTimerStarted(timerID string) {
	decision := h.getDecision(makeDecisionID(decisionTypeTimer, timerID))
	decision.handleInitiatedEvent()
}

func (h *decisionsHelper) handleTimerCanceled(timerID string) {
	decision := h.getDecision(makeDecisionID(decisionTypeTimer, timerID))
	decision.handleCanceledEvent()
}

func (h *decisionsHelper) handleCancelTimerFailed(timerID string) {
	decision := h.getDecision(makeDecisionID(decisionTypeTimer, timerID))
	decision.handleCancelFailedEvent()
}

func (h *decisionsHelper) handleChildWorkflowExecutionStarted(workflowID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
	decision.handleStartedEvent()
	return decision
}

func (h *decisionsHelper) handleChildWorkflowExecutionClosed(workflowID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
	decision.handleCompletionEvent()
	return decision
}

func (h *decisionsHelper) handleChildWorkflowExecutionCanceled(workflowID string) decisionStateMachine {
	decision := h.getDecision(makeDecisionID(decisionTypeChildWorkflow, workflowID))
	decision.handleCanceledEvent()
	return decision
}

func (h *decisionsHelper) getDecisions(markAsSent bool) []*apiv1.Decision {
	var result []*apiv1.Decision
	for curr := h.orderedDecisions.Front(); curr != nil; {
		next := curr.Next() // get next item here as we might need to remove curr in the loop
		d := curr.Value.(decisionStateMachine)
		decision := d.getDecision()
		if decision != nil {
			result = append(result, decision)
		}

		if markAsSent {
			d.handleDecisionSent()
		}

		// remove completed decision state machines
		if d.getState() == decisionStateCompleted {
			h.orderedDecisions.Remove(curr)
			delete(h.decisions, d.getID())
		}

		curr = next
	}

	return result
}

func (h *decisionsHelper) isCancelExternalWorkflowEventForChildWorkflow(cancellationID string) bool {
	// the cancellationID, i.e. Control in RequestCancelExternalWorkflowExecutionInitiatedEventAttributes
	// will be empty if the event is for child workflow.
	// for cancellation external workflow, Control in RequestCancelExternalWorkflowExecutionInitiatedEventAttributes
	// will have a client generated sequence ID
	return len(cancellationID) == 0
}
