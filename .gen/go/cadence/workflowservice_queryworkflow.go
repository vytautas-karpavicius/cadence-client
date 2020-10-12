// Copyright (c) 2017-2020 Uber Technologies Inc.
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

// Code generated by thriftrw v1.11.0. DO NOT EDIT.
// @generated

package cadence

import (
	"errors"
	"fmt"
	"go.uber.org/cadence/.gen/go/shared"
	"go.uber.org/thriftrw/wire"
	"strings"
)

// WorkflowService_QueryWorkflow_Args represents the arguments for the WorkflowService.QueryWorkflow function.
//
// The arguments for QueryWorkflow are sent and received over the wire as this struct.
type WorkflowService_QueryWorkflow_Args struct {
	QueryRequest *shared.QueryWorkflowRequest `json:"queryRequest,omitempty"`
}

// ToWire translates a WorkflowService_QueryWorkflow_Args struct into a Thrift-level intermediate
// representation. This intermediate representation may be serialized
// into bytes using a ThriftRW protocol implementation.
//
// An error is returned if the struct or any of its fields failed to
// validate.
//
//   x, err := v.ToWire()
//   if err != nil {
//     return err
//   }
//
//   if err := binaryProtocol.Encode(x, writer); err != nil {
//     return err
//   }
func (v *WorkflowService_QueryWorkflow_Args) ToWire() (wire.Value, error) {
	var (
		fields [1]wire.Field
		i      int = 0
		w      wire.Value
		err    error
	)

	if v.QueryRequest != nil {
		w, err = v.QueryRequest.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 1, Value: w}
		i++
	}

	return wire.NewValueStruct(wire.Struct{Fields: fields[:i]}), nil
}

func _QueryWorkflowRequest_Read(w wire.Value) (*shared.QueryWorkflowRequest, error) {
	var v shared.QueryWorkflowRequest
	err := v.FromWire(w)
	return &v, err
}

// FromWire deserializes a WorkflowService_QueryWorkflow_Args struct from its Thrift-level
// representation. The Thrift-level representation may be obtained
// from a ThriftRW protocol implementation.
//
// An error is returned if we were unable to build a WorkflowService_QueryWorkflow_Args struct
// from the provided intermediate representation.
//
//   x, err := binaryProtocol.Decode(reader, wire.TStruct)
//   if err != nil {
//     return nil, err
//   }
//
//   var v WorkflowService_QueryWorkflow_Args
//   if err := v.FromWire(x); err != nil {
//     return nil, err
//   }
//   return &v, nil
func (v *WorkflowService_QueryWorkflow_Args) FromWire(w wire.Value) error {
	var err error

	for _, field := range w.GetStruct().Fields {
		switch field.ID {
		case 1:
			if field.Value.Type() == wire.TStruct {
				v.QueryRequest, err = _QueryWorkflowRequest_Read(field.Value)
				if err != nil {
					return err
				}

			}
		}
	}

	return nil
}

// String returns a readable string representation of a WorkflowService_QueryWorkflow_Args
// struct.
func (v *WorkflowService_QueryWorkflow_Args) String() string {
	if v == nil {
		return "<nil>"
	}

	var fields [1]string
	i := 0
	if v.QueryRequest != nil {
		fields[i] = fmt.Sprintf("QueryRequest: %v", v.QueryRequest)
		i++
	}

	return fmt.Sprintf("WorkflowService_QueryWorkflow_Args{%v}", strings.Join(fields[:i], ", "))
}

// Equals returns true if all the fields of this WorkflowService_QueryWorkflow_Args match the
// provided WorkflowService_QueryWorkflow_Args.
//
// This function performs a deep comparison.
func (v *WorkflowService_QueryWorkflow_Args) Equals(rhs *WorkflowService_QueryWorkflow_Args) bool {
	if !((v.QueryRequest == nil && rhs.QueryRequest == nil) || (v.QueryRequest != nil && rhs.QueryRequest != nil && v.QueryRequest.Equals(rhs.QueryRequest))) {
		return false
	}

	return true
}

// MethodName returns the name of the Thrift function as specified in
// the IDL, for which this struct represent the arguments.
//
// This will always be "QueryWorkflow" for this struct.
func (v *WorkflowService_QueryWorkflow_Args) MethodName() string {
	return "QueryWorkflow"
}

// EnvelopeType returns the kind of value inside this struct.
//
// This will always be Call for this struct.
func (v *WorkflowService_QueryWorkflow_Args) EnvelopeType() wire.EnvelopeType {
	return wire.Call
}

// WorkflowService_QueryWorkflow_Helper provides functions that aid in handling the
// parameters and return values of the WorkflowService.QueryWorkflow
// function.
var WorkflowService_QueryWorkflow_Helper = struct {
	// Args accepts the parameters of QueryWorkflow in-order and returns
	// the arguments struct for the function.
	Args func(
		queryRequest *shared.QueryWorkflowRequest,
	) *WorkflowService_QueryWorkflow_Args

	// IsException returns true if the given error can be thrown
	// by QueryWorkflow.
	//
	// An error can be thrown by QueryWorkflow only if the
	// corresponding exception type was mentioned in the 'throws'
	// section for it in the Thrift file.
	IsException func(error) bool

	// WrapResponse returns the result struct for QueryWorkflow
	// given its return value and error.
	//
	// This allows mapping values and errors returned by
	// QueryWorkflow into a serializable result struct.
	// WrapResponse returns a non-nil error if the provided
	// error cannot be thrown by QueryWorkflow
	//
	//   value, err := QueryWorkflow(args)
	//   result, err := WorkflowService_QueryWorkflow_Helper.WrapResponse(value, err)
	//   if err != nil {
	//     return fmt.Errorf("unexpected error from QueryWorkflow: %v", err)
	//   }
	//   serialize(result)
	WrapResponse func(*shared.QueryWorkflowResponse, error) (*WorkflowService_QueryWorkflow_Result, error)

	// UnwrapResponse takes the result struct for QueryWorkflow
	// and returns the value or error returned by it.
	//
	// The error is non-nil only if QueryWorkflow threw an
	// exception.
	//
	//   result := deserialize(bytes)
	//   value, err := WorkflowService_QueryWorkflow_Helper.UnwrapResponse(result)
	UnwrapResponse func(*WorkflowService_QueryWorkflow_Result) (*shared.QueryWorkflowResponse, error)
}{}

func init() {
	WorkflowService_QueryWorkflow_Helper.Args = func(
		queryRequest *shared.QueryWorkflowRequest,
	) *WorkflowService_QueryWorkflow_Args {
		return &WorkflowService_QueryWorkflow_Args{
			QueryRequest: queryRequest,
		}
	}

	WorkflowService_QueryWorkflow_Helper.IsException = func(err error) bool {
		switch err.(type) {
		case *shared.BadRequestError:
			return true
		case *shared.EntityNotExistsError:
			return true
		case *shared.QueryFailedError:
			return true
		case *shared.LimitExceededError:
			return true
		case *shared.ServiceBusyError:
			return true
		case *shared.ClientVersionNotSupportedError:
			return true
		default:
			return false
		}
	}

	WorkflowService_QueryWorkflow_Helper.WrapResponse = func(success *shared.QueryWorkflowResponse, err error) (*WorkflowService_QueryWorkflow_Result, error) {
		if err == nil {
			return &WorkflowService_QueryWorkflow_Result{Success: success}, nil
		}

		switch e := err.(type) {
		case *shared.BadRequestError:
			if e == nil {
				return nil, errors.New("WrapResponse received non-nil error type with nil value for WorkflowService_QueryWorkflow_Result.BadRequestError")
			}
			return &WorkflowService_QueryWorkflow_Result{BadRequestError: e}, nil
		case *shared.EntityNotExistsError:
			if e == nil {
				return nil, errors.New("WrapResponse received non-nil error type with nil value for WorkflowService_QueryWorkflow_Result.EntityNotExistError")
			}
			return &WorkflowService_QueryWorkflow_Result{EntityNotExistError: e}, nil
		case *shared.QueryFailedError:
			if e == nil {
				return nil, errors.New("WrapResponse received non-nil error type with nil value for WorkflowService_QueryWorkflow_Result.QueryFailedError")
			}
			return &WorkflowService_QueryWorkflow_Result{QueryFailedError: e}, nil
		case *shared.LimitExceededError:
			if e == nil {
				return nil, errors.New("WrapResponse received non-nil error type with nil value for WorkflowService_QueryWorkflow_Result.LimitExceededError")
			}
			return &WorkflowService_QueryWorkflow_Result{LimitExceededError: e}, nil
		case *shared.ServiceBusyError:
			if e == nil {
				return nil, errors.New("WrapResponse received non-nil error type with nil value for WorkflowService_QueryWorkflow_Result.ServiceBusyError")
			}
			return &WorkflowService_QueryWorkflow_Result{ServiceBusyError: e}, nil
		case *shared.ClientVersionNotSupportedError:
			if e == nil {
				return nil, errors.New("WrapResponse received non-nil error type with nil value for WorkflowService_QueryWorkflow_Result.ClientVersionNotSupportedError")
			}
			return &WorkflowService_QueryWorkflow_Result{ClientVersionNotSupportedError: e}, nil
		}

		return nil, err
	}
	WorkflowService_QueryWorkflow_Helper.UnwrapResponse = func(result *WorkflowService_QueryWorkflow_Result) (success *shared.QueryWorkflowResponse, err error) {
		if result.BadRequestError != nil {
			err = result.BadRequestError
			return
		}
		if result.EntityNotExistError != nil {
			err = result.EntityNotExistError
			return
		}
		if result.QueryFailedError != nil {
			err = result.QueryFailedError
			return
		}
		if result.LimitExceededError != nil {
			err = result.LimitExceededError
			return
		}
		if result.ServiceBusyError != nil {
			err = result.ServiceBusyError
			return
		}
		if result.ClientVersionNotSupportedError != nil {
			err = result.ClientVersionNotSupportedError
			return
		}

		if result.Success != nil {
			success = result.Success
			return
		}

		err = errors.New("expected a non-void result")
		return
	}

}

// WorkflowService_QueryWorkflow_Result represents the result of a WorkflowService.QueryWorkflow function call.
//
// The result of a QueryWorkflow execution is sent and received over the wire as this struct.
//
// Success is set only if the function did not throw an exception.
type WorkflowService_QueryWorkflow_Result struct {
	// Value returned by QueryWorkflow after a successful execution.
	Success                        *shared.QueryWorkflowResponse          `json:"success,omitempty"`
	BadRequestError                *shared.BadRequestError                `json:"badRequestError,omitempty"`
	EntityNotExistError            *shared.EntityNotExistsError           `json:"entityNotExistError,omitempty"`
	QueryFailedError               *shared.QueryFailedError               `json:"queryFailedError,omitempty"`
	LimitExceededError             *shared.LimitExceededError             `json:"limitExceededError,omitempty"`
	ServiceBusyError               *shared.ServiceBusyError               `json:"serviceBusyError,omitempty"`
	ClientVersionNotSupportedError *shared.ClientVersionNotSupportedError `json:"clientVersionNotSupportedError,omitempty"`
}

// ToWire translates a WorkflowService_QueryWorkflow_Result struct into a Thrift-level intermediate
// representation. This intermediate representation may be serialized
// into bytes using a ThriftRW protocol implementation.
//
// An error is returned if the struct or any of its fields failed to
// validate.
//
//   x, err := v.ToWire()
//   if err != nil {
//     return err
//   }
//
//   if err := binaryProtocol.Encode(x, writer); err != nil {
//     return err
//   }
func (v *WorkflowService_QueryWorkflow_Result) ToWire() (wire.Value, error) {
	var (
		fields [7]wire.Field
		i      int = 0
		w      wire.Value
		err    error
	)

	if v.Success != nil {
		w, err = v.Success.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 0, Value: w}
		i++
	}
	if v.BadRequestError != nil {
		w, err = v.BadRequestError.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 1, Value: w}
		i++
	}
	if v.EntityNotExistError != nil {
		w, err = v.EntityNotExistError.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 3, Value: w}
		i++
	}
	if v.QueryFailedError != nil {
		w, err = v.QueryFailedError.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 4, Value: w}
		i++
	}
	if v.LimitExceededError != nil {
		w, err = v.LimitExceededError.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 5, Value: w}
		i++
	}
	if v.ServiceBusyError != nil {
		w, err = v.ServiceBusyError.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 6, Value: w}
		i++
	}
	if v.ClientVersionNotSupportedError != nil {
		w, err = v.ClientVersionNotSupportedError.ToWire()
		if err != nil {
			return w, err
		}
		fields[i] = wire.Field{ID: 7, Value: w}
		i++
	}

	if i != 1 {
		return wire.Value{}, fmt.Errorf("WorkflowService_QueryWorkflow_Result should have exactly one field: got %v fields", i)
	}

	return wire.NewValueStruct(wire.Struct{Fields: fields[:i]}), nil
}

func _QueryWorkflowResponse_Read(w wire.Value) (*shared.QueryWorkflowResponse, error) {
	var v shared.QueryWorkflowResponse
	err := v.FromWire(w)
	return &v, err
}

func _QueryFailedError_Read(w wire.Value) (*shared.QueryFailedError, error) {
	var v shared.QueryFailedError
	err := v.FromWire(w)
	return &v, err
}

// FromWire deserializes a WorkflowService_QueryWorkflow_Result struct from its Thrift-level
// representation. The Thrift-level representation may be obtained
// from a ThriftRW protocol implementation.
//
// An error is returned if we were unable to build a WorkflowService_QueryWorkflow_Result struct
// from the provided intermediate representation.
//
//   x, err := binaryProtocol.Decode(reader, wire.TStruct)
//   if err != nil {
//     return nil, err
//   }
//
//   var v WorkflowService_QueryWorkflow_Result
//   if err := v.FromWire(x); err != nil {
//     return nil, err
//   }
//   return &v, nil
func (v *WorkflowService_QueryWorkflow_Result) FromWire(w wire.Value) error {
	var err error

	for _, field := range w.GetStruct().Fields {
		switch field.ID {
		case 0:
			if field.Value.Type() == wire.TStruct {
				v.Success, err = _QueryWorkflowResponse_Read(field.Value)
				if err != nil {
					return err
				}

			}
		case 1:
			if field.Value.Type() == wire.TStruct {
				v.BadRequestError, err = _BadRequestError_Read(field.Value)
				if err != nil {
					return err
				}

			}
		case 3:
			if field.Value.Type() == wire.TStruct {
				v.EntityNotExistError, err = _EntityNotExistsError_Read(field.Value)
				if err != nil {
					return err
				}

			}
		case 4:
			if field.Value.Type() == wire.TStruct {
				v.QueryFailedError, err = _QueryFailedError_Read(field.Value)
				if err != nil {
					return err
				}

			}
		case 5:
			if field.Value.Type() == wire.TStruct {
				v.LimitExceededError, err = _LimitExceededError_Read(field.Value)
				if err != nil {
					return err
				}

			}
		case 6:
			if field.Value.Type() == wire.TStruct {
				v.ServiceBusyError, err = _ServiceBusyError_Read(field.Value)
				if err != nil {
					return err
				}

			}
		case 7:
			if field.Value.Type() == wire.TStruct {
				v.ClientVersionNotSupportedError, err = _ClientVersionNotSupportedError_Read(field.Value)
				if err != nil {
					return err
				}

			}
		}
	}

	count := 0
	if v.Success != nil {
		count++
	}
	if v.BadRequestError != nil {
		count++
	}
	if v.EntityNotExistError != nil {
		count++
	}
	if v.QueryFailedError != nil {
		count++
	}
	if v.LimitExceededError != nil {
		count++
	}
	if v.ServiceBusyError != nil {
		count++
	}
	if v.ClientVersionNotSupportedError != nil {
		count++
	}
	if count != 1 {
		return fmt.Errorf("WorkflowService_QueryWorkflow_Result should have exactly one field: got %v fields", count)
	}

	return nil
}

// String returns a readable string representation of a WorkflowService_QueryWorkflow_Result
// struct.
func (v *WorkflowService_QueryWorkflow_Result) String() string {
	if v == nil {
		return "<nil>"
	}

	var fields [7]string
	i := 0
	if v.Success != nil {
		fields[i] = fmt.Sprintf("Success: %v", v.Success)
		i++
	}
	if v.BadRequestError != nil {
		fields[i] = fmt.Sprintf("BadRequestError: %v", v.BadRequestError)
		i++
	}
	if v.EntityNotExistError != nil {
		fields[i] = fmt.Sprintf("EntityNotExistError: %v", v.EntityNotExistError)
		i++
	}
	if v.QueryFailedError != nil {
		fields[i] = fmt.Sprintf("QueryFailedError: %v", v.QueryFailedError)
		i++
	}
	if v.LimitExceededError != nil {
		fields[i] = fmt.Sprintf("LimitExceededError: %v", v.LimitExceededError)
		i++
	}
	if v.ServiceBusyError != nil {
		fields[i] = fmt.Sprintf("ServiceBusyError: %v", v.ServiceBusyError)
		i++
	}
	if v.ClientVersionNotSupportedError != nil {
		fields[i] = fmt.Sprintf("ClientVersionNotSupportedError: %v", v.ClientVersionNotSupportedError)
		i++
	}

	return fmt.Sprintf("WorkflowService_QueryWorkflow_Result{%v}", strings.Join(fields[:i], ", "))
}

// Equals returns true if all the fields of this WorkflowService_QueryWorkflow_Result match the
// provided WorkflowService_QueryWorkflow_Result.
//
// This function performs a deep comparison.
func (v *WorkflowService_QueryWorkflow_Result) Equals(rhs *WorkflowService_QueryWorkflow_Result) bool {
	if !((v.Success == nil && rhs.Success == nil) || (v.Success != nil && rhs.Success != nil && v.Success.Equals(rhs.Success))) {
		return false
	}
	if !((v.BadRequestError == nil && rhs.BadRequestError == nil) || (v.BadRequestError != nil && rhs.BadRequestError != nil && v.BadRequestError.Equals(rhs.BadRequestError))) {
		return false
	}
	if !((v.EntityNotExistError == nil && rhs.EntityNotExistError == nil) || (v.EntityNotExistError != nil && rhs.EntityNotExistError != nil && v.EntityNotExistError.Equals(rhs.EntityNotExistError))) {
		return false
	}
	if !((v.QueryFailedError == nil && rhs.QueryFailedError == nil) || (v.QueryFailedError != nil && rhs.QueryFailedError != nil && v.QueryFailedError.Equals(rhs.QueryFailedError))) {
		return false
	}
	if !((v.LimitExceededError == nil && rhs.LimitExceededError == nil) || (v.LimitExceededError != nil && rhs.LimitExceededError != nil && v.LimitExceededError.Equals(rhs.LimitExceededError))) {
		return false
	}
	if !((v.ServiceBusyError == nil && rhs.ServiceBusyError == nil) || (v.ServiceBusyError != nil && rhs.ServiceBusyError != nil && v.ServiceBusyError.Equals(rhs.ServiceBusyError))) {
		return false
	}
	if !((v.ClientVersionNotSupportedError == nil && rhs.ClientVersionNotSupportedError == nil) || (v.ClientVersionNotSupportedError != nil && rhs.ClientVersionNotSupportedError != nil && v.ClientVersionNotSupportedError.Equals(rhs.ClientVersionNotSupportedError))) {
		return false
	}

	return true
}

// MethodName returns the name of the Thrift function as specified in
// the IDL, for which this struct represent the result.
//
// This will always be "QueryWorkflow" for this struct.
func (v *WorkflowService_QueryWorkflow_Result) MethodName() string {
	return "QueryWorkflow"
}

// EnvelopeType returns the kind of value inside this struct.
//
// This will always be Reply for this struct.
func (v *WorkflowService_QueryWorkflow_Result) EnvelopeType() wire.EnvelopeType {
	return wire.Reply
}
