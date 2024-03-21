package types

import (
	"github.com/omniviewdev/plugin-sdk/pkg/resource/factories"
)

type InformerAction int

const (
	// InformerTypeAdd is used to inform the IDE that a resource has been created.
	InformerActionAdd InformerAction = iota
	// InformerTypeUpdate is used to inform the IDE that a resource has been updated.
	InformerActionUpdate
	// InformerTypeDelete is used to inform the IDE that a resource has been deleted.
	InformerActionDelete
)

type InformerAddPayload struct {
	Data      map[string]interface{}
	Key       string
	Context   string
	ID        string
	Namespace string
}

type InformerUpdatePayload struct {
	OldData   map[string]interface{}
	NewData   map[string]interface{}
	Key       string
	Context   string
	ID        string
	Namespace string
}

type InformerDeletePayload struct {
	Data      map[string]interface{}
	Key       string
	Context   string
	ID        string
	Namespace string
}

type InformerPayload interface {
	InformerAddPayload | InformerUpdatePayload | InformerDeletePayload
}

// InformerOptions defines the behavior for the integrating informers into a resource plugin..
type InformerOptions[ClientT, InformerT any] struct {
	Factory         factories.InformerFactory[ClientT, InformerT]
	RegisterHandler RegisterResourceFunc[InformerT]
	RunHandler      RunInformerFunc[InformerT]
}

type RegisterResourceFunc[InformerT any] func(
	informer InformerT,
	resource ResourceMeta,
) error

// RunInformerFunc is a function that should run the informer, submitting events to the three
// channels, and blocking until the stop channel is closed.
type RunInformerFunc[InformerT any] func(
	informer InformerT,
	stopCh chan struct{},
	addChan chan InformerAddPayload,
	updateChan chan InformerUpdatePayload,
	deleteChan chan InformerDeletePayload,
) error
