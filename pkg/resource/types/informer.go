package types

import (
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
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

type InformerControllerAddPayload struct {
	Data       map[string]interface{}
	PluginID   string
	Key        string
	Connection string
	ID         string
	Namespace  string
}

type InformerControllerUpdatePayload struct {
	OldData    map[string]interface{}
	NewData    map[string]interface{}
	PluginID   string
	Key        string
	Connection string
	ID         string
	Namespace  string
}

type InformerControllerDeletePayload struct {
	Data       map[string]interface{}
	PluginID   string
	Key        string
	Connection string
	ID         string
	Namespace  string
}

type InformerAddPayload struct {
	Data       map[string]interface{}
	Key        string
	Connection string
	ID         string
	Namespace  string
}

type InformerUpdatePayload struct {
	OldData    map[string]interface{}
	NewData    map[string]interface{}
	Key        string
	Connection string
	ID         string
	Namespace  string
}

type InformerDeletePayload struct {
	Data       map[string]interface{}
	Key        string
	Connection string
	ID         string
	Namespace  string
}

type InformerPayload interface {
	InformerAddPayload | InformerUpdatePayload | InformerDeletePayload
}

// InformerOptions defines the behavior for the integrating informers into a resource plugin..
type InformerOptions[ClientT, InformerT any] struct {
	// CreateInformerFunc is a function that should create a new informer base for a given resource connection.
	CreateInformerFunc CreateInformerFunc[ClientT, InformerT]

	// RegisterResourceInformerFunc is a function that should register an informer with a resource
	RegisterResourceFunc RegisterResourceInformerFunc[InformerT]

	// RunInformerFunc is a function that should run the informer, submitting events to the three
	// channels, and blocking until the stop channel is closed.
	RunInformerFunc RunInformerFunc[InformerT]
}

type CreateInformerFunc[ClientT, InformerT any] func(
	ctx *pkgtypes.PluginContext,
	client *ClientT,
) (InformerT, error)

type RegisterResourceInformerFunc[InformerT any] func(
	ctx *pkgtypes.PluginContext,
	resource ResourceMeta,
	informer InformerT,
	addChan chan InformerAddPayload,
	updateChan chan InformerUpdatePayload,
	deleteChan chan InformerDeletePayload,
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
