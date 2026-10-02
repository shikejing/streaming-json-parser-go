package jsonstream

import (
	"errors"
	"strconv"
)

// ValueKind identifies the kind of a matched scalar value.
type ValueKind int

const (
	VString ValueKind = iota
	VNumber
	VBool
	VNull
)

// Value is a decoded scalar JSON value.
type Value struct {
	Kind ValueKind
	Str  string // decoded string for VString; raw number text for VNumber
	Bool bool   // for VBool

	i    int64
	f    float64
	hasI bool
	hasF bool
}

// String returns the string representation of the value.
func (v Value) String() string {
	switch v.Kind {
	case VString:
		return v.Str
	case VNumber:
		return v.Str
	case VBool:
		return strconv.FormatBool(v.Bool)
	case VNull:
		return "null"
	}
	return ""
}

// Int64 returns the value as int64 for integer numbers.
func (v Value) Int64() (int64, error) {
	if v.Kind != VNumber {
		return 0, errors.New("jsonstream: value is not a number")
	}
	if v.hasI {
		return v.i, nil
	}
	return 0, errors.New("jsonstream: number is not an integer")
}

// Float64 returns the value as float64 for numbers.
func (v Value) Float64() (float64, error) {
	if v.Kind != VNumber {
		return 0, errors.New("jsonstream: value is not a number")
	}
	if v.hasF {
		return v.f, nil
	}
	return 0, errors.New("jsonstream: number cannot be represented as float64")
}

// BoolValue returns the value for booleans.
func (v Value) BoolValue() (bool, error) {
	if v.Kind != VBool {
		return false, errors.New("jsonstream: value is not a boolean")
	}
	return v.Bool, nil
}

// IsNull reports whether the value is JSON null.
func (v Value) IsNull() bool { return v.Kind == VNull }
