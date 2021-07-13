package llbfactory

import (
	"sync"

	"github.com/earthly/earthly/util/llbutil/pllb"

	"github.com/moby/buildkit/client/llb"
)

var gSharedLocalMutex sync.Mutex

var sharedLocalStateCache map[string]pllb.State

func init() {
	sharedLocalStateCache = map[string]pllb.State{}
}

// Factory is used for constructing llb states
type Factory interface {
	// Construct creates a pllb.State
	Construct() pllb.State
}

// PreconstructedFactory holds a pre-constructed pllb.State for cases
// where a factory is overkill.
type PreconstructedFactory struct {
	preconstructedState pllb.State
}

// LocalFactory holds data which can be used to create a pllb.Local state
type LocalFactory struct {
	name          string
	sharedKeyHint string
	opts          []llb.LocalOption
}

// PreconstructedState returns a pseudo-factory which returns
// the passed in state when Construct() is called.
// It is provided for cases where a factory is overkill.
func PreconstructedState(state pllb.State) Factory {
	return &PreconstructedFactory{
		preconstructedState: state,
	}
}

// Construct returns the preconstructed state that was passed to PreconstructedState()
func (f *PreconstructedFactory) Construct() pllb.State {
	return f.preconstructedState
}

// Local eventually creates a llb.Local
func Local(name string, opts ...llb.LocalOption) Factory {
	return &LocalFactory{
		name: name,
		opts: opts,
	}
}

// Copy makes a new copy of the localFactory
func (f *LocalFactory) Copy() *LocalFactory {
	newOpts := []llb.LocalOption{}
	for _, o := range f.opts {
		newOpts = append(newOpts, o)
	}

	return &LocalFactory{
		name: f.name,
		opts: newOpts,
	}
}

// GetName returns the name of the pllb.Local state that will
// eventually be created
func (f *LocalFactory) GetName() string {
	return f.name
}

// WithInclude adds include patterns to the factory's llb options
func (f *LocalFactory) WithInclude(patterns []string) *LocalFactory {
	f = f.Copy()
	f.opts = append(f.opts, llb.IncludePatterns(patterns))
	return f
}

// WithSharedKeyHint adds a shared key hint to the factory's llb options
func (f *LocalFactory) WithSharedKeyHint(key string) *LocalFactory {
	f = f.Copy()
	f.opts = append(f.opts, llb.SharedKeyHint(key))
	f.sharedKeyHint = key
	return f
}

// Construct constructs the pllb.Local state
func (f *LocalFactory) Construct() pllb.State {
	gSharedLocalMutex.Lock()
	defer gSharedLocalMutex.Unlock()

	if st, ok := sharedLocalStateCache[f.sharedKeyHint]; ok {
		return st
	}

	st := pllb.Local(f.name, f.opts...)
	sharedLocalStateCache[f.sharedKeyHint] = st
	return st
}
