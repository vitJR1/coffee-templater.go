package coffee

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/vitJR1/coffee-templater.go/internal/wasm"
)

// EngineVersion is the version of the embedded coffee-templater-engine.
const EngineVersion = wasm.Version

// EngineSHA256 is the sha256 of the embedded coffee_core.wasm.
const EngineSHA256 = wasm.SHA256

const supportedABI = 1

// EngineConfig configures an Engine.
type EngineConfig struct {
	// MaxInstances caps the number of engine instances, i.e. how many operations run in parallel.
	// Each instance is single-threaded and has its own memory. Default: runtime.GOMAXPROCS(0).
	MaxInstances int
	// CacheDir, when set, keeps the machine code compiled from the engine on disk, so later processes
	// start faster. See wazero.NewCompilationCacheWithDir.
	CacheDir string
	// Interpreter runs the engine with the wazero interpreter instead of compiling it to machine code.
	// Slower, but works on platforms without a wazero compiler.
	Interpreter bool
}

// Engine runs the embedded WebAssembly engine. It is safe for concurrent use: operations take an
// instance from a pool and return it when done.
//
// Most programs use the default engine implicitly; create one only to change EngineConfig.
type Engine struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	slots    chan struct{} // one token per instance that may run

	mu     sync.Mutex
	idle   []*instance
	closed bool
}

// NewEngine compiles the embedded engine. Compilation takes a moment; reuse the Engine.
func NewEngine(ctx context.Context, cfg EngineConfig) (*Engine, error) {
	if cfg.MaxInstances <= 0 {
		cfg.MaxInstances = runtime.GOMAXPROCS(0)
	}
	rc := wazero.NewRuntimeConfig()
	if cfg.Interpreter {
		rc = wazero.NewRuntimeConfigInterpreter()
	}
	// cancelling the context of an operation interrupts the engine (the instance is then dropped)
	rc = rc.WithCloseOnContextDone(true)
	if cfg.CacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(cfg.CacheDir)
		if err != nil {
			return nil, fmt.Errorf("coffee: compilation cache: %w", err)
		}
		rc = rc.WithCompilationCache(cache)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, rc)

	_, err := rt.NewHostModuleBuilder("ct_host").
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(hostCall), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("call").
		Instantiate(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("coffee: host module: %w", err)
	}
	compiled, err := rt.CompileModule(ctx, wasm.Binary)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("coffee: compile engine: %w", err)
	}
	e := &Engine{rt: rt, compiled: compiled, slots: make(chan struct{}, cfg.MaxInstances)}

	// check the ABI once, and keep the instance
	inst, err := e.newInstance(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	e.idle = append(e.idle, inst)
	return e, nil
}

// Close frees the engine. Templates compiled on it can no longer be used.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.idle = nil
	e.mu.Unlock()
	return e.rt.Close(ctx)
}

var (
	defaultOnce   sync.Once
	defaultEngine *Engine
	defaultErr    error
)

// DefaultEngine returns the engine used when none is given, creating it on first use.
func DefaultEngine() (*Engine, error) {
	defaultOnce.Do(func() {
		defaultEngine, defaultErr = NewEngine(context.Background(), EngineConfig{})
	})
	return defaultEngine, defaultErr
}

func engineOrDefault(e *Engine) (*Engine, error) {
	if e != nil {
		return e, nil
	}
	return DefaultEngine()
}

// ------------------------------------------------------------------------------- instances

// instance is one WebAssembly instance. It runs one operation at a time.
type instance struct {
	mod    api.Module
	mem    api.Memory
	alloc  api.Function
	free   api.Function
	call   api.Function
	host   hostHandler // callbacks of the running operation
	broken bool        // trapped or interrupted: never used again

	mu      sync.Mutex
	pending []uint32 // template handles to dispose before the next operation
}

func (e *Engine) newInstance(ctx context.Context) (*instance, error) {
	mod, err := e.rt.InstantiateModule(ctx, e.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("coffee: instantiate engine: %w", err)
	}
	inst := &instance{
		mod:   mod,
		mem:   mod.Memory(),
		alloc: mod.ExportedFunction("ct_alloc"),
		free:  mod.ExportedFunction("ct_free"),
		call:  mod.ExportedFunction("ct_call"),
	}
	abi := mod.ExportedFunction("ct_abi_version")
	if inst.mem == nil || inst.alloc == nil || inst.free == nil || inst.call == nil || abi == nil {
		mod.Close(ctx)
		return nil, errors.New("coffee: the engine binary lacks the expected exports")
	}
	res, err := abi.Call(ctx)
	if err != nil {
		mod.Close(ctx)
		return nil, fmt.Errorf("coffee: ct_abi_version: %w", err)
	}
	if v := uint32(res[0]); v != supportedABI {
		mod.Close(ctx)
		return nil, fmt.Errorf("coffee: engine ABI %d is not supported (expected %d)", v, supportedABI)
	}
	return inst, nil
}

// acquire takes an instance from the pool, waiting for a free slot.
func (e *Engine) acquire(ctx context.Context) (*instance, error) {
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		<-e.slots
		return nil, errors.New("coffee: the engine is closed")
	}
	if n := len(e.idle); n > 0 {
		inst := e.idle[n-1]
		e.idle = e.idle[:n-1]
		e.mu.Unlock()
		return inst, nil
	}
	e.mu.Unlock()
	inst, err := e.newInstance(ctx)
	if err != nil {
		<-e.slots
		return nil, err
	}
	return inst, nil
}

func (e *Engine) release(inst *instance) {
	e.mu.Lock()
	if !inst.broken && !e.closed {
		e.idle = append(e.idle, inst)
	}
	e.mu.Unlock()
	if inst.broken {
		inst.mod.Close(context.Background())
	}
	<-e.slots
}

// with runs fn on a pooled instance.
func (e *Engine) with(ctx context.Context, fn func(*instance) error) error {
	inst, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer e.release(inst)
	inst.flushDisposals(ctx)
	return fn(inst)
}

// queueDispose schedules a template handle for disposal on the instance's next operation.
func (inst *instance) queueDispose(handle uint32) {
	inst.mu.Lock()
	inst.pending = append(inst.pending, handle)
	inst.mu.Unlock()
}

func (inst *instance) flushDisposals(ctx context.Context) {
	inst.mu.Lock()
	pending := inst.pending
	inst.pending = nil
	inst.mu.Unlock()
	for _, h := range pending {
		_, _, _ = inst.request(ctx, fmt.Appendf(nil, `{"op":"dispose","template":%d}`, h), nil, nil)
	}
}

// ------------------------------------------------------------------------------- calls

type instanceKey struct{}

// rawCall sends a request frame and returns the response frame (copied out of the instance memory).
func (inst *instance) rawCall(ctx context.Context, frame []byte, host hostHandler) ([]byte, error) {
	if inst.broken {
		return nil, errorf(ErrInternal, "The engine instance is unusable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inst.host = host
	defer func() { inst.host = nil }()
	ctx = context.WithValue(ctx, instanceKey{}, inst)

	fail := func(err error) ([]byte, error) {
		// a trap or an interruption leaves the instance in an unknown state
		inst.broken = true
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, errorf(ErrInternal, "The engine crashed: %v", err)
	}

	res, err := inst.alloc.Call(ctx, uint64(len(frame)))
	if err != nil {
		return fail(err)
	}
	reqPtr := uint32(res[0])
	if reqPtr == 0 {
		return nil, errorf(ErrLimitExceeded, "The engine is out of memory")
	}
	if !inst.mem.Write(reqPtr, frame) {
		return fail(errors.New("request out of memory bounds"))
	}
	res, err = inst.call.Call(ctx, uint64(reqPtr), uint64(len(frame)))
	if err != nil {
		return fail(err)
	}
	if _, err := inst.free.Call(ctx, uint64(reqPtr), uint64(len(frame))); err != nil {
		return fail(err)
	}
	resPtr := uint32(res[0])
	if resPtr == 0 {
		return nil, errorf(ErrLimitExceeded, "The engine is out of memory")
	}
	n, ok := inst.mem.ReadUint32Le(resPtr)
	if !ok {
		return fail(errors.New("response out of memory bounds"))
	}
	view, ok := inst.mem.Read(resPtr+4, n)
	if !ok {
		return fail(errors.New("response out of memory bounds"))
	}
	out := make([]byte, n)
	copy(out, view)
	if _, err := inst.free.Call(ctx, uint64(resPtr), uint64(n)+4); err != nil {
		return fail(err)
	}
	return out, nil
}

// response is the JSON of a response frame.
type response struct {
	OK       bool            `json:"ok"`
	Result   json.RawMessage `json:"result"`
	Errors   []Problem       `json:"errors"`
	Warnings []Problem       `json:"warnings"`
}

// request runs one operation and unwraps the response; engine errors become *Error.
func (inst *instance) request(ctx context.Context, reqJSON []byte, blobs [][]byte, host hostHandler) (json.RawMessage, [][]byte, error) {
	frame, err := inst.rawCall(ctx, encodeFrame(reqJSON, blobs), host)
	if err != nil {
		return nil, nil, err
	}
	resJSON, resBlobs, err := decodeFrame(frame)
	if err != nil {
		return nil, nil, err
	}
	var r response
	if err := json.Unmarshal(resJSON, &r); err != nil {
		return nil, nil, fmt.Errorf("coffee: malformed response from the engine: %w", err)
	}
	if !r.OK {
		return nil, nil, newError(r.Errors)
	}
	return r.Result, resBlobs, nil
}

// Request runs one operation on a pooled instance.
func (e *Engine) request(ctx context.Context, reqJSON []byte, blobs [][]byte, host hostHandler) (result json.RawMessage, out [][]byte, err error) {
	err = e.with(ctx, func(inst *instance) error {
		result, out, err = inst.request(ctx, reqJSON, blobs, host)
		return err
	})
	return result, out, err
}

// ------------------------------------------------------------------------------- host callbacks

// hostHandler answers a callback frame from the engine with the JSON and blobs of the answer.
type hostHandler func(req json.RawMessage, blobs [][]byte) (json.RawMessage, [][]byte)

// hostCall implements the ct_host.call import: (req_ptr, req_len) → pointer to [u32 len][frame].
// It must never panic: a panic would trap and poison the instance.
func hostCall(ctx context.Context, m api.Module, stack []uint64) {
	ptr, n := api.DecodeU32(stack[0]), api.DecodeU32(stack[1])
	stack[0] = 0
	inst, _ := ctx.Value(instanceKey{}).(*instance)
	if inst == nil {
		return
	}

	frame := func() (out []byte) {
		defer func() {
			if r := recover(); r != nil {
				out = encodeFrame(hostFailure(fmt.Sprintf("panic in callback: %v", r)), nil)
			}
		}()
		view, ok := m.Memory().Read(ptr, n)
		if !ok {
			return encodeFrame(hostFailure("callback request out of memory bounds"), nil)
		}
		reqJSON, reqBlobs, err := decodeFrame(view)
		if err != nil {
			return encodeFrame(hostFailure(err.Error()), nil)
		}
		if inst.host == nil {
			return encodeFrame(hostFailure("no callbacks are configured"), nil)
		}
		resJSON, resBlobs := inst.host(reqJSON, reqBlobs)
		return encodeFrame(resJSON, resBlobs)
	}()

	res, err := inst.alloc.Call(ctx, uint64(len(frame))+4)
	if err != nil || res[0] == 0 {
		return
	}
	out := uint32(res[0])
	buf := binary.LittleEndian.AppendUint32(make([]byte, 0, len(frame)+4), uint32(len(frame)))
	buf = append(buf, frame...)
	if !m.Memory().Write(out, buf) {
		return
	}
	stack[0] = uint64(out)
}

func hostFailure(message string) json.RawMessage {
	b, _ := json.Marshal(struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{false, message})
	return b
}
