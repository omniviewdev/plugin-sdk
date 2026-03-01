package resourcetest

import resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"

// Common test ResourceMeta constants for Kubernetes-style resources.

var PodMeta = resource.ResourceMeta{
	Group:    "core",
	Version:  "v1",
	Kind:     "Pod",
	Label:    "Pod",
	Category: "Workloads",
}

var DeploymentMeta = resource.ResourceMeta{
	Group:    "apps",
	Version:  "v1",
	Kind:     "Deployment",
	Label:    "Deployment",
	Category: "Workloads",
}

var ServiceMeta = resource.ResourceMeta{
	Group:    "core",
	Version:  "v1",
	Kind:     "Service",
	Label:    "Service",
	Category: "Networking",
}

var SecretMeta = resource.ResourceMeta{
	Group:    "core",
	Version:  "v1",
	Kind:     "Secret",
	Label:    "Secret",
	Category: "Configuration",
}

var NodeMeta = resource.ResourceMeta{
	Group:    "core",
	Version:  "v1",
	Kind:     "Node",
	Label:    "Node",
	Category: "Cluster",
}
