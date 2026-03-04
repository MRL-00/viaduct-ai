package connector

import (
	"fmt"
	"sort"
	"sync"
)

type Capability string

const (
	CapabilityRead      Capability = "read"
	CapabilityWrite     Capability = "write"
	CapabilityMessenger Capability = "messenger"
)

type Descriptor struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Capabilities []Capability `json:"capabilities"`
}

type Registry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
}

func NewRegistry() *Registry {
	return &Registry{connectors: make(map[string]Connector)}
}

func (r *Registry) Register(c Connector) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := c.Name()
	if name == "" {
		return fmt.Errorf("connector name cannot be empty")
	}
	if _, exists := r.connectors[name]; exists {
		return fmt.Errorf("connector %q is already registered", name)
	}
	r.connectors[name] = c
	return nil
}

func (r *Registry) MustRegister(c Connector) {
	if err := r.Register(c); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(name string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[name]
	return c, ok
}

func (r *Registry) List() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Connector, 0, len(r.connectors))
	for _, c := range r.connectors {
		items = append(items, c)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name() < items[j].Name()
	})
	return items
}

func (r *Registry) Descriptors() []Descriptor {
	connectors := r.List()
	descriptors := make([]Descriptor, 0, len(connectors))
	for _, c := range connectors {
		descriptors = append(descriptors, Descriptor{
			Name:         c.Name(),
			Description:  c.Description(),
			Capabilities: capabilitiesOf(c),
		})
	}
	return descriptors
}

func capabilitiesOf(c Connector) []Capability {
	caps := make([]Capability, 0, 3)
	if _, ok := c.(Reader); ok {
		caps = append(caps, CapabilityRead)
	}
	if _, ok := c.(Writer); ok {
		caps = append(caps, CapabilityWrite)
	}
	if _, ok := c.(Messenger); ok {
		caps = append(caps, CapabilityMessenger)
	}
	return caps
}
