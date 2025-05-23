// Copyright 2021 Roger Chapman and the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package v8go

// #include <stdlib.h>
// #include "function_template.h"
import "C"
import (
	"runtime"
	"unsafe"
)

// FunctionCallback is a callback that is executed in Go when a function is executed in JS.
type FunctionCallback func(info *FunctionCallbackInfo) *Value

// FunctionCallbackWithError is a callback that is executed in Go when
// a function is executed in JS. If a ValueError is returned, its
// value will be thrown as an exception in V8, otherwise Error() is
// invoked, and the string is thrown.
type FunctionCallbackWithError func(info *FunctionCallbackInfo) (*Value, error)

// FunctionCallbackInfo is the argument that is passed to a FunctionCallback.
type FunctionCallbackInfo struct {
	ctx  *Context
	args []*Value
	this *Object
}

// A ValueError can be returned from a FunctionCallbackWithError, and
// its value will be thrown as an exception in V8.
type ValueError interface {
	error
	Valuer
}

// Context is the current context that the callback is being executed in.
func (i *FunctionCallbackInfo) Context() *Context {
	return i.ctx
}

// This returns the receiver object "this".
func (i *FunctionCallbackInfo) This() *Object {
	return i.this
}

// Args returns a slice of the value arguments that are passed to the JS function.
func (i *FunctionCallbackInfo) Args() []*Value {
	return i.args
}

func (i *FunctionCallbackInfo) Release() {
	for _, arg := range i.args {
		arg.Release()
	}
	i.this.Release()
}

// FunctionTemplate is used to create functions at runtime.
// There can only be one function created from a FunctionTemplate in a context.
// The lifetime of the created function is equal to the lifetime of the context.
//
// A FunctionTemplate can be used to create "constructors", and add methods to
// the "class". [FunctionTemplate.PrototypeTemplate] can be used to add normal
// methods on the class, and [FunctionTemplate.InstanceTemplate] can be used to
// add fields automatically to new instances of a class.
//
// V8 API Docs: https://v8.github.io/api/head/classv8_1_1FunctionTemplate.html
type FunctionTemplate struct {
	*template
}

// NewFunctionTemplate creates a FunctionTemplate for a given
// callback. Prefer using NewFunctionTemplateWithError.
func NewFunctionTemplate(iso *Isolate, callback FunctionCallback) *FunctionTemplate {
	if callback == nil {
		panic("nil FunctionCallback argument not supported")
	}
	return NewFunctionTemplateWithError(iso, func(info *FunctionCallbackInfo) (*Value, error) {
		return callback(info), nil
	})
}

// NewFunctionTemplateWithError creates a FunctionTemplate for a given
// callback. If the callback returns an error, it will be thrown as a
// JS error.
func NewFunctionTemplateWithError(
	iso *Isolate,
	callback FunctionCallbackWithError,
) *FunctionTemplate {
	if iso == nil {
		panic("nil Isolate argument not supported")
	}
	if callback == nil {
		panic("nil FunctionCallback argument not supported")
	}

	cbref := iso.registerCallback(callback)

	tmpl := &template{
		ptr: C.NewFunctionTemplate(iso.ptr, C.int(cbref)),
		iso: iso,
	}
	runtime.SetFinalizer(tmpl, (*template).finalizer)
	return &FunctionTemplate{tmpl}
}

// GetFunction returns an instance of this function template bound to the given context.
func (tmpl *FunctionTemplate) GetFunction(ctx *Context) *Function {
	rtn := C.FunctionTemplateGetFunction(tmpl.ptr, ctx.ptr)
	runtime.KeepAlive(tmpl)
	val, err := valueResult(ctx, rtn)
	if err != nil {
		panic(err) // TODO: Consider returning the error
	}
	return &Function{val}
}

// InstanceTemplate gets the [ObjectTemplate] that is used for new object
// instances created when this function is used as a constructor.
//
// You can add functions and values to new instance using [ObjectTemplate.Set]
// and [ObjectTemplate.SetSymbol]. Those values will become [own properties] on
// the instance, not the prototype.
//
// Adding a function to an instance template corresponds to the following
// JavaScript:
//
//	class Example() {
//		constructor() {
//			this.foo = function() { /* creates a function on the instance */ }
//		}
//	}
//
// [own properties]: https://developer.mozilla.org/en-US/docs/Web/JavaScript/Enumerability_and_ownership_of_properties
func (tmpl *FunctionTemplate) InstanceTemplate() *ObjectTemplate {
	result := &template{
		ptr: C.FunctionTemplateInstanceTemplate(tmpl.ptr),
		iso: tmpl.iso,
	}
	runtime.SetFinalizer(result, (*template).finalizer)
	return &ObjectTemplate{result}
}

// PrototypeTemplate gets the [ObjectTemplate] that is used to create the
// prototype object associated with the function.
//
// You can call [ObjectTemplate.Set] or [ObjectTemplate.SetSymbol], passing a
// [FunctionTemplate] to add a "method" to the class.
//
// Adding a function to a prototype template corresponds normal method on a
// JavaScript "class":
//
//	class Example {
//		foo() { /* this is a method on the prototype */ }
//	}
//
// Or the old-school way
//
//	function Example() {}
//	Example.prototype.foo = function() { }
//
// The function becomes an [own property] on the prototype, not the instance.
//
// [own property]: https://developer.mozilla.org/en-US/docs/Web/JavaScript/Enumerability_and_ownership_of_properties
func (tmpl *FunctionTemplate) PrototypeTemplate() *ObjectTemplate {
	result := &template{
		ptr: C.FunctionTemplatePrototypeTemplate(tmpl.ptr),
		iso: tmpl.iso,
	}
	runtime.SetFinalizer(result, (*template).finalizer)
	return &ObjectTemplate{result}
}

func (tmpl *FunctionTemplate) Inherit(base *FunctionTemplate) {
	C.FunctionTemplateInherit(tmpl.ptr, base.ptr)
}

// Note that ideally `thisAndArgs` would be split into two separate arguments, but they were combined
// to workaround an ERROR_COMMITMENT_LIMIT error on windows that was detected in CI.
//
//export goFunctionCallback
func goFunctionCallback(
	ctxref int,
	cbref int,
	thisAndArgs *C.ValuePtr,
	argsCount int,
) (rval C.ValuePtr, rerr C.ValuePtr) {
	ctx := getContext(ctxref)

	this := *thisAndArgs
	info := &FunctionCallbackInfo{
		ctx:  ctx,
		this: &Object{&Value{ptr: this, ctx: ctx}},
		args: make([]*Value, argsCount),
	}

	argv := (*[1 << 30]C.ValuePtr)(unsafe.Pointer(thisAndArgs))[1 : argsCount+1 : argsCount+1]
	for i, v := range argv {
		val := &Value{ptr: v, ctx: ctx}
		info.args[i] = val
	}

	callbackFunc := ctx.iso.getCallback(cbref)
	val, err := callbackFunc(info)
	if err != nil {
		if verr, ok := err.(ValueError); ok {
			return nil, verr.value().ptr
		}
		errv, err := NewValue(ctx.iso, err.Error())
		if err != nil {
			panic(err)
		}
		return nil, errv.ptr
	}
	if val == nil {
		return nil, nil
	}
	return val.ptr, nil
}
