package plugin

import (
	"context"
	"slices"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// Features negotiates optional remote traits independently of Client's method set.
func (c *Client) Features(ctx context.Context) (resourcev2.FeatureSet, error) {
	if err := ctx.Err(); err != nil {
		return resourcev2.FeatureSet{}, err
	}
	if cached := c.features.Load(); cached != nil {
		c.setFeatureMode(cached)
		return resourcev2.FeatureSet{Traits: slices.Clone(cached.Traits)}, nil
	}
	out, err := c.call(
		ctx,
		&resourcepb.Request{Operation: &resourcepb.Request_Features{Features: &resourcepb.FeatureRequest{}}},
	)
	if err != nil {
		return resourcev2.FeatureSet{}, err
	}
	p := out.GetFeatures()
	if p == nil {
		return resourcev2.FeatureSet{}, invalid("unexpected feature result")
	}
	features := resourcev2.FeatureSet{Traits: make([]resourcev2.Feature, len(p.Traits))}
	for i, trait := range p.Traits {
		features.Traits[i] = resourcev2.Feature(trait)
	}
	if err := resourcev2.ValidateFeatures(features); err != nil {
		return resourcev2.FeatureSet{}, err
	}
	c.features.CompareAndSwap(nil, &features)
	cached := c.features.Load()
	c.setFeatureMode(cached)
	return resourcev2.FeatureSet{Traits: slices.Clone(cached.Traits)}, nil
}

func (c *Client) setFeatureMode(features *resourcev2.FeatureSet) {
	if features.Has(resourcev2.FeatureSourceSessions) {
		c.featureMode.Store(1)
	} else {
		c.featureMode.CompareAndSwap(0, 2)
	}
}
