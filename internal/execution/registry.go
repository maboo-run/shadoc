package execution

import (
	"errors"
	"fmt"
)

type registry struct {
	engines map[EngineKind]Engine
}

func NewRegistry(engines ...Engine) Registry {
	registered := make(map[EngineKind]Engine, len(engines))
	for _, engine := range engines {
		if engine == nil {
			panic("execution registry cannot register a nil engine")
		}
		kind := engine.Kind()
		if kind == "" {
			panic("execution registry cannot register an engine without a kind")
		}
		if _, exists := registered[kind]; exists {
			panic("execution registry cannot register duplicate engine " + string(kind))
		}
		registered[kind] = engine
	}
	return registry{engines: registered}
}

func (r registry) Engine(kind EngineKind) (Engine, error) {
	engine, ok := r.engines[kind]
	if !ok || engine == nil {
		if kind == "" {
			return nil, errors.New("execution engine kind is required")
		}
		return nil, fmt.Errorf("execution engine %q is not registered", kind)
	}
	return engine, nil
}
