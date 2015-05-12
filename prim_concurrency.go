// Copyright 2014 SteelSeries ApS.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This package implements a basic LISP interpretor for embedding in a go program for scripting.
// This file contains the concurrency primitive functions.

package golisp

import (
	"fmt"
	"time"
	"unsafe"
	"runtime"
	"strings"
)

type Process struct {
	Env           *SymbolTableFrame
	Code          *Data
	Wake          chan bool
	Abort         chan bool
	Restart       chan bool
	ScheduleTimer *time.Timer
}

func RegisterConcurrencyPrimitives() {
	MakePrimitiveFunction("fork", 1, ForkImpl)
	MakePrimitiveFunction("proc-sleep", 2, ProcSleepImpl)
	MakePrimitiveFunction("wake", 1, WakeImpl)
	MakePrimitiveFunction("schedule", 2, ScheduleImpl)
	MakePrimitiveFunction("reset-timeout", 1, ResetTimeoutImpl)
	MakePrimitiveFunction("abandon", 1, AbandonImpl)
}

func ForkImpl(args *Data, env *SymbolTableFrame) (result *Data, err error) {
	f, err := Eval(Car(args), env)
	if err != nil {
		return
	}

	if !FunctionP(f) {
		err = ProcessError(fmt.Sprintf("fork expected a function, but received %v.", f), env)
		return
	}

	if FunctionValue(f).RequiredArgCount != 1 {
		err = ProcessError(fmt.Sprintf("fork expected a function with arity of 1, but it was %d.", FunctionValue(f).RequiredArgCount), env)
		return
	}

	proc := &Process{Env: env, Code: f, Wake: make(chan bool, 1), Abort: make(chan bool, 1), Restart: make(chan bool, 1)}
	procObj := ObjectWithTypeAndValue("Process", unsafe.Pointer(proc))

	go func() {
		callWithPanicProtection(func() {
			_, forkedErr := FunctionValue(f).ApplyWithoutEval(InternalMakeList(procObj), env)
			if forkedErr != nil {
				LogPrintf("error in forked process: %#v\n",forkedErr)
			}
		}, "fork")
	}()

	return procObj, nil
}

func ProcSleepImpl(args *Data, env *SymbolTableFrame) (result *Data, err error) {
	procObj, err := Eval(Car(args), env)
	if err != nil {
		return
	}

	if !ObjectP(procObj) || ObjectType(procObj) != "Process" {
		err = ProcessError(fmt.Sprintf("proc-sleep expects a Process object expected but received %s.", ObjectType(procObj)), env)
		return
	}

	proc := (*Process)(ObjectValue(procObj))

	millis, err := Eval(Cadr(args), env)
	if err != nil {
		return
	}
	if !IntegerP(millis) {
		err = ProcessError(fmt.Sprintf("proc-sleep expected an integer as a delay, but received %v.", millis), env)
		return
	}

	woken := false
	select {
	case <-proc.Wake:
		woken = true
	case <-time.After(time.Duration(IntegerValue(millis)) * time.Millisecond):
	}

	return BooleanWithValue(woken), nil
}

func WakeImpl(args *Data, env *SymbolTableFrame) (result *Data, err error) {
	procObj, err := Eval(Car(args), env)
	if err != nil {
		return
	}

	if !ObjectP(procObj) || ObjectType(procObj) != "Process" {
		err = ProcessError(fmt.Sprintf("wake expects a Process object expected but received %s.", ObjectType(procObj)), env)
		return
	}

	proc := (*Process)(ObjectValue(procObj))
	proc.Wake <- true
	return StringWithValue("OK"), nil
}

func ScheduleImpl(args *Data, env *SymbolTableFrame) (result *Data, err error) {
	millis, err := Eval(Car(args), env)
	if err != nil {
		return
	}
	if !IntegerP(millis) {
		err = ProcessError(fmt.Sprintf("schedule expected an integer as a delay, but received %v.", millis), env)
		return
	}
	f, err := Eval(Cadr(args), env)
	if err != nil {
		return
	}

	if !FunctionP(f) {
		err = ProcessError(fmt.Sprintf("schedule expected a function, but received %v.", f), env)
		return
	}

	if FunctionValue(f).RequiredArgCount != 1 {
		err = ProcessError(fmt.Sprintf("schedule expected a function with arity of 1, but it was %d.", FunctionValue(f).RequiredArgCount), env)
		return
	}

	proc := &Process{
		Env:           env,
		Code:          f,
		Wake:          make(chan bool, 1),
		Abort:         make(chan bool, 1),
		Restart:       make(chan bool, 1),
		ScheduleTimer: time.NewTimer(time.Duration(IntegerValue(millis)) * time.Millisecond)}
	procObj := ObjectWithTypeAndValue("Process", unsafe.Pointer(proc))

	aborted := false

	go func() {
		callWithPanicProtection(func() {
		Loop:
			for {
				select {
				case <-proc.Abort:
					aborted = true
					break Loop
				case <-proc.Restart:
					proc.ScheduleTimer.Reset(time.Duration(IntegerValue(millis)) * time.Millisecond)
				case <-proc.ScheduleTimer.C:
					_, forkedErr := FunctionValue(f).ApplyWithoutEval(InternalMakeList(procObj), env)
					if forkedErr != nil {
						LogPrintf("error in forked process: %#v\n",forkedErr)
					}
					break Loop
				}
			}
		}, "schedule")
	}()

	return procObj, nil

}

func AbandonImpl(args *Data, env *SymbolTableFrame) (result *Data, err error) {
	procObj, err := Eval(Car(args), env)
	if err != nil {
		return
	}

	if !ObjectP(procObj) || ObjectType(procObj) != "Process" {
		err = ProcessError(fmt.Sprintf("adandon expects a Process object expected but received %s.", ObjectType(procObj)), env)
		return
	}

	proc := (*Process)(ObjectValue(procObj))
	proc.Abort <- true
	return StringWithValue("OK"), nil
}

func ResetTimeoutImpl(args *Data, env *SymbolTableFrame) (result *Data, err error) {
	procObj, err := Eval(Car(args), env)
	if err != nil {
		return
	}

	if !ObjectP(procObj) || ObjectType(procObj) != "Process" {
		err = ProcessError(fmt.Sprintf("restart expects a Process object expected but received %s.", ObjectType(procObj)), env)
		return
	}

	proc := (*Process)(ObjectValue(procObj))
	var str string
	select {
	case proc.Restart <- true:
		str = "OK"
	default:
		str = "task was already completed or abandoned"
	}
	return StringWithValue(str), nil
}

func callWithPanicProtection(f func(), prefix string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stackBuf := make([]byte, 10000)
			stackBuf = stackBuf[:runtime.Stack(stackBuf, false)]
			stack := strings.Split(string(stackBuf), "\n")
			for i := 0; i < 7; i++ {
				LogPrintf("%s\n",stack[i])
			}
		}
	}()

	f()
}