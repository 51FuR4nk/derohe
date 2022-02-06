// Copyright (C) 2017 Michael J. Fromberger. All Rights Reserved.

// Package jctx implements an encoder and decoder for request context values,
// allowing context metadata to be propagated through JSON-RPC.
//
// A context.Context value carries request-scoped values across API boundaries
// and between processes. The jrpc2 package has hooks to allow clients and
// servers to propagate context values transparently through JSON-RPC calls.
// The jctx package provides functions that implement these hooks.
//
// The jrpc2 context plumbing works by injecting a wrapper message around the
// request parameters. The client adds this wrapper during the call, and the
// server removes it. The actual client parameters are embedded inside the
// wrapper unmodified.
//
// The format of the wrapper generated by this package is:
//
//    {
//      "jctx": "1",
//      "payload":  <original-params>,
//      "deadline": <rfc-3339-timestamp>,
//      "meta":     <json-value>
//    }
//
// Of these, only the "jctx" marker is required; the others are assumed to be
// empty if they do not appear in the message.
//
// Deadlines and Timeouts
//
// If the parent context contains a deadline, it is encoded into the wrapper as
// an RFC 3339 timestamp in UTC, for example "2009-11-10T23:00:00.00000015Z".
//
// Metadata
//
// The jctx.WithMetadata function allows the caller to attach an arbitrary
// JSON-encoded value to a context. This value will be transmitted over the
// wire during a JSON-RPC call. The recipient can decode this value from the
// context using the jctx.UnmarshalMetadata function.
//
package jctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const wireVersion = "1"

// wireContext is the encoded representation of a context value. It includes
// the deadline together with an underlying payload carrying the original
// request parameters. The resulting message replaces the parameters of the
// original JSON-RPC request.
type wireContext struct {
	V *string `json:"jctx"` // must be wireVersion

	Deadline *time.Time      `json:"deadline,omitempty"` // encoded in UTC
	Payload  json.RawMessage `json:"payload,omitempty"`
	Metadata json.RawMessage `json:"meta,omitempty"`
}

// Encode encodes the specified context and request parameters for transmission.
// If a deadline is set on ctx, it is converted to UTC before encoding.
// If metadata are set on ctx (see jctx.WithMetadata), they are included.
func Encode(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	v := wireVersion
	c := wireContext{V: &v, Payload: params}
	if dl, ok := ctx.Deadline(); ok {
		utcdl := dl.UTC()
		c.Deadline = &utcdl
	}

	// If there are metadata in the context, attach them.
	if v := ctx.Value(metadataKey{}); v != nil {
		c.Metadata = v.(json.RawMessage)
	}

	return json.Marshal(c)
}

// Decode decodes the specified request message as a context-wrapped request,
// and returns the updated context (based on ctx) and the embedded parameters.
// If the request does not have a context wrapper, it is returned as-is.
//
// If the encoded request specifies a deadline, that deadline is set in the
// context value returned.
//
// If the request includes context metadata, they are attached and can be
// recovered using jctx.UnmarshalMetadata.
func Decode(ctx context.Context, method string, req json.RawMessage) (context.Context, json.RawMessage, error) {
	if len(req) == 0 || req[0] != '{' {
		return ctx, req, nil // an empty message or non-object has no wrapper
	}
	var c wireContext
	if err := json.Unmarshal(req, &c); err != nil || c.V == nil {
		return ctx, req, nil // fall back assuming an un-wrapped message
	} else if *c.V != wireVersion {
		return nil, nil, fmt.Errorf("invalid context version %q", *c.V)
	}
	if c.Metadata != nil {
		ctx = context.WithValue(ctx, metadataKey{}, c.Metadata)
	}
	if c.Deadline != nil && !c.Deadline.IsZero() {
		var ignored context.CancelFunc
		ctx, ignored = context.WithDeadline(ctx, (*c.Deadline).UTC())
		_ = ignored // the caller cannot use this value
	}

	return ctx, c.Payload, nil
}

type metadataKey struct{}

// WithMetadata attaches the specified metadata value to the context.  The meta
// value must support encoding to JSON. In case of error, the original value of
// ctx is returned along with the error. If meta == nil, the resulting context
// has no metadata attached; this can be used to remove metadata from a context
// that has it.
func WithMetadata(ctx context.Context, meta interface{}) (context.Context, error) {
	if meta == nil {
		// Note we explicitly attach a value even if meta == nil, since ctx might
		// already have metadata so we need to mask it.
		return context.WithValue(ctx, metadataKey{}, json.RawMessage(nil)), nil
	}
	bits, err := json.Marshal(meta)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, metadataKey{}, json.RawMessage(bits)), nil
}

// UnmarshalMetadata decodes the metadata value attached to ctx into meta, or
// returns ErrNoMetadata if ctx does not have metadata attached.
func UnmarshalMetadata(ctx context.Context, meta interface{}) error {
	if v := ctx.Value(metadataKey{}); v != nil {
		// If the metadata value is explicitly nil, we should report that there
		// is no metadata message.
		if msg := v.(json.RawMessage); msg != nil {
			return json.Unmarshal(msg, meta)
		}
	}
	return ErrNoMetadata
}

// ErrNoMetadata is returned by the UnmarshalMetadata function if the context
// does not contain a metadata value.
var ErrNoMetadata = errors.New("context metadata not present")
